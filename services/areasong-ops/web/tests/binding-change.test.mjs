import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
function moduleURL(file) {
  let source = readFileSync(new URL(`../src/${file}.ts`, import.meta.url), 'utf8')
  source = source.replace(/from '\.\/(accessChangeApply|accessChangeReview|roleChange)'/g, (_, name) => `from '${moduleURL(name)}'`)
  return `data:text/javascript;base64,${Buffer.from(ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } }).outputText).toString('base64')}`
}
const m = await import(moduleURL('bindingChange'))
const { bindingInstant } = await import(moduleURL('accessChangeApply'))
const actor = 'a'.repeat(64)
const original = { id: 'existing', subject: 'c'.repeat(64), tenantId: 'default', roleId: 'viewer', objectIds: ['comma, space object', 'two', 'two', ' x '], expiresAt: '2030-01-01T08:00:00.123456789+08:00', createdAt: '2026-10-03T00:00:00Z', updatedAt: '0001-01-01T00:00:00Z', createdBy: actor }
const access = { version: 7, canManage: true, currentSubject: { subject: actor, tenantId: 'default' }, bindings: [original], roles: [{ id: 'viewer', permissions: ['ops.read'] }, { id: 'platform-admin', permissions: ['*'] }], tenants: [{ id: 'default', status: 'active' }] }
const input = () => ({ original: structuredClone(original), access: structuredClone(access), roleId: 'platform-admin', objects: [...original.objectIds], expiryMode: 'keep', expiresAt: '' })
test('单目标完整请求、仅角色变化无损保留原字段，内置通配权限可引用', () => {
  const body = m.bindingEditRequest(input())
  assert.deepEqual(body, { bindings: [{ id: 'existing', subject: original.subject, tenantId: 'default', roleId: 'platform-admin', objectIds: original.objectIds, expiresAt: original.expiresAt }], expectedVersion: 7, requiresDualApproval: true })
  assert.notEqual(body.bindings[0].objectIds, original.objectIds)
  assert.equal(m.bindingEditRestriction(original, access), '')
  assert.deepEqual(m.parseBindingObjects(JSON.stringify(original.objectIds)), original.objectIds)
  for (const text of ['', 'null', '{}', '[null]', '[""]', '["x\\n"]']) assert.throws(() => m.parseBindingObjects(text))
})
test('范围清空明确，保持、设置、清除期限和精确无变化', () => {
  assert.throws(() => m.bindingEditRequest({ ...input(), roleId: 'viewer' }), /没有实际变化/)
  const scope = m.bindingEditRequest({ ...input(), roleId: 'viewer', objects: [] })
  assert.deepEqual(scope.bindings[0].objectIds, []); assert.equal(scope.bindings[0].expiresAt, original.expiresAt)
  assert.equal(Object.hasOwn(m.bindingEditRequest({ ...input(), expiryMode: 'clear' }).bindings[0], 'expiresAt'), false)
  assert.throws(() => m.bindingEditRequest({ ...input(), roleId: 'viewer', expiryMode: 'set', expiresAt: '2030-01-01T00:00:00.123456789Z' }), /没有实际变化/)
  assert.equal(m.bindingEditRequest({ ...input(), roleId: 'viewer', expiryMode: 'set', expiresAt: '2030-01-01T00:00:00.123456788Z' }).bindings[0].expiresAt, '2030-01-01T00:00:00.123456788Z')
  assert.equal(m.bindingEditRequest({ ...input(), expiryMode: 'set', expiresAt: '2000-01-01T00:00:00Z' }).bindings[0].expiresAt, '2000-01-01T00:00:00Z')
})
test('日期无归一化、无时区拒绝、纳秒与偏移精确比较', () => {
  for (const date of ['2030-02-29T00:00:00Z', '2024-04-31T00:00:00Z', '2030-01-01T24:00:00Z', '2030-01-01T00:00:00+24:00', '2026-03-08T02:30:00', '', '2030-01-01T00:00:00.1234567890Z']) assert.equal(bindingInstant(date), undefined, date)
  assert.equal(bindingInstant('2024-02-29T00:00:00Z'), 1709164800000000000n)
  assert.equal(bindingInstant(original.expiresAt), 1893456000123456789n)
})
test('六审批字段、JIT、异常来源及未知字段均阻断，表行 createdAt 可非零', () => {
  for (const patch of [{ requiresDualApproval: true }, { approvalState: 'approved' }, { approvedByHash: actor }, { secondApprovedByHash: actor }, { approvedAt: '2000-01-01T00:00:00Z' }, { secondApprovedAt: '2000-01-01T00:00:00Z' }, { jit: true }, { createdBy: 'bootstrap' }, { id: 'UPPER' }, { tenantId: '*' }, { subject: 'x@y' }, { expiresAt: '' }, { future: false }]) assert.ok(m.bindingEditRestriction({ ...original, ...patch }, access), JSON.stringify(patch))
})
test('冻结身份、租户、版本与目标；消失或内容变更不转成新增', () => {
  m.verifyBindingEdit(original, access, access)
  for (const patch of [{ version: 8 }, { canManage: false }, { bindings: [] }, { bindings: [{ ...original, roleId: 'platform-admin' }] }, { currentSubject: { subject: 'b'.repeat(64), tenantId: 'default' } }, { currentSubject: { subject: actor, tenantId: 'other' } }]) assert.throws(() => m.verifyBindingEdit(original, access, { ...access, ...patch }))
})
test('幂等同身份/目标/版本/载荷复用，变更载荷隔离，撤销后显式新建', () => {
  const map = new Map(), storage = { getItem: k => map.get(k), setItem: (k,v) => map.set(k,v), removeItem: k => map.delete(k) }
  const body = m.bindingEditRequest(input()), key = m.bindingEditKey(actor, 'default', body, storage)
  assert.match(key, /^[0-9a-f-]{36}$/); assert.equal(key, m.bindingEditKey(actor, 'default', body, storage))
  for (const [a,t,b] of [['b', 'default', body], [actor, 'other', body], [actor, 'default', { ...body, expectedVersion: 8 }], [actor, 'default', m.bindingEditRequest({ ...input(), objects: [] })]]) assert.notEqual(key, m.bindingEditKey(a,t,b,storage))
  m.forgetBindingEditKey(actor, 'default', body, storage); assert.notEqual(key, m.bindingEditKey(actor, 'default', body, storage))
})
test('编辑响应必须是同一身份目标的真实 edit，不接受 create/revoke/迁移', () => {
  const body = m.bindingEditRequest(input()), change = { id: '00000000-0000-4000-8000-000000000001', requestDigest: 'sha256:'+'d'.repeat(64), state: 'pending_approval', actorHash: actor, approvalPolicy: 'two_party_v1', requiresDualApproval: true }
  const value = b => ({ id: b.id, subject: b.subject, tenantId: b.tenantId, roleId: b.roleId, objectIds: b.objectIds, expiresAt: '2030-01-01T00:00:00.123456789Z', jit: false, bindingApproval: 'default', permissions: b.roleId === 'viewer' ? ['ops.read'] : ['*'] })
  const d = { ...change, reviewerHash: actor, expectedVersion: 7, currentVersion: 7, kind: 'binding', operation: 'edit', availability: 'ready', binding: { before: value(original), after: value(body.bindings[0]), changedFields: ['roleId','permissions'] } }
  assert.equal(m.bindingEditDetailMatches(d,change,body,original),true)
  for (const operation of ['create','revoke']) assert.equal(m.bindingEditDetailMatches({...d,operation},change,body,original),false)
  assert.equal(m.bindingEditDetailMatches({...d,binding:{...d.binding,after:{...d.binding.after,subject:actor}}},change,body,original),false)
})
