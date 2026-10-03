import { useEffect, useRef, useState } from 'react'
import type { AccessChange, AccessControlUpdate, AccessControlView, AccessTenant } from '../types'
import { forgetTenantRequestKey, tenantChangeRequest, tenantRequestKey } from '../tenantChange'
import { usePlanDialog } from '../usePlanDialog'

interface Props {
  access: AccessControlView
  tenants: AccessTenant[]
  original?: AccessTenant
  onCancel: () => void
  onCreate: (body: AccessControlUpdate) => Promise<AccessChange>
}

export function TenantDialog({ access, tenants, original, onCancel, onCreate }: Props) {
  const [id, setID] = useState(original?.id ?? '')
  const [displayName, setDisplayName] = useState(original?.displayName ?? '')
  const [pending, setPending] = useState(false), [error, setError] = useState('')
  const [discard, setDiscard] = useState(false)
  const inFlight = useRef(false)
  const dirty = id !== (original?.id ?? '') || displayName !== (original?.displayName ?? '')
  const cancel = () => { if (inFlight.current) return; if (dirty) setDiscard(true); else onCancel() }
  const dialog = usePlanDialog<HTMLFormElement>(pending, cancel)
  function changeName(value: string) { setDisplayName(value); setError('') }
  function changeID(value: string) { setID(value); setError('') }
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (dirty) event.preventDefault() }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])

  async function submit() {
    if (inFlight.current) return
    setError('')
    try {
      if (!access.canManage || !access.currentSubject?.subject) throw new Error('当前身份无权管理访问策略')
      const body = tenantChangeRequest({ id, displayName, original, tenants, defaultTenant: access.defaultTenant, version: access.version })
      const idempotencyKey = tenantRequestKey(access.currentSubject.subject, body)
      inFlight.current = true; setPending(true)
      const change = await onCreate({ ...body, idempotencyKey })
      if (change.state === 'rejected') {
        forgetTenantRequestKey(access.currentSubject.subject, body)
        throw new Error('同一请求的原提案已撤销。本次未创建新提案，请核对后再次提交。')
      }
      onCancel()
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '提案创建失败，请核对审批列表后手动重试')
    } finally { inFlight.current = false; setPending(false) }
  }

  return <div className="modal-backdrop">
    <form ref={dialog} tabIndex={-1} className="modal plan-dialog tenant-dialog" role="dialog" aria-modal="true" aria-labelledby="tenant-title" noValidate onSubmit={(event) => { event.preventDefault(); if (!discard) void submit() }}>
      <header className="modal-header"><h2 id="tenant-title">{original ? '编辑租户名称' : '新增租户提案'}</h2></header>
      <div className="modal-body">
        <p>提交后仅创建访问策略变更提案；独立批准后，由创建人应用才生效。</p>
        <label className="confirmation-field"><span>租户 ID</span><input value={id} readOnly={Boolean(original)} disabled={pending} maxLength={40} onChange={(event) => changeID(event.target.value)} aria-describedby="tenant-id-help" /></label>
        <small id="tenant-id-help">{original ? 'ID 不可修改' : '2–40 位小写字母、数字或连字符，以字母开头'}</small>
        <label className="confirmation-field"><span>租户名称</span><input value={displayName} disabled={pending} maxLength={120} onChange={(event) => changeName(event.target.value)} /></label>
        <p>名称网页输入上限 120 字符。状态：{original?.status ?? 'active'}（本次不修改）；策略版本：{access.version}。</p>
        {original && <p>名称变更：{original.displayName} → {displayName.trim() || '（未填写）'}</p>}
        {error && <p className="inline-error" role="alert">{error}</p>}
        {discard && <p role="alert">尚有未提交内容，是否放弃？</p>}
      </div>
      <footer className="modal-footer">{discard ? <><button className="button secondary" type="button" onClick={() => setDiscard(false)}>继续编辑</button><button className="button danger" type="button" onClick={onCancel}>放弃修改</button></> : <><button className="button secondary" type="button" disabled={pending} onClick={cancel}>取消</button><button className="button primary" type="submit" disabled={pending}>{pending ? '提交中' : '创建租户提案'}</button></>}</footer>
    </form>
  </div>
}
