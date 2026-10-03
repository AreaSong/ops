import { useEffect, useRef, useState } from 'react'
import type { AccessChange, AccessControlUpdate, AccessControlView, AccessRole } from '../types'
import { forgetRoleDeletionKey, roleDeletionKey, roleDeletionRequest, roleDeletionRestriction } from '../roleChange'
import { usePlanDialog } from '../usePlanDialog'

interface Props {
  access: AccessControlView
  role: AccessRole
  onVerify: () => Promise<AccessControlView>
  onCreate: (body: AccessControlUpdate) => Promise<AccessChange>
  onCancel: () => void
}

export function RoleDeletionDialog({ access, role, onVerify, onCreate, onCancel }: Props) {
  const [pending, setPending] = useState(false), [error, setError] = useState(''), [invalid, setInvalid] = useState(false)
  const inFlight = useRef(false), alive = useRef(true)
  const dialog = usePlanDialog<HTMLDivElement>(pending, () => { if (!inFlight.current) onCancel() })
  useEffect(() => { alive.current = true; return () => { alive.current = false } }, [])
  useEffect(() => { if (error && !pending) dialog.current?.focus() }, [error, pending, dialog])

  async function submit() {
    if (inFlight.current || invalid) return
    inFlight.current = true; setPending(true); setError('')
    try {
      const actor = access.currentSubject?.subject, tenant = access.currentSubject?.tenantId
      if (!access.canManage || !actor) throw new Error('当前身份无权管理访问策略')
      const body = roleDeletionRequest(role, access)
      const current = await onVerify()
      if (!alive.current) return
      if (!current.canManage || current.currentSubject?.subject !== actor || current.currentSubject?.tenantId !== tenant) {
        setInvalid(true); throw new Error('会话身份已变化，请关闭弹窗并刷新后重新核对')
      }
      // 不替换打开时的版本；失败或响应丢失后只由用户显式重试同一请求。
      if (current.version !== access.version) throw new Error('策略版本已变化，请关闭弹窗并刷新后重新核对')
      const restriction = roleDeletionRestriction(role, current)
      if (restriction) throw new Error(restriction)
      const change = await onCreate({ ...body, idempotencyKey: roleDeletionKey(actor, tenant, body) })
      if (!alive.current) return
      if (change.state === 'rejected') {
        forgetRoleDeletionKey(actor, tenant, body)
        throw new Error('原删除提案已撤销。本次未创建新提案，请核对后再次提交。')
      }
      onCancel()
    } catch (reason) {
      if (alive.current) setError(reason instanceof Error ? reason.message : '提案创建失败，请核对审批列表后手动重试')
    } finally { inFlight.current = false; if (alive.current) setPending(false) }
  }

  return <div className="modal-backdrop"><div ref={dialog} tabIndex={-1} className="modal plan-dialog role-dialog" role="dialog" aria-modal="true" aria-labelledby="role-delete-title">
    <header className="modal-header"><h2 id="role-delete-title">创建角色删除提案</h2></header>
    <div className="modal-body">
      <p>独立批准后，由创建人应用才删除。不会解除绑定或清理其他引用；应用后不提供自动恢复。</p>
      <dl><dt>角色 ID</dt><dd>{role.id}</dd><dt>角色名称</dt><dd>{role.displayName}</dd></dl>
      <p>固定策略版本：{access.version}。列表未发现引用不代表已证明可删，创建后须读取服务端删除详情。</p>
      <p>取消不写入；关闭弹窗不等于撤销已经创建的提案。</p>
      {error && <p className="inline-error" role="alert">{error}</p>}
    </div>
    <footer className="modal-footer"><button type="button" className="button secondary" disabled={pending} onClick={onCancel}>取消</button><button type="button" className="button danger" disabled={pending || invalid} onClick={() => void submit()}>{pending ? '提交中' : '创建删除提案'}</button></footer>
  </div></div>
}
