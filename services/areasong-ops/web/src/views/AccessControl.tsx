import { Ban, CircleHelp, KeyRound, LoaderCircle, Play, Plus, RefreshCw, Save, ShieldCheck, UserRound } from 'lucide-react'
import { useMemo, useState, type FormEvent } from 'react'
import { runAction } from '../action'
import type { AccessBinding, AccessChange, AccessControlUpdate, AccessControlView, AccessRole, AccessTenant } from '../types'
import { formatTime, shortHash } from '../labels'

interface AccessControlProps {
  access: AccessControlView | null
  loading: boolean
  available: boolean
  error: string
  busy: string
  onRefresh: () => void
  onCreateChange: (body: AccessControlUpdate) => Promise<void>
  onApproveChange: (change: AccessChange, confirmation: string) => Promise<void>
  onApplyChange: (change: AccessChange) => Promise<void>
  onRejectChange: (change: AccessChange, reason: string) => Promise<void>
}

function values<T extends { id: string }>(value?: T[] | Record<string, T>): T[] {
  return Array.isArray(value) ? value : Object.values(value ?? {})
}

function maskedSubject(binding: AccessBinding): string {
  if (binding.subject.includes('@')) {
    const [name, domain] = binding.subject.split('@')
    return `${name.slice(0, 2)}***@${domain}`
  }
  return binding.subject.length > 18 ? `${binding.subject.slice(0, 8)}…${binding.subject.slice(-6)}` : binding.subject
}

export function AccessControl({
  access, loading, available, error, busy, onRefresh,
  onCreateChange, onApproveChange, onApplyChange, onRejectChange,
}: AccessControlProps) {
  const [formOpen, setFormOpen] = useState(false)
  const [enforced, setEnforced] = useState<boolean | null>(null)
  const [binding, setBinding] = useState({ subject: '', tenantId: '', roleId: '', objectIds: '' })
  const [changeConfirmations, setChangeConfirmations] = useState<Record<string, string>>({})
  const tenants = useMemo(() => values<AccessTenant>(access?.tenants), [access?.tenants])
  const roles = useMemo(() => values<AccessRole>(access?.roles), [access?.roles])
  const bindings = access?.bindings ?? []
  const effectiveEnforced = enforced ?? access?.enforced ?? false

  function toggleEnforced() {
    setEnforced(!effectiveEnforced)
  }

  async function savePolicy() {
    if (!access) return
    await onCreateChange({
      enforced: effectiveEnforced,
      requiresDualApproval: true,
      expectedVersion: access.version,
    })
    setEnforced(null)
  }

  async function submitBinding(event: FormEvent) {
    event.preventDefault()
    if (!access || !binding.subject || !binding.tenantId || !binding.roleId) return
    const next: AccessBinding = {
      id: `binding-${Date.now()}`,
      subject: binding.subject.trim(),
      tenantId: binding.tenantId,
      roleId: binding.roleId,
      objectIds: binding.objectIds.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean),
    }
    await onCreateChange({ bindings: [next], requiresDualApproval: true, expectedVersion: access.version })
    setBinding({ subject: '', tenantId: '', roleId: '', objectIds: '' })
    setFormOpen(false)
  }

  return (
    <div className="page">
      <header className="page-header"><div><span className="eyebrow">租户、角色与对象授权</span><h1>访问控制</h1></div><button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新访问策略" disabled={loading}>{loading ? <LoaderCircle className="spin" size={17} /> : <RefreshCw size={17} />}</button></header>
      {!available && !loading && <div className="empty-state feature-empty"><CircleHelp size={19} aria-hidden="true" />{error || '访问控制能力尚未启用'}</div>}
      {available && error && <div className="inline-error" role="alert">{error}</div>}
      {available && loading && !access && <div className="empty-state"><LoaderCircle className="spin" size={18} />读取访问策略</div>}
      {available && access && <>
        <section className="access-summary"><div className="access-summary-main"><span className="access-icon"><ShieldCheck size={20} /></span><div><h2>{effectiveEnforced ? '访问策略已强制执行' : '访问策略兼容模式'}</h2><p>默认租户：{access.defaultTenant || '未设置'} · 版本 {access.version ?? 0} · {shortHash(access.digest)}</p></div><span className={effectiveEnforced ? 'credential-health healthy' : 'credential-health warning'}>{effectiveEnforced ? 'Enforced' : '兼容'}</span></div>{access.canManage && <div className="access-summary-actions"><button className="button secondary" type="button" onClick={toggleEnforced}>{effectiveEnforced ? '切换兼容模式' : '启用强制执行'}</button><button className="button secondary" type="button" onClick={() => runAction(savePolicy())} disabled={busy === 'access-change-create' || enforced === null}><Save size={14} />创建审批变更</button></div>}</section>
        <section className="page-section"><div className="section-heading"><h2>当前身份</h2><span>{access.currentSubject?.email || '会话身份'}</span></div><div className="subject-strip"><UserRound size={17} /><span><strong>{access.currentSubject?.email || '当前会话'}</strong><small>{access.currentSubject?.subject || 'subject 未返回'} · tenant {access.currentSubject?.tenantId || access.defaultTenant || '—'}</small></span></div></section>
        <section className="page-section"><div className="section-heading"><h2>角色与租户</h2><span>{roles.length} 角色 · {tenants.length} 租户</span></div><div className="access-columns"><div><h3>角色</h3>{roles.length === 0 ? <div className="empty-state compact">暂无角色</div> : <div className="role-list">{roles.map((role) => <div className="role-row" key={role.id}><span><strong>{role.displayName}</strong><small><code>{role.id}</code>{role.builtIn ? ' · 内置' : ''}</small></span><span className="permission-list">{role.permissions.map((permission) => <code key={permission}>{permission}</code>)}</span></div>)}</div>}</div><div><h3>租户</h3>{tenants.length === 0 ? <div className="empty-state compact">暂无租户</div> : <div className="tenant-list">{tenants.map((tenant) => <div className="tenant-row" key={tenant.id}><span><strong>{tenant.displayName}</strong><small><code>{tenant.id}</code></small></span><span>{tenant.status}</span></div>)}</div>}</div></div></section>
        <section className="page-section"><div className="section-heading"><h2>角色绑定</h2>{access.canManage && <button className="button secondary" type="button" onClick={() => setFormOpen((value) => !value)}><Plus size={14} />新增绑定</button>}</div>{access.canManage && formOpen && <form className="inline-form access-form" onSubmit={(event) => runAction(submitBinding(event))}><label><span>主体</span><input required placeholder="邮箱或 subject" value={binding.subject} onChange={(event) => setBinding({ ...binding, subject: event.target.value })} /></label><label><span>租户</span><select required value={binding.tenantId} onChange={(event) => setBinding({ ...binding, tenantId: event.target.value })}><option value="">选择租户</option>{tenants.map((tenant) => <option value={tenant.id} key={tenant.id}>{tenant.displayName}</option>)}</select></label><label><span>角色</span><select required value={binding.roleId} onChange={(event) => setBinding({ ...binding, roleId: event.target.value })}><option value="">选择角色</option>{roles.map((role) => <option value={role.id} key={role.id}>{role.displayName}</option>)}</select></label><label><span>对象范围（可选）</span><input placeholder="object:id" value={binding.objectIds} onChange={(event) => setBinding({ ...binding, objectIds: event.target.value })} /></label><button className="button secondary" type="submit" disabled={busy === 'access-change-create'}>{busy === 'access-change-create' ? '提交中' : '创建审批变更'}</button></form>}{bindings.length === 0 ? <div className="empty-state compact">暂无角色绑定</div> : <div className="binding-table" role="table" aria-label="角色绑定列表">{bindings.map((item) => { const role = roles.find((candidate) => candidate.id === item.roleId); const tenant = tenants.find((candidate) => candidate.id === item.tenantId); return <div className="binding-row" key={item.id}><span><strong>{maskedSubject(item)}</strong><small><code>{item.id}</code></small></span><span>{tenant?.displayName || item.tenantId}</span><span><KeyRound size={13} />{role?.displayName || item.roleId}</span><span>{item.objectIds?.length ? `${item.objectIds.length} 个对象` : '全租户'}</span></div> })}</div>}</section>
        <section className="page-section"><div className="section-heading"><h2>策略审批</h2><span>{access.pendingChanges?.length ?? 0} 项</span></div>{(access.pendingChanges?.length ?? 0) === 0 ? <div className="empty-state compact">暂无策略审批变更</div> : <div className="runner-update-list">{access.pendingChanges?.map((change) => {
          const confirmation = changeConfirmations[change.id] ?? ''
          const canApprove = access.canManage && change.state === 'pending_approval'
          const canApply = access.canManage && change.state === 'approved'
          const canReject = access.canManage && change.state === 'pending_approval' && change.actorHash === access.currentSubject?.subject
          return <article className={`runner-update-card runner-update-${change.state}`} key={change.id}><header><div className="runner-update-title"><span className={`service-indicator ${change.state === 'applied' ? 'healthy' : change.state === 'rejected' ? 'error' : 'warning'}`} /><div><strong>策略变更 {shortHash(change.id)}</strong><small>{change.state} · {formatTime(change.createdAt)}</small></div></div><code>{shortHash(change.requestDigest)}</code></header><dl className="runner-update-detail"><div><dt>创建人</dt><dd><code>{shortHash(change.actorHash)}</code></dd></div><div><dt>第一批准</dt><dd><code>{shortHash(change.approvedByHash)}</code></dd></div><div><dt>第二批准</dt><dd><code>{shortHash(change.secondApprovedByHash)}</code></dd></div><div><dt>应用时间</dt><dd>{formatTime(change.appliedAt)}</dd></div></dl>{canApprove && <div className="runner-update-actions attention"><label><span>批准确认</span><code>{change.confirmationPhrase}</code><input value={confirmation} onChange={(event) => setChangeConfirmations((current) => ({ ...current, [change.id]: event.target.value }))} /></label><button className="button secondary" type="button" disabled={confirmation !== change.confirmationPhrase || busy === `access-change-approve:${change.id}`} onClick={() => runAction(onApproveChange(change, confirmation))}><ShieldCheck size={14} />{busy === `access-change-approve:${change.id}` ? '批准中' : change.approvedByHash ? '第二人批准' : '第一人批准'}</button>{canReject && <button className="button secondary" type="button" disabled={busy === `access-change-reject:${change.id}`} onClick={() => runAction(onRejectChange(change, '创建人撤销策略变更'))}><Ban size={14} />撤销</button>}</div>}{canApply && <div className="runner-update-actions"><span>双人批准完成，必须由第三人执行。</span><button className="button danger" type="button" disabled={busy === `access-change-apply:${change.id}`} onClick={() => runAction(onApplyChange(change))}><Play size={14} />{busy === `access-change-apply:${change.id}` ? '执行中' : '独立执行'}</button></div>}{change.error && <div className="runner-update-error">{change.error}</div>}</article>
        })}</div>}</section>
      </>}
    </div>
  )
}
