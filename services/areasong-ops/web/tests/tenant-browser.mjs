// 合成内存 API 验证真实 App；真实审批合同由 Go HTTP/Store 测试证明。
import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const context = await browser.newContext({ timezoneId: 'Asia/Shanghai', viewport: { width: 1280, height: 900 } })
const page = await context.newPage(); page.setDefaultTimeout(8000)
const errors = [], writes = [], saved = new Map()
page.on('pageerror', (e) => errors.push(e.message))
let actor = 'creator', canManage = true, fail = false
const access = { enforced: true, version: 7, defaultTenant: 'default', tenants: [
  { id: 'default', displayName: '系统租户', status: 'active', createdBy: 'bootstrap' },
  { id: 'tenant-one', displayName: '原租户', status: 'active', createdBy: 'creator' }],
  roles: [{ id: 'viewer', displayName: '观察者', permissions: ['read'], builtIn: true }], bindings: [], pendingChanges: [] }
await context.route('**/*', async (route) => {
  const req = route.request(), url = new URL(req.url()), path = url.pathname
  if (url.origin !== 'http://127.0.0.1:4173') return route.abort()
  if (!path.startsWith('/api/')) return route.continue()
  if (req.method() !== 'GET') {
    const body = req.postDataJSON(); writes.push({ path, body })
    if (fail) return route.fulfill({ status: 409, json: { error: '访问策略版本已变化，请重新读取' } })
    if (path === '/api/access/changes') {
      let change = saved.get(body.idempotencyKey)
      if (!change) { change = { id: `local-${saved.size}`, state: 'pending_approval', actorHash: actor, requestDigest: `sha256:${saved.size}`, requiresDualApproval: true, approvalPolicy: 'two_party_v1', confirmationPhrase: '批准租户变更', createdAt: '2030-01-01T00:00:00Z' }; saved.set(body.idempotencyKey, change); access.pendingChanges.unshift(change) }
      return route.fulfill({ json: change })
    }
    const change = access.pendingChanges.find((c) => c.id === path.split('/')[4])
    if (path.endsWith('/approve')) { change.state = 'approved'; change.approvedByHash = actor }
    else if (path.endsWith('/apply')) change.state = 'applied'
    else if (path.endsWith('/reject')) change.state = 'rejected'
    else throw new Error(`非预期写入 ${path}`)
    return route.fulfill({ json: change })
  }
  if (path.endsWith('/detail')) {
      const change = access.pendingChanges.find(c => c.id === path.split('/')[4])
      return route.fulfill({ json: { ...change, reviewerHash: actor, kind: 'tenant', availability: 'ready', operation: 'create', expectedVersion: access.version, currentVersion: access.version, after: { id: 'tenant-new', displayName: '新租户', status: 'active' } } })
    }
    const replies = { '/api/session': { email: `${actor}@example.test`, actorHash: actor, environment: 'development', csrfToken: 'local' }, '/api/access': { ...access, canManage, currentSubject: { subject: actor } }, '/api/services': { services: [] }, '/api/plans': { plans: [] } }
  return route.fulfill({ json: replies[path] || { tasks: [], events: [], entries: [], alerts: [] } })
})
const dialog = () => page.getByRole('dialog')
const button = (name) => page.getByRole('button', { name, exact: true })
async function open() { await page.goto('http://127.0.0.1:4173'); await button('访问控制').click(); await button('新增租户').waitFor() }
async function submit() { await dialog().getByRole('button', { name: '创建租户提案' }).click() }
try {
  await open(); assert.equal(await button('编辑租户 系统租户').isDisabled(), true)
  await button('新增租户').click(); await page.keyboard.press('Tab')
  assert.equal(await dialog().getByLabel('租户 ID').evaluate((el) => el === document.activeElement), true)
  await page.keyboard.press('Shift+Tab'); assert.equal(await dialog().getByRole('button', { name: '创建租户提案' }).evaluate((el) => el === document.activeElement), true)
  await submit(); await dialog().getByRole('alert').waitFor(); assert.equal(writes.length, 0)
  await dialog().getByLabel('租户 ID').fill('tenant-one'); await dialog().getByLabel('租户名称').fill('重复'); await submit(); assert.match(await dialog().getByRole('alert').innerText(), /已存在/)
  await page.keyboard.press('Escape'); await button('继续编辑').click()
  await dialog().getByLabel('租户 ID').fill('tenant-new'); await dialog().getByLabel('租户名称').fill('新租户')
  access.version = 8; fail = true
  await dialog().getByRole('button', { name: '创建租户提案' }).dblclick(); await dialog().getByRole('alert').waitFor()
  assert.equal(writes.length, 1); assert.equal(writes[0].body.expectedVersion, 7)
  assert.deepEqual(Object.keys(writes[0].body).sort(), ['expectedVersion', 'idempotencyKey', 'requiresDualApproval', 'tenants'])
  assert.deepEqual(writes[0].body.tenants, [{ id: 'tenant-new', displayName: '新租户', status: 'active' }])
  fail = false; await submit(); await dialog().waitFor({ state: 'hidden' }); assert.equal(writes[0].body.idempotencyKey, writes[1].body.idempotencyKey)
  assert.equal(await page.locator('.tenant-row').count(), 2); assert.match(await page.getByRole('status').innerText(), /pending_approval.*新租户/)
  assert.equal(await button('独立批准').count(), 0)
  actor = 'approver'; await open(); await button('读取服务端详情').click(); await page.locator('.runner-update-actions input').fill('批准租户变更'); await button('独立批准').click()
  await page.getByText('独立批准已完成，需由创建人提交应用。').waitFor(); assert.equal(await button('创建人应用').count(), 0)
  actor = 'creator'; await open(); fail = true; await button('创建人应用').click()
  await page.getByText('访问策略版本已变化，请重新读取', { exact: true }).first().waitFor(); const count = writes.length
  await page.waitForTimeout(150); assert.equal(writes.length, count); fail = false; await button('创建人应用').click(); await page.getByText(/applied ·/).waitFor()
  await open(); assert.equal(writes.length, count + 1)
  await button('编辑租户 原租户').click(); assert.equal(await dialog().getByLabel('租户 ID').getAttribute('readonly'), '')
  await submit(); assert.match(await dialog().getByRole('alert').innerText(), /没有实际变化/)
  await dialog().getByLabel('租户名称').fill('变更后名称'.repeat(20)); await page.setViewportSize({ width: 375, height: 812 })
  assert.equal(await dialog().evaluate((el) => el.scrollWidth <= el.clientWidth), true)
  await mkdir('../output/playwright', { recursive: true }); await page.screenshot({ path: '../output/playwright/s3.1-tenant-mobile.png' })
  await page.keyboard.press('Escape'); await button('放弃修改').click(); assert.equal(await button('编辑租户 原租户').evaluate((el) => el === document.activeElement), true)
  await page.setViewportSize({ width: 1280, height: 900 }); await button('编辑租户 原租户').click(); await dialog().getByLabel('租户名称').fill('更名'); await submit(); await dialog().waitFor({ state: 'hidden' })
  assert.deepEqual(writes.at(-1).body.tenants, [{ id: 'tenant-one', displayName: '更名', status: 'active' }])
  await button('撤销').click(); await page.getByText(/rejected ·/).waitFor()
  const rejectedKey = writes.at(-2).body.idempotencyKey
  await button('编辑租户 原租户').click(); await dialog().getByLabel('租户名称').fill('更名'); await submit()
  await dialog().getByRole('alert').waitFor(); assert.match(await dialog().getByRole('alert').innerText(), /原提案已撤销/)
  assert.equal(writes.at(-1).body.idempotencyKey, rejectedKey)
  await submit(); await dialog().waitFor({ state: 'hidden' }); assert.notEqual(writes.at(-1).body.idempotencyKey, rejectedKey)
  await button('新增绑定').click(); await page.getByLabel('主体', { exact: true }).fill('new@example.test'); await page.locator('.access-form select').nth(0).selectOption('tenant-one'); await page.locator('.access-form select').nth(1).selectOption('viewer')
  await page.locator('.access-form').getByRole('button', { name: '创建审批变更' }).click(); assert.equal(writes.at(-1).body.bindings.length, 1); assert.equal('tenants' in writes.at(-1).body, false)
  canManage = false; await page.reload(); await button('访问控制').click(); await page.getByRole('heading', { name: '访问控制', exact: true }).waitFor(); assert.equal(await button('新增租户').count(), 0)
  assert.deepEqual(errors, []); console.log('S3.1 Chromium App/内存 API 通过；不含服务端差异读取。')
} finally { await browser.close() }
