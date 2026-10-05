import { useEffect, useRef, useState } from 'react'
import type { AccessBinding, AccessChange, AccessChangeDetail, AccessControlUpdate, AccessControlView } from '../types'
import { bindingEditDetailMatches, bindingEditKey, bindingEditRequest, bindingRoles, bindingRoleSupported, forgetBindingEditKey, parseBindingObjects, verifyBindingEdit, type BindingExpiryMode } from '../bindingChange'
import { bindingInstant } from '../accessChangeApply'
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
export function BindingEditDialog({ access, original, onVerify, onCreate, onRead, onCreated, onCancel }: Props) {
  const [roleId, setRoleID] = useState(original.roleId)
  const [objects, setObjects] = useState(JSON.stringify(original.objectIds ?? []))
  const [expiryMode, setExpiryMode] = useState<BindingExpiryMode>('keep'), [expiresAt, setExpiresAt] = useState('')
  const [pending, setPending] = useState(false), [error, setError] = useState(''), [invalid, setInvalid] = useState(false), [discard, setDiscard] = useState(false)
  const inFlight = useRef(false), alive = useRef(true), request = useRef<AbortController | null>(null)
  const dirty = roleId !== original.roleId || objects !== JSON.stringify(original.objectIds ?? []) || expiryMode !== 'keep'
  const cancel = () => { if (inFlight.current) return; if (dirty) setDiscard(true); else onCancel() }
  const dialog = usePlanDialog<HTMLFormElement>(pending, cancel)
  useEffect(() => { alive.current = true; return () => { alive.current = false; request.current?.abort() } }, [])
  useEffect(() => { if (error && !pending) dialog.current?.focus() }, [error, pending, dialog])
  useEffect(() => {
    const warn = (event: BeforeUnloadEvent) => { if (dirty) event.preventDefault() }
    window.addEventListener('beforeunload', warn); return () => window.removeEventListener('beforeunload', warn)
  }, [dirty])
  let parsed: string[] | undefined, objectError = ''
  try { parsed = parseBindingObjects(objects) } catch (reason) { objectError = (reason as Error).message }
  const roles = bindingRoles(access), currentRole = roles.find(r => r.id === original.roleId), selectedRole = roles.find(r => r.id === roleId)
  const expiry = expiryMode === 'keep' ? original.expiresAt : expiryMode === 'clear' ? undefined : expiresAt
  const instant = bindingInstant(expiry)
  const permissionText = (permissions?: string[]) => permissions?.map(p => p === '*' ? '*（全部权限）' : p).join('、')

  async function submit() {
    if (inFlight.current || invalid || discard) return
    inFlight.current = true; setPending(true); setError('')
    try {
      if (!access.canManage || !access.currentSubject?.subject) throw new Error('当前身份无权管理访问策略')
      const actor = access.currentSubject.subject, tenant = access.currentSubject.tenantId
      const body = bindingEditRequest({ original, access, roleId, objects: parseBindingObjects(objects), expiryMode, expiresAt })
      const current = await onVerify()
      if (!alive.current) return
      try { verifyBindingEdit(original, access, current) } catch (reason) { setInvalid(true); throw reason }
      const change = await onCreate({ ...body, idempotencyKey: bindingEditKey(actor, tenant, body) })
      if (!alive.current) return
      if (change.actorHash !== actor) throw new Error('提案可能已保存，但创建身份与冻结身份不一致；请关闭并刷新核对，不可按本次编辑成功继续')
      if (change.state === 'rejected') {
        forgetBindingEditKey(actor, tenant, body)
        throw new Error('原编辑提案已撤销。本次未新建，请核对后再次提交以显式重建。')
      }
      if (change.state === 'applied') throw new Error('该提案已应用，当前结果待核对；请关闭并刷新，不要再次执行')
      const controller = new AbortController(); request.current = controller
      const detail = await onRead(change.id, actor, controller.signal)
      if (!alive.current) return
      if (detail.reviewerHash !== actor || !bindingEditDetailMatches(detail, change, body, original)) throw new Error('提案已保存，但服务端未确认本次固定身份的编辑差异；请在审批列表核对，不可按编辑成功继续')
      onCreated(change); onCancel()
    } catch (reason) {
      if (alive.current) setError(reason instanceof Error ? reason.message : '提案创建失败，请核对审批列表后手动重试')
    } finally { inFlight.current = false; if (alive.current) setPending(false) }
  }
  return <div className="modal-backdrop"><form ref={dialog} tabIndex={-1} className="modal plan-dialog binding-edit-dialog" role="dialog" aria-modal="true" aria-labelledby="binding-edit-title" noValidate onSubmit={event => { event.preventDefault(); void submit() }}>
    <header className="modal-header"><h2 id="binding-edit-title">编辑既有绑定</h2></header>
    <div className="modal-body">
      <p>仅创建提案；独立批准后，由创建人应用并读回核对才确认生效。取消不写入；关闭不等于撤销已创建的提案。</p>
      <dl className="binding-identity"><dt>绑定 ID（固定）</dt><dd>{original.id}</dd><dt>完整主体哈希（固定）</dt><dd>{original.subject}</dd><dt>租户 ID（固定）</dt><dd>{original.tenantId}</dd></dl>
      <p>固定策略版本：{access.version}。列表仅排除已知问题，历史来源及支持范围仍以服务端详情为准。哈希是可关联标识。</p>
      <fieldset disabled={pending || invalid || discard}>
        <label className="confirmation-field"><span>绑定角色</span><select value={roleId} onChange={e => { setRoleID(e.target.value); setError('') }}>{roles.map(r => <option key={r.id} value={r.id} disabled={!bindingRoleSupported(r)}>{r.displayName} · {r.id}</option>)}</select></label>
        <p>当前角色权限：{permissionText(currentRole?.permissions)}</p><p>选中角色权限：{permissionText(selectedRole?.permissions)}</p>
        <label className="confirmation-field"><span>完整对象数组（JSON）</span><textarea rows={4} value={objects} onChange={e => { setObjects(e.target.value); setError('') }} aria-describedby="binding-objects-help" /></label>
        <small id="binding-objects-help">每个字符串是一个精确对象 ID；保留内部空白、逗号、顺序及重复项，不自动排序或去重。无需登记对象。</small>
        <button className="button secondary" type="button" onClick={() => setObjects('[]')}>显式清空对象范围</button>
        {objectError ? <p role="alert" className="inline-error">{objectError}</p> : <><p>完整数组预览：<code>{JSON.stringify(parsed)}</code></p>{parsed && (!parsed.length || parsed.includes('*')) && <p role="status">广泛匹配任意对象，可能包含角色允许的平台资源。</p>}</>}
        <label className="confirmation-field"><span>期限操作</span><select value={expiryMode} onChange={e => { setExpiryMode(e.target.value as BindingExpiryMode); setError('') }}><option value="keep">保持原期限</option><option value="set">设置新期限</option><option value="clear">不设期限</option></select></label>
        <p>原期限：{original.expiresAt ?? '不设绑定到期时间'}。保持时原字符串与全部纳秒精度不变。</p>
        {expiryMode === 'set' && <><label className="confirmation-field"><span>新期限（RFC3339，含时区偏移）</span><input value={expiresAt} onChange={e => { setExpiresAt(e.target.value); setError('') }} placeholder="2030-01-01T12:00:00.123456789+08:00" /></label><p>输入实际日期、时间及 Z 或 ±HH:MM 偏移，精度为秒至纳秒（最多 9 位小数）。固定偏移不使用夏令时地区规则，请确认目标日期的实际偏移；这是主动替换原期限。</p></>}
        <p>请求期限：{expiry ?? '不设绑定到期时间'}</p>
        {instant === undefined && <p role="alert" className="inline-error">日期或偏移无效，不会自动归一化为其他时间。</p>}
        {typeof instant === 'bigint' && instant <= BigInt(Date.now()) * 1000000n && <p role="status">此时间已过去，应用后绑定已过期；其他授权来源仍可能有效。</p>}
        <p>以上仅为提交前预览；独立批准人须读取服务端保存提案的真实差异。</p>
      </fieldset>
      {error && <p className="inline-error" role="alert">{error}</p>}{discard && <p role="alert">尚有未提交草稿，是否放弃？已创建的提案不会被撤销。</p>}
    </div>
    <footer className="modal-footer">{discard ? <><button className="button secondary" type="button" onClick={() => setDiscard(false)}>继续编辑</button><button className="button danger" type="button" onClick={onCancel}>放弃修改</button></> : <><button className="button secondary" type="button" disabled={pending} onClick={cancel}>取消</button><button className="button primary" type="submit" disabled={pending || invalid}>{pending ? '提交中' : '创建编辑提案'}</button></>}</footer>
  </form></div>
}
