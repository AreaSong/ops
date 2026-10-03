import type { AccessControlUpdate, AccessTenant } from './types'

export function tenantEditRestriction(tenant: AccessTenant, defaultTenant?: string): string {
  if (tenant.id === defaultTenant || tenant.createdBy === 'bootstrap') return '默认或系统初始化租户不可在此编辑'
  if (tenant.id !== tenant.id.trim().toLowerCase()) return '此租户 ID 需要单独核对，不能通过名称编辑变更 ID'
  if (tenant.status !== 'active') return '此入口仅编辑 active 租户的名称'
  return ''
}

export function tenantChangeRequest(input: {
  id: string; displayName: string; original?: AccessTenant; tenants: AccessTenant[]
  defaultTenant?: string; version?: number
}): AccessControlUpdate {
  const { original, tenants, defaultTenant, version } = input
  const id = original ? original.id : input.id.trim().toLowerCase(), displayName = input.displayName.trim()
  if (!Number.isSafeInteger(version) || !version || version < 1) throw new Error('缺少有效策略版本，请刷新后重新核对')
  if (!displayName || displayName.length > 120) throw new Error('请输入 1–120 字符的租户名称')
  if (original) {
    if (input.id !== original.id) throw new Error('租户 ID 不可修改')
    const restriction = tenantEditRestriction(original, defaultTenant)
    if (restriction) throw new Error(restriction)
    if (displayName === original.displayName) throw new Error('名称没有实际变化，无需创建提案')
  } else {
    if (!/^[a-z][a-z0-9-]{1,39}$/.test(id)) throw new Error('ID 须为 2–40 位小写字母、数字或连字符，以字母开头')
    if (tenants.some((tenant) => tenant.id.toLowerCase() === id) || id === defaultTenant) throw new Error('租户 ID 已存在，请使用编辑入口')
  }
  // 单条 upsert；不展开原对象，避免发送系统元数据或其他策略字段。
  return { tenants: [{ id, displayName, status: original?.status ?? 'active' }], expectedVersion: version, requiresDualApproval: true }
}

// 刷新后仅恢复同一身份、同一请求的键，不自动重放写请求。
export function tenantRequestKey(actor: string, body: AccessControlUpdate, storage: Pick<Storage, 'getItem' | 'setItem'> = window.sessionStorage): string {
  const slot = tenantRequestSlot(actor, body)
  const saved = storage.getItem(slot)
  if (saved) return saved
  const key = crypto.randomUUID()
  storage.setItem(slot, key)
  return key
}

function tenantRequestSlot(actor: string, body: AccessControlUpdate): string {
  // idempotencyKey 不属于业务请求，清除键时也必须定位到同一个槽。
  return `ops.tenant-request:v2:${actor}:${JSON.stringify({ tenants: body.tenants, expectedVersion: body.expectedVersion, requiresDualApproval: true })}`
}

export function forgetTenantRequestKey(actor: string, body: AccessControlUpdate, storage: Pick<Storage, 'removeItem'> = window.sessionStorage) {
  storage.removeItem(tenantRequestSlot(actor, body))
}
