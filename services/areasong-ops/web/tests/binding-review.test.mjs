import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
function moduleURL(file, imports = {}) {
  let source = readFileSync(new URL(`../src/${file}.ts`, import.meta.url), 'utf8')
  for (const [path, url] of Object.entries(imports)) source = source.replaceAll(`'${path}'`, `'${url}'`)
  return `data:text/javascript;base64,${Buffer.from(ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } }).outputText).toString('base64')}`
}
const { reviewAllowsApproval: allows } = await import(moduleURL('accessChangeReview'))
const { applyReviewedAccessChange: apply } = await import(moduleURL('accessChangeApply', { './accessChangeReview': moduleURL('accessChangeReview') }))
const actor = 'a'.repeat(64), approver = 'b'.repeat(64)
const value = { id: 'binding-one', subject: 'c'.repeat(64), tenantId: 'default', roleId: 'viewer', permissions: ['ops.read'], objectIds: [], expiresAt: null, jit: false, bindingApproval: 'default' }
const fields = Object.keys(value)
const change = { id: '00000000-0000-4000-8000-000000000001', requestDigest: 'sha256:' + 'd'.repeat(64), actorHash: actor, approvedByHash: approver, state: 'approved', approvalPolicy: 'two_party_v1', requiresDualApproval: true }
function detail() { return { id: change.id, requestDigest: change.requestDigest, state: change.state, reviewerHash: actor, kind: 'binding', availability: 'ready', expectedVersion: 7, currentVersion: 7, operation: 'create', binding: { before: null, after: structuredClone(value), changedFields: fields } } }
test('严格 binding create/edit/revoke，字段与差异不能缺失或矛盾', () => {
  const d = detail(); assert.equal(allows(d, change, 7), true)
  for (const field of fields) { const v = structuredClone(d); delete v.binding.after[field]; assert.equal(allows(v, change, 7), false, field) }
  for (const patch of [{ jit: true }, { subject: 'x@y' }, { tenantId: '*' }, { id: 'UPPER' }, { permissions: ['future'] }, { objectIds: [null] }, { expiresAt: 'tomorrow' }, { approvedAt: '2000-01-01' }, { bindingApproval: 'pending' }]) {
    assert.equal(allows({ ...d, binding: { ...d.binding, after: { ...value, ...patch } } }, change, 7), false)
  }
  for (const patch of [{ kind: 'other', availability: 'unsupported' }, { kind: 'tenant' }, { operation: 'future' }, { availability: 'stale' }, { currentVersion: 8 }, { requestDigest: 'bad' }, { reviewerHash: '' }, { role: {} }, { roleDeletion: {} }, { before: {} }, { binding: undefined }]) assert.equal(allows({ ...d, ...patch }, change, 7), false)
  const before = { ...value, objectIds: ['one', 'one'], expiresAt: '2000-01-01T00:00:00Z' }
  const edit = { ...d, operation: 'edit', binding: { before, after: value, changedFields: ['objectIds', 'expiresAt'] } }
  assert.equal(allows(edit, change, 7), true)
  assert.equal(allows({ ...edit, binding: { ...edit.binding, changedFields: [] } }, change, 7), false)
  assert.equal(allows({ ...edit, binding: { ...edit.binding, after: { ...value, subject: actor } } }, change, 7), false)
  assert.equal(allows({ ...d, operation: 'revoke', binding: { before: value, after: null, changedFields: fields } }, change, 7), true)
  assert.equal(allows({ id: change.id, requestDigest: change.requestDigest, state: change.state, kind: 'other', availability: 'unsupported' }, change, 7), true)
})
function fixture(operation = 'create') {
  const d = detail(); if (operation === 'revoke') { d.operation = operation; d.binding = { before: value, after: null, changedFields: fields } }
  const expected = { canManage: true, version: 7, currentSubject: { subject: actor, tenantId: 'default' }, pendingChanges: [change] }
  let view = structuredClone(expected), writes = 0, lost = false
  const api = { access: async () => structuredClone(view), accessChangeDetail: async () => d, applyAccessChange: async () => {
    writes++; view = { ...view, version: 8, bindings: operation === 'revoke' ? [] : [{ ...value, permissions: undefined, bindingApproval: undefined }], roles: [{ id: 'viewer', permissions: ['ops.read'] }], pendingChanges: [{ ...change, state: 'applied', appliedPolicyVersion: 8 }] }
    if (lost) throw Error('response lost')
    return view.pendingChanges[0]
  } }
  return { api, d, expected, writes: () => writes, lose: () => { lost = true }, patch: p => { Object.assign(view, p) } }
}
test('公共 apply 正常及响应丢失只写一次；成功重放不读 before', async () => {
  for (const operation of ['create', 'revoke']) for (const lost of [false, true]) {
    const f = fixture(operation); if (lost) f.lose()
    await apply(f.api, f.expected, change)
    f.api.accessChangeDetail = async () => { throw Error('must not read') }
    await apply(f.api, f.expected, change); assert.equal(f.writes(), 1)
  }
})
test('公共 apply 拒绝身份、版本、摘要漂移和 other 降级；读回不足不重写', async () => {
  for (const patch of [{ kind: 'other', availability: 'unsupported' }, { reviewerHash: approver }, { currentVersion: 8 }, { requestDigest: 'bad' }]) {
    const f = fixture(); Object.assign(f.d, patch); await assert.rejects(apply(f.api, f.expected, change)); assert.equal(f.writes(), 0)
  }
  for (const patch of [{ version: 9 }, { bindings: undefined }, { bindings: [{ ...value, approvedAt: '2000-01-01T00:00:00Z' }] }, { roles: [] }]) {
    const f = fixture(), real = f.api.applyAccessChange; f.api.applyAccessChange = async () => { const result = await real(); f.patch(patch); return result }
    await assert.rejects(apply(f.api, f.expected, change), /已应用，当前结果待核对/); assert.equal(f.writes(), 1)
  }
  const f = fixture(); f.api.accessChangeDetail = async () => { f.patch({ currentSubject: { subject: approver } }); return f.d }
  await assert.rejects(apply(f.api, f.expected, change), /身份/); assert.equal(f.writes(), 0)
})

test('期限读回保留纳秒精度且接受等价时区', async () => {
  for (const [readback, valid] of [['2030-01-01T08:00:00.123456789+08:00', true], ['2030-01-01T00:00:00.123999999Z', false]]) {
    const f = fixture(); f.d.binding.after.expiresAt = '2030-01-01T00:00:00.123456789Z'
    const real = f.api.applyAccessChange
    f.api.applyAccessChange = async () => { const result = await real(); f.patch({ bindings: [{ ...value, expiresAt: readback }] }); return result }
    if (valid) await apply(f.api, f.expected, change)
    else await assert.rejects(apply(f.api, f.expected, change), /已应用，当前结果待核对/)
    assert.equal(f.writes(), 1)
  }
})
