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
