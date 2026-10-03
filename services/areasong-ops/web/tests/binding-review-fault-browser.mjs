import assert from 'node:assert/strict'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const page = await browser.newPage({ viewport: { width: 1280, height: 900 } }); page.setDefaultTimeout(5000)
await page.route('**/*', route => {
  const u = new URL(route.request().url())
  if (u.origin !== 'http://127.0.0.1:4173') return route.abort()
  return u.pathname === '/binding-harness' ? route.fulfill({ contentType: 'text/html', body: '<div id="root"></div>' }) : route.continue()
})
try {
  await page.goto('http://127.0.0.1:4173/binding-harness')
  await page.evaluate(async () => {
    const { default: RefreshRuntime } = await import('/@react-refresh')
    RefreshRuntime.injectIntoGlobalHook(window); window.$RefreshReg$ = () => {}; window.$RefreshSig$ = () => type => type; window.__vite_plugin_react_preamble_installed__ = true
    const { default: React } = await import('/node_modules/.vite/deps/react.js')
    const { default: ReactDOM } = await import('/node_modules/.vite/deps/react-dom_client.js')
    const { AccessControl } = await import('/src/views/AccessControl.tsx')
    const root = ReactDOM.createRoot(document.getElementById('root'))
    const change = { id: '00000000-0000-4000-8000-000000000001', requestDigest: 'sha256:' + 'd'.repeat(64), state: 'pending_approval', actorHash: 'a'.repeat(64), approvalPolicy: 'two_party_v1', requiresDualApproval: true, confirmationPhrase: '确认' }
    const r = window.review = { change, actor: 'b'.repeat(64), tenant: 'default', version: 7, mode: 'ready', queue: [], approvals: 0 }
    r.render = () => root.render(React.createElement(AccessControl, {
      access: { canManage: true, enforced: true, version: r.version, currentSubject: { subject: r.actor, tenantId: r.tenant }, pendingChanges: [{ ...r.change }] },
      loading: false, available: true, error: '', busy: '', onRefresh() {},
      onReadDetail(id, actor) {
        const value = { id: 'binding-' + actor[0], subject: 'c'.repeat(64), tenantId: 'default', roleId: 'viewer', permissions: ['ops.read'], objectIds: [], expiresAt: null, jit: false, bindingApproval: 'default' }
        const detail = { ...r.change, reviewerHash: actor, kind: 'binding', operation: 'create', availability: 'ready', expectedVersion: 7, currentVersion: 7, binding: { before: null, after: value, changedFields: Object.keys(value) } }
        if (r.mode === 'delay') return new Promise(resolve => r.queue.push(() => resolve(detail)))
        if (r.mode === 'fail') return Promise.reject(Error('读取失败'))
        if (r.mode === 'stale') detail.availability = 'stale'
        if (r.mode === 'other') { detail.kind = 'other'; detail.availability = 'unsupported' }
        return Promise.resolve(detail)
      },
      onApproveChange: async () => { r.approvals++ }, onCreateChange: async () => r.change, onApplyChange: async () => {}, onRejectChange: async () => {},
    })); r.render()
  })
  const read = () => page.getByRole('button', { name: /读取.*详情/ }).click()
  const approve = () => page.getByRole('button', { name: '独立批准', exact: true })
  await read(); await page.getByText('binding-b', { exact: true }).waitFor(); assert.equal(await approve().count(), 1)
  for (const mode of ['fail', 'stale', 'other']) {
    await page.evaluate(mode => { window.review.mode = mode }, mode); await read(); await page.getByRole('alert').waitFor()
    assert.equal(await approve().count(), 0); assert.equal(await page.getByText('binding-b', { exact: true }).count(), 0)
  }
  await page.evaluate(() => { window.review.mode = 'delay' }); await read()
  await page.evaluate(() => { const r = window.review; r.actor = 'e'.repeat(64); r.mode = 'ready'; r.render() })
  await read(); await page.getByText('binding-e', { exact: true }).waitFor(); await page.evaluate(() => window.review.queue.shift()())
  assert.equal(await page.getByText('binding-b', { exact: true }).count(), 0)
  for (const axis of ['tenant', 'proposal']) {
    await page.evaluate(() => { window.review.mode = 'delay' }); await read()
    await page.evaluate(axis => { const r = window.review; if (axis === 'tenant') r.tenant = 'isolated'; else r.change = { ...r.change, id: '00000000-0000-4000-8000-000000000002', requestDigest: 'sha256:' + 'f'.repeat(64) }; r.mode = 'ready'; r.render() }, axis)
    await read(); await page.getByText('binding-e', { exact: true }).waitFor(); await page.evaluate(() => window.review.queue.shift()())
  }
  await page.locator('.access-change-review input').fill('确认'); await page.evaluate(() => { window.review.mode = 'stale' }); await approve().click(); await page.getByRole('alert').waitFor()
  assert.equal(await page.evaluate(() => window.review.approvals), 0)
  console.log('binding component: read failure, stale, other bypass, actor/tenant/proposal switch, late response, approval reread passed')
} finally { await browser.close() }
