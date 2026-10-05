import assert from 'node:assert/strict'
import { mkdir, writeFile, access } from 'node:fs/promises'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const origin = process.env.BP_BROWSER_URL
assert.match(origin, /^http:\/\/127\.0\.0\.1:\d+$/)
const out = new URL('../../output/s3.4b2b/b-p/browser/', import.meta.url)
await mkdir(out, { recursive: true })
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const errors = [], checks = []
try {
  const context = await browser.newContext({ viewport: { width: 1280, height: 960 } })
  await context.route('**/*', route => new URL(route.request().url()).origin === origin ? route.continue() : route.abort())
  const page = await context.newPage()
  page.on('pageerror', error => errors.push(error.message))
  await page.goto(origin + '/tests/release-preparation-browser.html')
  const dialog = page.getByRole('dialog')
  await dialog.waitFor()
  assert.equal(await dialog.getByText('9223372036854775807', { exact: false }).count(), 1)
  for (const [label, width, colorScheme] of [['desktop', 1280, 'light'], ['narrow', 390, 'light'], ['dark', 390, 'dark']]) {
    await page.setViewportSize({ width, height: 960 })
    await page.emulateMedia({ colorScheme })
    const geometry = await dialog.evaluate(element => ({
      dialogFits: element.scrollWidth <= element.clientWidth,
      fieldsFit: [...element.querySelectorAll('dd')].every(item => item.scrollWidth <= item.clientWidth),
    }))
    assert.equal(geometry.dialogFits, true, label + ' dialog overflow')
    assert.equal(geometry.fieldsFit, true, label + ' fields overflow')
    const path = new URL('review-' + label + '.png', out).pathname
    await assert.rejects(access(path))
    await dialog.screenshot({ path })
    checks.push({ label, width, colorScheme, ...geometry })
  }
  const approve = dialog.getByRole('button', { name: '批准计划', exact: true })
  assert.equal(await approve.isDisabled(), true)
  await dialog.getByRole('textbox').fill('批准合成计划')
  assert.equal(await approve.isEnabled(), true)
  await approve.focus()
  await page.keyboard.press('Enter')
  assert.equal(await page.locator('body').getAttribute('data-approvals'), '1')
  await dialog.getByRole('button', { name: '取消', exact: true }).click()
  await page.getByRole('button', { name: '旧观察计划', exact: true }).click()
  assert.equal(await dialog.getByText('旧计划缺少完整目标代次', { exact: false }).count(), 1)
  assert.equal(await dialog.getByRole('button', { name: '确认收口', exact: true }).isDisabled(), true)
  await page.keyboard.press('Enter')
  assert.equal(await page.locator('body').getAttribute('data-closed'), null)
  await dialog.getByRole('button', { name: '取消', exact: true }).click()
  await page.getByRole('button', { name: '已批准计划', exact: true }).click()
  assert.equal(await dialog.getByRole('button', { name: '执行计划', exact: true }).isDisabled(), true)
  assert.deepEqual(errors, [])
  await writeFile(new URL('result.json', out), JSON.stringify({
    scope: '真实 React 审阅组件＋合成页面，不证明后端或真实 profile',
    checks, keyboardApproval: true, oldClosureDisabled: true, executionDisabled: true, errors,
  }, null, 2) + '\n', { flag: 'wx' })
  console.log(JSON.stringify({ checks: checks.length, errors, status: 'passed' }))
} finally { await browser.close() }
