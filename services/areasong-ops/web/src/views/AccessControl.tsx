import { Ban, CircleHelp, KeyRound, LoaderCircle, Play, Plus, RefreshCw, Save, ShieldCheck, UserRound } from 'lucide-react'
import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { runAction } from '../action'
import { canCurrentActorApprove } from '../approval'
import type { AccessBinding, AccessChange, AccessChangeDetail, AccessControlUpdate, AccessControlView, AccessRole, AccessTenant } from '../types'
import type { AccessApplyResult } from '../accessChangeApply'
import { BindingRevokeDialog } from '../components/BindingRevokeDialog'
import { bindingRevokeRestriction } from '../bindingRevoke'
import { BindingEditDialog } from '../components/BindingEditDialog'
import { bindingEditRestriction } from '../bindingChange'
import { RoleDialog } from '../components/RoleDialog'
import { RoleDeletionDialog } from '../components/RoleDeletionDialog'
import { roleDeletionRestriction, roleEditRestriction } from '../roleChange'
import { TenantDialog } from '../components/TenantDialog'
import { AccessChangeReview } from '../components/AccessChangeReview'
import { tenantEditRestriction } from '../tenantChange'
import { formatTime, shortHash } from '../labels'

interface AccessControlProps {
  access: AccessControlView | null
  loading: boolean
  available: boolean
  error: string
  busy: string
  onVerifyActor: () => Promise<AccessControlView>
  onRefresh: () => void
  onReadDetail: (id: string, actor: string, signal?: AbortSignal) => Promise<AccessChangeDetail>
  onCreateChange: (body: AccessControlUpdate) => Promise<AccessChange>
  onApproveChange: (change: AccessChange, confirmation: string) => Promise<void>
  onApplyChange: (change: AccessChange) => Promise<AccessApplyResult | void>
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
  access, loading, available, error, busy, onRefresh, onReadDetail, onVerifyActor,
  onCreateChange, onApproveChange, onApplyChange, onRejectChange,
}: AccessControlProps) {
  const [tenantDraft, setTenantDraft] = useState<{ access: AccessControlView; tenants: AccessTenant[]; original?: AccessTenant } | null>(null)
  const [roleDraft, setRoleDraft] = useState<{ access: AccessControlView; roles: AccessRole[]; original?: AccessRole } | null>(null)
  const [deletionDraft, setDeletionDraft] = useState<{ access: AccessControlView; role: AccessRole } | null>(null)
  const [bindingDraft, setBindingDraft] = useState<{ access: AccessControlView; original: AccessBinding } | null>(null)
  const [revokeDraft, setRevokeDraft] = useState<{ access: AccessControlView; original: AccessBinding } | null>(null)
  const [bindingNotice, setBindingNotice] = useState('')
  const [roleNotice, setRoleNotice] = useState('')
  const identityGeneration = useRef(0)
  useEffect(() => {
    identityGeneration.current++
    setRoleNotice(''); setBindingNotice(''); setBindingDraft(null); setRevokeDraft(null)
    return () => { identityGeneration.current++ }
  }, [access?.currentSubject?.subject, access?.currentSubject?.tenantId])
  useEffect(() => {
    if (deletionDraft && (access?.currentSubject?.subject !== deletionDraft.access.currentSubject?.subject || access?.currentSubject?.tenantId !== deletionDraft.access.currentSubject?.tenantId || !access?.canManage)) setDeletionDraft(null)
  }, [access, deletionDraft])
  useEffect(() => { if (!access?.canManage) { setBindingDraft(null); setRevokeDraft(null) } }, [access?.canManage])
  const [tenantNotice, setTenantNotice] = useState('')
  const [formOpen, setFormOpen] = useState(false)
  const [enforced, setEnforced] = useState<boolean | null>(null)
  const [binding, setBinding] = useState({ subject: '', tenantId: '', roleId: '', objectIds: '' })
  const tenants = useMemo(() => values<AccessTenant>(access?.tenants), [access?.tenants])
  const roles = useMemo(() => values<AccessRole>(access?.roles), [access?.roles])
  const bindings = access?.bindings ?? []
  const effectiveEnforced = enforced ?? access?.enforced ?? false

  async function createDeletionChange(body: AccessControlUpdate) {
    const generation = identityGeneration.current
    const change = await onCreateChange(body)
    if (generation === identityGeneration.current && change.state !== 'rejected') setRoleNotice(`删除提案 ${shortHash(change.id)} · ${change.state}。${change.state === 'applied' ? '该提案已应用，请刷新核对角色列表。' : '角色尚未删除，需独立批准后由创建人应用。'}`)
    return change
  }

  async function applyChange(change: AccessChange) {
    setBindingNotice(''); setRoleNotice('')
    const generation = identityGeneration.current
    const target = await onApplyChange(change)
    if (generation !== identityGeneration.current) return
    if (typeof target === 'string') setRoleNotice(`角色 ${target} 已删除，已刷新确认目标不存在。`)
    else if (target?.bindingID) setBindingNotice(target.operation === 'revoke' ? `绑定 ${target.bindingID} 撤销已核对完成，已按实际应用版本确认目标不存在。其他授权仍可能有效。` : `绑定 ${target.bindingID} 编辑已生效，已按实际应用版本读回确认授权字段。`)
  }

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
        <section className="page-section"><div className="section-heading"><h2>角色与租户</h2><span>{roles.length} 角色 · {tenants.length} 租户</span></div><div className="access-columns"><div><div className="section-heading"><h3>角色</h3>{access.canManage && <button className="button secondary" type="button" disabled={loading || Boolean(busy)} onClick={() => setRoleDraft({ access: structuredClone(access), roles: structuredClone(roles) })}><Plus size={14} aria-hidden="true" />新增角色</button>}</div>{roleNotice && <p role="status">{roleNotice}</p>}{roles.length === 0 ? <div className="empty-state compact">暂无角色</div> : <div className="role-list">{roles.map((role) => <div className="role-row" key={role.id}><span><strong>{role.displayName}</strong><small><code>{role.id}</code>{role.builtIn ? ' · 内置' : ''}</small></span><span className="permission-list">{role.permissions.map((permission, index) => <code key={`${permission}:${index}`}>{permission}</code>)}</span>{access.canManage && <button className="button secondary" type="button" aria-label={`编辑角色 ${role.displayName}`} disabled={loading || Boolean(busy) || Boolean(roleEditRestriction(role))} title={roleEditRestriction(role)} onClick={() => setRoleDraft({ access: structuredClone(access), roles: structuredClone(roles), original: structuredClone(role) })}>编辑角色</button>}{access.canManage && <div className="role-deletion-action"><button className="button danger" type="button" aria-label={`删除角色 ${role.displayName}`} disabled={loading || Boolean(busy) || Boolean(roleDeletionRestriction(role, access))} onClick={() => { setRoleNotice(''); setDeletionDraft({ access: structuredClone(access), role: structuredClone(role) }) }}>创建删除提案</button>{roleDeletionRestriction(role, access) && <small>{roleDeletionRestriction(role, access)}</small>}</div>}</div>)}</div>}</div><div><div className="section-heading"><h3>租户</h3>{access.canManage && <button className="button secondary" type="button" disabled={loading || Boolean(busy)} onClick={() => setTenantDraft({ access, tenants })}><Plus size={14} aria-hidden="true" />新增租户</button>}</div>{tenantNotice && <p role="status">{tenantNotice}</p>}{tenants.length === 0 ? <div className="empty-state compact">暂无租户</div> : <div className="tenant-list">{tenants.map((tenant) => <div className="tenant-row" key={tenant.id}><span><strong>{tenant.displayName}</strong><small><code>{tenant.id}</code></small></span><span>{tenant.status}</span>{access.canManage && <button className="button secondary" type="button" aria-label={`编辑租户 ${tenant.displayName}`} disabled={loading || Boolean(busy) || Boolean(tenantEditRestriction(tenant, access.defaultTenant))} title={tenantEditRestriction(tenant, access.defaultTenant)} onClick={() => setTenantDraft({ access, tenants, original: tenant })}>编辑名称</button>}</div>)}</div>}</div></div></section>
        <section className="page-section"><div className="section-heading"><h2>角色绑定</h2>{access.canManage && <button className="button secondary" type="button" onClick={() => setFormOpen((value) => !value)}><Plus size={14} />新增绑定</button>}</div>{bindingNotice && <p role="status">{bindingNotice}</p>}{access.canManage && formOpen && <p>创建的是待审批提案，尚未生效；以服务端详情判定新增或同 ID 编辑。空范围或 * 可匹配角色允许的平台资源。</p>}{access.canManage && formOpen && <form className="inline-form access-form" onSubmit={(event) => runAction(submitBinding(event))}><label><span>主体</span><input required placeholder="邮箱或 subject" value={binding.subject} onChange={(event) => setBinding({ ...binding, subject: event.target.value })} /></label><label><span>租户</span><select required value={binding.tenantId} onChange={(event) => setBinding({ ...binding, tenantId: event.target.value })}><option value="">选择租户</option>{tenants.map((tenant) => <option value={tenant.id} key={tenant.id}>{tenant.displayName}</option>)}</select></label><label><span>角色</span><select required value={binding.roleId} onChange={(event) => setBinding({ ...binding, roleId: event.target.value })}><option value="">选择角色</option>{roles.map((role) => <option value={role.id} key={role.id}>{role.displayName}</option>)}</select></label><label><span>对象范围（可选；空或 * 广泛匹配，可能含平台资源）</span><input placeholder="object:id" value={binding.objectIds} onChange={(event) => setBinding({ ...binding, objectIds: event.target.value })} /></label><button className="button secondary" type="submit" disabled={busy === 'access-change-create'}>{busy === 'access-change-create' ? '提交中' : '创建审批变更'}</button></form>}{bindings.length === 0 ? <div className="empty-state compact">暂无角色绑定</div> : <div className="binding-table" role="table" aria-label="角色绑定列表">{bindings.map((item) => { const role = roles.find((candidate) => candidate.id === item.roleId); const tenant = tenants.find((candidate) => candidate.id === item.tenantId); return <div className="binding-row" key={item.id}><span><strong>{maskedSubject(item)}</strong><small><code>{item.id}</code></small></span><span>{tenant?.displayName || item.tenantId}</span><span><KeyRound size={13} />{role?.displayName || item.roleId}</span><span>{!item.objectIds?.length || item.objectIds.includes('*') ? '广泛匹配（可能含平台资源）' : `${item.objectIds.length} 个对象`}</span>{access.canManage && <div className="binding-edit-action"><button className="button secondary" type="button" aria-label={`编辑绑定 ${item.id}`} disabled={loading || Boolean(busy) || Boolean(bindingEditRestriction(item, access))} onClick={() => { setBindingNotice(''); setBindingDraft({ access: structuredClone(access), original: structuredClone(item) }) }}>编辑绑定</button>{bindingEditRestriction(item, access) && <small>{bindingEditRestriction(item, access)}</small>}<button className="button danger" type="button" aria-label={`撤销绑定 ${item.id}`} disabled={loading || Boolean(busy) || Boolean(bindingRevokeRestriction(item, access))} onClick={() => { setBindingNotice(''); setRevokeDraft({ access: structuredClone(access), original: structuredClone(item) }) }}>创建撤销提案</button>{bindingRevokeRestriction(item, access) && <small>{bindingRevokeRestriction(item, access)}</small>}</div>}</div> })}</div>}</section>
        <section className="page-section"><div className="section-heading"><h2>策略审批</h2><span>{access.pendingChanges?.length ?? 0} 项</span></div>{(access.pendingChanges?.length ?? 0) === 0 ? <div className="empty-state compact">暂无策略审批变更</div> : <div className="runner-update-list">{access.pendingChanges?.map((change) => {
          const twoParty = change.approvalPolicy === 'two_party_v1'
          const approvalPending = change.state === 'pending_approval'
          const canApprove = access.canManage && approvalPending && canCurrentActorApprove(change, access.currentSubject?.subject)
          const canApply = access.canManage && change.state === 'approved' && change.actorHash === access.currentSubject?.subject
          const canReject = access.canManage && change.state === 'pending_approval' && change.actorHash === access.currentSubject?.subject
          return <article className={`runner-update-card runner-update-${change.state}`} key={change.id}><header><div className="runner-update-title"><span className={`service-indicator ${change.state === 'applied' ? 'healthy' : change.state === 'rejected' ? 'error' : 'warning'}`} /><div><strong>策略变更 {shortHash(change.id)}</strong><small>{change.state} · {formatTime(change.createdAt)}</small></div></div><code>{shortHash(change.requestDigest)}</code></header><dl className="runner-update-detail"><div><dt>创建人</dt><dd><code>{shortHash(change.actorHash)}</code></dd></div><div><dt>独立批准人</dt><dd><code>{shortHash(change.approvedByHash)}</code></dd></div>{!twoParty && <div><dt>第二批准</dt><dd><code>{shortHash(change.secondApprovedByHash)}</code></dd></div>}<div><dt>应用时间</dt><dd>{formatTime(change.appliedAt)}</dd></div></dl>{access.canManage && !loading && !error && <AccessChangeReview key={`${access.currentSubject?.subject}:${access.currentSubject?.tenantId}:${change.id}:${change.requestDigest}:${change.state}:${access.version}`} change={change} actor={access.currentSubject?.subject ?? ''} version={access.version} canApprove={canApprove} busy={busy} onRead={onReadDetail} onApprove={onApproveChange} />}{approvalPending && !canApprove && <small>当前身份不能批准此变更，请切换到尚未参与该计划的独立批准账号。</small>}{canReject && <div className="runner-update-actions"><button className="button secondary" type="button" disabled={busy === `access-change-reject:${change.id}`} onClick={() => runAction(onRejectChange(change, '创建人撤销策略变更'))}><Ban size={14} />撤销</button></div>}{change.state === 'approved' && access.canManage && !canApply && <small className="inline-error">独立批准已完成，需由创建人提交应用。</small>}{canApply && <div className="runner-update-actions"><span>{twoParty ? '独立批准完成，由创建人应用。' : '双人批准完成，必须由第三人执行。'}</span><button className="button danger" type="button" disabled={busy === `access-change-apply:${change.id}`} onClick={() => runAction(applyChange(change))}><Play size={14} />{busy === `access-change-apply:${change.id}` ? '执行中' : twoParty ? '创建人应用' : '独立执行'}</button></div>}{change.error && <div className="runner-update-error">{change.error}</div>}</article>
        })}</div>}</section>
      </>}
      {revokeDraft && <BindingRevokeDialog key={`${identityGeneration.current}:${revokeDraft.original.id}`} {...revokeDraft} onVerify={onVerifyActor} onCreate={onCreateChange} onRead={onReadDetail} onCancel={() => setRevokeDraft(null)} onCreated={change => setBindingNotice(`撤销提案 ${shortHash(change.id)} · ${change.state}。目标绑定仍存在，需独立批准后由创建人应用。`)} />}
      {bindingDraft && <BindingEditDialog key={`${identityGeneration.current}:${bindingDraft.original.id}`} {...bindingDraft} onVerify={onVerifyActor} onCreate={onCreateChange} onRead={onReadDetail} onCancel={() => setBindingDraft(null)} onCreated={change => setBindingNotice(`编辑提案 ${shortHash(change.id)} · ${change.state}。正式绑定尚未改变，需独立批准后由创建人应用。`)} />}
      {deletionDraft && <RoleDeletionDialog {...deletionDraft} onVerify={onVerifyActor} onCancel={() => setDeletionDraft(null)} onCreate={createDeletionChange} />}
      {roleDraft && <RoleDialog {...roleDraft} onVerify={onVerifyActor} onCancel={() => setRoleDraft(null)} onCreate={async body => { const change = await onCreateChange(body); if (change.state !== 'rejected') setRoleNotice(`角色提案 ${shortHash(change.id)} · ${change.state}。${change.state === 'applied' ? '该提案已应用，请核对正式角色。' : '待审批或应用，本次尚未生效。'}`); return change }} />}
      {tenantDraft && <TenantDialog {...tenantDraft} onCancel={() => setTenantDraft(null)} onCreate={async (body) => { const change = await onCreateChange(body); if (change.state !== 'rejected') setTenantNotice(`租户提案 ${shortHash(change.id)} · ${change.state}：${body.tenants?.[0]?.id} / ${body.tenants?.[0]?.displayName}。${change.state === 'applied' ? '该提案已应用。' : '本次仅取得提案，尚未应用策略。'}`); return change }} />}
    </div>
  )
}
