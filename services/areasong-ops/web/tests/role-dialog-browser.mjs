import assert from 'node:assert/strict'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const page = await browser.newPage(); page.setDefaultTimeout(5000)
await page.route('**/*', route => {
  const u = new URL(route.request().url())
  if (u.origin !== 'http://127.0.0.1:4173') return route.abort()
  if (u.pathname === '/role-harness') return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
  return route.continue()
})
try {
  await page.goto('http://127.0.0.1:4173/role-harness')
  await page.evaluate(async () => {
    const { default: RefreshRuntime } = await import('/@react-refresh')
    RefreshRuntime.injectIntoGlobalHook(window); window.$RefreshReg$ = () => {}; window.$RefreshSig$ = () => type => type; window.__vite_plugin_react_preamble_installed__ = true
    const { default: React } = await import('/node_modules/.vite/deps/react.js')
    const { default: ReactDOM } = await import('/node_modules/.vite/deps/react-dom_client.js')
    const { RoleDialog } = await import('/src/components/RoleDialog.tsx')
    const root = ReactDOM.createRoot(document.getElementById('root'))
    const access = { canManage: true, version: 7, currentSubject: { subject: 'creator' } }
    const h = window.h = { writes: [], mode: 'fail', version: 7, actor: 'creator', generation: 1 }
    h.render = () => root.render(React.createElement(RoleDialog, { key: h.generation, access, roles: [], original: h.original, onCancel: () => root.render(null), onVerify: async () => ({ ...access, version: h.version, currentSubject: { subject: h.actor } }), onCreate: async body => {
      h.writes.push(body)
      if (h.mode === 'delay') await new Promise(resolve => { h.release = resolve })
      if (h.mode === 'fail' || h.mode === 'delay') throw new Error('模拟写入失败，未自动重试')
      if (h.mode === 'conflict') throw new Error('访问策略版本已变化，请重新读取')
      return { id: 'proposal', state: h.mode === 'rejected' ? 'rejected' : 'pending_approval' }
    } }))
    h.render()
  })
  const submit = () => page.getByRole('button', { name: '创建角色提案', exact: true })
  async function fill() { await page.getByLabel('角色 ID', { exact: true }).fill('retry-role'); await page.getByLabel('角色名称', { exact: true }).fill('重试角色'); await page.getByLabel('ops.read', { exact: true }).check() }
  await fill(); await submit().click(); await page.getByRole('alert').waitFor()
  await submit().click(); await page.getByRole('alert').waitFor()
  let writes = await page.evaluate(() => h.writes); assert.equal(writes.length, 2); assert.equal(writes[0].idempotencyKey, writes[1].idempotencyKey)
  await page.getByLabel('角色名称', { exact: true }).fill('改变载荷'); await submit().click(); await page.getByRole('alert').waitFor()
  writes = await page.evaluate(() => h.writes); assert.notEqual(writes[0].idempotencyKey, writes[2].idempotencyKey)
  await page.getByLabel('角色名称', { exact: true }).fill('重试角色')
  await page.evaluate(() => { h.mode = 'delay' }); await submit().click()
  await page.getByRole('button', { name: '提交中' }).waitFor(); await page.locator('form').evaluate(el => { el.requestSubmit(); el.requestSubmit() })
  assert.equal(await page.evaluate(() => h.writes.length), 4)
  await page.evaluate(() => h.release()); await page.getByRole('alert').waitFor()
  await page.keyboard.press('Escape'); await page.getByRole('button', { name: '放弃修改' }).click()
  await page.evaluate(() => { h.generation++; h.mode = 'conflict'; h.version = 9; h.render() }); await fill()
  await submit().click(); await page.getByText('访问策略版本已变化，请重新读取').waitFor()
  writes = await page.evaluate(() => h.writes); assert.equal(writes.at(-1).expectedVersion, 7); assert.equal(writes.at(-1).idempotencyKey, writes[0].idempotencyKey)
  await page.evaluate(() => { h.mode = 'rejected' }); await submit().click(); await page.getByText('同一请求的原提案已撤销。本次未创建新提案，请核对后再次提交。').waitFor()
  await page.evaluate(() => { h.mode = 'success' }); await submit().click(); await page.getByRole('dialog').waitFor({ state: 'hidden' })
  writes = await page.evaluate(() => h.writes); assert.notEqual(writes.at(-1).idempotencyKey, writes.at(-2).idempotencyKey)
  const count = writes.length
  await page.evaluate(() => { h.generation++; h.render() }); await page.getByRole('button', { name: '取消', exact: true }).click(); assert.equal(await page.evaluate(() => h.writes.length), count)
  await page.evaluate(() => { h.generation++; h.original = { id: 'legacy', displayName: '名'.repeat(121), permissions: ['ops.read'], builtIn: false, createdBy: 'creator' }; h.render() })
  assert.equal((await page.getByLabel('角色名称', { exact: true }).inputValue()).length, 121)
  await page.getByLabel('ops.inspect', { exact: true }).check(); await submit().click(); await page.getByRole('dialog').waitFor({ state: 'hidden' })
  assert.equal(await page.evaluate(() => h.writes.at(-1).roles[0].displayName), '名'.repeat(121))
  console.log('S3.2b RoleDialog：失败同键、载荷换键、双击锁、关闭重开、固定版本冲突、不自动重试、撤销后显式重建、取消零写通过。')
} finally { await browser.close() }
