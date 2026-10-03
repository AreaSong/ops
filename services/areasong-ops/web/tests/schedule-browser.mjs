// 本地隔离验收：PLAYWRIGHT_MODULE 指向已有 playwright 的 ESM 入口，不安装依赖。
import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const context = await browser.newContext({ timezoneId: 'Asia/Shanghai', viewport: { width: 1280, height: 900 } })
const page = await context.newPage()
page.setDefaultTimeout(8000)
const errors = []
page.on('pageerror', (error) => errors.push(error.message))
const writes = []
let plans = [], actor = 'creator', rejectCreate = false, rejectExecute = false
const at = '2030-01-02T02:00:00.000Z'
const base = {
  id: 'local-plan', service: 'demo', action: 'restart', actorHash: 'creator',
  tenantId: 'local', serverId: 'isolated', risk: 'high', state: 'pending_approval',
  digest: 'sha256:local-schedule', requiresDualApproval: true, approvalPolicy: 'two_party_v1', requiresConfirmation: true,
  confirmationPhrase: '确认 demo', approvalSummary: {
    approvalPolicy: 'two_party_v1', expectedBefore: {}, steps: ['inspect', 'restart'],
    scope: '隔离数据', impact: '仅内存模拟', rollback: '无真实执行器',
  },
}
const service = { name: 'demo', displayName: '隔离服务', description: 'S2.1 合成数据', managedCompose: true,
  status: { currentVersion: '1.0.0' }, actions: { restart: { name: 'restart', displayName: '重启服务', enabled: true, risk: 'high' } } }
await context.route('**/*', async (route) => {
  const request = route.request(), url = new URL(request.url()), path = url.pathname
  if (url.origin !== 'http://127.0.0.1:4173') return route.abort()
  if (!path.startsWith('/api/')) return route.continue()
  let body = {}
  if (request.method() !== 'GET') {
    body = request.postDataJSON(); writes.push({ path, body })
    if (path === '/api/plans') {
      if (rejectCreate) return route.fulfill({ status: 503, json: { error: '隔离网络失败' } })
      const plan = { ...structuredClone(base), ...body, id: `local-${writes.length}` }
      plans = [plan]; return route.fulfill({ json: plan })
    }
    if (path.endsWith('/approve')) {
      plans[0] = { ...plans[0], approvedByHash: actor, state: plans[0].scheduleAt ? 'scheduled' : 'approved' }
      return route.fulfill({ json: plans[0] })
    }
    if (path.endsWith('/execute')) {
      if (rejectExecute) return route.fulfill({ status: 409, json: { error: '发布计划尚未到达调度时间' } })
      plans[0].state = 'executing'
      return route.fulfill({ json: { id: 'local-task', service: 'demo', action: 'restart', state: 'queued', createdAt: at } })
    }
    throw new Error(`非预期写入 ${path}`)
  }
  const replies = {
    '/api/session': { email: `${actor}@example.test`, actorHash: actor, environment: 'development', csrfToken: 'local' },
    '/api/services': { services: [service] }, '/api/automatic-tasks': { tasks: [] },
    '/api/tasks': { tasks: [] }, '/api/audit': { entries: [] }, '/api/plans': { plans },
    '/api/alerts': { alerts: [] },
  }
  return route.fulfill({ json: replies[path] || { events: [], tasks: [], entries: [] } })
})
await page.clock.install({ time: new Date('2030-01-02T01:00:00Z') })
await page.clock.pauseAt(new Date('2030-01-02T01:00:00Z'))
const dialog = () => page.locator('.plan-dialog')
async function servicePage() {
  await page.goto('http://127.0.0.1:4173')
  await page.getByRole('button', { name: '服务操作', exact: true }).click()
}
async function newPlan() {
  await page.getByRole('button', { name: /重启服务/ }).click()
  await dialog().waitFor()
}
try {
  await servicePage()
  await newPlan()
  assert.equal(await dialog().getByLabel('执行方式').inputValue(), 'immediate')
  await dialog().getByLabel('执行方式').selectOption('scheduled')
  await dialog().getByLabel('计划执行时间').fill('2030-01-02T10:00')
  await dialog().getByLabel('执行方式').selectOption('immediate')
  assert.equal(await dialog().locator('input[type="datetime-local"]').count(), 0)
  await dialog().getByRole('button', { name: '创建计划', exact: true }).click()
  await page.getByRole('heading', { name: '批准发布计划' }).waitFor()
  assert.equal(writes.length, 1)
  assert.equal('scheduleAt' in writes[0].body, false)
  assert.equal(await dialog().getByRole('button', { name: '当前身份不能批准', exact: true }).isDisabled(), true)
  await dialog().getByRole('button', { name: '取消', exact: true }).click()
  await newPlan()
  await dialog().getByLabel('执行方式').selectOption('scheduled')
  await dialog().getByRole('button', { name: '创建计划', exact: true }).click()
  assert.match(await dialog().getByRole('alert').innerText(), /请选择执行时间/)
  assert.equal(writes.length, 1)
  await dialog().getByLabel('计划执行时间').fill('2030-01-02T10:00')
  assert.match(await dialog().innerText(), /Asia\/Shanghai/)
  // 失败后保留草稿；双击只有一个网络请求。
  rejectCreate = true
  await dialog().getByRole('button', { name: '创建计划', exact: true }).dblclick()
  await dialog().getByRole('alert').waitFor()
  assert.equal(writes.length, 2)
  assert.equal(writes[1].body.scheduleAt, at)
  await page.clock.runFor(3600001)
  rejectCreate = false
  await dialog().getByRole('button', { name: '创建计划', exact: true }).click()
  await page.getByRole('heading', { name: '批准发布计划' }).waitFor()
  assert.equal(writes[2].body.idempotencyKey, writes[1].body.idempotencyKey)
  assert.equal(writes.length, 3)
  // 刷新并换为独立审批人，再换回创建人；全部 API 为本地内存模型。
  await page.clock.setSystemTime(new Date('2030-01-02T01:00:00Z'))
  actor = 'approver'
  await servicePage()
  await page.getByRole('button', { name: /查看.*计划|查看.*批准|批准.*计划/ }).last().click()
  await dialog().getByRole('textbox').fill('确认 demo')
  await dialog().getByRole('button', { name: '批准计划', exact: true }).click()
  await page.getByRole('heading', { name: '等待执行时间' }).waitFor()
  actor = 'creator'
  await servicePage()
  await page.getByRole('button', { name: /查看.*计划|查看.*批准|批准.*计划/ }).last().click()
  assert.equal(await dialog().getByRole('button', { name: '等待执行时间', exact: true }).isDisabled(), true)
  const before = writes.length
  await page.clock.runFor(3600001)
  assert.equal(await dialog().getByRole('button', { name: '执行计划', exact: true }).isEnabled(), true)
  assert.equal(writes.length, before)
  await page.setViewportSize({ width: 375, height: 812 })
  assert.equal(await dialog().evaluate((el) => el.scrollWidth <= el.clientWidth), true)
  await mkdir('../output/playwright', { recursive: true })
  await page.screenshot({ path: '../output/playwright/s2.1-due-mobile.png', fullPage: true })
  rejectExecute = true
  await dialog().getByRole('button', { name: '执行计划', exact: true }).click()
  await dialog().getByRole('alert').waitFor()
  assert.match(await dialog().getByRole('alert').innerText(), /尚未到达调度时间/)
  await page.clock.runFor(10000)
  assert.equal(writes.length, before + 1)
  rejectExecute = false
  await dialog().getByRole('button', { name: '执行计划', exact: true }).click()
  await dialog().waitFor({ state: 'hidden' })
  assert.equal(writes.length, before + 2)
  assert.equal(writes.at(-1).body.idempotencyKey, writes.at(-2).body.idempotencyKey)
  console.log('PASS App：立即/定时创建、审批、刷新重入、到期、显式执行、失败重试、窄屏')
  // 独立挂载真实组件，覆盖服务端列表不展示的终态及旧审批策略。
  await page.evaluate(async () => {
    const { default: React } = await import('/node_modules/.vite/deps/react.js')
    const { default: ReactDOM } = await import('/node_modules/.vite/deps/react-dom_client.js')
    const { ConfirmationDialog } = await import('/src/components/ConfirmationDialog.tsx')
    const host = document.createElement('div'); document.body.replaceChildren(host)
    const root = ReactDOM.createRoot(host)
    window.testCalls = []
    window.renderPlan = (plan, currentActorHash) => root.render(React.createElement(ConfirmationDialog, {
      plan, currentActorHash, pending: false, onCancel: () => window.testCalls.push('cancel'),
      onConfirm: () => window.testCalls.push('execute'), onClosePlan: () => window.testCalls.push('close'),
    }))
  })
  const duePlan = { ...base, state: 'scheduled', scheduleAt: at, approvedByHash: 'approver' }
  for (const [name, plan, identity, enabled] of [
    ['due creator', duePlan, 'creator', true],
    ['wrong actor', duePlan, 'approver', false],
    ['medium wrong approver', { ...base, risk: 'medium', requiresConfirmation: false, requiresDualApproval: false }, 'approver', false],
    ['medium creator', { ...base, risk: 'medium', requiresConfirmation: false, requiresDualApproval: false }, 'creator', true],
    ['medium duplicate approval', { ...base, risk: 'medium', approvalPolicy: 'legacy', requiresConfirmation: false, approvedByHash: 'creator' }, 'creator', false],
    ['medium second approval', { ...base, risk: 'medium', approvalPolicy: 'legacy', requiresConfirmation: false, approvedByHash: 'creator' }, 'approver', true],
    ['missing approval', { ...duePlan, approvedByHash: undefined }, 'creator', false],
    ['invalid time', { ...duePlan, scheduleAt: 'invalid' }, 'creator', false],
    ...['executing', 'completed', 'invalidated', 'needs_attention'].map((state) => [state, { ...duePlan, state }, 'creator', false]),
    ['legacy executor', { ...duePlan, approvalPolicy: 'legacy', secondApprovedByHash: 'second' }, 'fourth', true],
    ['legacy insufficient', { ...duePlan, approvalPolicy: 'legacy' }, 'fourth', false],
  ]) {
    await page.evaluate(({ plan, identity }) => window.renderPlan(plan, identity), { plan, identity })
    await page.waitForTimeout(30)
    assert.equal(await dialog().locator('.modal-footer button').last().isEnabled(), enabled, name)
  }
  await page.evaluate((plan) => window.renderPlan(plan, 'creator'), duePlan)
  await dialog().focus()
  await page.keyboard.press('Shift+Tab')
  assert.equal(await dialog().locator('.modal-footer button').last().evaluate((el) => el === document.activeElement), true)
  await page.keyboard.press('Tab')
  assert.equal(await dialog().getByRole('button', { name: '关闭', exact: true }).evaluate((el) => el === document.activeElement), true)
  await page.keyboard.press('Escape')
  assert.deepEqual(await page.evaluate(() => window.testCalls), ['cancel'])
  console.log('PASS 真实组件：到期身份/审批/终态/旧策略矩阵，键盘焦点循环与 Escape')
  assert.deepEqual(errors, [])

} catch (error) { console.error((await page.locator('body').innerText()).slice(-3000)); throw error } finally { await browser.close() }
