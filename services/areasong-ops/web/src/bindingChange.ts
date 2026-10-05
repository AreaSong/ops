import type { AccessBinding, AccessChange, AccessChangeDetail, AccessControlUpdate, AccessControlView, AccessRole } from './types'
import { bindingInstant } from './accessChangeApply'
import { reviewAllowsApproval } from './accessChangeReview'
import { rolePermissions } from './roleChange'

const id = (v: unknown) => typeof v === 'string' && /^[a-z0-9][a-z0-9._-]*$/.test(v)
const hash = (v: unknown) => typeof v === 'string' && /^[a-f0-9]{64}$/.test(v)
const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)
export const bindingRoles = (access: AccessControlView): AccessRole[] => Array.isArray(access.roles) ? access.roles : Object.values(access.roles ?? {})
export function bindingRoleSupported(role: AccessRole): boolean {
  return id(role.id) && Array.isArray(role.permissions) && role.permissions.length > 0 && role.permissions.every(p => p === '*' || (rolePermissions as readonly string[]).includes(p))
}
function objectsSupported(value: unknown): value is string[] {
  return Array.isArray(value) && value.every(v => typeof v === 'string' && v.length > 0 && !Array.from(v).some(c => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127))
}
// 列表只能排除已知问题；历史快照与来源一致性由服务端详情最终判断。
export function bindingEditRestriction(binding: AccessBinding, access: AccessControlView): string {
  return ordinaryBindingRestriction(binding, access)
}
export function ordinaryBindingRestriction(binding: AccessBinding, access: AccessControlView): string {
  if (Object.keys(binding).some(k => !['id', 'subject', 'tenantId', 'roleId', 'objectIds', 'expiresAt', 'jit', 'requiresDualApproval', 'approvalState', 'approvedByHash', 'secondApprovedByHash', 'approvedAt', 'secondApprovedAt', 'createdAt', 'updatedAt', 'createdBy'].includes(k))) return '绑定含未识别字段，不可省略后提交'
  if (!id(binding.id) || !hash(binding.subject) || !id(binding.tenantId)) return '绑定 ID、主体或租户无法规范解释，不支持编辑'
  if (!hash(binding.createdBy)) return '初始化或来源不明绑定不可编辑'
  if ((binding.jit !== undefined && binding.jit !== false) || (binding.requiresDualApproval !== undefined && binding.requiresDualApproval !== false) ||
    [binding.approvalState, binding.approvedByHash, binding.secondApprovedByHash].some(v => v !== undefined && v !== '') ||
    [binding.approvedAt, binding.secondApprovedAt].some(v => v != null)) return 'JIT 或绑定自身六审批字段非默认，不支持编辑'
  // createdAt 是当前存储行时间，不作为历史快照来源的证据。
  if (binding.updatedAt !== undefined && binding.updatedAt !== '0001-01-01T00:00:00Z') return '绑定含不支持的更新元数据'
  if (!objectsSupported(binding.objectIds ?? []) || bindingInstant(binding.expiresAt) === undefined) return '对象范围或原期限无法无损解释，不支持编辑'
  const tenants = Array.isArray(access.tenants) ? access.tenants : Object.values(access.tenants ?? {})
  if (tenants.filter(t => t.id === binding.tenantId && t.status === 'active').length !== 1) return '具体有效租户未能确认'
  const roles = bindingRoles(access).filter(r => r.id === binding.roleId)
  if (roles.length !== 1 || !bindingRoleSupported(roles[0])) return '当前角色权限无法完整解释'
  const matches = access.bindings?.filter(b => b.id.trim().toLowerCase() === binding.id)
  if (matches?.length !== 1 || matches[0].id !== binding.id) return '绑定不存在或 ID 有别名歧义，请刷新核对'
  return ''
}
export function parseBindingObjects(text: string): string[] {
  let value: unknown
  try { value = JSON.parse(text) } catch { throw new Error('对象范围须为 JSON 字符串数组，解析失败不会清空原范围') }
  if (!objectsSupported(value)) throw new Error('对象须为非空字符串，不含控制字符；用 [] 显式放宽范围')
  return value
}
export type BindingExpiryMode = 'keep' | 'set' | 'clear'
export function bindingEditRequest(input: { original: AccessBinding; access: AccessControlView; roleId: string; objects: string[]; expiryMode: BindingExpiryMode; expiresAt: string }): AccessControlUpdate {
  const { original, access, roleId, objects, expiryMode } = input
  const restriction = bindingEditRestriction(original, access)
  if (restriction) throw new Error(restriction)
  if (!Number.isSafeInteger(access.version) || !access.version || access.version < 1) throw new Error('缺少有效策略版本，请刷新核对')
  const roles = bindingRoles(access).filter(r => r.id === roleId)
  if (roles.length !== 1 || !bindingRoleSupported(roles[0])) throw new Error('请选择可完整解释权限的既有角色')
  if (!objectsSupported(objects)) throw new Error('对象范围无法完整解释')
  if (!['keep', 'set', 'clear'].includes(expiryMode)) throw new Error('期限操作无效')
  const expiresAt = expiryMode === 'keep' ? original.expiresAt : expiryMode === 'clear' ? undefined : input.expiresAt
  if (bindingInstant(expiresAt) === undefined || (expiryMode === 'set' && !expiresAt)) throw new Error('请输入有效 RFC3339 日期和明确偏移，最多 9 位小数；不会自动归一化日期')
  if (roleId === original.roleId && same(objects, original.objectIds ?? []) && bindingInstant(expiresAt) === bindingInstant(original.expiresAt)) throw new Error('没有实际变化，无需创建提案')
  return { bindings: [{ id: original.id, subject: original.subject, tenantId: original.tenantId, roleId, objectIds: [...objects], ...(expiresAt === undefined ? {} : { expiresAt }) }], expectedVersion: access.version, requiresDualApproval: true }
}
export function verifyBindingEdit(original: AccessBinding, frozen: AccessControlView, current: AccessControlView) {
  if (!current.canManage || !frozen.currentSubject?.subject || current.currentSubject?.subject !== frozen.currentSubject.subject || current.currentSubject?.tenantId !== frozen.currentSubject.tenantId) throw new Error('会话身份或管理资格已变化，请关闭并刷新后重新核对')
  if (current.version !== frozen.version) throw new Error('策略版本已变化，请关闭并刷新后重新核对')
  const restriction = bindingEditRestriction(original, current)
  if (restriction) throw new Error(restriction)
  if (!same(current.bindings?.find(b => b.id === original.id), original) || !same(bindingRoles(current), bindingRoles(frozen))) throw new Error('目标绑定或角色已变化，请关闭并刷新后重新核对')
}
export function bindingEditDetailMatches(detail: AccessChangeDetail, change: AccessChange, body: AccessControlUpdate, original: AccessBinding): boolean {
  if (detail.kind !== 'binding' || detail.operation !== 'edit' || !reviewAllowsApproval(detail, change, body.expectedVersion)) return false
  const { before, after, changedFields } = detail.binding!
  const target = body.bindings![0]
  const matches = (value: typeof before, row: AccessBinding) => value && ['id', 'subject', 'tenantId', 'roleId'].every(k => value[k as keyof typeof value] === row[k as keyof AccessBinding]) && same(value.objectIds, row.objectIds ?? []) && bindingInstant(value.expiresAt) === bindingInstant(row.expiresAt)
  return Boolean(changedFields.length && matches(before, original) && matches(after, target))
}
function slot(actor: string, tenant: string | undefined, body: AccessControlUpdate) {
  return `ops.binding-edit:v1:${JSON.stringify([actor, tenant ?? '', body.expectedVersion, body.bindings, true])}`
}
export function bindingEditKey(actor: string, tenant: string | undefined, body: AccessControlUpdate, storage: Pick<Storage, 'getItem' | 'setItem'> = window.sessionStorage): string {
  const name = slot(actor, tenant, body), saved = storage.getItem(name)
  if (saved) return saved
  const key = crypto.randomUUID(); storage.setItem(name, key); return key
}
export function forgetBindingEditKey(actor: string, tenant: string | undefined, body: AccessControlUpdate, storage: Pick<Storage, 'removeItem'> = window.sessionStorage) { storage.removeItem(slot(actor, tenant, body)) }
