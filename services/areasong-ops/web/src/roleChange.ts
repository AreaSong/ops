import type { AccessControlUpdate, AccessControlView, AccessRole } from './types'

// 权威枚举来自 internal/model/control.go；测试核对两端一致。仅为网页支持范围。
export const rolePermissions = ['ops.read', 'ops.inspect', 'ops.lifecycle', 'ops.deploy', 'ops.batch', 'ops.recover', 'fleet.manage', 'access.manage', 'config.manage', 'break_glass', 'runner.update'] as const
const protectedIDs = ['viewer', 'operator', 'release-manager', 'platform-admin']
export function roleEditRestriction(role: AccessRole): string {
  if (protectedIDs.includes(role.id.trim().toLowerCase()) || role.builtIn !== false || !role.createdBy || role.createdBy === 'bootstrap') return '默认、内置、初始化或来源不明角色不可编辑'
  if (!role.id || role.id !== role.id.trim().toLowerCase() || !role.displayName) return '历史角色无法完整表达，不可通过此入口编辑'
  if (!supportedPermissions(role.permissions)) return '角色含未知、通配或空权限，不可静默过滤后提交'
  return ''
}
function supportedPermissions(permissions: string[]): boolean {
  return Array.isArray(permissions) && permissions.length > 0 && permissions.every(p => (rolePermissions as readonly string[]).includes(p))
}
export function roleChangeRequest(input: {
  id: string; displayName: string; permissions: string[]; original?: AccessRole; roles: AccessRole[]; version?: number
}): AccessControlUpdate {
  const { original, version } = input
  const id = original ? original.id : input.id.trim().toLowerCase(), displayName = input.displayName.trim()
  if (!Number.isSafeInteger(version) || !version || version < 1) throw new Error('缺少有效策略版本，请刷新后重新核对')
  if (!displayName || (displayName.length > 120 && displayName !== original?.displayName)) throw new Error('请输入 1–120 字符的角色名称')
  if (!supportedPermissions(input.permissions)) throw new Error('请选择至少一个已登记权限，不支持未知或通配权限')
  if (original) {
    if (input.id !== original.id) throw new Error('角色 ID 不可修改')
    const restriction = roleEditRestriction(original)
    if (restriction) throw new Error(restriction)
    if (displayName === original.displayName && JSON.stringify(input.permissions) === JSON.stringify(original.permissions)) throw new Error('没有实际变化，无需创建提案')
  } else {
    if (!/^[a-z][a-z0-9-]{1,39}$/.test(id)) throw new Error('ID 须为 2–40 位小写字母、数字或连字符，以字母开头')
    if (protectedIDs.includes(id) || input.roles.some(role => role.id.trim().toLowerCase() === id)) throw new Error('角色 ID 已存在或受保护')
  }
  return { roles: [{ id, displayName, permissions: [...input.permissions] }], expectedVersion: version, requiresDualApproval: true }
}
function slot(actor: string, body: AccessControlUpdate) {
  return `ops.role-request:v1:${actor}:${JSON.stringify({ roles: body.roles, expectedVersion: body.expectedVersion, requiresDualApproval: true })}`
}
export function roleRequestKey(actor: string, body: AccessControlUpdate, storage: Pick<Storage, 'getItem' | 'setItem'> = window.sessionStorage): string {
  const name = slot(actor, body), saved = storage.getItem(name)
  if (saved) return saved
  const key = crypto.randomUUID(); storage.setItem(name, key); return key
}
export function forgetRoleRequestKey(actor: string, body: AccessControlUpdate, storage: Pick<Storage, 'removeItem'> = window.sessionStorage) {
  storage.removeItem(slot(actor, body))
}

// 列表只能阻断已知问题；完整引用范围仍由服务端历史详情判断。
export function roleDeletionRestriction(role: AccessRole, access: AccessControlView): string {
  if (protectedIDs.includes(role.id?.trim().toLowerCase()) || role.builtIn !== false || !/^[a-f0-9]{64}$/.test(role.createdBy ?? '')) return '默认、内置、初始化或来源不明角色不可删除'
  if (!/^[a-z0-9][a-z0-9._-]*$/.test(role.id) || typeof role.displayName !== 'string' || !role.displayName.trim() || !supportedPermissions(role.permissions)) return '历史角色无法可靠审阅，不支持删除'
  const roles = Array.isArray(access.roles) ? access.roles : Object.values(access.roles ?? {})
  if (roles.filter(r => r.id.trim().toLowerCase() === role.id).length !== 1) return '角色不存在或 ID 有歧义，请刷新后核对'
  if (access.bindings?.some(b => b.roleId.trim().toLowerCase() === role.id)) return '角色仍被绑定（包含过期绑定），不可删除'
  if ([...Object.values(access.principals ?? {}), ...(access.principalList ?? [])].some(p => p.roles?.some(id => id.trim().toLowerCase() === role.id))) return '角色仍被主体直接引用，不可删除'
  return ''
}

export function roleDeletionRequest(role: AccessRole, access: AccessControlView): AccessControlUpdate {
  if (!Number.isSafeInteger(access.version) || !access.version || access.version < 1) throw new Error('缺少有效策略版本，请刷新后重新核对')
  const restriction = roleDeletionRestriction(role, access)
  if (restriction) throw new Error(restriction)
  return { removeRoleIds: [role.id], expectedVersion: access.version, requiresDualApproval: true }
}

function deletionSlot(actor: string, tenant: string | undefined, body: AccessControlUpdate) {
  return `ops.role-deletion:v1:${JSON.stringify([actor, tenant ?? '', body.removeRoleIds, body.expectedVersion, true])}`
}
export function roleDeletionKey(actor: string, tenant: string | undefined, body: AccessControlUpdate, storage: Pick<Storage, 'getItem' | 'setItem'> = window.sessionStorage): string {
  const slot = deletionSlot(actor, tenant, body), saved = storage.getItem(slot)
  if (saved) return saved
  const key = crypto.randomUUID(); storage.setItem(slot, key); return key
}
export function forgetRoleDeletionKey(actor: string, tenant: string | undefined, body: AccessControlUpdate, storage: Pick<Storage, 'removeItem'> = window.sessionStorage) {
  storage.removeItem(deletionSlot(actor, tenant, body))
}
