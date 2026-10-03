// 真实 AccessControl 组件的可控延迟/身份切换测试；故障注入使用内存读取函数。
import assert from 'node:assert/strict'
const { chromium } = await import(process.env.PLAYWRIGHT_MODULE || 'playwright')
const browser = await chromium.launch({ headless: true, executablePath: process.env.PLAYWRIGHT_EXECUTABLE })
const page = await browser.newPage({ viewport: { width: 1280, height: 900 } }); page.setDefaultTimeout(5000)
await page.route('**/*', route => {
  const u = new URL(route.request().url())
  if (u.origin !== 'http://127.0.0.1:4173') return route.abort()
  if (u.pathname === '/review-harness') return route.fulfill({ contentType: 'text/html', body: '<div id="review-root"></div>' })
  return route.continue()
})
try {
  await page.goto('http://127.0.0.1:4173/review-harness')
  await page.evaluate(async () => {
    const { default: RefreshRuntime } = await import('/@react-refresh')
    RefreshRuntime.injectIntoGlobalHook(window); window.$RefreshReg$ = () => {}; window.$RefreshSig$ = () => type => type; window.__vite_plugin_react_preamble_installed__ = true
    const { default: React } = await import('/node_modules/.vite/deps/react.js')
    const { default: ReactDOM } = await import('/node_modules/.vite/deps/react-dom_client.js')
    const { AccessControl } = await import('/src/views/AccessControl.tsx')
    const root = ReactDOM.createRoot(document.getElementById('review-root'))
    const c = { id: 'one', requestDigest: 'digest-one', state: 'pending_approval', actorHash: 'creator', approvalPolicy: 'two_party_v1', confirmationPhrase: '确认' }
    window.review = { actor: 'approver', change: c, version: 7, mode: 'ready', queue: [], approvals: 0 }
    window.review.render = () => root.render(React.createElement(AccessControl, {
      access: { canManage: true, enforced: true, version: window.review.version, currentSubject: { subject: window.review.actor }, pendingChanges: [{ ...window.review.change }] },
      loading: false, available: true, error: '', busy: '', onRefresh() {},
      onRead: undefined,
      onReadDetail(id, actor) {
        const r = window.review
        const result = { ...r.change, reviewerHash: actor, kind: 'tenant', availability: 'ready', operation: 'create', expectedVersion: 7, currentVersion: 7, after: { id, displayName: r.mode === 'long' ? '长名称'.repeat(40) : `名称-${id}-${actor}`, status: 'active' } }
        if (r.mode === 'delay') return new Promise(resolve => r.queue.push(() => resolve(result)))
        if (r.mode === 'fail') return Promise.reject(new Error('权限不足，请刷新'))
        if (r.mode === 'stale') return Promise.resolve({ ...result, availability: 'stale', after: undefined })
        if (r.mode === 'unsupported') return Promise.resolve({ ...result, kind: 'unsupported', availability: 'unsupported', after: undefined })
        if (r.mode === 'other') return Promise.resolve({ ...result, kind: 'other', availability: 'unsupported', after: undefined })
        return Promise.resolve(result)
      },
      onApproveChange: async () => { window.review.approvals++ }, onCreateChange: async () => c, onApplyChange: async () => {}, onRejectChange: async () => {},
    }))
    window.review.render()
  })
  const read = () => page.getByRole('button', { name: /读取.*详情/ }).click()
  const approve = () => page.getByRole('button', { name: '独立批准', exact: true })
  await read(); await page.getByText('名称-one-approver', { exact: true }).waitFor(); assert.equal(await approve().count(), 1)
  await page.evaluate(() => { window.review.mode = 'fail' }); await read(); await page.getByRole('alert').waitFor()
  assert.equal(await page.getByText('名称-one-approver', { exact: true }).count(), 0); assert.equal(await approve().count(), 0)
  for (const mode of ['stale', 'unsupported']) { await page.evaluate(mode => { window.review.mode = mode }, mode); await read(); await page.getByRole('alert').waitFor(); assert.equal(await approve().count(), 0) }
  await page.evaluate(() => { window.review.mode = 'delay' }); await read()
  await page.evaluate(() => { window.review.actor = 'second'; window.review.mode = 'ready'; window.review.render() })
  await read(); await page.getByText('名称-one-second', { exact: true }).waitFor()
  await page.evaluate(() => window.review.queue.shift()()); assert.equal(await page.getByText('名称-one-approver', { exact: true }).count(), 0)
  await page.evaluate(() => { window.review.mode = 'delay' }); await read()
  await page.evaluate(() => { window.review.change = { ...window.review.change, id: 'two', requestDigest: 'digest-two' }; window.review.mode = 'ready'; window.review.render() })
  await read(); await page.getByText('名称-two-second', { exact: true }).waitFor()
  await page.evaluate(() => window.review.queue.shift()()); assert.equal(await page.getByText('名称-one-second', { exact: true }).count(), 0)
  await page.locator('.access-change-review input').fill('确认')
  await page.evaluate(() => { window.review.mode = 'stale' }); await approve().click(); await page.getByRole('alert').waitFor()
  assert.equal(await page.evaluate(() => window.review.approvals), 0)
  await page.evaluate(() => { window.review.mode = 'other' }); await read(); await page.locator('.access-change-review input').fill('确认'); await approve().click()
  await page.waitForFunction(() => window.review.approvals === 1)
  await page.evaluate(() => { window.review.mode = 'long' }); await read()
  await page.setViewportSize({ width: 375, height: 812 })
  assert.equal(await page.locator('.access-change-review dd').evaluateAll(nodes => nodes.every(el => el.scrollWidth <= el.clientWidth)), true)
  await page.evaluate(() => { window.review.actor = 'creator'; window.review.render() })
  // createRoot.render 异步提交；先观察身份已呈现在 DOM，再检查批准入口。
  await page.locator('.subject-strip small').filter({ hasText: 'creator' }).waitFor()
  assert.equal(await approve().count(), 0)
  console.log('S3.1a 真实 AccessControl 组件：读取失败清除、stale/unsupported 禁批、身份/提案切换及迟到响应、批准前漂移、既有非租户批准通过（内存故障注入）。')
} finally { await browser.close() }
