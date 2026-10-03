import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
const { outputText } = ts.transpileModule(readFileSync(new URL('../src/tenantChange.ts', import.meta.url), 'utf8'), {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 },
})
const { tenantChangeRequest: request, tenantEditRestriction, tenantRequestKey, forgetTenantRequestKey } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
const original = { id: 'tenant-one', displayName: '原名称', status: 'active', createdBy: 'creator', createdAt: '2030-01-01' }
const base = { id: 'tenant-new', displayName: ' 新租户 ', tenants: [original], version: 7, defaultTenant: 'default' }
test('新增仅提交一个租户和版本、审批标记；编辑只改名称', () => {
  assert.deepEqual(request({ ...base, roles: ['tampered'], enforced: false }), {
    tenants: [{ id: 'tenant-new', displayName: '新租户', status: 'active' }], expectedVersion: 7, requiresDualApproval: true,
  })
  assert.deepEqual(request({ ...base, id: original.id, original }), {
    tenants: [{ id: 'tenant-one', displayName: '新租户', status: 'active' }], expectedVersion: 7, requiresDualApproval: true,
  })
  assert.equal(original.displayName, '原名称')
  const legacy = { ...original, id: 'Legacy-ID' }
  assert.throws(() => request({ ...base, id: legacy.id, original: legacy }), /不能通过名称编辑变更 ID/)
})
test('重复、空白、非法格式与长度拒绝', () => {
  for (const id of ['', '*', 'a', 'a/b', 'a'.repeat(41), 'tenant-one', ' TENANT-ONE ']) assert.throws(() => request({ ...base, id }))
  for (const displayName of ['', '  ', '名'.repeat(121)]) assert.throws(() => request({ ...base, displayName }))
  for (const version of [undefined, 0, -1, 1.5]) assert.throws(() => request({ ...base, version }))
})
test('不可更改 ID、无变化及受保护租户拒绝', () => {
  assert.throws(() => request({ ...base, original }), /ID 不可修改/)
  assert.throws(() => request({ ...base, id: original.id, original, displayName: original.displayName }), /没有实际变化/)
  for (const tenant of [{ ...original, id: 'default' }, { ...original, createdBy: 'bootstrap' }, { ...original, status: 'disabled' }, { ...original, status: 'suspended' }]) {
    assert.ok(tenantEditRestriction(tenant, 'default'))
    assert.throws(() => request({ ...base, id: tenant.id, original: tenant }))
  }
})
test('刷新/网络重试同身份同请求保留键；身份、名称、版本变化不复用', () => {
  const memory = new Map()
  const storage = { getItem: (key) => memory.get(key), setItem: (key, value) => memory.set(key, value), removeItem: (key) => memory.delete(key) }
  const body = request(base), key = tenantRequestKey('creator', body, storage)
  assert.equal(tenantRequestKey('creator', structuredClone(body), storage), key)
  assert.notEqual(tenantRequestKey('approver', body, storage), key)
  assert.notEqual(tenantRequestKey('creator', request({ ...base, displayName: '改名' }), storage), key)
  assert.notEqual(tenantRequestKey('creator', request({ ...base, version: 8 }), storage), key)
  assert.equal(tenantRequestKey('creator', body, storage), key) // A→B→A 保留原键
  forgetTenantRequestKey('creator', { ...body, idempotencyKey: key }, storage)
  assert.notEqual(tenantRequestKey('creator', body, storage), key)
})
