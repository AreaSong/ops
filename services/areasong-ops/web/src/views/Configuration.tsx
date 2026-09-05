import { Check, Code2, FlaskConical, LoaderCircle, RefreshCw, ShieldCheck, TerminalSquare, Upload } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { runAction } from '../action'
import { shortHash } from '../labels'
import type { ComposeRevision, ComposeServiceView, ExtensionManifest, ExtensionPlan, ExtensionPolicyView, KubernetesConfigView, KubernetesOperation, KubernetesPlan, ServiceView } from '../types'

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
  onSaveCompose: (body: { content: string; expectedDigest: string; mode: 'validate' | 'propose'; confirmation?: string; recoveryPointId?: string }) => Promise<void>
  onComposeApprove?: (revision: ComposeRevision, confirmation: string) => Promise<void>
  onComposeApply?: (revision: ComposeRevision) => Promise<void>
  onComposeRollback?: (revision: ComposeRevision, confirmation: string) => Promise<void>
  onKubernetesRefresh: () => void
  onKubernetesOperation: (body: { target: KubernetesOperation['target']; manifest?: string }) => Promise<void>
  onKubernetesPlan: (body: { target: KubernetesOperation['target']; manifest: string }) => Promise<void>
  onKubernetesApprove: (plan: KubernetesPlan, confirmation: string) => Promise<void>
  onKubernetesExecute: (plan: KubernetesPlan) => Promise<void>
	onKubernetesRollbackPlan: (plan: KubernetesPlan, manifest: string) => Promise<void>
  onExtensionsRefresh: () => void
  onExtensionUpload: (manifest: ExtensionManifest, content: string) => Promise<void>
  onExtensionPlan: (body: { extensionId: string; extensionVersion: string; objectId: string; input: Record<string, unknown>; timeoutSeconds?: number }) => Promise<void>
  onExtensionApprove: (plan: ExtensionPlan, confirmation: string) => Promise<void>
  onExtensionExecute: (plan: ExtensionPlan) => Promise<void>
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

function isExtensionManifest(value: unknown): value is ExtensionManifest {
  if (!value || typeof value !== 'object') return false
  const manifest = value as Record<string, unknown>
  return manifest.purpose === 'areasong-ops.extension' && manifest.schemaVersion === 1 &&
    typeof manifest.id === 'string' && typeof manifest.version === 'string' &&
    typeof manifest.type === 'string' && typeof manifest.entrypoint === 'string' &&
    typeof manifest.digest === 'string' && typeof manifest.signature === 'string' &&
    typeof manifest.publisher === 'string'
}

function fileAsBase64(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(new Error('扩展包读取失败'))
    reader.onload = () => {
      const result = reader.result
      if (typeof result !== 'string' || !result.includes(',')) {
        reject(new Error('扩展包编码失败'))
        return
      }
      resolve(result.slice(result.indexOf(',') + 1))
    }
    reader.readAsDataURL(file)
  })
}

export function Configuration({
  services, selectedService, compose, composeLoading, composeAvailable, composeError,
  kubernetes, kubernetesLoading, kubernetesAvailable, kubernetesError,
  extensions, extensionsLoading, extensionsAvailable, extensionsError, busy,
  onService, onComposeRefresh, onSaveCompose, onComposeApprove, onComposeApply, onComposeRollback,
  onKubernetesRefresh, onKubernetesOperation,
  onKubernetesPlan, onKubernetesApprove, onKubernetesExecute, onKubernetesRollbackPlan,
  onExtensionsRefresh, onExtensionUpload, onExtensionPlan, onExtensionApprove, onExtensionExecute,
}: ConfigurationProps) {
  const [tab, setTab] = useState<ConfigTab>('compose')
  const [composeContent, setComposeContent] = useState('')
  const [composeMode, setComposeMode] = useState<'validate' | 'propose'>('validate')
  const [composeConfirmation, setComposeConfirmation] = useState('')
	const [composeRecoveryPointId, setComposeRecoveryPointId] = useState('')
  const [composeConfirmations, setComposeConfirmations] = useState<Record<string, string>>({})
  const [manifest, setManifest] = useState('')
  const [kubeConfirmations, setKubeConfirmations] = useState<Record<string, string>>({})
  const [targetKey, setTargetKey] = useState('')
  const [extensionKey, setExtensionKey] = useState('')
  const [extensionTarget, setExtensionTarget] = useState('')
  const [extensionInput, setExtensionInput] = useState('{"operation":"inspect"}')
  const [extensionInputError, setExtensionInputError] = useState('')
  const [extensionManifest, setExtensionManifest] = useState<ExtensionManifest | null>(null)
  const [extensionManifestName, setExtensionManifestName] = useState('')
  const [extensionPackage, setExtensionPackage] = useState<File | null>(null)
  const [extensionUploadError, setExtensionUploadError] = useState('')
  const [extensionConfirmations, setExtensionConfirmations] = useState<Record<string, string>>({})

  useEffect(() => {
    setComposeContent(compose?.current?.content ?? compose?.content ?? '')
    setComposeConfirmation('')
		const latest = compose?.recoveryPoints?.find((point) => point.status === 'verified')
		setComposeRecoveryPointId(latest?.id ?? '')
    setComposeConfirmations({})
  }, [compose])
  useEffect(() => {
    if (!targetKey && targetEntries(kubernetes).length > 0) setTargetKey(targetEntries(kubernetes)[0][0])
  }, [kubernetes, targetKey])
  useEffect(() => {
    if (!extensionTarget && services.length > 0) setExtensionTarget(services[0].objectId)
    if (!extensionKey && (extensions?.extensions?.length ?? 0) > 0) {
      const item = extensions?.extensions?.find((candidate) => candidate.state === 'stored') ?? extensions?.extensions?.[0]
      if (item?.id && item.version) setExtensionKey(`${item.id}:${item.version}`)
    }
  }, [extensions, extensionKey, extensionTarget, services])

  const targets = useMemo(() => targetEntries(kubernetes), [kubernetes])
  const composeServices = useMemo(() => services.filter((service) => service.managedCompose), [services])
  const selectedTarget = targets.find(([key]) => key === targetKey)?.[1] ?? targets[0]?.[1]
  const composeDigest = compose?.current?.digest ?? compose?.digest ?? ''

  async function saveCompose() {
    await onSaveCompose({ content: composeContent, expectedDigest: composeDigest, mode: composeMode,
		confirmation: composeConfirmation || undefined,
		recoveryPointId: composeMode === 'propose' ? composeRecoveryPointId || undefined : undefined })
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
		case 'expired': return '提案已过期'
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

  function extensionConfirmation(planId: string): string {
    return extensionConfirmations[planId] ?? ''
  }

  function updateExtensionConfirmation(planId: string, value: string) {
    setExtensionConfirmations((current) => ({ ...current, [planId]: value }))
  }

  async function createExtensionExecutionPlan() {
    const [extensionId, extensionVersion] = extensionKey.split(':')
    if (!extensionId || !extensionVersion || !extensionTarget) return
    let input: Record<string, unknown>
    try {
      const parsed: unknown = JSON.parse(extensionInput)
      if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) throw new Error('输入必须是 JSON 对象')
      input = parsed as Record<string, unknown>
    } catch (reason) {
      setExtensionInputError(reason instanceof Error ? reason.message : '扩展输入 JSON 无效')
      return
    }
    setExtensionInputError('')
    await onExtensionPlan({ extensionId, extensionVersion, objectId: extensionTarget, input })
  }

  async function selectExtensionManifest(file?: File) {
    setExtensionManifest(null)
    setExtensionManifestName(file?.name ?? '')
    setExtensionUploadError('')
    if (!file) return
    try {
      const parsed: unknown = JSON.parse(await file.text())
      if (!isExtensionManifest(parsed)) throw new Error('扩展 manifest 字段或用途无效')
      setExtensionManifest(parsed)
    } catch (reason) {
      setExtensionUploadError(reason instanceof Error ? reason.message : '扩展 manifest 读取失败')
    }
  }

  async function uploadExtensionPackage() {
    if (!extensionManifest || !extensionPackage) return
    const maxBytes = extensions?.maxPackageBytes ?? 0
    if (maxBytes > 0 && extensionPackage.size > maxBytes) {
      setExtensionUploadError(`扩展包超过 ${maxBytes} 字节限制`)
      return
    }
    setExtensionUploadError('')
    try {
      await onExtensionUpload(extensionManifest, await fileAsBase64(extensionPackage))
      setExtensionPackage(null)
      setExtensionManifest(null)
      setExtensionManifestName('')
    } catch (reason) {
      setExtensionUploadError(reason instanceof Error ? reason.message : '扩展包上传失败')
    }
  }

  return (
    <div className="page">
      <header className="page-header"><div><span className="eyebrow">受控声明与执行目标</span><h1>配置中心</h1></div></header>
      <nav className="view-tabs" aria-label="配置类型">{tabs.map(({ id, label, icon: Icon }) => <button key={id} type="button" className={tab === id ? 'active' : ''} onClick={() => setTab(id)}><Icon size={16} />{label}</button>)}</nav>

      {tab === 'compose' && <section className="config-panel">
        <div className="config-panel-header"><div><h2>Compose 受控版本</h2><p>编辑只生成校验或提案，运行态变更仍需审批计划。</p></div><div className="config-toolbar"><select aria-label="选择服务" value={selectedService} onChange={(event) => onService(event.target.value)}>{composeServices.map((service) => <option key={service.name} value={service.name}>{service.displayName}</option>)}</select><button className="icon-button bordered" type="button" onClick={onComposeRefresh} title="刷新 Compose" disabled={composeLoading || composeServices.length === 0}>{composeLoading ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}</button></div></div>
        {!composeAvailable && !composeLoading && <div className="empty-state compact"><Code2 size={17} />{composeError || 'Compose 配置能力尚未启用'}</div>}
        {composeAvailable && composeError && <div className="inline-error">{composeError}</div>}
        {composeAvailable && compose && <>
          <dl className="config-facts"><div><dt>当前摘要</dt><dd><code>{shortHash(composeDigest)}</code></dd></div><div><dt>租户 / 服务器</dt><dd>{compose.tenantId ?? '—'} / {compose.serverId ?? '—'}</dd></div><div><dt>固定项目</dt><dd>{compose.projectName ?? '—'}</dd></div><div><dt>校验</dt><dd>{compose.current?.validated ?? compose.validated ? '已通过' : '未通过'}</dd></div><div><dt>运行服务</dt><dd>{compose.applicationService ?? '—'}</dd></div><div><dt>提案有效期</dt><dd>{compose.proposalTtlSeconds ?? 900} 秒</dd></div></dl>
          {compose.validationError && <div className="inline-error" role="alert">Compose 校验失败：{compose.validationError}</div>}
          <label className="config-editor"><span>Compose 内容</span><textarea value={composeContent} onChange={(event) => setComposeContent(event.target.value)} spellCheck={false} rows={18} /></label>
          <div className="config-actions"><div className="segmented-control" role="group" aria-label="Compose 操作模式"><button type="button" className={composeMode === 'validate' ? 'active' : ''} onClick={() => setComposeMode('validate')}><Check size={14} />仅校验</button><button type="button" className={composeMode === 'propose' ? 'active' : ''} onClick={() => setComposeMode('propose')}><ShieldCheck size={14} />提交提案</button></div>{composeMode === 'propose' && <label className="inline-confirm"><span>新鲜恢复点</span><select value={composeRecoveryPointId} onChange={(event) => setComposeRecoveryPointId(event.target.value)}><option value="">选择已验证恢复点</option>{compose.recoveryPoints?.filter((point) => point.status === 'verified').map((point) => <option key={point.id} value={point.id}>{point.id.slice(0, 8)} · {point.recoverableUntil ? new Date(point.recoverableUntil).toLocaleString() : '无有效期'}</option>)}</select></label>}<button className="button secondary" type="button" onClick={() => runAction(saveCompose())} disabled={busy === 'compose' || !composeContent || (composeMode === 'propose' && !composeRecoveryPointId)}>{busy === 'compose' ? '提交中' : composeMode === 'propose' ? '创建受控提案' : '校验 Compose'}</button></div>
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
									<small>{revision.tenantId ?? '—'} / {revision.serverId ?? '—'} · 到期 {revision.expiresAt ? new Date(revision.expiresAt).toLocaleString() : '—'}</small>
										<small>策略 <code>{revision.policyDigest ?? '—'}</code> · 恢复点 <code>{revision.recoveryPointId ?? '—'}</code></small>
										<small>告警证据 <code>{revision.alertEvidenceDigest ?? '—'}</code> · 运行身份 <code>{revision.expectedRuntimeIdentityDigest ?? '—'}</code></small>
										<small>目标镜像 <code>{revision.candidateImage ?? '—'}</code></small>
										<small>目标 digest <code>{revision.candidateImageDigest ?? '—'}</code> · Image ID <code>{revision.candidateImageId ?? '—'}</code></small>
										<small>有效基线 <code>{revision.baselineEffectiveDigest ?? '—'}</code> · 有效候选 <code>{revision.candidateEffectiveDigest ?? '—'}</code></small>
										<small>env 文件摘要 <code>{revision.envFileDigest ?? '—'}</code></small>
										{(revision.semanticDiff?.length ?? 0) > 0 && <div className="permission-list">{revision.semanticDiff?.map((entry) => <code key={entry.path}>{entry.path}: {entry.before ?? '∅'} → {entry.after ?? '∅'}</code>)}</div>}
										{revision.content && <details><summary>查看完整候选 Compose</summary><pre className="extension-output">{revision.content}</pre></details>}
                  {approvalReady && <label className="inline-confirm"><span>批准确认</span><code>{approvalPhrase}</code><input value={confirmation} onChange={(event) => setComposeConfirmationFor(revision.id, event.target.value)} placeholder="输入确认短语" /></label>}
                  {approvalReady && <button className="button secondary" type="button" disabled={confirmation !== approvalPhrase || busy === `compose-approve:${revision.id}`} onClick={() => { if (onComposeApprove) runAction(onComposeApprove(revision, confirmation)) }}>{busy === `compose-approve:${revision.id}` ? '批准中' : state === 'proposed' ? '第一人批准' : '第二人批准'}</button>}
                  {state === 'approved' && onComposeApply && <button className="button danger" type="button" disabled={busy === `compose-apply:${revision.id}`} onClick={() => runAction(onComposeApply(revision))}>{busy === `compose-apply:${revision.id}` ? '应用中' : '独立应用'}</button>}
                  {state === 'applied' && onComposeRollback && <label className="inline-confirm"><span>回滚确认</span><code>{rollbackPhrase}</code><input value={confirmation} onChange={(event) => setComposeConfirmationFor(revision.id, event.target.value)} placeholder="输入回滚短语" /><button className="button danger" type="button" disabled={confirmation !== rollbackPhrase || busy === `compose-rollback:${revision.id}`} onClick={() => runAction(onComposeRollback(revision, confirmation))}>{busy === `compose-rollback:${revision.id}` ? '回滚中' : '回滚'}</button></label>}
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
              <button className="button secondary" type="button" disabled={!selectedTarget || !manifest || busy === 'kubernetes'} onClick={() => runAction(validateKubernetes())}>{busy === 'kubernetes' ? '校验中' : '服务端 Dry-run'}</button>
              <button className="button danger" type="button" disabled={!selectedTarget || !manifest || busy === 'kubernetes-plan'} onClick={() => runAction(createKubernetesPlan())}>{busy === 'kubernetes-plan' ? '创建中' : '创建正式 Apply 计划'}</button>
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
                  <span><strong>{plan.target.cluster} · {plan.target.namespace}</strong><small>{plan.action === 'rollback' ? '回滚' : 'Apply'} · {plan.state} · {plan.approvedByHash ? '第一批准已完成' : '等待第一批准'} · {plan.secondApprovedByHash ? '第二批准已完成' : '等待第二批准'}</small></span>
                  <code>{shortHash(plan.manifestDigest)}</code>
                  {approvable && <label className="inline-confirm kube-confirm"><span>精确确认</span><code>{plan.confirmationPhrase}</code><input value={confirmation} onChange={(event) => updateKubernetesConfirmation(plan.id, event.target.value)} placeholder="输入计划确认短语" /></label>}
                  {approvable && <button className="button secondary" type="button" disabled={confirmation !== plan.confirmationPhrase || busy === `kubernetes-approve:${plan.id}`} onClick={() => runAction(onKubernetesApprove(plan, confirmation))}>{busy === `kubernetes-approve:${plan.id}` ? '批准中' : plan.approvedByHash ? '第二人批准' : '第一人批准'}</button>}
                  {plan.state === 'approved' && <button className="button danger" type="button" disabled={busy === `kubernetes-execute:${plan.id}`} onClick={() => runAction(onKubernetesExecute(plan))}>{busy === `kubernetes-execute:${plan.id}` ? '执行中' : '由独立执行人执行'}</button>}
				  {plan.state === 'succeeded' && plan.action === 'apply' && <button className="button secondary" type="button" disabled={!manifest || busy === `kubernetes-rollback:${plan.id}`} onClick={() => runAction(onKubernetesRollbackPlan(plan, manifest))}>{busy === `kubernetes-rollback:${plan.id}` ? '创建中' : '以编辑器清单创建回滚计划'}</button>}
                  {plan.error && <small className="inline-error">{plan.error}</small>}
                </div>
              })}</div>}
          </div>
          <div className="page-section">
            <div className="section-heading"><h2>最近操作</h2><span>{kubernetes.operations?.length ?? 0} 项</span></div>
            {(kubernetes.operations?.length ?? 0) === 0 ? <div className="empty-state compact">暂无 Kubernetes 操作记录</div> : <div className="operation-list">{kubernetes.operations?.map((operation) => <div className="operation-row" key={operation.id}><span><strong>{operation.action}</strong><small>{operation.target.cluster} · {operation.target.namespace}</small></span><code>{shortHash(operation.manifestDigest)}</code><span>{operation.dryRun ? 'Dry-run' : '正式'}</span><span>{operation.state}</span></div>)}</div>}
          </div>
		  <div className="page-section">
			<div className="section-heading"><h2>Rollout 状态</h2><span>{kubernetes.operations?.length ?? 0} 项</span></div>
			{(kubernetes.operations?.length ?? 0) === 0 ? <div className="empty-state compact">暂无 Kubernetes 操作</div> : <div className="operation-list">{kubernetes.operations?.map((operation) => <div className="operation-row" key={operation.id}><span><strong>{operation.action} · {operation.target.namespace}</strong><small>{operation.phase || operation.state} · rollout {operation.rolloutState || '未记录'}</small></span><code>{shortHash(operation.manifestDigest || '')}</code>{(operation.rolloutResources?.length ?? 0) > 0 && <small>{operation.rolloutResources?.join(', ')}</small>}{operation.error && <small className="inline-error">{operation.error}</small>}</div>)}</div>}
		  </div>
        </>}
      </section>}

      {tab === 'extensions' && <section className="config-panel">
        <div className="config-panel-header"><div><h2>扩展策略</h2><p>扩展必须保持签名校验与受限沙箱，策略变更会写入审计。</p></div><button className="icon-button bordered" type="button" onClick={onExtensionsRefresh} title="刷新扩展策略" disabled={extensionsLoading}>{extensionsLoading ? <LoaderCircle className="spin" size={16} /> : <RefreshCw size={16} />}</button></div>
        {!extensionsAvailable && !extensionsLoading && <div className="empty-state compact"><FlaskConical size={17} />{extensionsError || '扩展能力尚未启用'}</div>}
        {extensionsAvailable && extensionsError && <div className="inline-error">{extensionsError}</div>}
        {extensionsAvailable && extensionsLoading && !extensions && <div className="empty-state compact"><LoaderCircle className="spin" size={17} />读取扩展策略</div>}
        {extensionsAvailable && extensions && <><dl className="config-facts"><div><dt>状态</dt><dd>{extensions.enabled ? '已启用' : '已关闭'}</dd></div><div><dt>签名校验</dt><dd>{extensions.requireSignature ? '强制' : '未强制'}</dd></div><div><dt>沙箱</dt><dd>{extensions.sandbox ?? '未配置'}</dd></div><div><dt>受信发布者</dt><dd>{extensions.trustedPublishers?.length ?? 0} 个</dd></div><div><dt>执行限制</dt><dd>{extensions.maxExecutionSeconds ?? '—'} 秒 / {extensions.maxMemoryPages ?? '—'} pages</dd></div></dl><div className="page-section"><div className="section-heading"><h2>受信发布者</h2><span>受控配置</span></div>{(extensions.trustedPublishers?.length ?? 0) === 0 ? <div className="empty-state compact">暂无受信发布者</div> : <div className="permission-list">{extensions.trustedPublishers?.map((publisher) => <code key={publisher}>{publisher}</code>)}</div>}</div><div className="page-section"><div className="section-heading"><h2>上传签名扩展</h2><span>{extensions.maxPackageBytes ?? 0} B 上限</span></div><div className="extension-policy-grid"><label><span>Manifest JSON</span><input aria-label="扩展 manifest" type="file" accept="application/json,.json" onChange={(event) => runAction(selectExtensionManifest(event.target.files?.[0]))} /></label><label><span>扩展包</span><input aria-label="扩展包" type="file" accept="application/wasm,application/octet-stream,.wasm" onChange={(event) => { setExtensionPackage(event.target.files?.[0] ?? null); setExtensionUploadError('') }} /></label><div className="extension-upload-summary"><strong>{extensionManifest ? `${extensionManifest.id} · ${extensionManifest.version}` : extensionManifestName || '未选择 manifest'}</strong><small>{extensionPackage ? `${extensionPackage.name} · ${extensionPackage.size} B` : '未选择扩展包'}</small></div><button className="button secondary" type="button" disabled={!extensions.enabled || !extensionManifest || !extensionPackage || busy === 'extension-upload'} onClick={() => runAction(uploadExtensionPackage())}><Upload size={14} />{busy === 'extension-upload' ? '上传中' : '校验并上传'}</button></div>{extensionUploadError && <div className="inline-error" role="alert">{extensionUploadError}</div>}</div><div className="page-section"><div className="section-heading"><h2>已登记扩展</h2><span>{extensions.extensions?.length ?? 0} 项</span></div>{(extensions.extensions?.length ?? 0) === 0 ? <div className="empty-state compact">暂无扩展登记</div> : <div className="extension-list">{extensions.extensions?.map((item) => <div className="extension-row" key={`${item.id}:${item.version ?? 'unknown'}`}><span><strong>{item.id}</strong><small>{item.publisher ?? '发布者未知'} · {item.type ?? 'extension'}</small></span><code>{shortHash(item.digest)}</code><span>{item.state ?? item.version ?? '—'}</span></div>)}</div>}</div><div className="page-section"><div className="section-heading"><h2>扩展执行计划</h2><span>{extensions.plans?.length ?? 0} 项</span></div><div className="extension-policy-grid"><label><span>扩展版本</span><select value={extensionKey} onChange={(event) => setExtensionKey(event.target.value)}><option value="">选择已登记扩展</option>{extensions.extensions?.filter((item) => item.state === 'stored' && item.version).map((item) => <option key={`${item.id}:${item.version}`} value={`${item.id}:${item.version}`}>{item.id} · {item.version}</option>)}</select></label><label><span>目标对象</span><select value={extensionTarget} onChange={(event) => setExtensionTarget(event.target.value)}>{services.map((service) => <option key={service.objectId} value={service.objectId}>{service.displayName}</option>)}</select></label><label><span>输入 JSON</span><textarea value={extensionInput} onChange={(event) => setExtensionInput(event.target.value)} rows={3} spellCheck={false} /></label><button className="button secondary" type="button" disabled={!extensionKey || !extensionTarget || busy === 'extension-plan'} onClick={() => { runAction(createExtensionExecutionPlan()) }}>{busy === 'extension-plan' ? '创建中' : '创建执行计划'}</button></div>{extensionInputError && <div className="inline-error" role="alert">{extensionInputError}</div>}{(extensions.plans?.length ?? 0) === 0 ? <div className="empty-state compact">暂无扩展执行计划</div> : <div className="operation-list">{extensions.plans?.map((plan) => { const confirmation = extensionConfirmation(plan.id); const approvable = plan.state === 'pending_approval' || plan.state === 'pending_second_approval'; return <div className="operation-row" key={plan.id}><span><strong>{plan.extensionId} · {plan.extensionVersion}</strong><small>{plan.objectId} · {plan.state} · {plan.sandbox} · {plan.maxMemoryPages} pages / {plan.maxOutputBytes} B 输出</small></span><code>{shortHash(plan.planDigest)}</code>{approvable && <label className="inline-confirm"><span>批准确认</span><code>{plan.confirmationPhrase}</code><input value={confirmation} onChange={(event) => updateExtensionConfirmation(plan.id, event.target.value)} placeholder="输入确认短语" /></label>}{approvable && <button className="button secondary" type="button" disabled={confirmation !== plan.confirmationPhrase || busy === `extension-approve:${plan.id}`} onClick={() => runAction(onExtensionApprove(plan, confirmation))}>{busy === `extension-approve:${plan.id}` ? '批准中' : plan.state === 'pending_approval' ? '第一人批准' : '第二人批准'}</button>}{plan.state === 'approved' && <button className="button danger" type="button" disabled={busy === `extension-execute:${plan.id}`} onClick={() => runAction(onExtensionExecute(plan))}>{busy === `extension-execute:${plan.id}` ? '执行中' : '独立执行'}</button>}{plan.output && <pre className="extension-output">{plan.output}</pre>}{plan.error && <small className="inline-error">{plan.error}</small>}</div>})}</div>}</div></>}
      </section>}
    </div>
  )
}
