import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
const source = readFileSync(new URL('../src/accessChangeReview.ts', import.meta.url), 'utf8')
const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } })
const { reviewAllowsApproval: allows } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
const change = { id: 'one', requestDigest: 'digest', state: 'pending_approval' }
const detail = { ...change, kind: 'tenant', availability: 'ready', expectedVersion: 7, currentVersion: 7, operation: 'create', after: { id: 'tenant-one', displayName: 'One', status: 'active' } }
test('租户详情必须绑定同一提案、摘要、状态和版本', () => {
  assert.equal(allows(detail, change, 7), true)
  for (const patch of [{ id: 'two' }, { requestDigest: 'other' }, { state: 'applied' }, { expectedVersion: 6 }, { currentVersion: 8 }, { availability: 'stale' }, { availability: 'unsupported' }, { availability: 'unavailable' }, { after: undefined }, { operation: 'unknown' }]) assert.equal(allows({ ...detail, ...patch }, change, 7), false)
  assert.equal(allows(detail, change), false)
})
test('新增不得带旧值；改名必须同目标同状态；原非租户流程兼容', () => {
  assert.equal(allows({ ...detail, before: detail.after }, change, 7), false)
  const edit = { ...detail, operation: 'rename', before: { ...detail.after, displayName: 'Old' } }
  assert.equal(allows(edit, change, 7), true)
  assert.equal(allows({ ...edit, before: { ...edit.before, id: 'other' } }, change, 7), false)
  assert.equal(allows({ ...change, kind: 'other', availability: 'unsupported' }, change, 7), true)
  assert.equal(allows({ ...change, kind: 'unsupported', availability: 'unsupported' }, change, 7), false)
})
test('角色详情由独立字段承载，角色 ready 可审阅，混合提案仍拒绝', () => {
  const role = { after: { id: 'custom', displayName: 'Custom', permissions: ['ops.read'] }, permissionDiff: { added: ['ops.read'], removed: [], unchanged: [] }, impact: { bindingCount: 0, tenantCount: 0, affectsExistingBindings: false } }
  for (const availability of ['ready', 'stale', 'unsupported', 'unavailable']) {
    assert.equal(allows({ ...change, kind: 'role', availability, operation: 'create', expectedVersion: 7, currentVersion: 7, role }, change, 7), availability === 'ready')
  }
  assert.equal(allows({ ...change, kind: 'unsupported', availability: 'unsupported' }, change, 7), false)
})

test('删除详情严格校验单一投影、引用、状态与双人审批合同', () => {
  const c = { ...change, requiresDualApproval: true, approvalPolicy: 'two_party_v1' }
  const deletion = { ...c, kind: 'role_deletion', availability: 'ready', operation: 'delete', expectedVersion: 7, currentVersion: 7, roleDeletion: { before: { id: 'custom', displayName: '待删角色', permissions: ['ops.inspect', 'ops.read', 'ops.read'] }, references: { bindings: 'none', directPrincipals: 'none' } } }
  assert.equal(allows(deletion, c, 7), true)
  for (const patch of [{ availability: 'stale' }, { availability: 'unsupported' }, { availability: 'unavailable' }, { kind: 'other' }, { kind: 'role' }, { kind: 'tenant' }, { kind: 'unknown' }, { operation: 'edit' }, { operation: 'create' }, { role: {} }, { after: {} }, { before: {} }, { id: 'other' }, { requestDigest: 'other' }, { state: 'applied' }, { expectedVersion: 6 }, { currentVersion: 8 }]) assert.equal(allows({ ...deletion, ...patch }, c, 7), false, JSON.stringify(patch))
  for (const projection of [undefined, null, {}, [], { ...deletion.roleDeletion, after: {} }, { ...deletion.roleDeletion, before: {} }, { ...deletion.roleDeletion, before: { ...deletion.roleDeletion.before, permissions: 'ops.read' } }, { ...deletion.roleDeletion, before: { ...deletion.roleDeletion.before, permissions: [null] } }, ...[{}, { bindings: 'none' }, { bindings: 'none', directPrincipals: 'unknown' }, { bindings: 0, directPrincipals: 'none' }].map(references => ({ ...deletion.roleDeletion, references }))]) assert.equal(allows({ ...deletion, roleDeletion: projection }, c, 7), false)
  for (const patch of [{ requiresDualApproval: false }, { approvalPolicy: 'legacy' }, { state: 'applied' }, { state: 'rejected' }]) assert.equal(allows({ ...deletion, state: patch.state ?? c.state }, { ...c, ...patch }, 7), false)
})
