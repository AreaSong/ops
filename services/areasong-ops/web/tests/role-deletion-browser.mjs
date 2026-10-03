import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const url = process.env.S32D2_URL
assert.match(url, /^http:\/\/127\.0\.0\.1:\d+$/)
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const contexts = [], writes = [], errors = []
const btn = (p, name) => p.getByRole('button', { name, exact: true })
async function session(actor) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } }); contexts.push(context)
  await context.addCookies([{ name: 'review_actor', value: actor, url }])
  await context.route('**/*', r => new URL(r.request().url()).origin === url ? r.continue() : r.abort())
  const page = await context.newPage(); page.setDefaultTimeout(10000)
  page.on('pageerror', e => errors.push(e.message))
  page.on('request', r => { if (r.method() === 'POST') writes.push({ actor, url: r.url(), body: r.postDataJSON() }) })
  await page.goto(url); await btn(page, '访问控制').click(); await page.getByText('角色与租户', { exact: true }).waitFor()
  return page
}
async function refresh(p) { const response = p.waitForResponse(r => r.url().endsWith('/api/access')); await p.getByTitle('刷新访问策略').click(); await response }
const card = (p, id) => p.locator('.runner-update-card').filter({ hasText: id.slice(0, 8) })
async function createRole(p, id, name) {
  await btn(p, '新增角色').click(); await p.getByLabel('角色 ID', { exact: true }).fill(id); await p.getByLabel('角色名称', { exact: true }).fill(name); await p.getByLabel('ops.read', { exact: true }).check(); await p.getByLabel('ops.inspect', { exact: true }).check()
  const response = p.waitForResponse(r => r.url().endsWith('/changes') && r.request().method() === 'POST')
  await btn(p, '创建角色提案').click(); const c = await (await response).json(); await p.getByRole('dialog').waitFor({ state: 'hidden' }); return c
}
async function approve(p, c) {
  await refresh(p); const row = card(p, c.id)
  await btn(row, '读取服务端详情').click(); await row.locator('input').waitFor(); await row.locator('input').fill(c.confirmationPhrase)
  await btn(row, '独立批准').click(); await row.getByText('独立批准已完成，需由创建人提交应用。').waitFor()
}
async function apply(p, c) {
  await refresh(p); const row = card(p, c.id)
  await btn(row, '创建人应用').click(); await row.getByText(/applied ·/).waitFor()
}
async function deletion(p, name) {
  await btn(p, `删除角色 ${name}`).click()
  const response = p.waitForResponse(r => r.url().endsWith('/changes') && r.request().method() === 'POST')
  await btn(p, '创建删除提案').click(); const c = await (await response).json(); await p.getByRole('dialog').waitFor({ state: 'hidden' }); return c
}
const roleRow = (p, id) => p.locator('.role-row').filter({ has: p.locator('code').filter({ hasText: new RegExp(`^${id}$`) }) })
try {
  const creator = await session('creator'), approver = await session('approver'), viewer = await session('viewer')
  assert.equal(await btn(viewer, '新增角色').count(), 0); assert.equal(await viewer.getByRole('button', { name: /^删除角色/ }).count(), 0)
  for (const name of ['Admin', 'Manager', '已有绑定角色', '直接引用角色']) assert.equal(await btn(creator, `删除角色 ${name}`).isDisabled(), true)
  const name = '长角色名称'.repeat(24)
  const created = await createRole(creator, 'browser-delete', name); await approve(approver, created); await apply(creator, created)
  await btn(creator, `删除角色 ${name}`).click(); const dialog = creator.getByRole('dialog')
  const count = writes.length
  await creator.keyboard.press('Enter'); assert.equal(writes.length, count)
  await creator.keyboard.press('Tab'); assert.equal(await btn(dialog, '取消').evaluate(el => el === document.activeElement), true)
  await creator.keyboard.press('Shift+Tab'); assert.equal(await btn(dialog, '创建删除提案').evaluate(el => el === document.activeElement), true)
  await creator.setViewportSize({ width: 375, height: 812 }); await mkdir('../../output/playwright', { recursive: true })
  await dialog.screenshot({ path: '../../output/playwright/s3.2d2-confirm-mobile.png' })
  assert.equal(await dialog.evaluate(el => el.scrollWidth <= el.clientWidth), true)
  await creator.keyboard.press('Escape'); await dialog.waitFor({ state: 'hidden' }); assert.equal(writes.length, count)
  assert.equal(await btn(creator, `删除角色 ${name}`).evaluate(el => el === document.activeElement), true)
  const deleted = await deletion(creator, name)
  assert.equal(await roleRow(creator, 'browser-delete').count(), 1)
  await refresh(approver); const review = card(approver, deleted.id)
  await btn(review, '读取服务端详情').click(); await review.getByText('删除自定义角色 · 应用后角色将不存在').waitFor()
  await review.getByText(/基准版本未发现绑定及主体直接引用/).waitFor()
  assert.equal(await review.locator('dl div').filter({ hasText: '原完整权限' }).locator('dd').innerText(), 'ops.read、ops.inspect')
  await review.screenshot({ path: '../../output/playwright/s3.2d2-review-desktop.png' })
  await approver.setViewportSize({ width: 375, height: 812 }); await review.screenshot({ path: '../../output/playwright/s3.2d2-review-mobile.png' })
  assert.equal(await review.locator('dd').evaluateAll(els => els.every(e => e.scrollWidth <= e.clientWidth)), true)
  await review.locator('input').fill(deleted.confirmationPhrase); await btn(review, '独立批准').click(); await review.getByText('独立批准已完成，需由创建人提交应用。').waitFor()
  await refresh(creator); assert.equal(await roleRow(creator, 'browser-delete').count(), 1)
  // 实际 apply 已提交但浏览器丢失响应，必须只读恢复为成功。
  await creator.route(`**/changes/${deleted.id}/apply`, async route => { await route.fetch(); await route.abort('failed') })
  await apply(creator, deleted); await creator.getByText('角色 browser-delete 已删除，已刷新确认目标不存在。', { exact: true }).waitFor()
  assert.equal(await roleRow(creator, 'browser-delete').count(), 0)
  await refresh(creator); await btn(card(creator, deleted.id), '读取服务端详情').click(); await card(creator, deleted.id).getByText('该提案已应用，请以刷新后的生效策略核对结果。').waitFor()
  assert.equal(writes.filter(w => w.url.endsWith(`/${deleted.id}/apply`)).length, 1)
  const staleRole = await createRole(creator, 'stale-delete', '版本漂移角色'); await approve(approver, staleRole); await apply(creator, staleRole)
  const stale = await deletion(creator, '版本漂移角色'); await approve(approver, stale)
  const drift = await createRole(creator, 'drift-role', '无关版本变化'); await approve(approver, drift); await apply(creator, drift)
  const beforeApply = writes.length
  await btn(card(creator, stale.id), '创建人应用').click(); await creator.getByText('策略版本已变化，请刷新后重新核对。', { exact: true }).waitFor()
  assert.equal(writes.length, beforeApply); assert.equal(await roleRow(creator, 'stale-delete').count(), 1)
  const deletionWrites = writes.filter(w => w.body?.removeRoleIds)
  assert.equal(deletionWrites.length, 2)
  for (const w of deletionWrites) assert.deepEqual(Object.keys(w.body).sort(), ['expectedVersion', 'idempotencyKey', 'removeRoleIds', 'requiresDualApproval'])
  assert.deepEqual(errors, [])
  console.log('S3.2d2 真实 Web认证/CSRF/代理/Runner/SQLite，两个独立 Cookie：网页创建角色→删除提案→真实删除详情→独立批准→创建人应用；创建/批准仍存在，丢失 apply 响应恢复、刷新后不存在、终态读取零重写、引用/无权限入口、版本漂移零 apply、取消/Enter/焦点/375px 均通过。')
} finally { await Promise.all(contexts.map(c => c.close())); await browser.close() }
