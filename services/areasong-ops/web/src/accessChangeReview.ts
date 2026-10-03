import type { AccessChange, AccessChangeDetail } from './types'

// 服务端差异必须绑定列表中的同一提案、摘要、状态和本次策略版本。
export function reviewAllowsApproval(detail: AccessChangeDetail, change: AccessChange, version?: number): boolean {
  if (detail.id !== change.id || detail.requestDigest !== change.requestDigest || detail.state !== change.state) return false
  if (detail.kind === 'binding') return bindingReady(detail, change, version)
  if (detail.binding !== undefined) return false
  if (detail.kind === 'role_deletion') return deletionReady(detail, change, version)
  if (detail.roleDeletion !== undefined) return false
  if (detail.kind === 'other' && detail.availability === 'unsupported') return true
  if (!['tenant', 'role'].includes(detail.kind) || detail.availability !== 'ready' || !version || detail.expectedVersion !== version || detail.currentVersion !== version) return false
  if (detail.kind === 'role') {
    const role = detail.role
    if (!role?.after?.id || !role.after.displayName || !role.after.permissions?.length || !role.permissionDiff || !role.impact) return false
    if (detail.before || detail.after) return false
    if (detail.operation === 'create') return !role.before
    return detail.operation === 'edit' && role.before?.id === role.after.id && Boolean(role.before.permissions?.length)
  }
  if (!detail.after || detail.after.status !== 'active') return false
  if (detail.operation === 'create') return !detail.before
  return detail.operation === 'rename' && detail.before?.id === detail.after.id && detail.before.status === detail.after.status
}

function record(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
function exact(value: unknown, keys: string[]): value is Record<string, unknown> {
  return record(value) && Object.keys(value).length === keys.length && keys.every(key => Object.hasOwn(value, key))
}
function deletionReady(detail: AccessChangeDetail, change: AccessChange, version?: number): boolean {
  if (typeof detail.id !== 'string' || !detail.id.trim() || typeof detail.requestDigest !== 'string' || !detail.requestDigest.trim()) return false
  if (detail.operation !== 'delete' || detail.availability !== 'ready' || !Number.isSafeInteger(version) || !version || version < 1 || detail.expectedVersion !== version || detail.currentVersion !== version) return false
  if (!['pending_approval', 'approved'].includes(change.state) || change.approvalPolicy !== 'two_party_v1' || change.requiresDualApproval !== true) return false
  if (detail.role !== undefined || detail.before !== undefined || detail.after !== undefined) return false
  const deletion = detail.roleDeletion
  if (!exact(deletion, ['before', 'references']) || !exact(deletion.before, ['id', 'displayName', 'permissions']) || !exact(deletion.references, ['bindings', 'directPrincipals'])) return false
  const { before, references } = deletion
  return typeof before.id === 'string' && /^[a-z0-9][a-z0-9._-]*$/.test(before.id) && typeof before.displayName === 'string' && Boolean(before.displayName.trim()) && Array.isArray(before.permissions) && before.permissions.length > 0 && before.permissions.every(p => typeof p === 'string' && Boolean(p.trim())) && references.bindings === 'none' && references.directPrincipals === 'none'
}

export function deletionUnavailableMessage(detail: AccessChangeDetail): string {
  const reasons: Record<string, string> = {
    binding_reference: '基准版本存在绑定引用（含过期绑定），不可删除。',
    principal_reference: '基准版本存在主体直接引用，不可删除。',
    protected_role: '内置、默认、初始化或来源不明角色不可删除。',
    unsupported_reference_scope: '无法可靠解释完整引用范围，不支持删除。',
    incompatible_role: '历史角色内容无法完整审阅，不支持删除。',
    invalid_role_id: '角色 ID 不符合删除详情支持条件。',
    role_not_found: '基准版本不存在此角色。',
  }
  return reasons[detail.reason ?? ''] ?? (detail.availability === 'stale' ? '策略版本已变化，请刷新后重新核对。' : '删除详情不可用或不完整，不能批准或应用。')
}

const bindingFields = ['id', 'subject', 'tenantId', 'roleId', 'permissions', 'objectIds', 'expiresAt', 'jit', 'bindingApproval']
const hash = (v: unknown) => typeof v === 'string' && /^[a-f0-9]{64}$/.test(v)
const bindingID = (v: unknown) => typeof v === 'string' && /^[a-z0-9][a-z0-9._-]*$/.test(v)
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)
function bindingValueReady(value: unknown): value is Record<string, unknown> {
  if (!exact(value, bindingFields)) return false
  return bindingID(value.id) && hash(value.subject) && bindingID(value.tenantId) && bindingID(value.roleId) &&
    Array.isArray(value.permissions) && value.permissions.length > 0 && value.permissions.every(p => typeof p === 'string' && ['*', 'ops.read', 'ops.inspect', 'ops.lifecycle', 'ops.deploy', 'ops.batch', 'ops.recover', 'fleet.manage', 'access.manage', 'config.manage', 'break_glass', 'runner.update'].includes(p)) &&
    Array.isArray(value.objectIds) && value.objectIds.every(id => typeof id === 'string' && id.length > 0 && !Array.from(id).some(c => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127)) &&
    (value.expiresAt === null || (typeof value.expiresAt === 'string' && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(value.expiresAt) && Number.isFinite(Date.parse(value.expiresAt)))) && value.jit === false && value.bindingApproval === 'default'
}
function bindingReady(detail: AccessChangeDetail, change: AccessChange, version?: number): boolean {
  if (!hash(detail.reviewerHash) || !(typeof detail.requestDigest === 'string' && /^sha256:[a-f0-9]{64}$/.test(detail.requestDigest)) || !hash(change.actorHash) || typeof detail.id !== 'string' || !/^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i.test(detail.id)) return false
  if (detail.availability !== 'ready' || !Number.isSafeInteger(version) || !version || version < 1 || detail.expectedVersion !== version || detail.currentVersion !== version) return false
  if (!['pending_approval', 'approved'].includes(change.state) || change.approvalPolicy !== 'two_party_v1' || change.requiresDualApproval !== true) return false
  if (change.state === 'approved' && (!hash(change.approvedByHash) || change.approvedByHash === change.actorHash)) return false
  if (detail.before !== undefined || detail.after !== undefined || detail.role !== undefined || detail.roleDeletion !== undefined) return false
  const binding = detail.binding
  if (!exact(binding, ['before', 'after', 'changedFields'])) return false
  const { before, after, changedFields } = binding
  if (!Array.isArray(changedFields) || (before !== null && !bindingValueReady(before)) || (after !== null && !bindingValueReady(after))) return false
  if (detail.operation === 'create') { if (before !== null || after === null) return false }
  else if (detail.operation === 'revoke') { if (before === null || after !== null) return false }
  else if (detail.operation === 'edit') {
    if (!before || !after || ['id', 'subject', 'tenantId'].some(k => before[k] !== after[k])) return false
    if (before.roleId === after.roleId && !same(before.permissions, after.permissions)) return false
  } else return false
  return same(changedFields, before && after ? bindingFields.filter(k => !same(before[k], after[k])) : bindingFields)
}
