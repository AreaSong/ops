import assert from 'node:assert/strict'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const page = await browser.newPage(); page.setDefaultTimeout(5000)
await page.route('**/*', route => {
  const u = new URL(route.request().url())
  if (u.origin !== 'http://127.0.0.1:4173') return route.abort()
  if (u.pathname === '/lifecycle-harness') return route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' })
  return route.continue()
})
try {
  await page.goto('http://127.0.0.1:4173/lifecycle-harness')
  await page.evaluate(async () => {
    const { default: RefreshRuntime } = await import('/@react-refresh')
    RefreshRuntime.injectIntoGlobalHook(window); window.$RefreshReg$ = () => {}; window.$RefreshSig$ = () => type => type; window.__vite_plugin_react_preamble_installed__ = true
    const { default: React } = await import('/node_modules/.vite/deps/react.js')
    const { default: ReactDOM } = await import('/node_modules/.vite/deps/react-dom_client.js')
    const { AccessControl } = await import('/src/views/AccessControl.tsx')
    const root = ReactDOM.createRoot(document.getElementById('root'))
    const role = { id: 'custom', displayName: '合成角色', permissions: ['ops.read'], builtIn: false, createdBy: 'a'.repeat(64) }
    const c = { id: 'old-proposal', requestDigest: 'digest', state: 'approved', actorHash: 'creator', approvedByHash: 'approver', requiresDualApproval: true, approvalPolicy: 'two_party_v1' }
    const h = window.h = { actor: 'creator', tenant: 'default', kind: 'create' }
    h.render = () => {
      const access = { canManage: true, version: 7, roles: [role], currentSubject: { subject: h.actor, tenantId: h.tenant }, pendingChanges: h.kind === 'apply' ? [c] : [] }
      root.render(React.createElement(AccessControl, { access, available: true, loading: false, error: '', busy: '', onRefresh() {}, onVerifyActor: async () => access,
        onCreateChange: async () => new Promise(resolve => { h.release = () => resolve({ ...c, state: 'pending_approval' }) }),
        onApplyChange: async () => new Promise(resolve => { h.release = () => resolve('custom') }),
      }))
    }
    h.render()
  })
  const frame = () => page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))))
  for (const kind of ['create', 'apply']) for (const change of ['actor', 'tenant', 'roundtrip']) {
    await page.evaluate(kind => { h.actor = 'creator'; h.tenant = 'default'; h.kind = kind; h.release = null; h.render() }, kind); await frame()
    if (kind === 'create') { await page.getByRole('button', { name: '删除角色 合成角色', exact: true }).click(); await page.getByRole('button', { name: '创建删除提案', exact: true }).last().click() }
    else await page.getByRole('button', { name: '创建人应用', exact: true }).click()
    await page.waitForFunction(() => typeof h.release === 'function')
    await page.evaluate(change => { if (change === 'tenant') h.tenant = 'other'; else h.actor = 'other'; h.render() }, change); await frame()
    if (change === 'roundtrip') { await page.evaluate(() => { h.actor = 'creator'; h.render() }); await frame() }
    await page.evaluate(() => h.release()); await frame()
    assert.equal(await page.getByText(/删除提案 old-prop|角色 custom 已删除/).count(), 0, `${kind}/${change} must discard late notice`)
  }
  console.log('S3.2d2 父组件迟到通知：创建与应用结果在身份切换、租户切换及身份往返后均作废。')
} finally { await browser.close() }
