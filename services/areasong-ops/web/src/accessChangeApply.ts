import type { AccessChange, AccessChangeDetail, AccessControlView } from './types'
import { deletionUnavailableMessage, reviewAllowsApproval } from './accessChangeReview'

interface ApplyAPI {
  access: () => Promise<AccessControlView>
  accessChangeDetail: (id: string, actor: string) => Promise<AccessChangeDetail>
  applyAccessChange: (change: AccessChange) => Promise<AccessChange>
}

function currentChange(view: AccessControlView, expected: AccessControlView, change: AccessChange) {
  const actor = expected.currentSubject?.subject
  if (!actor || !view.canManage || view.currentSubject?.subject !== actor || view.currentSubject?.tenantId !== expected.currentSubject?.tenantId) throw new Error('会话身份已变化，请刷新后重新核对')
  const current = view.pendingChanges?.find(item => item.id === change.id)
  if (!current || current.requestDigest !== change.requestDigest || current.actorHash !== change.actorHash) throw new Error('提案信息已变化，请刷新后重新核对')
  return current
}

function confirmDeletion(view: AccessControlView, target: string) {
  if (!view.roles) throw new Error('提案已应用，但角色列表未读取完整，请刷新核对')
  const roles = Array.isArray(view.roles) ? view.roles : Object.values(view.roles)
  if (roles.some(role => role.id === target)) throw new Error('提案已应用，但角色仍在当前策略中，请刷新核对')
}

// 所有网页应用入口共用此检查；GET/POST 非原子，最终保护仍由 Runner/Store 提供。
export async function applyReviewedAccessChange(api: ApplyAPI, expected: AccessControlView, change: AccessChange): Promise<string | undefined> {
  const view = await api.access(), actor = expected.currentSubject?.subject ?? ''
  const current = currentChange(view, expected, change)
  if (current.state === 'applied') return // 终态无需重新取得已经不可用的 before，也不重发写入。
  if (current.state !== 'approved') throw new Error('提案尚未完成独立批准，请刷新后重新核对')
  const detail = await api.accessChangeDetail(change.id, actor)
  if (detail.reviewerHash !== actor || !reviewAllowsApproval(detail, current, view.version)) throw new Error(detail.kind === 'role_deletion' ? deletionUnavailableMessage(detail) : '提案详情已失效或不支持，请刷新后重新审阅')
  const target = detail.kind === 'role_deletion' ? detail.roleDeletion!.before.id : undefined
  if ((target || detail.kind === 'binding') && (current.actorHash !== actor || !current.approvedByHash || current.approvedByHash === actor)) throw new Error('提案须独立批准后由创建人应用')
  const latest = await api.access()
  const latestChange = currentChange(latest, expected, current)
  if (latestChange.state === 'applied') { if (target) confirmDeletion(latest, target); if (detail.binding) confirmBinding(latest, detail, latestChange); return target }
  if (latestChange.state !== current.state || latest.version !== view.version) throw new Error('策略或提案状态已变化，请刷新后重新核对')
  try {
    const result = await api.applyAccessChange(current)
    if (result.id !== current.id || result.requestDigest !== current.requestDigest || result.state !== 'applied') throw new Error('应用结果尚未确认，请刷新提案状态')
  } catch (reason) {
    // 响应可能丢失；只读恢复状态，绝不自动重发 apply 或另建提案。
    const recovered = await api.access().catch(() => { throw new Error('应用结果未知，请只读刷新提案状态；不要重复提交') })
    if (currentChange(recovered, expected, current).state !== 'applied') throw reason
    if (target) confirmDeletion(recovered, target)
    if (detail.binding) confirmBinding(recovered, detail, currentChange(recovered, expected, current))
    return target
  }
  if (target || detail.binding) {
    const refreshed = await api.access().catch(() => { throw new Error('提案已应用，当前结果待核对，请刷新访问策略') })
    if (currentChange(refreshed, expected, current).state !== 'applied') throw new Error('应用状态尚未确认，请刷新核对')
    if (target) confirmDeletion(refreshed, target)
    if (detail.binding) confirmBinding(refreshed, detail, currentChange(refreshed, expected, current))
  }
  return target
}

// 核对授权字段，不比较快照和表行已知不同的创建元数据。
function confirmBinding(view: AccessControlView, detail: AccessChangeDetail, change: AccessChange) {
  const pending = '提案已应用，当前结果待核对，请刷新访问策略'
  const review = detail.binding!
  const target = review.after ?? review.before!
  if (view.version !== detail.expectedVersion + 1 || change.appliedPolicyVersion !== view.version || (detail.operation !== 'revoke' && !Array.isArray(view.bindings)) || (view.bindings !== undefined && !Array.isArray(view.bindings))) throw new Error(pending)
  const rows = (view.bindings ?? []).filter(b => b.id.trim().toLowerCase() === target.id)
  if (detail.operation === 'revoke') { if (rows.length) throw new Error(pending); return }
  if (rows.length !== 1) throw new Error(pending)
  const row = rows[0], after = review.after!
  const same = (a: unknown, b: unknown) => JSON.stringify(a) === JSON.stringify(b)
  if (row.id !== after.id || row.subject !== after.subject || row.tenantId !== after.tenantId || row.roleId !== after.roleId || !same(row.objectIds ?? [], after.objectIds) ||
    bindingInstant(row.expiresAt) !== bindingInstant(after.expiresAt) ||
    (row.jit !== undefined && row.jit !== false) || (row.requiresDualApproval !== undefined && row.requiresDualApproval !== false) ||
    [row.approvalState, row.approvedByHash, row.secondApprovedByHash, row.approvedAt, row.secondApprovedAt].some(v => v !== undefined && v !== '')) throw new Error(pending)
  const roles = Array.isArray(view.roles) ? view.roles : Object.values(view.roles ?? {})
  const role = roles.filter(r => r.id === after.roleId)
  if (role.length !== 1 || !same(role[0].permissions, after.permissions)) throw new Error(pending)
}

// Date.parse 仅用于无小数的秒与时区；小数独立转为纳秒，避免亚毫秒误判。
function bindingInstant(value: string | null | undefined): bigint | null | undefined {
  if (value == null) return null
  if (typeof value !== 'string') return undefined
  const parts = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/.exec(value)
  if (!parts) return undefined
  const seconds = Date.parse(parts[1] + parts[3])
  return Number.isFinite(seconds) ? BigInt(seconds) * 1000000n + BigInt((parts[2] ?? '').padEnd(9, '0')) : undefined
}
