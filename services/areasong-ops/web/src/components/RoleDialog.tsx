import { useEffect, useRef, useState } from 'react'
import type { AccessChange, AccessControlUpdate, AccessControlView, AccessRole } from '../types'
import { forgetRoleRequestKey, roleChangeRequest, roleRequestKey, rolePermissions } from '../roleChange'
import { usePlanDialog } from '../usePlanDialog'

interface Props {
  access: AccessControlView
  roles: AccessRole[]
  original?: AccessRole
  onVerify: () => Promise<AccessControlView>
  onCancel: () => void
  onCreate: (body: AccessControlUpdate) => Promise<AccessChange>
}

export function RoleDialog({ access, roles, original, onVerify, onCancel, onCreate }: Props) {
  const [id, setID] = useState(original?.id ?? '')
  const [displayName, setDisplayName] = useState(original?.displayName ?? '')
  const [permissions, setPermissions] = useState<string[]>(() => [...(original?.permissions ?? [])])
  const [invalidIdentity, setInvalidIdentity] = useState(false)
  const alive = useRef(true)
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  const [pending, setPending] = useState(false), [error, setError] = useState('')
  const [discard, setDiscard] = useState(false)
  const inFlight = useRef(false)
  const dirty = id !== (original?.id ?? '') || displayName !== (original?.displayName ?? '') || JSON.stringify(permissions) !== JSON.stringify(original?.permissions ?? [])
  const cancel = () => { if (inFlight.current) return; if (dirty) setDiscard(true); else onCancel() }
  const dialog = usePlanDialog<HTMLFormElement>(pending, cancel)
  useEffect(() => { if (error && !pending) dialog.current?.focus() }, [error, pending, dialog])
  function changeName(value: string) { setDisplayName(value); setError('') }
  function changeID(value: string) { setID(value); setError('') }
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (dirty) event.preventDefault() }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])

  async function submit() {
    if (inFlight.current || invalidIdentity) return
    inFlight.current = true; setPending(true)
    setError('')
    try {
      if (!access.canManage || !access.currentSubject?.subject) throw new Error('当前身份无权管理访问策略')
      const body = roleChangeRequest({ id, displayName, original, roles, permissions, version: access.version })
      const current = await onVerify()
      if (!alive.current) return
      if (!current.canManage || current.currentSubject?.subject !== access.currentSubject.subject || current.currentSubject?.tenantId !== access.currentSubject.tenantId) {
        setInvalidIdentity(true); throw new Error('会话身份已变化，请关闭草稿并刷新后重新填写')
      }
      const idempotencyKey = roleRequestKey(access.currentSubject.subject, body)
      const change = await onCreate({ ...body, idempotencyKey })
      if (change.state === 'rejected') {
        forgetRoleRequestKey(access.currentSubject.subject, body)
        throw new Error('同一请求的原提案已撤销。本次未创建新提案，请核对后再次提交。')
      }
      if (alive.current) onCancel()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '提案创建失败，请核对审批列表后手动重试')
    } finally { inFlight.current = false; setPending(false) }
  }

  return <div className="modal-backdrop">
    <form ref={dialog} tabIndex={-1} className="modal plan-dialog role-dialog" role="dialog" aria-modal="true" aria-labelledby="role-title" noValidate onSubmit={(event) => { event.preventDefault(); if (!discard) void submit() }}>
      <header className="modal-header"><h2 id="role-title">{original ? '编辑自定义角色' : '新增角色提案'}</h2></header>
      <div className="modal-body">
        <p>提交后仅创建访问策略变更提案；独立批准后，由创建人应用才生效。</p>
        <label className="confirmation-field"><span>角色 ID</span><input value={id} readOnly={Boolean(original)} disabled={pending} maxLength={40} onChange={(event) => changeID(event.target.value)} aria-describedby="role-id-help" /></label>
        <small id="role-id-help">{original ? 'ID 不可修改' : '2–40 位小写字母、数字或连字符，以字母开头'}</small>
        <label className="confirmation-field"><span>角色名称</span><input value={displayName} disabled={pending} maxLength={120} onChange={(event) => changeName(event.target.value)} /></label>
        <p>新增或改名上限 120 字符；未改动的历史长名称保留。固定策略版本：{access.version}。</p>
        <fieldset disabled={pending || invalidIdentity}><legend>完整权限</legend><div className="role-permission-options">{rolePermissions.map(permission => <label key={permission}><input type="checkbox" checked={permissions.includes(permission)} onChange={event => { const next = new Set(permissions); if (event.target.checked) next.add(permission); else next.delete(permission); setPermissions(rolePermissions.filter(p => next.has(p))); setError('') }} />{permission}</label>)}</div></fieldset>
        {original && <p>权限调整在应用后影响引用此角色的既有绑定；本次不修改绑定。仅改名称时保留原权限数组的顺序和重复项。</p>}
        {original && <p>名称变更：{original.displayName} → {displayName.trim() || '（未填写）'}</p>}
        {error && <p className="inline-error" role="alert">{error}</p>}
        {discard && <p role="alert">尚有未提交内容，是否放弃？</p>}
      </div>
      <footer className="modal-footer">{discard ? <><button className="button secondary" type="button" onClick={() => setDiscard(false)}>继续编辑</button><button className="button danger" type="button" onClick={onCancel}>放弃修改</button></> : <><button className="button secondary" type="button" disabled={pending} onClick={cancel}>取消</button><button className="button primary" type="submit" disabled={pending || invalidIdentity}>{pending ? '提交中' : '创建角色提案'}</button></>}</footer>
    </form>
  </div>
}
