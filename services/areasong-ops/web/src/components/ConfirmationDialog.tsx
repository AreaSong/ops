import { AlertTriangle, Check, X } from 'lucide-react'
import { useEffect, useState } from 'react'
import { formatTime, phaseLabel } from '../labels'
import { canCurrentActorApproveReleasePlan, canCurrentActorExecuteReleasePlan } from '../approval'
import type { ReleasePlan } from '../types'
import { formatSchedule, isScheduleDue } from '../schedule'
import { usePlanDialog } from '../usePlanDialog'
import { StatusBadge } from './StatusBadge'

interface ConfirmationDialogProps {
  error?: string
  plan: ReleasePlan
  pending: boolean
  onCancel: () => void
  onConfirm: (value: string) => void
  onClosePlan: () => void
  currentActorHash: string
}
export function ConfirmationDialog({ plan, pending, onCancel, onConfirm, onClosePlan, currentActorHash, error }: ConfirmationDialogProps) {
  const dialogRef = usePlanDialog<HTMLElement>(pending, onCancel)
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])
  const [value, setValue] = useState('')
  useEffect(() => setValue(''), [plan.id, plan.state])
  const phrase = plan.confirmationPhrase ?? ''
  const approving = plan.state === 'pending_approval'
  const scheduled = plan.state === 'scheduled'
  const waiting = scheduled && !isScheduleDue(plan.scheduleAt, now)
  const observing = plan.state === 'observing'
  const summary = plan.approvalSummary
  const prepared = summary.schemaVersion === 2 && Boolean(summary.lifecycle)
  // 非高风险首批仍归创建人；保留已有双审批下一步的独立身份例外。
  const approvalIdentityAllowed = plan.risk === 'high' ||
    ((currentActorHash === plan.actorHash || Boolean(plan.requiresDualApproval && plan.approvedByHash)) &&
      currentActorHash !== plan.approvedByHash && currentActorHash !== plan.secondApprovedByHash)
  const canApprove = approvalIdentityAllowed && canCurrentActorApproveReleasePlan({
    actorHash: plan.actorHash,
    approvedByHash: plan.approvedByHash,
    secondApprovedByHash: plan.secondApprovedByHash,
    approvalPolicy: plan.approvalPolicy ?? summary.approvalPolicy,
    approvalException: summary.approvalException,
    risk: plan.risk,
    service: plan.service,
    action: plan.action,
  }, currentActorHash)
  const canExecute = canCurrentActorExecuteReleasePlan({
    actorHash: plan.actorHash,
    approvedByHash: plan.approvedByHash,
    secondApprovedByHash: plan.secondApprovedByHash,
    approvalPolicy: plan.approvalPolicy ?? summary.approvalPolicy,
    approvalException: summary.approvalException,
    risk: plan.risk,
    service: plan.service,
    action: plan.action,
  }, currentActorHash)
  const canClose = Boolean(plan.observationEndsAt && now >= new Date(plan.observationEndsAt).getTime())
  const matches = !plan.requiresConfirmation || value === phrase
  // 格式只支持审阅；新执行与收口还需要服务端当前能力投影。
  const enabled = !pending && prepared && (approving ? matches && canApprove : observing
    ? plan.closureAvailable === true && canClose && currentActorHash === plan.actorHash
    : (plan.state === 'approved' || scheduled) && !waiting && canExecute && plan.executionAvailable === true)

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={(event) => {
      if (event.currentTarget === event.target && !pending) onCancel()
    }}>
      <section ref={dialogRef} tabIndex={-1} className="modal plan-dialog" role="dialog" aria-modal="true" aria-labelledby="confirm-title">
        <header className="modal-header">
          <div className="modal-title-group">
            <span className="warning-icon"><AlertTriangle size={20} aria-hidden="true" /></span>
            <div>
              <h2 id="confirm-title">{observing ? '收口观察计划' : approving ? '批准发布计划' : waiting ? '等待执行时间' : '执行已批准计划'}</h2>
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
            <div><dt>租户 / 服务器</dt><dd><code>{plan.tenantId} / {plan.serverId || '未绑定'}</code></dd></div>
            {summary.lifecycle && <>
              <div><dt>创建者租户</dt><dd>{summary.lifecycle.creatorTenantId}</dd></div>
              <div><dt>已绑定目标代次</dt><dd>
                {summary.lifecycle.targets.map((target) => (
                  <div key={target.tenantId} style={{ overflowWrap: 'anywhere' }}>
                    <code>{target.tenantId} · {target.expectedGeneration}</code>
                  </div>
                ))}
              </dd></div>
              <div><dt>实际目标对象</dt><dd>
                {summary.lifecycle.targetObjects.map((target) => (
                  <div key={target.objectId} style={{ overflowWrap: 'anywhere' }}>
                    <code>{target.objectId} · {target.tenantId} / {target.serverId}</code>
                  </div>
                ))}
              </dd></div>
              <div><dt>作用域摘要</dt><dd style={{ overflowWrap: 'anywhere' }}><code>{summary.lifecycle.scopeDigest}</code></dd></div>
            </>}
            <div><dt>目标版本</dt><dd>{summary.target || '当前版本'}</dd></div>
            <div><dt>当前身份</dt><dd><code>{summary.expectedBefore.runtimeIdentityHash ?? '未提供'}</code></dd></div>
            <div><dt>影响范围</dt><dd>{summary.scope}</dd></div>
            <div><dt>预期影响</dt><dd>{summary.impact}</dd></div>
            <div><dt>失败处理</dt><dd>{summary.rollback}</dd></div>
            {summary.timeoutSeconds && <div><dt>执行超时</dt><dd>{summary.timeoutSeconds} 秒</dd></div>}
            {plan.observationStartedAt && <div><dt>观察开始</dt><dd>{formatTime(plan.observationStartedAt)}</dd></div>}
            {plan.observationEndsAt && <div><dt>最早收口</dt><dd>{formatTime(plan.observationEndsAt)}</dd></div>}
            {plan.maintenanceSilenceEndsAt && (
              <div><dt>维护静默</dt><dd>{plan.maintenanceSilenceReleasedAt ? '已解除' : `至 ${formatTime(plan.maintenanceSilenceEndsAt)}`}</dd></div>
            )}
            {plan.blockingAlertFingerprints && plan.blockingAlertFingerprints.length > 0 && (
              <div><dt>阻断告警</dt><dd>{plan.blockingAlertFingerprints.length} 项仍在触发</dd></div>
            )}
            {plan.closureReason && <div><dt>收口阻断</dt><dd>{plan.closureReason}</dd></div>}
            {plan.scheduleAt && <div><dt>计划执行时间</dt><dd>{formatSchedule(plan.scheduleAt)}</dd></div>}
          </dl>
          <p role="status">{prepared
            ? (plan.executionAvailable || plan.closureAvailable ? '执行结束后仍需完成观察与清理确认，才可关闭登记。' : '当前执行能力尚未核验或执行所有权不可用；可以审阅，执行与新收口保持阻断。')
            : '旧计划缺少完整目标代次，不能新增批准、执行或收口；历史记录保留，请重新提案。'}</p>
          {scheduled && <p role="status">{waiting ? '尚未到达执行时间。' : '已到执行时间，请有权用户手动执行。'}以服务端时间校验为准；不会自动执行，也不代表维护窗口授权。</p>}
          {error && <p className="inline-error" role="alert">{error}</p>}
          <div className="step-line" aria-label="执行阶段">
            {summary.steps.map((step, index) => (
              <span key={step}>
                <b>{index + 1}</b>{phaseLabel[step] ?? step}
              </span>
            ))}
          </div>
          {approving && plan.requiresConfirmation && canApprove && (
            <label className="confirmation-field">
              <span>输入确认短语</span>
              <code>{phrase}</code>
              <input
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
            disabled={!enabled}
            onClick={() => { if (enabled) { if (observing) onClosePlan(); else onConfirm(value) } }}
          >
            <Check size={17} aria-hidden="true" />
            {pending ? '提交中' : waiting ? '等待执行时间' : observing ? canClose ? '确认收口' : '观察期未结束' : approving ? canApprove ? '批准计划' : '当前身份不能批准' : canExecute ? '执行计划' : '当前身份不能执行'}
          </button>
          {approving && !canApprove && <small className="inline-error">当前身份不满足此计划的批准身份要求。</small>}
          {(plan.state === 'approved' || scheduled) && !canExecute && <small className="inline-error">当前身份不满足此计划的审批与执行身份要求。</small>}
        </footer>
      </section>
    </div>
  )
}
