import { Check, Code2, FlaskConical, LoaderCircle, RefreshCw, ShieldCheck, TerminalSquare } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { shortHash } from '../labels'
import type { ComposeRevision, ComposeServiceView, ExtensionPolicyView, KubernetesConfigView, KubernetesOperation, KubernetesPlan, ServiceView } from '../types'

type ConfigTab = 'compose' | 'kubernetes' | 'extensions'

interface ConfigurationProps {
  services: ServiceView[]
  selectedService: string
  compose: ComposeServiceView | null
  composeLoading: boolean
  composeAvailable: boolean
  composeError: string
  kubernetes: KubernetesConfigView | null
  kubernetesLoading: boolean
  kubernetesAvailable: boolean
  kubernetesError: string
  extensions: ExtensionPolicyView | null
  extensionsLoading: boolean
  extensionsAvailable: boolean
  extensionsError: string
  busy: string
  onService: (name: string) => void
  onComposeRefresh: () => void
  onSaveCompose: (body: { content: string; expectedDigest: string; mode: 'validate' | 'propose'; confirmation?: string }) => Promise<void>
  onComposeApprove?: (revision: ComposeRevision, confirmation: string) => Promise<void>
  onComposeApply?: (revision: ComposeRevision) => Promise<void>
  onComposeRollback?: (revision: ComposeRevision, confirmation: string) => Promise<void>
  onKubernetesRefresh: () => void
  onKubernetesOperation: (body: { target: KubernetesOperation['target']; manifest?: string }) => Promise<void>
  onKubernetesPlan: (body: { target: KubernetesOperation['target']; manifest: string }) => Promise<void>
  onKubernetesApprove: (plan: KubernetesPlan, confirmation: string) => Promise<void>
  onKubernetesExecute: (plan: KubernetesPlan) => Promise<void>
  onExtensionsRefresh: () => void
}

const tabs: Array<{ id: ConfigTab; label: string; icon: typeof Code2 }> = [
  { id: 'compose', label: 'Compose', icon: Code2 },
  { id: 'kubernetes', label: 'Kubernetes', icon: TerminalSquare },
  { id: 'extensions', label: '扩展', icon: FlaskConical },
]

function targetEntries(config: KubernetesConfigView | null): Array<[string, KubernetesOperation['target']]> {
  if (!config?.targets) return []
  if (Array.isArray(config.targets)) return config.targets.map((target, index) => [String(index), target])
  return Object.entries(config.targets)
}

export function Configuration({
  services, selectedService, compose, composeLoading, composeAvailable, composeError,
  kubernetes, kubernetesLoading, kubernetesAvailable, kubernetesError,
  extensions, extensionsLoading, extensionsAvailable, extensionsError, busy,
  onService, onComposeRefresh, onSaveCompose, onComposeApprove, onComposeApply, onComposeRollback,
  onKubernetesRefresh, onKubernetesOperation,
  onKubernetesPlan, onKubernetesApprove, onKubernetesExecute,
  onExtensionsRefresh,
}: ConfigurationProps) {
  const [tab, setTab] = useState<ConfigTab>('compose')
  const [composeContent, setComposeContent] = useState('')
  const [composeMode, setComposeMode] = useState<'validate' | 'propose'>('validate')
  const [composeConfirmation, setComposeConfirmation] = useState('')
  const [composeConfirmations, setComposeConfirmations] = useState<Record<string, string>>({})
  const [manifest, setManifest] = useState('')
  const [kubeConfirmations, setKubeConfirmations] = useState<Record<string, string>>({})
  const [targetKey, setTargetKey] = useState('')

  useEffect(() => {
    setComposeContent(compose?.current?.content ?? compose?.content ?? '')
    setComposeConfirmation('')
    setComposeConfirmations({})
  }, [compose])
  useEffect(() => {
    if (!targetKey && targetEntries(kubernetes).length > 0) setTargetKey(targetEntries(kubernetes)[0][0])
  }, [kubernetes, targetKey])

  const targets = useMemo(() => targetEntries(kubernetes), [kubernetes])
  const selectedTarget = targets.find(([key]) => key === targetKey)?.[1] ?? targets[0]?.[1]
  const composeDigest = compose?.current?.digest ?? compose?.digest ?? ''

  async function saveCompose() {
    await onSaveCompose({ content: composeContent, expectedDigest: composeDigest, mode: composeMode, confirmation: composeConfirmation || undefined })
  }

  function revisionConfirmation(id: string): string {
    return composeConfirmations[id] ?? ''
  }

  function setComposeConfirmationFor(id: string, value: string) {
    setComposeConfirmations((current) => ({ ...current, [id]: value }))
  }

  function revisionStateLabel(state?: ComposeRevision['state']): string {
    switch (state) {
      case 'proposed': return '等待第一批准'
      case 'pending_second_approval': return '等待第二批准'
      case 'approved': return '等待独立执行'
      case 'applying': return '应用中'
      case 'applied': return '已应用'
      case 'rolling_back': return '回滚中'
      case 'rolled_back': return '已回滚'
      case 'failed': return '应用失败'
      case 'needs_attention': return '需要人工核对'
      default: return state || '未知状态'
    }
  }

  async function validateKubernetes() {
    if (!selectedTarget) return
    await onKubernetesOperation({ target: selectedTarget, manifest })
  }

  async function createKubernetesPlan() {
    if (!selectedTarget) return
    await onKubernetesPlan({ target: selectedTarget, manifest })
  }

  function updateKubernetesConfirmation(planId: string, value: string) {
    setKubeConfirmations((current) => ({ ...current, [planId]: value }))
  }

  return (
    <div className="page">
      <header className="page-header"><div><span className="eyebrow">受控声明与执行目标</span><h1>配置中心</h1></div></header>
      <nav className="view-tabs" aria-label="配置类型">{tabs.map(({ id, label, icon: Icon }) => <button key={id} type="button" className={tab === id ? 'active' : ''} onClick={() => setTab(id)}><Icon size={16} />{label}</button>)}</nav>

      {tab === 'compose' && <section className="config-panel">
        <div className="config-panel-header"><div><h2>Compose 受控版本</h2><p>编辑只生成校验或提案，运行态变更仍需审批计划。</p></div><div className="config-toolbar"><select aria-label="选择服务" value={selectedService} onChange={(event) => onService(event.target.value)}>{services.map((service) => <option key={service.name} value={service.name}>{service.displayName}</option>)}</select><button className="icon-button bordered" type="button" onClick={onComposeRefresh} title="刷新 Compose" disabled={composeLoading}>{composeLoading ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}</button></div></div>
        {!composeAvailable && !composeLoading && <div className="empty-state compact"><Code2 size={17} />{composeError || 'Compose 配置能力尚未启用'}</div>}
        {composeAvailable && composeError && <div className="inline-error">{composeError}</div>}
        {composeAvailable && compose && <>
          <dl className="config-facts"><div><dt>当前摘要</dt><dd><code>{shortHash(composeDigest)}</code></dd></div><div><dt>来源</dt><dd>{compose.current?.source ?? compose.source ?? '—'}</dd></div><div><dt>校验</dt><dd>{compose.current?.validated ?? compose.validated ? '已通过' : '未通过'}</dd></div><div><dt>运行服务</dt><dd>{compose.applicationService ?? '—'}</dd></div></dl>
          <label className="config-editor"><span>Compose 内容</span><textarea value={composeContent} onChange={(event) => setComposeContent(event.target.value)} spellCheck={false} rows={18} /></label>
          <div className="config-actions"><div className="segmented-control" role="group" aria-label="Compose 操作模式"><button type="button" className={composeMode === 'validate' ? 'active' : ''} onClick={() => setComposeMode('validate')}><Check size={14} />仅校验</button><button type="button" className={composeMode === 'propose' ? 'active' : ''} onClick={() => setComposeMode('propose')}><ShieldCheck size={14} />提交提案</button></div><label className="inline-confirm"><span>确认短语</span><input value={composeConfirmation} onChange={(event) => setComposeConfirmation(event.target.value)} placeholder={composeMode === 'propose' ? '按后端要求填写' : '提案模式可选'} /></label><button className="button secondary" type="button" onClick={() => void saveCompose()} disabled={busy === 'compose' || !composeContent}>{busy === 'compose' ? '提交中' : '保存 Compose'}</button></div>
          <div className="page-section">
            <div className="section-heading"><h2>Compose 提案</h2><span>{compose.revisions?.length ?? 0} 项</span></div>
            {(compose.revisions?.length ?? 0) === 0
              ? <div className="empty-state compact">暂无 Compose 提案</div>
              : <div className="operation-list">{compose.revisions?.map((revision) => {
                const state = revision.state
                const confirmation = revisionConfirmation(revision.id)
                const approvalReady = (state === 'proposed' || state === 'pending_second_approval') && Boolean(onComposeApprove)
                const approvalPhrase = revision.confirmationPhrase ?? ''
                const rollbackPhrase = `回滚 Compose 变更 ${revision.id}`
                return <div className="operation-row" key={revision.id}>
                  <span><strong>{revision.service}</strong><small>{revisionStateLabel(state)} · {revision.source}</small></span>
                  <code>{shortHash(revision.digest)}</code>
                  {approvalReady && <label className="inline-confirm"><span>批准确认</span><code>{approvalPhrase}</code><input value={confirmation} onChange={(event) => setComposeConfirmationFor(revision.id, event.target.value)} placeholder="输入确认短语" /></label>}
                  {approvalReady && <button className="button secondary" type="button" disabled={confirmation !== approvalPhrase || busy === `compose-approve:${revision.id}`} onClick={() => { if (onComposeApprove) void onComposeApprove(revision, confirmation) }}>{busy === `compose-approve:${revision.id}` ? '批准中' : state === 'proposed' ? '第一人批准' : '第二人批准'}</button>}
                  {state === 'approved' && onComposeApply && <button className="button danger" type="button" disabled={busy === `compose-apply:${revision.id}`} onClick={() => void onComposeApply(revision)}>{busy === `compose-apply:${revision.id}` ? '应用中' : '独立应用'}</button>}
                  {state === 'applied' && onComposeRollback && <label className="inline-confirm"><span>回滚确认</span><code>{rollbackPhrase}</code><input value={confirmation} onChange={(event) => setComposeConfirmationFor(revision.id, event.target.value)} placeholder="输入回滚短语" /><button className="button danger" type="button" disabled={confirmation !== rollbackPhrase || busy === `compose-rollback:${revision.id}`} onClick={() => void onComposeRollback(revision, confirmation)}>{busy === `compose-rollback:${revision.id}` ? '回滚中' : '回滚'}</button></label>}
                  {revision.error && <small className="inline-error">{revision.error}</small>}
                </div>
              })}</div>}
          </div>
        </>}
      </section>}

      {tab === 'kubernetes' && <section className="config-panel">
        <div className="config-panel-header">
          <div><h2>Kubernetes 目标</h2><p>校验可直接执行；正式 Apply 固定经过计划、双人批准和独立执行人。</p></div>
          <button className="icon-button bordered" type="button" onClick={onKubernetesRefresh} title="刷新 Kubernetes" disabled={kubernetesLoading}>{kubernetesLoading ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}</button>
        </div>
        {!kubernetesAvailable && !kubernetesLoading && <div className="empty-state compact"><TerminalSquare size={17} />{kubernetesError || 'Kubernetes 能力尚未启用'}</div>}
        {kubernetesAvailable && kubernetesError && <div className="inline-error">{kubernetesError}</div>}
        {kubernetesAvailable && kubernetesLoading && !kubernetes && <div className="empty-state compact"><LoaderCircle className="spin" size={17} />读取 Kubernetes 目标</div>}
        {kubernetesAvailable && kubernetes && <>
          <div className="kube-target-list">
            {targets.length === 0
              ? <div className="empty-state compact">暂无允许的集群目标</div>
              : targets.map(([key, target]) => <button key={key} type="button" className={targetKey === key ? 'active' : ''} onClick={() => setTargetKey(key)}><strong>{target.cluster}</strong><small>{target.context} · {target.namespace}</small></button>)}
          </div>
          <div className="kube-operation-form">
            <label className="config-editor"><span>Manifest</span><textarea value={manifest} onChange={(event) => setManifest(event.target.value)} rows={12} spellCheck={false} placeholder="粘贴受控 manifest" /></label>
            <div className="config-actions">
              <button className="button secondary" type="button" disabled={!selectedTarget || !manifest || busy === 'kubernetes'} onClick={() => void validateKubernetes()}>{busy === 'kubernetes' ? '校验中' : '服务端 Dry-run'}</button>
              <button className="button danger" type="button" disabled={!selectedTarget || !manifest || busy === 'kubernetes-plan'} onClick={() => void createKubernetesPlan()}>{busy === 'kubernetes-plan' ? '创建中' : '创建正式 Apply 计划'}</button>
            </div>
          </div>
          <div className="page-section">
            <div className="section-heading"><h2>正式变更计划</h2><span>{kubernetes.plans?.length ?? 0} 项</span></div>
            {(kubernetes.plans?.length ?? 0) === 0
              ? <div className="empty-state compact">暂无 Kubernetes 正式变更计划</div>
              : <div className="operation-list">{kubernetes.plans?.map((plan) => {
                const confirmation = kubeConfirmations[plan.id] ?? ''
                const approvable = plan.state === 'pending_approval'
                return <div className="operation-row kube-plan-row" key={plan.id}>
                  <span><strong>{plan.target.cluster} · {plan.target.namespace}</strong><small>{plan.state} · {plan.approvedByHash ? '第一批准已完成' : '等待第一批准'} · {plan.secondApprovedByHash ? '第二批准已完成' : '等待第二批准'}</small></span>
                  <code>{shortHash(plan.manifestDigest)}</code>
                  {approvable && <label className="inline-confirm kube-confirm"><span>精确确认</span><code>{plan.confirmationPhrase}</code><input value={confirmation} onChange={(event) => updateKubernetesConfirmation(plan.id, event.target.value)} placeholder="输入计划确认短语" /></label>}
                  {approvable && <button className="button secondary" type="button" disabled={confirmation !== plan.confirmationPhrase || busy === `kubernetes-approve:${plan.id}`} onClick={() => void onKubernetesApprove(plan, confirmation)}>{busy === `kubernetes-approve:${plan.id}` ? '批准中' : plan.approvedByHash ? '第二人批准' : '第一人批准'}</button>}
                  {plan.state === 'approved' && <button className="button danger" type="button" disabled={busy === `kubernetes-execute:${plan.id}`} onClick={() => void onKubernetesExecute(plan)}>{busy === `kubernetes-execute:${plan.id}` ? '执行中' : '由独立执行人执行'}</button>}
                  {plan.error && <small className="inline-error">{plan.error}</small>}
                </div>
              })}</div>}
          </div>
          <div className="page-section">
            <div className="section-heading"><h2>最近操作</h2><span>{kubernetes.operations?.length ?? 0} 项</span></div>
            {(kubernetes.operations?.length ?? 0) === 0 ? <div className="empty-state compact">暂无 Kubernetes 操作记录</div> : <div className="operation-list">{kubernetes.operations?.map((operation) => <div className="operation-row" key={operation.id}><span><strong>{operation.action}</strong><small>{operation.target.cluster} · {operation.target.namespace}</small></span><code>{shortHash(operation.manifestDigest)}</code><span>{operation.dryRun ? 'Dry-run' : '正式'}</span><span>{operation.state}</span></div>)}</div>}
          </div>
        </>}
      </section>}

      {tab === 'extensions' && <section className="config-panel">
        <div className="config-panel-header"><div><h2>扩展策略</h2><p>扩展必须保持签名校验与受限沙箱，策略变更会写入审计。</p></div><button className="icon-button bordered" type="button" onClick={onExtensionsRefresh} title="刷新扩展策略" disabled={extensionsLoading}>{extensionsLoading ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}</button></div>
        {!extensionsAvailable && !extensionsLoading && <div className="empty-state compact"><FlaskConical size={17} />{extensionsError || '扩展能力尚未启用'}</div>}
        {extensionsAvailable && extensionsError && <div className="inline-error">{extensionsError}</div>}
        {extensionsAvailable && extensionsLoading && !extensions && <div className="empty-state compact"><LoaderCircle className="spin" size={17} />读取扩展策略</div>}
        {extensionsAvailable && extensions && <><dl className="config-facts"><div><dt>状态</dt><dd>{extensions.enabled ? '已启用' : '已关闭'}</dd></div><div><dt>签名校验</dt><dd>{extensions.requireSignature ? '强制' : '未强制'}</dd></div><div><dt>沙箱</dt><dd>{extensions.sandbox ?? '未配置'}</dd></div><div><dt>受信发布者</dt><dd>{extensions.trustedPublishers?.length ?? 0} 个</dd></div></dl><div className="page-section"><div className="section-heading"><h2>受信发布者</h2><span>受控配置</span></div>{(extensions.trustedPublishers?.length ?? 0) === 0 ? <div className="empty-state compact">暂无受信发布者</div> : <div className="permission-list">{extensions.trustedPublishers?.map((publisher) => <code key={publisher}>{publisher}</code>)}</div>}</div><div className="page-section"><div className="section-heading"><h2>已登记扩展</h2><span>{extensions.extensions?.length ?? 0} 项</span></div>{(extensions.extensions?.length ?? 0) === 0 ? <div className="empty-state compact">暂无扩展登记</div> : <div className="extension-list">{extensions.extensions?.map((item) => <div className="extension-row" key={`${item.id}:${item.version ?? 'unknown'}`}><span><strong>{item.id}</strong><small>{item.publisher ?? '发布者未知'} · {item.type ?? 'extension'}</small></span><code>{shortHash(item.digest)}</code><span>{item.state ?? item.version ?? '—'}</span></div>)}</div>}</div></>}
      </section>}
    </div>
  )
}
