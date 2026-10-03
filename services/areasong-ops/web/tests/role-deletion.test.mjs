import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
function moduleURL(file, imports = {}) {
  let source = readFileSync(new URL(`../src/${file}.ts`, import.meta.url), 'utf8')
  for (const [path, url] of Object.entries(imports)) source = source.replaceAll(`'${path}'`, `'${url}'`)
  return `data:text/javascript;base64,${Buffer.from(ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } }).outputText).toString('base64')}`
}
const { roleDeletionRequest, roleDeletionRestriction, roleDeletionKey, forgetRoleDeletionKey } = await import(moduleURL('roleChange'))
const { applyReviewedAccessChange } = await import(moduleURL('accessChangeApply', { './accessChangeReview': moduleURL('accessChangeReview') }))
const role = { id: 'custom', displayName: '合成角色', permissions: ['ops.read', 'ops.read'], builtIn: false, createdBy: 'a'.repeat(64) }
const access = { canManage: true, version: 7, roles: [role], currentSubject: { subject: 'creator', tenantId: 'default' } }
test('删除精确单目标、固定版本，保护对象及已知引用不得提交', () => {
  assert.deepEqual(roleDeletionRequest(role, access), { removeRoleIds: ['custom'], expectedVersion: 7, requiresDualApproval: true })
  for (const patch of [{ id: 'viewer' }, { id: 'custom ' }, { builtIn: true }, { createdBy: undefined }, { createdBy: 'bootstrap' }, { permissions: [] }, { permissions: ['*'] }, { permissions: ['unknown'] }, { displayName: '' }]) assert.ok(roleDeletionRestriction({ ...role, ...patch }, access))
  for (const patch of [{ bindings: [{ roleId: 'custom', expiresAt: '2000-01-01' }] }, { principals: { user: { roles: ['custom'] } } }, { principalList: [{ roles: ['custom'] }] }, { roles: [role, { ...role, id: 'CUSTOM' }] }, { roles: [] }]) assert.throws(() => roleDeletionRequest(role, { ...access, ...patch }))
  for (const version of [0, -1, 1.1, undefined]) assert.throws(() => roleDeletionRequest(role, { ...access, version }))
})
test('删除幂等按身份、租户、目标和版本隔离，关闭/失败保留，撤销后显式换键', () => {
  const saved = new Map(), storage = { getItem: k => saved.get(k), setItem: (k, v) => saved.set(k, v), removeItem: k => saved.delete(k) }
  const body = roleDeletionRequest(role, access), key = roleDeletionKey('creator', 'default', body, storage)
  assert.match(key, /^[0-9a-f-]{36}$/)
  assert.equal(roleDeletionKey('creator', 'default', structuredClone(body), storage), key)
  for (const [actor, tenant, request] of [['other', 'default', body], ['creator', 'other', body], ['creator', 'default', { ...body, expectedVersion: 8 }], ['creator', 'default', { ...body, removeRoleIds: ['other'] }]]) assert.notEqual(roleDeletionKey(actor, tenant, request, storage), key)
  forgetRoleDeletionKey('creator', 'default', body, storage)
  assert.notEqual(roleDeletionKey('creator', 'default', body, storage), key)
})
function applyFixture() {
  const change = { id: 'proposal', requestDigest: 'digest', actorHash: 'creator', approvedByHash: 'approver', state: 'approved', requiresDualApproval: true, approvalPolicy: 'two_party_v1' }
  let view = { ...structuredClone(access), pendingChanges: [change] }, writes = 0, loseResponse = false
  const detail = { ...change, reviewerHash: 'creator', kind: 'role_deletion', availability: 'ready', operation: 'delete', expectedVersion: 7, currentVersion: 7, roleDeletion: { before: { id: role.id, displayName: role.displayName, permissions: role.permissions }, references: { bindings: 'none', directPrincipals: 'none' } } }
  const api = { access: async () => structuredClone(view), accessChangeDetail: async () => detail, applyAccessChange: async () => { writes++; view = { ...view, version: 8, roles: [], pendingChanges: [{ ...change, state: 'applied' }] }; if (loseResponse) throw Error('响应丢失'); return view.pendingChanges[0] } }
  return { api, change, detail, writes: () => writes, lose: () => { loseResponse = true }, setView: v => { view = { ...view, ...v } } }
}
test('通用应用入口拒绝无引用证明、未知分支、身份、版本、摘要及未批准提案', async () => {
  for (const patch of [{ availability: 'stale' }, { availability: 'unsupported', reason: 'binding_reference' }, { kind: 'unsupported' }, { kind: 'other' }, { requestDigest: 'other' }, { reviewerHash: 'other' }, { currentVersion: 8 }, { roleDeletion: undefined }]) {
    const f = applyFixture(); Object.assign(f.detail, patch)
    await assert.rejects(applyReviewedAccessChange(f.api, access, f.change)); assert.equal(f.writes(), 0)
  }
  for (const patch of [{ canManage: false }, { currentSubject: { subject: 'other' } }, { pendingChanges: [] }]) {
    const f = applyFixture(); f.setView(patch); await assert.rejects(applyReviewedAccessChange(f.api, access, f.change)); assert.equal(f.writes(), 0)
  }
  const f = applyFixture(); f.api.accessChangeDetail = async () => { f.setView({ currentSubject: { subject: 'other' } }); return f.detail }
  await assert.rejects(applyReviewedAccessChange(f.api, access, f.change)); assert.equal(f.writes(), 0)
})
test('正常应用/响应丢失只写一次，已应用重入不读失效详情不写第二次', async () => {
  for (const lost of [false, true]) {
    const f = applyFixture(); if (lost) f.lose()
    assert.equal(await applyReviewedAccessChange(f.api, access, f.change), 'custom')
    f.api.accessChangeDetail = async () => { throw Error('终态不应读取详情') }
    await applyReviewedAccessChange(f.api, access, f.change); assert.equal(f.writes(), 1)
  }
})
test('应用成功后必须核对目标消失，真实失败保持错误且不自动重试', async () => {
  const f = applyFixture(); f.api.applyAccessChange = async () => { f.setView({ pendingChanges: [{ ...f.change, state: 'applied' }] }); return { ...f.change, state: 'applied' } }
  await assert.rejects(applyReviewedAccessChange(f.api, access, f.change), /角色仍在/)
  const g = applyFixture(); let writes = 0; g.api.applyAccessChange = async () => { writes++; throw Error('引用冲突') }
  await assert.rejects(applyReviewedAccessChange(g.api, access, g.change), /引用冲突/); assert.equal(writes, 1)
})
