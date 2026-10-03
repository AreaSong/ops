import assert from 'node:assert/strict'
import { mkdir } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const url = process.env.S31A_URL
assert.match(url, /^http:\/\/127\.0\.0\.1:\d+$/)
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const contexts = []
const errors = [], reads = [], writes = []
async function session(actor) {
  const context = await browser.newContext({ viewport: { width: 1280, height: 900 } }); contexts.push(context)
  await context.addCookies([{ name: 'review_actor', value: actor, url }])
  await context.route('**/*', (route) => new URL(route.request().url()).origin === url ? route.continue() : route.abort())
  const page = await context.newPage(); page.setDefaultTimeout(10000)
  page.on('pageerror', e => errors.push(e.message))
  page.on('request', req => { if (req.url().endsWith('/detail')) reads.push({ actor, url: req.url() }); if (req.method() === 'POST') writes.push({ actor, url: req.url(), body: req.postDataJSON() }) })
  await page.goto(url); await page.getByRole('button', { name: '访问控制', exact: true }).click()
  await page.getByRole('button', { name: '新增租户', exact: true }).waitFor()
  return page
}
const btn = (page, name) => page.getByRole('button', { name, exact: true })
async function refresh(page) { await page.getByTitle('刷新访问策略').click(); await btn(page, '读取服务端详情').first().waitFor() }
try {
  const creator = await session('creator'), approver = await session('approver')
  await btn(creator, '新增租户').click()
  await creator.getByLabel('租户 ID').fill('browser-tenant'); await creator.getByLabel('租户名称', { exact: true }).fill('浏览器原名称')
  await btn(creator, '创建租户提案').click(); await creator.getByRole('dialog').waitFor({ state: 'hidden' })
  assert.equal(await creator.locator('.tenant-row').filter({ hasText: 'browser-tenant' }).count(), 0)
  await btn(creator, '读取服务端详情').click(); await creator.getByText('此前不存在', { exact: true }).waitFor()
  assert.equal(await btn(creator, '独立批准').count(), 0)
  await refresh(approver); assert.equal(await btn(approver, '独立批准').count(), 0)
  await btn(approver, '读取服务端详情').click(); await approver.getByText('此前不存在', { exact: true }).waitFor()
  const first = approver.locator('.access-change-review').first(); const id = await first.getAttribute('aria-label')
  assert.equal(await creator.locator('.access-change-review').first().getAttribute('aria-label'), id)
  await first.locator('input').fill(await first.locator('.runner-update-actions code').innerText())
  await btn(approver, '独立批准').click(); await approver.getByText('独立批准已完成，需由创建人提交应用。').waitFor()
  await refresh(creator); await btn(creator, '创建人应用').click(); await creator.locator('.tenant-row').filter({ hasText: 'browser-tenant' }).waitFor()
  await btn(creator, '编辑租户 浏览器原名称').click(); await creator.getByLabel('租户名称', { exact: true }).fill('浏览器审阅后名称'); await btn(creator, '创建租户提案').click(); await creator.getByRole('dialog').waitFor({ state: 'hidden' })
  await refresh(approver); const card = approver.locator('.runner-update-card').filter({ hasText: 'pending_approval' })
  await card.getByRole('button', { name: '读取服务端详情' }).click()
  await card.getByText('浏览器原名称', { exact: true }).waitFor(); await card.getByText('浏览器审阅后名称', { exact: true }).waitFor()
  await mkdir('../../output/playwright', { recursive: true }); await card.scrollIntoViewIfNeeded(); await card.screenshot({ path: '../../output/playwright/s3.1a-review-desktop.png' })
  await approver.setViewportSize({ width: 375, height: 812 }); await card.scrollIntoViewIfNeeded()
  assert.equal(await card.evaluate(el => el.scrollWidth <= el.clientWidth), true)
  assert.equal(await card.locator('.access-change-review dd').evaluateAll(nodes => nodes.every(el => el.scrollWidth <= el.clientWidth)), true)
  await card.screenshot({ path: '../../output/playwright/s3.1a-review-mobile.png' })
  await card.locator('input').fill(await card.locator('.runner-update-actions code').innerText()); await card.getByRole('button', { name: '独立批准' }).click()
  await refresh(creator); await btn(creator, '创建人应用').click(); await btn(creator, '编辑租户 浏览器审阅后名称').waitFor()
  await refresh(approver); await btn(approver, '读取服务端详情').first().click(); await approver.getByText('差异已过期，请刷新策略并重新创建提案。').waitFor()
  assert.equal(await btn(approver, '独立批准').count(), 0)
  assert.ok(reads.filter(r => r.actor === 'approver').length >= 5)
  const approvals = writes.filter(r => r.url.endsWith('/approve')); assert.equal(approvals.length, 2); assert.ok(approvals.every(r => r.actor === 'approver' && r.body.digest))
  assert.equal(writes.filter(r => r.url.endsWith('/apply')).length, 2)
  assert.deepEqual(errors, [])
  console.log('S3.1a Chromium：双独立 Cookie 会话→真实 Web 认证/CSRF/Unix 代理→真实 Runner→临时 SQLite；新增、改名、服务端差异、批准、应用、过期和窄屏通过。')
} finally { await Promise.all(contexts.map(c => c.close())); await browser.close() }
