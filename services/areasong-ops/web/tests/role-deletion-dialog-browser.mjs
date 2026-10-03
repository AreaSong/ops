import assert from 'node:assert/strict'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const page = await browser.newPage(); page.setDefaultTimeout(5000)
await page.route('**/*', route => {
  const u = new URL(route.request().url())
  if (u.origin !== 'http://127.0.0.1:4173') return route.abort()
  if (u.pathname === '/delete-harness') return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
  return route.continue()
})
try {
  await page.goto('http://127.0.0.1:4173/delete-harness')
  await page.evaluate(async () => {
    const { default: RefreshRuntime } = await import('/@react-refresh')
    RefreshRuntime.injectIntoGlobalHook(window); window.$RefreshReg$ = () => {}; window.$RefreshSig$ = () => type => type; window.__vite_plugin_react_preamble_installed__ = true
    const { default: React } = await import('/node_modules/.vite/deps/react.js')
    const { default: ReactDOM } = await import('/node_modules/.vite/deps/react-dom_client.js')
    const { RoleDeletionDialog } = await import('/src/components/RoleDeletionDialog.tsx')
    const root = ReactDOM.createRoot(document.getElementById('root'))
    const role = { id: 'custom', displayName: '合成角色', permissions: ['ops.read'], builtIn: false, createdBy: 'a'.repeat(64) }
    const access = { canManage: true, version: 7, currentSubject: { subject: 'creator', tenantId: 'default' }, roles: [role] }
    const h = window.h = { writes: [], mode: 'fail', generation: 1, current: structuredClone(access) }
    h.render = () => root.render(React.createElement(RoleDeletionDialog, { key: h.generation, access, role, onCancel: () => root.render(null), onVerify: async () => {
      const result = structuredClone(h.current)
      if (h.mode === 'verify-delay') await new Promise(resolve => { h.release = resolve })
      return result
    }, onCreate: async body => {
      h.writes.push(body)
      if (h.mode === 'delay') await new Promise(resolve => { h.release = resolve })
      if (['fail', 'delay'].includes(h.mode)) throw Error('网络失败，请核对后手动重试')
      return { id: 'proposal', state: h.mode === 'rejected' ? 'rejected' : 'pending_approval' }
    } }))
    h.render()
  })
  const submit = () => page.getByRole('button', { name: '创建删除提案', exact: true })
  const cancel = () => page.getByRole('button', { name: '取消', exact: true })
  await page.getByRole('dialog').waitFor(); await page.keyboard.press('Enter'); assert.equal(await page.evaluate(() => h.writes.length), 0)
  await submit().click(); await page.getByRole('alert').waitFor(); await submit().click(); await page.getByRole('alert').waitFor()
  let writes = await page.evaluate(() => h.writes); assert.equal(writes.length, 2); assert.equal(writes[0].idempotencyKey, writes[1].idempotencyKey)
  await cancel().click(); await page.getByRole('dialog').waitFor({ state: 'hidden' })
  await page.evaluate(() => { h.generation++; h.render() }); await submit().click(); await page.getByRole('alert').waitFor()
  writes = await page.evaluate(() => h.writes); assert.equal(writes[2].idempotencyKey, writes[0].idempotencyKey)
  await page.evaluate(() => { h.mode = 'delay' }); await submit().click(); await page.getByRole('button', { name: '提交中' }).waitFor()
  await page.getByRole('button', { name: '提交中' }).evaluate(el => { el.click(); el.click() }); await page.keyboard.press('Escape')
  assert.equal(await page.getByRole('dialog').count(), 1); assert.equal(await page.evaluate(() => h.writes.length), 4)
  await page.evaluate(() => h.release()); await page.getByRole('alert').waitFor()
  await page.evaluate(() => { h.mode = 'rejected' }); await submit().click(); await page.getByText('原删除提案已撤销。本次未创建新提案，请核对后再次提交。').waitFor()
  await page.evaluate(() => { h.mode = 'fail' }); await submit().click(); await page.getByRole('alert').waitFor()
  writes = await page.evaluate(() => h.writes); assert.notEqual(writes[5].idempotencyKey, writes[4].idempotencyKey)
  await page.evaluate(() => { h.current.version = 8 }); await submit().click(); await page.getByText('策略版本已变化，请关闭弹窗并刷新后重新核对').waitFor()
  assert.equal(await page.evaluate(() => h.writes.length), 6)
  await page.evaluate(() => { h.current.version = 7; h.current.currentSubject.tenantId = 'other' }); await submit().click(); await page.getByText('会话身份已变化，请关闭弹窗并刷新后重新核对').waitFor()
  assert.equal(await submit().isDisabled(), true); assert.equal(await page.evaluate(() => h.writes.length), 6)
  await cancel().click(); await page.getByRole('dialog').waitFor({ state: 'hidden' })
  await page.evaluate(() => { h.generation++; h.current.currentSubject.tenantId = 'default'; h.mode = 'verify-delay'; h.render() })
  await submit().click(); await page.getByRole('button', { name: '提交中' }).waitFor()
  await page.evaluate(() => { h.generation++; h.render() }); await submit().waitFor(); await page.evaluate(() => h.release())
  assert.equal(await page.evaluate(() => h.writes.length), 6)
  console.log('S3.2d2 删除弹窗：精确请求、失败原键、关闭重开、双击锁、撤销后显式换键、固定版本、租户身份切换、迟到只读结果、取消/Enter 零写通过（合成故障夹具）。')
} finally { await browser.close() }
