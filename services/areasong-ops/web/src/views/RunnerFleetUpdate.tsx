import { AlertTriangle, Ban, CheckCircle2, Cpu, LoaderCircle, PackageCheck, Play, RefreshCw, RotateCcw, ShieldCheck, WifiOff } from 'lucide-react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { runAction } from '../action'
import { formatTime, shortHash } from '../labels'
import type {
  FleetBatchPolicy, FleetBatchStrategy, FleetRunnerUpdateItem, FleetRunnerUpdatePlan,
  FleetRunnerUpdatePlanInput, FleetRunnerUpdateStatus,
} from '../types'

interface RunnerFleetUpdateProps {
  status: FleetRunnerUpdateStatus | null
  loading: boolean
  available: boolean
  error: string
  busy: string
  onRefresh: () => void
  onCreate: (input: FleetRunnerUpdatePlanInput) => Promise<void>
  onApprove: (plan: FleetRunnerUpdatePlan, confirmation: string) => Promise<void>
  onExecute: (plan: FleetRunnerUpdatePlan) => Promise<void>
  onCancel: (plan: FleetRunnerUpdatePlan, confirmation: string) => Promise<void>
}

const stateLabels: Record<FleetRunnerUpdatePlan['state'], string> = {
  pending_approval: '等待第一人批准', pending_second_approval: '等待第二人批准', approved: '等待独立执行',
  running: '执行中', observing: 'Canary 观察中', rolling_back: '逐节点回滚中', succeeded: '已成功',
  rolled_back: '已回滚', needs_attention: '需要人工核对', cancelled: '已取消', expired: '已过期',
}

const itemLabels: Record<FleetRunnerUpdateItem['state'], string> = {
  pending: '待处理', ready: '待领取', running: '执行中', succeeded: '成功', failed: '失败',
  rollback_ready: '待回滚', rolling_back: '回滚中', rolled_back: '已回滚', needs_attention: '需核对', skipped: '已跳过',
}

function stateTone(state: string): string {
  if (state === 'succeeded' || state === 'rolled_back') return 'healthy'
  if (state === 'running' || state === 'observing' || state === 'approved' || state === 'pending_approval' || state === 'pending_second_approval') return 'warning'
  if (state === 'cancelled' || state === 'expired' || state === 'skipped') return 'unknown'
  return 'error'
}

function localDateTime(offsetMinutes: number): string {
  const date = new Date(Date.now() + offsetMinutes * 60_000)
  const local = new Date(date.getTime() - date.getTimezoneOffset() * 60_000)
  return local.toISOString().slice(0, 16)
}

function toISOString(value: string): string {
  return new Date(value).toISOString()
}

export function RunnerFleetUpdate({
  status, loading, available, error, busy, onRefresh, onCreate, onApprove, onExecute, onCancel,
}: RunnerFleetUpdateProps) {
  const [targetVersion, setTargetVersion] = useState('')
  const [artifactPath, setArtifactPath] = useState('')
  const [artifactDigest, setArtifactDigest] = useState('')
  const [artifactRevision, setArtifactRevision] = useState('')
  const [artifactSignature, setArtifactSignature] = useState('')
  const [selected, setSelected] = useState<string[]>([])
  const [strategy, setStrategy] = useState<FleetBatchStrategy>('canary')
  const [canarySize, setCanarySize] = useState('1')
  const [batchSize, setBatchSize] = useState('1')
  const [pauseSeconds, setPauseSeconds] = useState('0')
  const [observationSeconds, setObservationSeconds] = useState('60')
  const [maxConcurrent, setMaxConcurrent] = useState('1')
  const [windowStart, setWindowStart] = useState(() => localDateTime(5))
  const [windowEnd, setWindowEnd] = useState(() => localDateTime(125))
  const [confirmation, setConfirmation] = useState('')
  const [approvals, setApprovals] = useState<Record<string, string>>({})
  const [cancellations, setCancellations] = useState<Record<string, string>>({})

  const onlineRunners = useMemo(
    () => (status?.runners ?? []).filter((runner) => runner.state === 'online'),
    [status?.runners],
  )
  const plans = status?.plans ?? []
  const phrase = targetVersion ? `创建 Runner Fleet 更新到 ${targetVersion}，目标 ${selected.length} 个` : ''

  useEffect(() => {
    setSelected((current) => current.filter((id) => onlineRunners.some((runner) => runner.id === id)))
  }, [onlineRunners])

  function toggleRunner(id: string) {
    setSelected((current) => current.includes(id) ? current.filter((item) => item !== id) : [...current, id])
  }

  function batchPolicy(): FleetBatchPolicy {
    if (strategy === 'serial') return { strategy, pauseSeconds: Number(pauseSeconds) || 0 }
    if (strategy === 'fixed') return { strategy, batchSize: Number(batchSize), pauseSeconds: Number(pauseSeconds) || 0 }
    if (strategy === 'percentage') return { strategy, batchPercentage: Number(batchSize), pauseSeconds: Number(pauseSeconds) || 0 }
    return {
      strategy, canarySize: Number(canarySize), batchSize: Number(batchSize), pauseSeconds: Number(pauseSeconds) || 0,
      observationSeconds: Number(observationSeconds),
    }
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!status || selected.length === 0 || !targetVersion || !windowStart || !windowEnd) return
    try {
      await onCreate({
        manifest: {
          purpose: status.manifestPurpose, schema: status.manifestSchema,
          goos: status.manifestGoos, goarch: status.manifestGoarch,
          targetVersion, artifactDigest, artifactRevision, publisher: status.publisher,
        },
        artifactPath, artifactSignature, targetRunnerIds: selected,
        batchPolicy: batchPolicy(), maxConcurrent: Number(maxConcurrent), rollbackOnFailure: true,
        changeWindow: { startAt: toISOString(windowStart), endAt: toISOString(windowEnd), timezone: Intl.DateTimeFormat().resolvedOptions().timeZone },
        confirmation,
      })
      setTargetVersion(''); setArtifactPath(''); setArtifactDigest(''); setArtifactRevision(''); setArtifactSignature(''); setConfirmation('')
    } catch {
      // App owns the visible error; retain fields for correction or replay.
    }
  }

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">显式目标、Canary 与逐节点回滚</span><h1>Runner Fleet 更新</h1></div>
        <button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新 Runner Fleet 更新" disabled={loading}>
          {loading ? <LoaderCircle className="spin" size={17} /> : <RefreshCw size={17} />}
        </button>
      </header>
      {!available && !loading && <div className="empty-state feature-empty"><WifiOff size={19} />{error || 'Runner Fleet 自更新尚未启用'}</div>}
      {available && error && <div className="inline-error" role="alert"><AlertTriangle size={15} />{error}</div>}
      {available && loading && !status && <div className="empty-state"><LoaderCircle className="spin" size={18} />读取 Runner Fleet 策略</div>}
      {available && status && <>
        <dl className="config-facts runner-update-facts">
          <div><dt>发布者</dt><dd>{status.publisher}</dd></div>
          <div><dt>目标平台</dt><dd><code>{status.manifestGoos}/{status.manifestGoarch}</code></dd></div>
          <div><dt>登记 Runner</dt><dd>{status.runners.length} 个</dd></div>
          <div><dt>当前权限</dt><dd>{status.canManage ? '可创建/批准' : '只读'}</dd></div>
        </dl>
        {status.canManage && <section className="page-section">
          <div className="section-heading"><h2>创建分批更新计划</h2><span>双人批准 + 独立执行</span></div>
          <form className="runner-fleet-update-form" onSubmit={(event) => runAction(submit(event))}>
            <label><span>目标版本</span><input required value={targetVersion} onChange={(event) => setTargetVersion(event.target.value)} placeholder="v1.0.4" /></label>
            <label><span>制品相对路径</span><input required value={artifactPath} onChange={(event) => setArtifactPath(event.target.value)} placeholder="runner-v1.0.4" /></label>
            <label><span>制品 SHA-256</span><input required value={artifactDigest} onChange={(event) => setArtifactDigest(event.target.value)} placeholder="sha256:..." spellCheck={false} /></label>
            <label><span>制品 revision</span><input required value={artifactRevision} onChange={(event) => setArtifactRevision(event.target.value)} spellCheck={false} /></label>
            <label className="wide"><span>发布签名（base64）</span><input required value={artifactSignature} onChange={(event) => setArtifactSignature(event.target.value)} spellCheck={false} /></label>
            <fieldset className="runner-fleet-targets wide"><legend>显式 Runner 目标（仅在线节点）</legend>
              {onlineRunners.length === 0 ? <div className="empty-state compact">暂无在线 Runner</div> : <div className="runner-fleet-target-grid">{onlineRunners.map((runner) => <label className="runner-fleet-target" key={runner.id}>
                <input type="checkbox" checked={selected.includes(runner.id)} onChange={() => toggleRunner(runner.id)} />
                <span><strong>{runner.id}</strong><small>{runner.tenantId} · {runner.serverId} · {runner.version}</small><small>租约 {formatTime(runner.leaseExpiresAt)} · mTLS {shortHash(runner.certificateFingerprint)}</small></span>
              </label>)}</div>}
            </fieldset>
            <label><span>批次策略</span><select value={strategy} onChange={(event) => setStrategy(event.target.value as FleetBatchStrategy)}><option value="canary">Canary</option><option value="serial">逐节点</option><option value="fixed">固定批次</option><option value="percentage">百分比批次</option></select></label>
            {strategy === 'canary' && <label><span>Canary 数量</span><input type="number" min="1" value={canarySize} onChange={(event) => setCanarySize(event.target.value)} /></label>}
            {strategy === 'fixed' && <label><span>每批数量</span><input type="number" min="1" value={batchSize} onChange={(event) => setBatchSize(event.target.value)} /></label>}
            {strategy === 'percentage' && <label><span>每批百分比</span><input type="number" min="1" max="100" value={batchSize} onChange={(event) => setBatchSize(event.target.value)} /></label>}
            {strategy === 'canary' && <label><span>Canary 后每批数量</span><input type="number" min="1" value={batchSize} onChange={(event) => setBatchSize(event.target.value)} /></label>}
            <label><span>最大并发</span><input type="number" min="1" max="32" value={maxConcurrent} onChange={(event) => setMaxConcurrent(event.target.value)} /></label>
            <label><span>批次暂停（秒）</span><input type="number" min="0" max="600" value={pauseSeconds} onChange={(event) => setPauseSeconds(event.target.value)} /></label>
            {strategy === 'canary' && <label><span>Canary 观察（秒）</span><input type="number" min="30" max="3600" value={observationSeconds} onChange={(event) => setObservationSeconds(event.target.value)} /></label>}
            <label><span>窗口开始</span><input required type="datetime-local" value={windowStart} onChange={(event) => setWindowStart(event.target.value)} /></label>
            <label><span>窗口结束</span><input required type="datetime-local" value={windowEnd} onChange={(event) => setWindowEnd(event.target.value)} /></label>
            <label className="wide"><span>创建确认短语</span><code>{phrase || '先填写版本和目标'}</code><input required value={confirmation} onChange={(event) => setConfirmation(event.target.value)} /></label>
            <div className="form-actions wide"><span className="runner-fleet-safety-note"><ShieldCheck size={15} />失败自动停止后续批次并逐节点回滚</span><button className="button danger" type="submit" disabled={busy === 'runner-fleet-create' || selected.length === 0 || confirmation !== phrase}><PackageCheck size={14} />{busy === 'runner-fleet-create' ? '创建中' : '创建计划'}</button></div>
          </form>
        </section>}
        <section className="page-section">
          <div className="section-heading"><h2>计划与节点状态</h2><span>{plans.length} 项</span></div>
          {plans.length === 0 ? <div className="empty-state compact">暂无 Runner Fleet 更新计划</div> : <div className="runner-update-list">{plans.map((plan) => <FleetPlanCard key={plan.id} plan={plan} status={status} busy={busy} approvals={approvals} cancellations={cancellations} setApprovals={setApprovals} setCancellations={setCancellations} onApprove={onApprove} onExecute={onExecute} onCancel={onCancel} />)}</div>}
        </section>
      </>}
    </div>
  )
}

interface FleetPlanCardProps {
  plan: FleetRunnerUpdatePlan
  status: FleetRunnerUpdateStatus
  busy: string
  approvals: Record<string, string>
  cancellations: Record<string, string>
  setApprovals: (value: Record<string, string>) => void
  setCancellations: (value: Record<string, string>) => void
  onApprove: (plan: FleetRunnerUpdatePlan, confirmation: string) => Promise<void>
  onExecute: (plan: FleetRunnerUpdatePlan) => Promise<void>
  onCancel: (plan: FleetRunnerUpdatePlan, confirmation: string) => Promise<void>
}

function FleetPlanCard({ plan, status, busy, approvals, cancellations, setApprovals, setCancellations, onApprove, onExecute, onCancel }: FleetPlanCardProps) {
  const approval = approvals[plan.id] ?? ''
  const cancellation = cancellations[plan.id] ?? ''
  const cancelPhrase = `取消 Runner Fleet 更新 ${plan.id}`
  const itemCount = plan.items.length
  return <article className={`runner-update-card runner-update-${stateTone(plan.state)}`}>
    <header><div className="runner-update-title"><span className={`service-indicator ${stateTone(plan.state)}`} /><div><strong>{plan.manifest.targetVersion} · {itemCount} 个 Runner</strong><small>{stateLabels[plan.state]} · 批次 {plan.currentBatch + 1} · {formatTime(plan.createdAt)}</small></div></div><span className={`credential-health ${stateTone(plan.state)}`}>{stateLabels[plan.state]}</span></header>
    <dl className="runner-update-detail"><div><dt>计划摘要</dt><dd><code>{shortHash(plan.planDigest)}</code></dd></div><div><dt>目标</dt><dd>{plan.targetRunnerIds.join(', ')}</dd></div><div><dt>第一批准</dt><dd><code>{shortHash(plan.approvedByHash)}</code></dd></div><div><dt>第二批准</dt><dd><code>{shortHash(plan.secondApprovedByHash)}</code></dd></div></dl>
    <div className="runner-fleet-item-list">{plan.items.map((item) => <div className={`runner-fleet-item runner-fleet-item-${stateTone(item.state)}`} key={item.id}><span className="runner-fleet-item-main"><Cpu size={14} /><strong>{item.runnerId}</strong><small>批次 {item.batchIndex + 1} · {itemLabels[item.state]}</small></span><span><code>{item.observedRevision ? shortHash(item.observedRevision) : shortHash(item.previousRevision)}</code><small>{formatTime(item.lastHeartbeatAt || item.finishedAt)}</small></span>{item.error && <span className="runner-fleet-item-error">{item.error}</span>}</div>)}</div>
    {plan.error && <div className="runner-update-error"><AlertTriangle size={15} />{plan.error}</div>}
    {(plan.state === 'pending_approval' || plan.state === 'pending_second_approval') && <div className="runner-update-actions attention"><label><span>{plan.state === 'pending_approval' ? '第一人批准' : '第二人批准'}确认</span><code>{plan.confirmationPhrase}</code><input value={approval} onChange={(event) => setApprovals({ ...approvals, [plan.id]: event.target.value })} /></label><button className="button secondary" type="button" disabled={approval !== plan.confirmationPhrase || busy === `runner-fleet-approve:${plan.id}`} onClick={() => runAction(onApprove(plan, approval))}><ShieldCheck size={14} />{busy === `runner-fleet-approve:${plan.id}` ? '批准中' : '批准'}</button><label><span>取消确认</span><code>{cancelPhrase}</code><input value={cancellation} onChange={(event) => setCancellations({ ...cancellations, [plan.id]: event.target.value })} /></label><button className="button secondary" type="button" disabled={cancellation !== cancelPhrase || busy === `runner-fleet-cancel:${plan.id}`} onClick={() => runAction(onCancel(plan, cancellation))}><Ban size={14} />取消计划</button></div>}
    {plan.state === 'approved' && <div className="runner-update-actions"><span>两名批准人已独立完成，执行人必须是第三方。</span><button className="button danger" type="button" disabled={busy === `runner-fleet-execute:${plan.id}` || plan.actorHash === status.currentActorHash || plan.approvedByHash === status.currentActorHash || plan.secondApprovedByHash === status.currentActorHash} onClick={() => runAction(onExecute(plan))}><Play size={14} />{busy === `runner-fleet-execute:${plan.id}` ? '执行中' : '独立执行'}</button></div>}
    {plan.state === 'rolling_back' && <div className="runner-update-progress"><RotateCcw size={15} />后续批次已停止，正在逐节点回滚</div>}
    {plan.state === 'succeeded' && <div className="runner-update-progress"><CheckCircle2 size={15} />所有目标身份已复验</div>}
  </article>
}
