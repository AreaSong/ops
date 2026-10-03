import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import ts from 'typescript'
const { outputText } = ts.transpileModule(readFileSync(new URL('../src/roleChange.ts', import.meta.url), 'utf8'), { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } })
const { roleChangeRequest: request, roleEditRestriction: restriction, rolePermissions, roleRequestKey: key, forgetRoleRequestKey: forget } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`)
const original = { id: 'custom', displayName: '原名称', permissions: ['ops.inspect', 'ops.read', 'ops.inspect'], builtIn: false, createdBy: 'creator' }
const base = { id: ' New-Role ', displayName: ' 名称 ', permissions: ['ops.read'], roles: [original], version: 7 }
test('枚举与 Go 权威登记一致；只发送单角色允许字段', () => {
  const go = readFileSync(new URL('../../internal/model/control.go', import.meta.url), 'utf8')
  assert.deepEqual(rolePermissions, [...go.matchAll(/Permission\w+\s+Permission = "([^"]+)"/g)].map(m => m[1]))
  assert.deepEqual(request({ ...base, enforced: false, builtIn: true }), { roles: [{ id: 'new-role', displayName: '名称', permissions: ['ops.read'] }], expectedVersion: 7, requiresDualApproval: true })
})
test('名称编辑保留完整权限顺序/重复，不改原对象；拒绝无变化和 ID 修改', () => {
  const edit = { ...base, id: original.id, original, permissions: [...original.permissions] }
  assert.deepEqual(request(edit).roles[0].permissions, original.permissions)
  assert.notEqual(request(edit).roles[0].permissions, original.permissions)
  assert.throws(() => request({ ...edit, displayName: original.displayName }), /没有实际变化/)
  assert.throws(() => request({ ...edit, id: 'other' }), /ID 不可修改/)
})
test('保护 ID、规范化碰撞、来源不明、未知/通配历史权限、空权限拒绝', () => {
  for (const id of ['viewer', 'operator', 'release-manager', 'platform-admin', ' CUSTOM ', '', '*']) assert.throws(() => request({ ...base, id }))
  assert.throws(() => request({ ...base, roles: [{ ...original, id: ' NEW-ROLE ' }] }))
  for (const patch of [{ builtIn: true }, { builtIn: undefined }, { createdBy: undefined }, { createdBy: 'bootstrap' }, { permissions: ['*'] }, { permissions: ['future'] }, { permissions: [] }, { id: 'CUSTOM' }]) {
    const role = { ...original, ...patch }; assert.ok(restriction(role)); assert.throws(() => request({ ...base, id: role.id, original: role }))
  }
  for (const permissions of [[], ['*'], ['ops.*'], ['future']]) assert.throws(() => request({ ...base, permissions }))
  for (const version of [0, undefined, -1, 1.2]) assert.throws(() => request({ ...base, version }))
})
test('幂等键匹配身份/完整载荷/固定版本，A→B→A 复用，撤销后显式清除换键', () => {
  const map = new Map(), storage = { getItem: k => map.get(k), setItem: (k,v) => map.set(k,v), removeItem: k => map.delete(k) }
  const body = request(base), a = key('creator', body, storage)
  assert.equal(key('creator', structuredClone(body), storage), a)
  for (const b of [request({ ...base, displayName: '其他' }), request({ ...base, version: 8 }), request({ ...base, permissions: ['ops.inspect'] })]) assert.notEqual(key('creator', b, storage), a)
  assert.notEqual(key('approver', body, storage), a); assert.equal(key('creator', body, storage), a)
  forget('creator', { ...body, idempotencyKey: a }, storage); assert.notEqual(key('creator', body, storage), a)
})

test('历史长名称仅改权限不强迫改名；新增或改名仍限制长度', () => {
  const legacy = { ...original, displayName: '名'.repeat(121) }
  assert.equal(restriction(legacy), '')
  const edit = { ...base, id: legacy.id, original: legacy, displayName: legacy.displayName, permissions: ['ops.deploy'] }
  assert.equal(request(edit).roles[0].displayName, legacy.displayName)
  assert.throws(() => request({ ...edit, displayName: '新'.repeat(121) }))
  assert.throws(() => request({ ...base, displayName: legacy.displayName }))
})
