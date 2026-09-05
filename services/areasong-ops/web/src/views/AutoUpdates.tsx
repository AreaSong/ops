import { CalendarClock, LoaderCircle, PlayCircle, RefreshCw, Save, ShieldCheck } from 'lucide-react'
import { useEffect, useState } from 'react'
import { runAction } from '../action'
import { formatTime, shortHash } from '../labels'
import type { AutoUpdateChannel, AutoUpdateEvaluation, AutoUpdatePolicyInput, AutoUpdatePolicyView } from '../types'

interface AutoUpdatesProps {
  policies: AutoUpdatePolicyView[]
  evaluations: AutoUpdateEvaluation[]
  loading: boolean
  available: boolean
  error: string
  busy: string
  onRefresh: () => void
  onSave: (service: string, input: AutoUpdatePolicyInput) => Promise<void>
  onEvaluate: () => Promise<void>
}

const channels: Array<{ value: AutoUpdateChannel; label: string }> = [
  { value: 'stable', label: '稳定版' },
  { value: 'candidate', label: '候选版' },
  { value: 'security', label: '安全修复' },
]

function draftFromPolicy(policy: AutoUpdatePolicyView): AutoUpdatePolicyInput {
  const channel = channels.some((item) => item.value === policy.channel)
    ? policy.channel as AutoUpdateChannel
    : 'stable'
  return {
    enabled: policy.enabled,
    channel,
    maintenanceWindow: policy.maintenanceWindow ?? '',
    maintenanceTimezone: policy.maintenanceTimezone || 'UTC',
    canaryPercent: policy.canaryPercent ?? 0,
    maxUnavailable: policy.maxUnavailable ?? 0,
    requireBackup: policy.requireBackup,
    requireApproval: policy.requireApproval,
    rollbackOnAlert: policy.rollbackOnAlert,
    observationSeconds: policy.observationSeconds || 300,
  }
}

function evaluationTone(evaluation: AutoUpdateEvaluation): string {
  if (evaluation.updateCreated) return 'healthy'
  if (evaluation.eligible) return 'warning'
  return 'unknown'
}

export function AutoUpdates({
  policies, evaluations, loading, available, error, busy, onRefresh, onSave, onEvaluate,
}: AutoUpdatesProps) {
  const [drafts, setDrafts] = useState<Record<string, AutoUpdatePolicyInput>>({})

  useEffect(() => {
    setDrafts((current) => {
      const next: Record<string, AutoUpdatePolicyInput> = {}
      for (const policy of policies) next[policy.service] = current[policy.service] ?? draftFromPolicy(policy)
      return next
    })
  }, [policies])

  function updateDraft(service: string, patch: Partial<AutoUpdatePolicyInput>) {
    setDrafts((current) => ({
      ...current,
      [service]: { ...(current[service] ?? draftFromPolicy(policies.find((item) => item.service === service) as AutoUpdatePolicyView)), ...patch },
    }))
  }

  async function save(service: string) {
    const input = drafts[service]
    if (!input) return
    try {
      await onSave(service, input)
    } catch {
      // The parent owns the visible API error; retain the draft for correction.
    }
  }

  async function evaluate() {
    try {
      await onEvaluate()
    } catch {
      // The parent owns the visible API error.
    }
  }

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">自动发现只生成计划</span><h1>自动更新</h1></div>
        <div className="page-header-actions">
          <button className="button secondary" type="button" onClick={() => runAction(evaluate())} disabled={busy === 'auto-updates-evaluate'}>
            {busy === 'auto-updates-evaluate' ? <LoaderCircle className="spin" size={15} /> : <PlayCircle size={15} />}评估现在的策略
          </button>
          <button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新自动更新策略" disabled={loading}>
            {loading ? <LoaderCircle className="spin" size={17} /> : <RefreshCw size={17} />}
          </button>
        </div>
      </header>

      {!available && !loading && <div className="empty-state feature-empty"><CalendarClock size={19} />{error || '自动更新能力尚未启用'}</div>}
      {available && error && <div className="inline-error" role="alert">{error}</div>}
      {available && loading && policies.length === 0 && <div className="empty-state"><LoaderCircle className="spin" size={18} />读取自动更新策略</div>}

      {available && policies.length > 0 && <section className="page-section no-top-gap">
        <div className="section-heading"><h2>服务策略</h2><span>{policies.length} 项</span></div>
        <div className="automatic-task-list">
          {policies.map((policy) => {
            const draft = drafts[policy.service] ?? draftFromPolicy(policy)
            const saveBusy = busy === `auto-updates:${policy.service}`
            return <article className="automatic-task-row auto-update-policy-row" key={policy.service}>
              <div className="automatic-task-main">
                <span className="automatic-task-icon"><CalendarClock size={18} aria-hidden="true" /></span>
                <span><strong>{policy.service}</strong><small>{policy.objectId} · {policy.tenantId}</small></span>
              </div>
              <div className="extension-policy-grid">
                <label><span>通道</span><select value={draft.channel} onChange={(event) => updateDraft(policy.service, { channel: event.target.value as AutoUpdateChannel })}>{channels.map((channel) => <option key={channel.value} value={channel.value}>{channel.label}</option>)}</select></label>
                <label><span>维护窗口</span><input value={draft.maintenanceWindow ?? ''} placeholder="02:00-04:00" onChange={(event) => updateDraft(policy.service, { maintenanceWindow: event.target.value })} /></label>
                <label><span>窗口时区</span><input list="auto-update-timezones" value={draft.maintenanceTimezone} placeholder="UTC" onChange={(event) => updateDraft(policy.service, { maintenanceTimezone: event.target.value })} /></label>
                <label><span>观察秒数</span><input type="number" min={60} max={86400} value={draft.observationSeconds} onChange={(event) => updateDraft(policy.service, { observationSeconds: Number(event.target.value) || 0 })} /></label>
                <label><span>Canary %</span><input type="number" min={0} max={100} value={draft.canaryPercent} onChange={(event) => updateDraft(policy.service, { canaryPercent: Number(event.target.value) || 0 })} /></label>
                <label><span>最大不可用 %</span><input type="number" min={0} max={100} value={draft.maxUnavailable} onChange={(event) => updateDraft(policy.service, { maxUnavailable: Number(event.target.value) || 0 })} /></label>
                <label className="toggle-field"><input type="checkbox" checked={draft.enabled} onChange={(event) => updateDraft(policy.service, { enabled: event.target.checked })} /><span>启用自动发现</span></label>
                <label className="toggle-field"><input type="checkbox" checked={draft.requireApproval} onChange={(event) => updateDraft(policy.service, { requireApproval: event.target.checked })} /><span>保留人工批准</span></label>
                <label className="toggle-field"><input type="checkbox" checked={draft.requireBackup} onChange={(event) => updateDraft(policy.service, { requireBackup: event.target.checked })} /><span>要求新鲜备份</span></label>
                <label className="toggle-field"><input type="checkbox" checked={draft.rollbackOnAlert} onChange={(event) => updateDraft(policy.service, { rollbackOnAlert: event.target.checked })} /><span>告警时回滚</span></label>
              </div>
              <div className="automatic-task-facts">
                <div><dt>下次评估</dt><dd>{formatTime(policy.nextEvaluationAt)}</dd></div>
                <div><dt>最近计划</dt><dd><code>{shortHash(policy.lastPlanId)}</code></dd></div>
                <div><dt>最近错误</dt><dd>{policy.lastError || '—'}</dd></div>
              </div>
              <button className="button secondary automatic-task-action" type="button" disabled={saveBusy} onClick={() => runAction(save(policy.service))}>
                {saveBusy ? <LoaderCircle className="spin" size={14} /> : <Save size={14} />}保存策略
              </button>
            </article>
          })}
        </div>
      </section>}

      {available && <section className="page-section">
        <div className="section-heading"><h2>最近评估</h2><span>{evaluations.length} 项</span></div>
        {evaluations.length === 0 ? <div className="empty-state compact">尚未运行本次评估</div> : <div className="operation-list">{evaluations.map((evaluation) => <div className="operation-row" key={`${evaluation.service}:${evaluation.evaluatedAt}`}>
          <span><strong>{evaluation.service}</strong><small>{formatTime(evaluation.evaluatedAt)} · {evaluation.reason || '符合评估条件'}</small></span>
          <code>{shortHash(evaluation.target || evaluation.planId)}</code>
          <span className={`credential-health ${evaluationTone(evaluation)}`}>{evaluation.updateCreated ? '已生成计划' : evaluation.eligible ? '可更新' : '未生成'}</span>
          {evaluation.planId && <span>计划 {shortHash(evaluation.planId)}</span>}
        </div>)}</div>}
      </section>}

      {available && <div className="recovery-warning"><ShieldCheck size={15} />自动更新只创建普通发布计划，不会绕过批准、备份或观察门禁。</div>}
      <datalist id="auto-update-timezones"><option value="UTC" /><option value="Asia/Shanghai" /><option value="America/Los_Angeles" /></datalist>
    </div>
  )
}
