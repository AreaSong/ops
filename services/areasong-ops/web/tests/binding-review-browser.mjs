import assert from 'node:assert/strict'
import { mkdir, writeFile } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const url = process.env.S33A2_URL
assert.match(url, /^http:\/\/127\.0\.0\.1:\d+$/)
const output = new URL('../../output/s3.3a2/browser/', import.meta.url)
await mkdir(output, { recursive: true })
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const contexts = [], errors = [], details = [], posts = []
const btn = (p, name) => p.getByRole('button', { name, exact: true })
async function session(actor) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } }); contexts.push(context)
  await context.addCookies([{ name: 'review_actor', value: actor, url }])
  await context.route('**/*', route => new URL(route.request().url()).origin === url ? route.continue() : route.abort())
  const page = await context.newPage(); page.setDefaultTimeout(12000)
  page.on('pageerror', e => errors.push(e.message))
  page.on('response', async res => { if (res.url().endsWith('/detail') && res.status() === 200) details.push(await res.json()) })
  page.on('request', req => { if (req.method() === 'POST') posts.push({ actor, url: req.url(), body: req.postDataJSON() }) })
  await page.goto(url); await btn(page, '访问控制').click(); await btn(page, '新增绑定').waitFor()
  return page
}
async function access(page) { return page.evaluate(async () => (await fetch('/api/access')).json()) }
async function post(page, path, body) {
  const csrfToken = (await page.context().cookies()).find(c => c.name === 'areasong_ops_csrf').value
  return page.evaluate(async ({ path, body, csrfToken }) => {
    const res = await fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-AreaSong-Ops-CSRF': csrfToken }, body: JSON.stringify(body) })
    const data = await res.json(); if (!res.ok) throw Error(JSON.stringify(data)); return data
  }, { path, body, csrfToken })
}
async function refresh(page) { await page.getByTitle('刷新访问策略').click(); await btn(page, '新增绑定').waitFor() }
// 列表使用截断标识，按完整提案详情对应的读取按钮选择顺序。
async function currentCard(page, state = 'pending_approval') {
  await refresh(page); const card = page.locator(`.runner-update-${state}`); await card.first().waitFor(); return card.first()
}
async function reviewApprove(approver, operation) {
  const card = await currentCard(approver)
  await btn(card, '读取服务端详情').click()
  await card.getByText(operation, { exact: false }).first().waitFor()
  await card.getByText('完整主体哈希', { exact: true }).first().waitFor()
  const hashes = await card.locator('dl div').filter({ hasText: '完整主体哈希' }).locator('dd').allTextContents()
  assert.ok(hashes.every(s => /^[a-f0-9]{64}$/.test(s)))
  await card.locator('.runner-update-actions input').fill(await card.locator('.runner-update-actions code').innerText())
  await btn(card, '独立批准').focus(); assert.equal(await btn(card, '独立批准').evaluate(el => el === document.activeElement), true)
  await approver.keyboard.press('Enter'); await approver.getByText('独立批准已完成，需由创建人提交应用。').waitFor()
}
async function applyCreator(creator) {
  await refresh(creator); await btn(creator, '创建人应用').click(); await creator.locator('.runner-update-approved').waitFor({ state: 'hidden' })
}
try {
  const creator = await session('creator'), approver = await session('approver')
  await btn(creator, '新增绑定').click()
  await creator.getByLabel('主体', { exact: true }).fill('synthetic@example.test')
  await creator.locator('.access-form select').nth(0).selectOption('default')
  await creator.locator('.access-form select').nth(1).selectOption('platform-admin')
  await btn(creator.locator('.access-form'), '创建审批变更').click(); await creator.locator('.runner-update-pending_approval').waitFor()
  assert.equal(((await access(creator)).bindings ?? []).length, 0, 'proposal must not take effect')
  let card = await currentCard(approver); await btn(card, '读取服务端详情').click(); await card.getByText('新增绑定 · 当前差异有效', { exact: true }).waitFor()
  await card.getByText('*（全部权限）', { exact: false }).waitFor()
  await card.getByText('不设绑定到期时间', { exact: true }).waitFor()
  for (const [name, width, scheme] of [['desktop', 1280, 'light'], ['narrow', 375, 'light'], ['dark-preference', 375, 'dark']]) {
    await approver.setViewportSize({ width, height: 900 }); await approver.emulateMedia({ colorScheme: scheme })
    assert.equal(await card.locator('dd').evaluateAll(nodes => nodes.every(el => el.scrollWidth <= el.clientWidth)), true)
    await card.screenshot({ path: new URL(`create-${name}.png`, output).pathname })
  }
  await reviewApprove(approver, '新增绑定')
  assert.equal(((await access(creator)).bindings ?? []).length, 0, 'approval must not take effect')
  await applyCreator(creator)
  let view = await access(creator), binding = view.bindings[0]
  assert.equal(view.bindings.length, 1); assert.match(binding.subject, /^[a-f0-9]{64}$/)
  const id = binding.id
  // 编辑/撤销仅通过真实 HTTP 提案，审阅批准应用使用实际网页。
  await post(creator, '/api/access/changes', { bindings: [{ id, subject: binding.subject, tenantId: 'default', roleId: 'viewer', objectIds: ['unknown:two', 'unknown:one', 'unknown:two'], expiresAt: '2000-01-01T12:00:00+08:00' }], expectedVersion: view.version, requiresDualApproval: true, idempotencyKey: crypto.randomUUID() })
  await reviewApprove(approver, '编辑既有绑定'); await applyCreator(creator)
  view = await access(creator); binding = view.bindings[0]
  assert.deepEqual(binding.objectIds, ['unknown:two', 'unknown:one', 'unknown:two']); assert.equal(binding.expiresAt, '2000-01-01T04:00:00Z')
  await post(creator, '/api/access/changes', { bindings: [{ id, subject: binding.subject, tenantId: 'default', roleId: 'viewer' }], expectedVersion: view.version, requiresDualApproval: true, idempotencyKey: crypto.randomUUID() })
  card = await currentCard(approver); await btn(card, '读取服务端详情').click(); await card.getByText('编辑既有绑定（完整替换） · 当前差异有效').waitFor()
  await card.getByText('实际变化：对象范围、期限。').waitFor(); await card.screenshot({ path: new URL('edit-clear.png', output).pathname })
  await reviewApprove(approver, '编辑既有绑定'); await applyCreator(creator)
  view = await access(creator); assert.equal(view.bindings[0].expiresAt, undefined)
  await post(creator, '/api/access/changes', { removeBindingIds: [id], expectedVersion: view.version, requiresDualApproval: true, idempotencyKey: crypto.randomUUID() })
  card = await currentCard(approver); await btn(card, '读取服务端详情').click(); await card.getByText('绑定将不存在（撤销）').waitFor(); await card.screenshot({ path: new URL('revoke.png', output).pathname })
  await reviewApprove(approver, '撤销单条绑定'); await applyCreator(creator)
  view = await access(creator); assert.equal((view.bindings ?? []).length, 0)
  await post(creator, '/api/access/changes', { bindings: [{ id: 'unsupported-jit', subject: binding.subject, tenantId: 'default', roleId: 'viewer', jit: true }], expectedVersion: view.version, requiresDualApproval: true, idempotencyKey: crypto.randomUUID() })
  card = await currentCard(approver); await btn(card, '读取服务端详情').click(); await card.getByText('不支持此提案的完整差异，不能据此批准。').waitFor()
  assert.equal(await btn(card, '独立批准').count(), 0); assert.equal(await card.getByText('完整主体哈希', { exact: true }).count(), 0)
  await card.screenshot({ path: new URL('unsupported.png', output).pathname })
  assert.deepEqual(errors, []); assert.equal(posts.filter(p => p.url.endsWith('/approve')).length, 4); assert.equal(posts.filter(p => p.url.endsWith('/apply')).length, 4)
  assert.ok(details.some(d => d.operation === 'create') && details.some(d => d.operation === 'edit') && details.some(d => d.operation === 'revoke'))
  await writeFile(new URL('results.json', output), JSON.stringify({ posts, details, errors, stages: ['create-not-effective', 'approve-not-effective', 'create-applied', 'http-edit', 'clear-expiry', 'http-revoke', 'unsupported-blocked'] }, null, 2))
  console.log('S3.3a2 real local Cookie auth/CSRF/proxy/Runner/SQLite: existing create, HTTP edit/revoke, trusted review, approve/apply/readback, unsupported, narrow/keyboard passed')
} finally { await Promise.all(contexts.map(c => c.close())); await browser.close() }
