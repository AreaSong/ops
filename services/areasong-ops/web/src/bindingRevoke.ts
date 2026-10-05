import type { AccessBinding, AccessChange, AccessChangeDetail, AccessControlUpdate, AccessControlView } from './types'
import { ordinaryBindingRestriction, bindingRoles, verifyBindingEdit } from './bindingChange'
import { bindingInstant } from './accessChangeApply'
import { reviewAllowsApproval } from './accessChangeReview'

export function bindingRevokeRestriction(binding: AccessBinding, access: AccessControlView): string {
  return ordinaryBindingRestriction(binding, access).replaceAll('编辑', '撤销')
}
export function bindingRevokeRequest(original: AccessBinding, access: AccessControlView): AccessControlUpdate {
  const restriction = bindingRevokeRestriction(original, access)
  if (restriction) throw new Error(restriction)
  if (!Number.isSafeInteger(access.version) || !access.version || access.version < 1) throw new Error('缺少有效策略版本，请刷新核对')
  return { removeBindingIds: [original.id], expectedVersion: access.version, requiresDualApproval: true }
}
export function verifyBindingRevoke(original: AccessBinding, frozen: AccessControlView, current: AccessControlView) {
  // 只共用目标冻结检查；不使用编辑的角色选择、变化或期限输入校验。
  try { verifyBindingEdit(original, frozen, current) } catch (reason) {
    throw new Error(reason instanceof Error ? reason.message.replaceAll('编辑', '撤销') : '无法核对撤销目标')
  }
}
export function bindingRevokeDetailMatches(detail: AccessChangeDetail, change: AccessChange, frozen: AccessControlView, original: AccessBinding): boolean {
  if (detail.kind !== 'binding' || detail.operation !== 'revoke' || !reviewAllowsApproval(detail, change, frozen.version)) return false
  const before = detail.binding!.before!
  return ['id', 'subject', 'tenantId', 'roleId'].every(k => before[k as keyof typeof before] === original[k as keyof AccessBinding]) &&
    JSON.stringify(before.objectIds) === JSON.stringify(original.objectIds ?? []) &&
    bindingInstant(before.expiresAt) !== undefined && bindingInstant(before.expiresAt) === bindingInstant(original.expiresAt) &&
    JSON.stringify(before.permissions) === JSON.stringify(bindingRoles(frozen).find(r => r.id === original.roleId)?.permissions)
}
export function validBindingRevokeResponse(change: AccessChange, actor: string): boolean {
  return Boolean(change && /^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$/i.test(change.id) &&
    /^sha256:[a-f0-9]{64}$/.test(change.requestDigest) && change.actorHash === actor &&
    ['pending_approval', 'approved', 'applied', 'rejected'].includes(change.state) &&
    change.approvalPolicy === 'two_party_v1' && change.requiresDualApproval === true)
}
function slot(actor: string, tenant: string | undefined, body: AccessControlUpdate) {
  return `ops.binding-revoke:v1:${JSON.stringify([actor, tenant ?? '', body.expectedVersion, body.removeBindingIds, true])}`
}
export function bindingRevokeKey(actor: string, tenant: string | undefined, body: AccessControlUpdate, storage: Pick<Storage, 'getItem' | 'setItem'> = window.sessionStorage): string {
  const name = slot(actor, tenant, body), saved = storage.getItem(name)
  if (saved) return saved
  const key = crypto.randomUUID(); storage.setItem(name, key); return key
}
export function forgetBindingRevokeKey(actor: string, tenant: string | undefined, body: AccessControlUpdate, storage: Pick<Storage, 'removeItem'> = window.sessionStorage) { storage.removeItem(slot(actor, tenant, body)) }
