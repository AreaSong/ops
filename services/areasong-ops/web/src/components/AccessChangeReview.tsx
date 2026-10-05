import { useEffect, useRef, useState } from 'react'
import { BindingChangeReview } from './BindingChangeReview'
import { ShieldCheck } from 'lucide-react'
import type { AccessChange, AccessChangeDetail } from '../types'
import { deletionUnavailableMessage, reviewAllowsApproval } from '../accessChangeReview'

interface Props {
  change: AccessChange
  actor: string
  version?: number
  canApprove: boolean
  busy: string
  onRead: (id: string, actor: string, signal?: AbortSignal) => Promise<AccessChangeDetail>
  onApprove: (change: AccessChange, confirmation: string) => Promise<void>
}

export function AccessChangeReview({ change, actor, version, canApprove, busy, onRead, onApprove }: Props) {
  const [detail, setDetail] = useState<AccessChangeDetail | null>(null)
  const [error, setError] = useState('')
  const [loading, setLoading] = useState(false)
  const [confirmation, setConfirmation] = useState('')
  const pending = useRef<AbortController | null>(null)
  useEffect(() => () => { pending.current?.abort() }, [])
  const allowed = detail && reviewAllowsApproval(detail, change, version)

  async function read(approve = false) {
    if (pending.current) return
    const controller = new AbortController()
    pending.current = controller
    setLoading(true); setDetail(null); setError('')
    if (!approve) setConfirmation('')
    try {
      const result = await onRead(change.id, actor, controller.signal)
      if (controller.signal.aborted) return
      if (result.reviewerHash !== actor) throw new Error('会话身份已变化，请刷新访问策略后重新审阅。')
      if (result.id !== change.id || result.requestDigest !== change.requestDigest || result.state !== change.state) throw new Error('提案信息已变化，请刷新访问策略后重新审阅。')
      setDetail(result)
      if (approve) {
        if (!reviewAllowsApproval(result, change, version)) throw new Error('详情已失效，请刷新访问策略后重新审阅。')
        await onApprove(change, confirmation)
      }
    } catch (reason) {
      if (!controller.signal.aborted) { setDetail(null); setConfirmation(''); setError(reason instanceof Error ? reason.message : '详情读取失败，请重试。') }
    } finally {
      if (!controller.signal.aborted) { pending.current = null; setLoading(false) }
    }
  }

  return <section className="access-change-review" aria-label={`审阅提案 ${change.id}`} aria-busy={loading}>
    <button className="button secondary" type="button" disabled={loading || Boolean(busy)} onClick={() => void read()}>{loading ? '正在核验详情' : detail ? '重新读取详情' : '读取服务端详情'}</button>
    {error && <p role="alert" className="inline-error">{error}</p>}
    {detail && <>
      <dl className="runner-update-detail">
        <div><dt>提案 ID</dt><dd><code>{detail.id}</code></dd></div>
        <div><dt>请求摘要</dt><dd><code>{detail.requestDigest}</code></dd></div>
        <div><dt>提案状态</dt><dd>{detail.state}</dd></div>
        <div><dt>基准版本</dt><dd>{detail.expectedVersion || '不可用'}</dd></div>
      </dl>
      {detail.kind === 'binding' && allowed && <BindingChangeReview detail={detail} />}
      {detail.kind === 'tenant' && allowed && <>
        <p role="status">{detail.operation === 'create' ? '新增租户' : '修改租户名称'} · 当前差异有效</p>
        <dl className="runner-update-detail"><div><dt>租户 ID</dt><dd>{detail.after?.id}</dd></div><div><dt>修改前</dt><dd>{detail.before ? detail.before.displayName : '此前不存在'}</dd></div><div><dt>修改后</dt><dd>{detail.after?.displayName}</dd></div><div><dt>状态</dt><dd>{detail.after?.status}</dd></div></dl>
      </>}
      {detail.kind === 'role' && allowed && detail.role && <>
        <p role="status">{detail.operation === 'create' ? '新增自定义角色' : '编辑自定义角色'} · 当前差异有效</p>
        <dl className="runner-update-detail">
          <div><dt>角色 ID</dt><dd>{detail.role.after.id}</dd></div>
          <div><dt>名称修改前</dt><dd>{detail.role.before?.displayName ?? '此前不存在'}</dd></div>
          <div><dt>名称修改后</dt><dd>{detail.role.after.displayName}</dd></div>
          <div><dt>原完整权限（保留顺序与重复）</dt><dd>{detail.role.before?.permissions.join('、') || '此前不存在'}</dd></div>
          <div><dt>新完整权限（保留顺序与重复）</dt><dd>{detail.role.after.permissions.join('、')}</dd></div>
          <div><dt>新增权限</dt><dd>{detail.role.permissionDiff.added.join('、') || '无'}</dd></div>
          <div><dt>移除权限</dt><dd>{detail.role.permissionDiff.removed.join('、') || '无'}</dd></div>
          <div><dt>保持权限</dt><dd>{detail.role.permissionDiff.unchanged.join('、') || '无'}</dd></div>
        </dl>
        <p>基准版本的全部引用绑定，包含过期绑定：{detail.role.impact.bindingCount} 条，涉及 {detail.role.impact.tenantCount} 个租户。</p>
        <p>{detail.role.impact.affectsExistingBindings ? '应用后权限调整会影响既有绑定，本次不重写绑定。' : '基准版本无引用绑定。'} 此统计不是当前有效用户数。</p>
      </>}
      {detail.kind === 'role_deletion' && allowed && detail.roleDeletion && <>
        <p role="status">删除自定义角色 · 应用后角色将不存在</p>
        <dl className="runner-update-detail"><div><dt>角色 ID</dt><dd>{detail.roleDeletion.before.id}</dd></div><div><dt>角色名称</dt><dd>{detail.roleDeletion.before.displayName}</dd></div><div><dt>原完整权限（保留顺序与重复）</dt><dd>{detail.roleDeletion.before.permissions.join('、')}</dd></div></dl>
        <p>基准版本未发现绑定及主体直接引用。此结论不保证后续不会产生引用；应用时仍需通过服务端检查。</p>
      </>}
      {detail.kind === 'other' && allowed && <p>此提案不属于租户或角色详情支持范围，请沿原有流程审阅其完整内容。</p>}
      {!allowed && !(['role_deletion', 'binding'].includes(detail.kind) && change.state === 'applied') && <p role="alert" className="inline-error">{detail.kind === 'role_deletion' ? deletionUnavailableMessage(detail) : detail.availability === 'stale' || detail.availability === 'ready' ? '差异已过期，请刷新策略并重新创建提案。' : detail.availability === 'unsupported' ? '不支持此提案的完整差异，不能据此批准。' : '差异基准不可用，不能据此批准。'}</p>}
      {detail.kind === 'binding' && change.state === 'applied' && <p role="status">该绑定提案已应用；历史差异可能已失效，请只读核对生效策略。不要重新应用或重建提案。</p>}
      {detail.kind === 'role_deletion' && change.state === 'applied' && <p role="status">该提案已应用，请以刷新后的生效策略核对结果。</p>}
    </>}
    {canApprove && allowed && !loading && <div className="runner-update-actions attention"><label><span>批准确认</span><code>{change.confirmationPhrase}</code><input value={confirmation} onChange={(event) => setConfirmation(event.target.value)} /></label><button className="button secondary" type="button" disabled={confirmation !== change.confirmationPhrase || Boolean(busy)} onClick={() => void read(true)}><ShieldCheck size={14} />{change.approvalPolicy === 'two_party_v1' ? '独立批准' : change.approvedByHash ? '第二人批准' : '第一人批准'}</button></div>}
  </section>
}
