import assert from 'node:assert/strict'
import { writeFile, access, mkdir } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const url = process.env.BR_URL
assert.match(url, /^http:\/\/127\.0\.0\.1:\d+$/)
const out = new URL(`../../output/s3.4b2b/b-r/browser/run-${Date.now()}/`, import.meta.url)
await mkdir(out, { recursive: true })
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const errors = [], posts = [], screenshots = []
const btn = (page, name) => page.getByRole('button', { name, exact: true })
let creator
async function session(actor) {
 const context = await browser.newContext({ viewport: { width: 1280, height: 960 } })
 await context.addCookies([{ name: 'review_actor', value: actor, url }])
 await context.route('**/*', r => new URL(r.request().url()).origin === url ? r.continue() : r.abort())
 const page = await context.newPage(); page.setDefaultTimeout(12000)
 page.on('pageerror', error => errors.push(error.message))
 page.on('request', r => { if (r.method() === 'POST' && new URL(r.url()).pathname.startsWith('/api/plans')) posts.push({ actor, path: new URL(r.url()).pathname, body: r.postDataJSON() }) })
 await page.goto(url); await btn(page, '服务操作').click(); return page
}
async function screenshot(page, name) {
 const path = new URL(name + '.png', out).pathname
 await assert.rejects(access(path)); await page.screenshot({ path, fullPage: true }); screenshots.push(name)
}
async function refresh(page) { await page.getByTitle('刷新全部服务状态').click() }
try {
 creator = await session('creator')
 await creator.getByRole('button', { name: /^更新 demo/ }).click()
 await btn(creator, '创建计划').click()
 await creator.getByRole('dialog').getByText('批准发布计划', { exact: true }).waitFor()
 await btn(creator, '取消').click()
 const approver = await session('approver')
 await btn(approver, '查看计划').click()
 const dialog = approver.getByRole('dialog')
 await dialog.getByRole('textbox').fill(await dialog.locator('.confirmation-field code').innerText())
 await btn(dialog, '批准计划').focus(); await approver.keyboard.press('Enter')
 await dialog.getByText('执行已批准计划', { exact: true }).waitFor()
 assert.equal(await btn(dialog, '当前身份不能执行').isDisabled(), true)
 await refresh(creator); await btn(creator, '查看计划').click()
 await btn(creator.getByRole('dialog'), '执行计划').click()
 await creator.getByRole('dialog', { name: '执行已批准计划' }).waitFor({ state: 'hidden' })
 await creator.evaluate(() => fetch('/__br/control?blocked=true', { method: 'POST' }))
 // 重新读取产品HTTP状态，刷新不会执行任务。
 await creator.reload(); await btn(creator, '服务操作').click(); await btn(creator, '查看计划').click()
 const close = btn(creator.getByRole('dialog'), '确认收口')
 await close.click()
 await creator.getByRole('dialog').getByText('本次尝试已结束', { exact: false }).waitFor()
 for (const [name, width, colorScheme] of [['closure-blocked-desktop', 1280, 'light'], ['closure-blocked-narrow', 390, 'light'], ['closure-blocked-dark', 390, 'dark']]) {
  await creator.setViewportSize({ width, height: 960 }); await creator.emulateMedia({ colorScheme })
  assert.equal(await creator.getByRole('dialog').evaluate(el => el.scrollWidth <= el.clientWidth), true)
  await screenshot(creator, name)
  await close.scrollIntoViewIfNeeded()
  const box = await close.boundingBox()
  assert.ok(box && box.y >= 0 && box.y + box.height <= 960, '收口按钮滚动后完整可见')
  await screenshot(creator, name + '-controls')
 }
 await creator.evaluate(() => fetch('/__br/control?blocked=false', { method: 'POST' }))
 await close.focus(); await creator.keyboard.press('Enter'); await creator.getByRole('dialog').waitFor({ state: 'hidden' })
 const plans = await creator.evaluate(async () => (await (await fetch('/api/plans')).json()).plans)
 assert.equal(plans.length, 1); assert.equal(plans[0].state, 'completed')
 const closures = posts.filter(p => p.path.endsWith('/close'))
 assert.equal(closures.length, 2); assert.notEqual(closures[0].body.idempotencyKey, closures[1].body.idempotencyKey)
 assert.equal(posts.filter(p => p.path.endsWith('/execute')).length, 1)
 assert.deepEqual(errors, [])
 await writeFile(new URL('result.json', out), JSON.stringify({ scope: '真实App→Web代理→Runner→临时SQLite；合成Cookie、执行器和回环Alertmanager，不证明生产profile', posts, screenshots, errors, completed: true }, null, 2) + '\n', { flag: 'wx' })
 console.log(JSON.stringify({ completed: true, posts: posts.length, screenshots, evidence: out.pathname }))
} catch (error) {
 if (creator) { console.error((await creator.locator('body').innerText()).slice(0, 12000)); await creator.screenshot({ path: new URL('failure-' + Date.now() + '.png', out).pathname, fullPage: true }) }
 throw error
} finally { await browser.close() }
