import { AlertTriangle, Check, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { formatTime, phaseLabel } from '../labels'
import type { ReleasePlan } from '../types'
import { StatusBadge } from './StatusBadge'

interface ConfirmationDialogProps {
  plan: ReleasePlan
  pending: boolean
  onCancel: () => void
  onConfirm: (value: string) => void
  onClosePlan: () => void
}
export function ConfirmationDialog({ plan, pending, onCancel, onConfirm, onClosePlan }: ConfirmationDialogProps) {
  const [value, setValue] = useState('')
  useEffect(() => setValue(''), [plan.id, plan.state])
  const phrase = plan.confirmationPhrase ?? ''
  const approving = plan.state === 'pending_approval'
  const observing = plan.state === 'observing'
  const canClose = Boolean(plan.observationEndsAt && Date.now() >= new Date(plan.observationEndsAt).getTime())
  const matches = !plan.requiresConfirmation || value === phrase
  const summary = plan.approvalSummary

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={(event) => {
      if (event.currentTarget === event.target && !pending) onCancel()
    }}>
      <section className="modal" role="dialog" aria-modal="true" aria-labelledby="confirm-title">
        <header className="modal-header">
          <div className="modal-title-group">
            <span className="warning-icon"><AlertTriangle size={20} aria-hidden="true" /></span>
            <div>
              <h2 id="confirm-title">{observing ? '收口观察计划' : approving ? '批准发布计划' : '执行已批准计划'}</h2>
              <span>{plan.service} · {plan.action}</span>
            </div>
          </div>
          <StatusBadge kind="risk" value={plan.risk} />
          <button className="icon-button" type="button" onClick={onCancel} disabled={pending} title="关闭">
            <X size={18} aria-hidden="true" />
          </button>
        </header>
        <div className="modal-body">
          <dl className="governance-list">
            <div><dt>计划摘要</dt><dd><code>{plan.digest}</code></dd></div>
            <div><dt>目标版本</dt><dd>{summary.target || '当前版本'}</dd></div>
            <div><dt>当前身份</dt><dd><code>{summary.expectedBefore.runtimeIdentityHash ?? '未提供'}</code></dd></div>
            <div><dt>影响范围</dt><dd>{summary.scope}</dd></div>
            <div><dt>预期影响</dt><dd>{summary.impact}</dd></div>
            <div><dt>失败处理</dt><dd>{summary.rollback}</dd></div>
            {plan.observationStartedAt && <div><dt>观察开始</dt><dd>{formatTime(plan.observationStartedAt)}</dd></div>}
            {plan.observationEndsAt && <div><dt>最早收口</dt><dd>{formatTime(plan.observationEndsAt)}</dd></div>}
            {plan.maintenanceSilenceEndsAt && (
              <div><dt>维护静默</dt><dd>{plan.maintenanceSilenceReleasedAt ? '已解除' : `至 ${formatTime(plan.maintenanceSilenceEndsAt)}`}</dd></div>
            )}
            {plan.blockingAlertFingerprints && plan.blockingAlertFingerprints.length > 0 && (
              <div><dt>阻断告警</dt><dd>{plan.blockingAlertFingerprints.length} 项仍在触发</dd></div>
            )}
            {plan.closureReason && <div><dt>收口阻断</dt><dd>{plan.closureReason}</dd></div>}
          </dl>
          <div className="step-line" aria-label="执行阶段">
            {summary.steps.map((step, index) => (
              <span key={step}>
                <b>{index + 1}</b>{phaseLabel[step] ?? step}
              </span>
            ))}
          </div>
          {approving && plan.requiresConfirmation && (
            <label className="confirmation-field">
              <span>输入确认短语</span>
              <code>{phrase}</code>
              <input
                autoFocus
                type="text"
                value={value}
                onChange={(event) => setValue(event.target.value)}
                autoComplete="off"
                spellCheck={false}
              />
            </label>
          )}
        </div>
        <footer className="modal-footer">
          <button type="button" className="button secondary" onClick={onCancel} disabled={pending}>取消</button>
          <button
            type="button"
            className={observing ? 'button secondary' : 'button danger'}
            disabled={(approving && !matches) || (observing && !canClose) || pending}
            onClick={() => observing ? onClosePlan() : onConfirm(value)}
          >
            <Check size={17} aria-hidden="true" />
            {pending ? '提交中' : observing ? canClose ? '确认收口' : '观察期未结束' : approving ? '批准计划' : '执行计划'}
          </button>
        </footer>
      </section>
    </div>
  )
}
