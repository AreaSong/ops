import { useEffect, useRef, useState } from 'react'
import type { AccessBinding, AccessChange, AccessChangeDetail, AccessControlUpdate, AccessControlView } from '../types'
import { bindingRoles } from '../bindingChange'
import { bindingRevokeDetailMatches, bindingRevokeKey, bindingRevokeRequest, forgetBindingRevokeKey, validBindingRevokeResponse, verifyBindingRevoke } from '../bindingRevoke'
import { usePlanDialog } from '../usePlanDialog'

interface Props {
  access: AccessControlView
  original: AccessBinding
  onVerify: () => Promise<AccessControlView>
  onCreate: (body: AccessControlUpdate) => Promise<AccessChange>
  onRead: (id: string, actor: string, signal?: AbortSignal) => Promise<AccessChangeDetail>
  onCreated: (change: AccessChange) => void
  onCancel: () => void
}
export function BindingRevokeDialog({ access, original, onVerify, onCreate, onRead, onCreated, onCancel }: Props) {
  const [pending, setPending] = useState(false), [error, setError] = useState(''), [invalid, setInvalid] = useState(false)
  const inFlight = useRef(false), alive = useRef(true), request = useRef<AbortController | null>(null)
  const dialog = usePlanDialog<HTMLDivElement>(pending, () => { if (!inFlight.current) onCancel() })
  useEffect(() => { alive.current = true; return () => { alive.current = false; request.current?.abort() } }, [])
  useEffect(() => { if (error && !pending) dialog.current?.focus() }, [error, pending, dialog])
  async function submit() {
    if (inFlight.current || invalid) return
    inFlight.current = true; setPending(true); setError('')
    try {
      const actor = access.currentSubject?.subject, tenant = access.currentSubject?.tenantId
      if (!access.canManage || !actor) throw new Error('当前身份无权管理访问策略')
      const body = bindingRevokeRequest(original, access), current = await onVerify()
      if (!alive.current) return
      try { verifyBindingRevoke(original, access, current) } catch (reason) { setInvalid(true); throw reason }
      const change = await onCreate({ ...body, idempotencyKey: bindingRevokeKey(actor, tenant, body) })
      if (!alive.current) return
      if (!validBindingRevokeResponse(change, actor)) {
        setInvalid(true)
        throw new Error('提案可能已保存，但返回标识、摘要、状态或创建身份与冻结身份不一致；请关闭并只读核对审批列表，不可自动补发。')
      }
      if (change.state === 'rejected') {
        forgetBindingRevokeKey(actor, tenant, body)
        throw new Error('原撤销提案已拒绝。本次未新建，请核对后再次提交以显式重建。')
      }
      if (change.state === 'applied') { setInvalid(true); throw new Error('该提案已应用，当前结果待核对；请关闭并只读刷新，不要再次执行。') }
      const controller = new AbortController(); request.current = controller
      const detail = await onRead(change.id, actor, controller.signal)
      if (!alive.current) return
      if (detail.reviewerHash !== actor || !bindingRevokeDetailMatches(detail, change, access, original)) {
        setInvalid(true)
        throw new Error('提案已保存，但服务端未确认本次固定目标的撤销差异；请只读核对审批列表，不可按撤销成功继续。')
      }
      onCreated(change); onCancel()
    } catch (reason) {
      if (alive.current) setError(reason instanceof Error ? reason.message : '提案创建结果未确认，请先只读核对审批列表；不要换键补发。')
    } finally { inFlight.current = false; if (alive.current) setPending(false) }
  }
  const role = bindingRoles(access).find(r => r.id === original.roleId)
  return <div className="modal-backdrop"><div ref={dialog} tabIndex={-1} className="modal plan-dialog binding-edit-dialog binding-revoke-dialog" role="dialog" aria-modal="true" aria-labelledby="binding-revoke-title">
    <header className="modal-header"><h2 id="binding-revoke-title">创建绑定撤销提案</h2></header>
    <div className="modal-body">
      <p>创建提案和批准均不移除绑定；只有独立批准并由创建人应用后，才移除下列单条绑定。</p>
      <dl className="binding-identity"><dt>绑定 ID（固定）</dt><dd>{original.id}</dd><dt>完整主体哈希（固定）</dt><dd>{original.subject}</dd><dt>租户 ID（固定）</dt><dd>{original.tenantId}</dd><dt>角色 ID（固定）</dt><dd>{original.roleId}</dd><dt>当前角色完整权限</dt><dd>{role?.permissions.map(p => p === '*' ? '*（全部权限）' : p).join('、')}</dd><dt>原对象列表（顺序与重复保留）</dt><dd><code>{JSON.stringify(original.objectIds ?? [])}</code></dd><dt>原期限</dt><dd>{original.expiresAt ?? '不设绑定到期时间'}</dd></dl>
      <p>固定策略版本：{access.version}。列表仅排除已知问题，历史来源及支持范围以服务端详情为准。完整哈希是可关联标识。</p>
      <p>撤销不保证主体完全失权，不注销登录会话、不停止在途任务，也不撤销其他授权。若撤销了自己的管理权限，应用后的读回可能无法完成。</p>
      <p>取消不写入；关闭弹窗不撤销已创建的提案。应用后不提供自动重新授权。</p>
      {error && <p className="inline-error" role="alert">{error}</p>}
    </div>
    <footer className="modal-footer"><button className="button secondary" type="button" disabled={pending} onClick={onCancel}>取消</button><button className="button danger" type="button" disabled={pending || invalid} onClick={() => void submit()}>{pending ? '提交中' : '创建撤销提案'}</button></footer>
  </div></div>
}
