import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'

// 复用项目的 TypeScript 编译器，不引入测试运行器依赖或落盘编译副本。
const source = readFileSync(new URL('../src/api.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 },
})
const { OpsAPI, APIError, isFeatureUnavailable } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)

const policy = {
  enabled: false, channel: 'stable', maintenanceTimezone: 'UTC', canaryPercent: 0,
  maxUnavailable: 0, requireBackup: true, requireApproval: true,
  rollbackOnAlert: true, observationSeconds: 300,
}

for (const detail of ['缺少 CSRF Cookie', 'CSRF 令牌无效']) {
  test(`会话拒绝显示恢复步骤且不重试：${detail}`, async (t) => {
    const calls = []
    t.mock.method(globalThis, 'fetch', async (url, options) => {
      calls.push({ url, options })
      return Response.json({ error: detail }, { status: 403 })
    })
    await assert.rejects(new OpsAPI().updateAutoUpdatePolicy('areaforge', policy), (error) => {
      assert.ok(error instanceof APIError)
      assert.equal(error.status, 403)
      assert.deepEqual(error.payload, { error: detail })
      assert.match(error.message, /保留未提交内容并刷新页面/)
      assert.match(error.message, /核对执行记录/)
      assert.match(error.message, /不会自动重试/)
      assert.equal(isFeatureUnavailable(error), false)
      return true
    })
    assert.equal(calls.length, 1)
    assert.equal(calls[0].url, '/api/auto-updates/areaforge')
    assert.equal(calls[0].options.method, 'PUT')
  })
}

test('来源、权限和服务错误保持原有语义', async (t) => {
  for (const [status, detail] of [[403, '请求来源不匹配'], [403, '当前角色没有平台资源权限'], [401, '身份验证失败'], [503, 'Runner 当前不可用']]) {
    const fetchMock = t.mock.method(globalThis, 'fetch', async () => Response.json({ error: detail }, { status }))
    await assert.rejects(new OpsAPI().services(), (error) => error.status === status && error.message === detail)
    fetchMock.mock.restore()
  }
})

test('手动获取会话后合法写请求仍带原 CSRF 头且只提交一次', async (t) => {
  const calls = []
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    calls.push({ url, options })
    return Response.json(url === '/api/session' ? { csrfToken: 'test-csrf', email: 'operator@example.test' } : policy)
  })
  const api = new OpsAPI()
  await api.session()
  assert.deepEqual(await api.updateAutoUpdatePolicy('areaforge', policy), policy)
  assert.equal(calls.length, 2)
  assert.equal(calls[1].options.headers['X-AreaSong-Ops-CSRF'], 'test-csrf')
  assert.equal(calls[1].options.credentials, 'same-origin')
})

test('计划创建默认兼容；失败重试同键，改变时间换键，成功后新建换键', async (t) => {
  const bodies = []
  let fail = true
  t.mock.method(globalThis, 'fetch', async (_url, options) => {
    bodies.push(JSON.parse(options.body))
    if (fail) throw new Error('网络中断')
    return Response.json({ id: 'plan-local' })
  })
  const api = new OpsAPI()
  const create = (time) => api.createPlan('demo', 'restart', '', time)
  await assert.rejects(create(), /网络中断/)
  await assert.rejects(create(), /网络中断/)
  assert.equal('scheduleAt' in bodies[0], false)
  assert.equal(bodies[0].idempotencyKey, bodies[1].idempotencyKey)
  const at = '2030-01-02T01:30:00.000Z'
  await assert.rejects(create(at), /网络中断/)
  await assert.rejects(create(at), /网络中断/)
  assert.equal(bodies[2].scheduleAt, at)
  assert.equal(bodies[2].idempotencyKey, bodies[3].idempotencyKey)
  assert.notEqual(bodies[1].idempotencyKey, bodies[2].idempotencyKey)
  await assert.rejects(create('2030-01-02T02:30:00.000Z'), /网络中断/)
  assert.notEqual(bodies[3].idempotencyKey, bodies[4].idempotencyKey)
  fail = false
  await create(at)
  await create(at)
  assert.equal(bodies[5].idempotencyKey, bodies[2].idempotencyKey)
  assert.notEqual(bodies[6].idempotencyKey, bodies[5].idempotencyKey)
  await create()
  assert.equal('scheduleAt' in bodies[7], false)
})

test('显式执行失败不自动重试；同计划重试沿用键且不携带时间', async (t) => {
  const calls = []
  let status = 409
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    calls.push({ url, body: JSON.parse(options.body) })
    return Response.json(status === 200 ? { id: 'task-local' } : { error: '发布计划尚未到达调度时间' }, { status })
  })
  const api = new OpsAPI()
  await assert.rejects(api.executePlan('p'), /尚未到达调度时间/)
  assert.equal(calls.length, 1)
  status = 200
  await api.executePlan('p')
  await api.executePlan('p')
  assert.deepEqual(calls[0], calls[1])
  assert.deepEqual(calls[1], calls[2])
  assert.deepEqual(Object.keys(calls[0].body), ['idempotencyKey'])
})

for (const method of ['create', 'execute']) {
  test(`计划${method}会话失效不自动写重试`, async (t) => {
    let count = 0
    t.mock.method(globalThis, 'fetch', async () => {
      count++
      return Response.json({ error: 'CSRF 令牌无效' }, { status: 403 })
    })
    const api = new OpsAPI()
    await assert.rejects(method === 'create' ? api.createPlan('demo', 'restart') : api.executePlan('p'), /不会自动重试/)
    assert.equal(count, 1)
  })
}

test('租户提案保留显式版本与幂等键；错误不自动重试，应用只引用提案 ID', async (t) => {
  const calls = []
  t.mock.method(globalThis, 'fetch', async (url, options) => {
    calls.push({ url, body: JSON.parse(options.body) })
    return Response.json({ error: '访问策略版本已变化，请重新读取' }, { status: 409 })
  })
  const body = { tenants: [{ id: 'tenant-one', displayName: '新名称', status: 'active' }], expectedVersion: 7, idempotencyKey: 'local-key' }
  const api = new OpsAPI()
  await assert.rejects(api.createAccessChange(body), /版本已变化/)
  await assert.rejects(api.createAccessChange(body), /版本已变化/)
  assert.equal(calls.length, 2)
  assert.deepEqual(calls[0].body, { ...body, requiresDualApproval: true })
  assert.deepEqual(calls[1].body, calls[0].body)
  await assert.rejects(api.applyAccessChange({ id: 'proposal' }), /版本已变化/)
  assert.deepEqual(calls[2], { url: '/api/access/changes/proposal/apply', body: {} })
})

test('提案详情 GET 不缓存、绑定响应身份且不重试', async (t) => {
  const calls = []
  const controller = new AbortController()
  t.mock.method(globalThis, 'fetch', async (url, options) => { calls.push({ url, options }); return Response.json({ reviewerHash: 'approver', id: 'proposal' }) })
  const api = new OpsAPI()
  assert.equal((await api.accessChangeDetail('a/b', 'approver', controller.signal)).id, 'proposal')
  assert.equal(calls[0].url, '/api/access/changes/a%2Fb/detail')
  assert.equal(calls[0].options.cache, 'no-store')
  assert.equal(calls[0].options.signal, controller.signal)
  assert.equal(calls[0].options.body, undefined)
  await assert.rejects(api.accessChangeDetail('proposal', 'creator'), /会话身份已变化/)
  assert.equal(calls.length, 2)
})

test('收口仅匹配已结束阻断允许下次人工新键；网络和未知响应保留原键', async (t) => {
  const bodies = []
  let mode = 'network'
  t.mock.method(globalThis, 'fetch', async (_url, options) => {
    const body = JSON.parse(options.body); bodies.push(body)
    if (mode === 'network') throw new Error('response lost')
    return Response.json({ error: '告警阻断', code: 'plan_closure_blocked',
      attemptId: mode === 'mismatch' ? 'wrong' : body.idempotencyKey,
      attemptState: mode === 'running' ? 'running' : 'finished', newAttemptAllowed: true,
    }, { status: 409 })
  })
  const api = new OpsAPI()
  for (mode of ['network', 'network', 'mismatch', 'running']) {
    await assert.rejects(api.closePlan('p'), error => !error.newClosureAttemptAllowed)
  }
  assert.equal(new Set(bodies.map(b => b.idempotencyKey)).size, 1)
  mode = 'finished'
  await assert.rejects(api.closePlan('p'), error => error.newClosureAttemptAllowed)
  assert.equal(bodies.length, 5, '没有自动重试')
  mode = 'network'
  await assert.rejects(api.closePlan('p'))
  assert.notEqual(bodies[4].idempotencyKey, bodies[5].idempotencyKey)
  await assert.rejects(api.closePlan('p'))
  assert.equal(bodies[5].idempotencyKey, bodies[6].idempotencyKey)
})
