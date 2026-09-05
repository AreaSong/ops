import { AlertTriangle, Ban, CheckCircle2, LoaderCircle, PackageCheck, RefreshCw, RotateCcw, ShieldCheck } from 'lucide-react'
import { useEffect, useMemo, useState, type FormEvent } from 'react'
import { runAction } from '../action'
import { formatTime, shortHash } from '../labels'
import type {
  RunnerUpdate as RunnerUpdateRecord,
  RunnerUpdatePrepareInput,
  RunnerUpdateResolutionEvidence,
  RunnerUpdateStatus,
} from '../types'

type RunnerUpdatePrepareForm = Omit<RunnerUpdatePrepareInput,
  'manifest' | 'manifestPurpose' | 'manifestSchema' | 'manifestGoos' | 'manifestGoarch' | 'runnerId'>

interface RunnerUpdateProps {
  status: RunnerUpdateStatus | null
  loading: boolean
  available: boolean
  error: string
  busy: string
  onRefresh: () => void
  onPrepare: (input: RunnerUpdatePrepareInput) => Promise<void>
  onActivate: (id: string, confirmation: string) => Promise<void>
  onCancel: (id: string, confirmation: string) => Promise<void>
  onResolve: (id: string, confirmation: string, evidence: RunnerUpdateResolutionEvidence) => Promise<void>
}

const stateLabel: Record<RunnerUpdateRecord['state'], string> = {
  prepared: '等待独立批准',
  activating: '执行中',
  succeeded: '已成功',
  rolled_back: '已自动回滚',
  failed: '已失败',
  needs_attention: '需要人工核对',
  cancelled: '已取消',
}

function stateClass(state: RunnerUpdateRecord['state']): string {
  if (state === 'succeeded') return 'healthy'
  if (state === 'prepared' || state === 'activating') return 'warning'
  if (state === 'cancelled') return 'unknown'
  return 'error'
}

export function RunnerUpdate({
  status, loading, available, error, busy, onRefresh, onPrepare, onActivate, onCancel, onResolve,
}: RunnerUpdateProps) {
  const [form, setForm] = useState<RunnerUpdatePrepareForm>({
    targetVersion: '', artifactPath: '', artifactDigest: '', artifactRevision: '',
    publisher: '', artifactSignature: '', confirmation: '',
  })
  const [confirmations, setConfirmations] = useState<Record<string, string>>({})
  const [resolutionEvidence, setResolutionEvidence] = useState<Record<string, RunnerUpdateResolutionEvidence>>({})
  const pending = useMemo(() => status?.pending ?? [], [status?.pending])
  const pendingIDs = useMemo(() => new Set(pending.map((item) => item.id)), [pending])
  const recent = useMemo(
    () => (status?.recent ?? []).filter((item) => !pendingIDs.has(item.id)),
    [pendingIDs, status?.recent],
  )

  useEffect(() => {
    if (status?.publisher && !form.publisher) {
      setForm((current) => ({ ...current, publisher: status.publisher }))
    }
  }, [form.publisher, status?.publisher])

  const preparePhrase = form.targetVersion ? `准备 Runner 更新到 ${form.targetVersion}` : ''

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!status) return
    const manifest = {
      purpose: status.manifestPurpose,
      schema: status.manifestSchema,
      goos: status.manifestGoos,
      goarch: status.manifestGoarch,
      runnerId: status.runnerId,
      targetVersion: form.targetVersion,
      artifactDigest: form.artifactDigest,
      artifactRevision: form.artifactRevision,
      publisher: form.publisher,
    }
    try {
      await onPrepare({
        ...form,
        manifest,
        manifestPurpose: manifest.purpose,
        manifestSchema: manifest.schema,
        manifestGoos: manifest.goos,
        manifestGoarch: manifest.goarch,
        runnerId: manifest.runnerId,
      })
      setForm((current) => ({
        targetVersion: '', artifactPath: '', artifactDigest: '', artifactRevision: '',
        publisher: current.publisher, artifactSignature: '', confirmation: '',
      }))
    } catch {
      // App owns the visible error; retain the form for correction or replay.
    }
  }

  function setConfirmation(id: string, value: string) {
    setConfirmations((current) => ({ ...current, [id]: value }))
  }

  function evidenceFor(id: string): RunnerUpdateResolutionEvidence {
    return resolutionEvidence[id] ?? {
      decision: 'keep', observedVersion: '', observedRevision: '', observedDigest: '', reason: '',
    }
  }

  function setEvidence(id: string, patch: Partial<RunnerUpdateResolutionEvidence>) {
    setResolutionEvidence((current) => ({
      ...current,
      [id]: current[id] ? { ...current[id], ...patch } : {
        decision: 'keep', observedVersion: '', observedRevision: '', observedDigest: '', reason: '',
        ...patch,
      },
    }))
  }

  function completeEvidence(evidence: RunnerUpdateResolutionEvidence): boolean {
    return Boolean(
      evidence.decision && evidence.observedVersion.trim() && evidence.observedRevision.trim()
      && evidence.observedDigest.trim() && evidence.reason.trim(),
    )
  }

  async function run(action: 'activate' | 'cancel' | 'resolve', update: RunnerUpdateRecord) {
    const confirmation = confirmations[`${action}:${update.id}`] ?? ''
    try {
      if (action === 'activate') await onActivate(update.id, confirmation)
      if (action === 'cancel') await onCancel(update.id, confirmation)
      if (action === 'resolve') await onResolve(update.id, confirmation, evidenceFor(update.id))
      setConfirmations((current) => ({ ...current, [`${action}:${update.id}`]: '' }))
      if (action === 'resolve') {
        setResolutionEvidence((current) => {
          const next = { ...current }
          delete next[update.id]
          return next
        })
      }
    } catch {
      // App owns the visible error and refreshes persisted state after failures.
    }
  }

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">签名制品与独立激活</span><h1>Runner 更新</h1></div>
        <button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新 Runner 更新状态" disabled={loading}>
          {loading ? <LoaderCircle className="spin" size={17} /> : <RefreshCw size={17} />}
        </button>
      </header>

      {!available && !loading && <div className="empty-state feature-empty"><PackageCheck size={19} />{error || 'Runner 自更新尚未启用'}</div>}
      {available && error && <div className="inline-error" role="alert">{error}</div>}
      {available && loading && !status && <div className="empty-state"><LoaderCircle className="spin" size={18} />读取 Runner 身份</div>}

      {available && status && <>
        <dl className="config-facts runner-update-facts">
          <div><dt>Runner</dt><dd>{status.runnerId}</dd></div>
          <div><dt>当前版本</dt><dd><code>{status.currentVersion}</code></dd></div>
          <div><dt>Git revision</dt><dd><code>{shortHash(status.revision)}</code></dd></div>
          <div><dt>当前权限</dt><dd>{status.canManage ? '可管理' : '只读'}</dd></div>
        </dl>

        <section className="page-section">
          <div className="section-heading"><h2>待处理更新</h2><span>{pending.length} 项</span></div>
          {pending.length === 0 ? <div className="empty-state compact">暂无待处理更新</div> : <div className="runner-update-list">
            {pending.map((update) => {
              const sameActor = update.preparedByHash === status.currentActorHash
              const activatePhrase = update.confirmationPhrase ?? ''
              const cancelPhrase = `取消 Runner 更新 ${update.id}`
              const resolvePhrase = '确认 Runner 更新已人工核对'
              const activateConfirmation = confirmations[`activate:${update.id}`] ?? ''
              const cancelConfirmation = confirmations[`cancel:${update.id}`] ?? ''
              const resolveConfirmation = confirmations[`resolve:${update.id}`] ?? ''
              const evidence = evidenceFor(update.id)
              return <article className={`runner-update-card runner-update-${update.state}`} key={update.id}>
                <header>
                  <div className="runner-update-title"><span className={`service-indicator ${stateClass(update.state)}`} /><div><strong>{update.previousVersion || '未知'}{' -> '}{update.targetVersion}</strong><small>{update.phase || update.state} · {formatTime(update.createdAt)}</small></div></div>
                  <span className={`credential-health ${stateClass(update.state)}`}>{stateLabel[update.state]}</span>
                </header>
                <dl className="runner-update-detail">
                  <div><dt>目标 revision</dt><dd><code>{shortHash(update.artifactRevision)}</code></dd></div>
                  <div><dt>制品摘要</dt><dd><code>{shortHash(update.artifactDigest)}</code></dd></div>
                  <div><dt>准备人</dt><dd><code>{shortHash(update.preparedByHash)}</code></dd></div>
                  <div><dt>批准人</dt><dd><code>{shortHash(update.approvedByHash)}</code></dd></div>
                </dl>
                {update.error && <div className="runner-update-error"><AlertTriangle size={15} />{update.error}</div>}
                {update.state === 'activating' && <div className="runner-update-progress"><LoaderCircle className="spin" size={16} /><span>{update.phase || '执行中'}</span></div>}
                {update.state === 'prepared' && <div className="runner-update-actions">
                  <label><span>独立激活确认</span><code>{activatePhrase}</code><input value={activateConfirmation} onChange={(event) => setConfirmation(`activate:${update.id}`, event.target.value)} /></label>
                  <button className="button danger" type="button" disabled={!status.canManage || sameActor || busy === `runner-activate:${update.id}` || activateConfirmation !== activatePhrase} onClick={() => runAction(run('activate', update))}><ShieldCheck size={14} />{sameActor ? '需其他操作人激活' : busy === `runner-activate:${update.id}` ? '激活中' : '激活更新'}</button>
                  <label><span>取消确认</span><code>{cancelPhrase}</code><input value={cancelConfirmation} onChange={(event) => setConfirmation(`cancel:${update.id}`, event.target.value)} /></label>
                  <button className="button secondary" type="button" disabled={!status.canManage || busy === `runner-cancel:${update.id}` || cancelConfirmation !== cancelPhrase} onClick={() => runAction(run('cancel', update))}><Ban size={14} />{busy === `runner-cancel:${update.id}` ? '取消中' : '取消准备'}</button>
                </div>}
                {update.state === 'needs_attention' && <div className="runner-update-actions attention">
                  <div className="runner-update-form">
                    <label><span>处置决策</span><select value={evidence.decision} onChange={(event) => setEvidence(update.id, { decision: event.target.value as RunnerUpdateResolutionEvidence['decision'] })}><option value="keep">保留当前版本</option><option value="rollback">确认已回滚</option><option value="abort">终止更新</option></select></label>
                    <label><span>观测版本</span><input value={evidence.observedVersion} onChange={(event) => setEvidence(update.id, { observedVersion: event.target.value })} /></label>
                    <label><span>观测 PID（可选）</span><input type="number" min="1" value={evidence.observedPid ?? ''} onChange={(event) => setEvidence(update.id, { observedPid: event.target.value ? Number(event.target.value) : undefined })} /></label>
                    <label className="wide"><span>观测 revision</span><input value={evidence.observedRevision} onChange={(event) => setEvidence(update.id, { observedRevision: event.target.value })} spellCheck={false} /></label>
                    <label className="wide"><span>观测 SHA-256 摘要</span><input value={evidence.observedDigest} onChange={(event) => setEvidence(update.id, { observedDigest: event.target.value })} spellCheck={false} /></label>
                    <label className="wide"><span>核对原因与处置说明</span><textarea rows={3} value={evidence.reason} onChange={(event) => setEvidence(update.id, { reason: event.target.value })} /></label>
                    <label className="wide"><span>人工核对收口</span><code>{resolvePhrase}</code><input value={resolveConfirmation} onChange={(event) => setConfirmation(`resolve:${update.id}`, event.target.value)} /></label>
                  </div>
                  <button className="button danger" type="button" disabled={!status.canManage || busy === `runner-resolve:${update.id}` || resolveConfirmation !== resolvePhrase || !completeEvidence(evidence)} onClick={() => runAction(run('resolve', update))}><RotateCcw size={14} />{busy === `runner-resolve:${update.id}` ? '收口中' : '标记核对完成'}</button>
                </div>}
              </article>
            })}
          </div>}
        </section>

        <section className="page-section">
          <div className="section-heading"><h2>准备签名制品</h2><span>{status.publisher}</span></div>
          <dl className="config-facts runner-update-facts">
            <div><dt>签名 purpose</dt><dd><code>{status.manifestPurpose}</code></dd></div>
            <div><dt>Manifest schema</dt><dd>{status.manifestSchema}</dd></div>
            <div><dt>目标平台</dt><dd><code>{status.manifestGoos}/{status.manifestGoarch}</code></dd></div>
            <div><dt>Runner ID</dt><dd><code>{status.runnerId}</code></dd></div>
          </dl>
          <form className="runner-update-form" onSubmit={(event) => runAction(submit(event))}>
            <label><span>目标版本</span><input required pattern="[A-Za-z0-9][A-Za-z0-9._+:-]{0,63}" value={form.targetVersion} onChange={(event) => setForm({ ...form, targetVersion: event.target.value })} /></label>
            <label><span>受控目录相对路径</span><input required value={form.artifactPath} onChange={(event) => setForm({ ...form, artifactPath: event.target.value })} /></label>
            <label><span>发布者</span><input required readOnly value={form.publisher} /></label>
            <label><span>完整 Git revision</span><input required pattern="[a-f0-9]{40}" maxLength={40} value={form.artifactRevision} onChange={(event) => setForm({ ...form, artifactRevision: event.target.value })} spellCheck={false} /></label>
            <label className="wide"><span>SHA-256 摘要</span><input required pattern="sha256:[a-f0-9]{64}" maxLength={71} value={form.artifactDigest} onChange={(event) => setForm({ ...form, artifactDigest: event.target.value })} spellCheck={false} /></label>
            <label className="wide"><span>Ed25519 签名</span><textarea required rows={3} value={form.artifactSignature} onChange={(event) => setForm({ ...form, artifactSignature: event.target.value })} spellCheck={false} /></label>
            <label className="wide runner-prepare-confirm"><span>准备确认</span><code>{preparePhrase || '先填写目标版本'}</code><input required value={form.confirmation} onChange={(event) => setForm({ ...form, confirmation: event.target.value })} /></label>
            <div className="form-actions"><button className="button danger" type="submit" disabled={!status.canManage || pending.length > 0 || busy === 'runner-prepare' || form.confirmation !== preparePhrase}><PackageCheck size={14} />{busy === 'runner-prepare' ? '准备中' : '校验并准备'}</button></div>
          </form>
        </section>

        <section className="page-section">
          <div className="section-heading"><h2>最近更新</h2><span>{recent.length} 项</span></div>
          {recent.length === 0 ? <div className="empty-state compact">暂无历史更新</div> : <div className="runner-update-history">{recent.map((update) => <div className="runner-update-history-row" key={update.id}>
            {update.state === 'succeeded' ? <CheckCircle2 size={16} /> : <AlertTriangle size={16} />}
            <span><strong>{update.targetVersion}</strong><small>{formatTime(update.finishedAt ?? update.createdAt)} · {update.phase || update.state}</small></span>
            <code>{shortHash(update.artifactRevision)}</code><span className={`credential-health ${stateClass(update.state)}`}>{stateLabel[update.state]}</span>
          </div>)}</div>}
        </section>
      </>}
    </div>
  )
}
