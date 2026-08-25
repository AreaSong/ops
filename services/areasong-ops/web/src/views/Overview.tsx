import { AlertTriangle, BellRing, CheckCheck, Clock3, Eye, ShieldCheck } from 'lucide-react'
import { StatusBadge } from '../components/StatusBadge'
import { formatTime } from '../labels'
import type { ActiveAlert, AutomaticTaskView, ReleasePlan, ServiceView, Task } from '../types'

interface OverviewProps {
  services: ServiceView[]
  automaticTasks: AutomaticTaskView[]
  tasks: Task[]
  plans: ReleasePlan[]
  alerts: ActiveAlert[]
  alertsError: string
  alertsURL?: string
  onService: (name: string) => void
  onTask: (task: Task) => void
  onPlan: (plan: ReleasePlan) => void
  onAutomaticTasks: () => void
}

const activeTaskStates = new Set<Task['state']>(['queued', 'running', 'rolling_back'])
const attentionTaskStates = new Set<Task['state']>(['failed_recoverable', 'needs_attention', 'recovery_uncertain'])

function serviceGate(service: ServiceView): 'healthy' | 'error' | 'warning' {
  if (service.statusError || !service.status) return 'error'
  if (service.activeTaskId) return 'warning'
  return 'healthy'
}

export function Overview({
  services, automaticTasks, tasks, plans, alerts, alertsError, alertsURL,
  onService, onTask, onPlan, onAutomaticTasks,
}: OverviewProps) {
  const pendingPlans = plans.filter((plan) => plan.state === 'pending_approval')
  const scheduledPlans = plans.filter((plan) => plan.state === 'scheduled')
  const approvedPlans = plans.filter((plan) => plan.state === 'approved')
  const observingPlans = plans.filter((plan) => plan.state === 'observing')
  const attentionPlans = plans.filter((plan) => plan.state === 'needs_attention')
  const activeTasks = tasks.filter((task) => activeTaskStates.has(task.state))
  const attentionTasks = tasks.filter((task) => attentionTaskStates.has(task.state))
  const orphanedAttentionPlans = attentionPlans.filter((plan) => !attentionTasks.some((task) => task.id === plan.taskId))
  const actionablePlans = [...pendingPlans, ...scheduledPlans, ...approvedPlans]
  const failedAutomaticTasks = automaticTasks.filter((task) =>
    Boolean(task.statusError) || !task.status || task.status.health !== 'healthy')
  const handlingCount = alerts.length + actionablePlans.length + observingPlans.length + attentionTasks.length +
    orphanedAttentionPlans.length + failedAutomaticTasks.length

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">受控生产操作</span><h1>操作总览</h1></div>
        <span className="last-updated">自动刷新 · SSE 实时事件</span>
      </header>
      <section className="metric-strip operation-metrics" aria-label="操作状态">
        <div><ShieldCheck size={18} aria-hidden="true" /><span><b>{pendingPlans.length}</b>待批准计划</span></div>
        <div><CheckCheck size={18} aria-hidden="true" /><span><b>{approvedPlans.length}</b>已批准待执行</span></div>
        <div><Clock3 size={18} aria-hidden="true" /><span><b>{activeTasks.length}</b>活动执行</span></div>
        <div><Eye size={18} aria-hidden="true" /><span><b>{observingPlans.length}</b>观察中</span></div>
        <div className={attentionTasks.length + orphanedAttentionPlans.length > 0 ? 'metric-alert' : ''}>
          <AlertTriangle size={18} aria-hidden="true" /><span><b>{attentionTasks.length + orphanedAttentionPlans.length}</b>恢复待处理</span>
        </div>
        <div className={failedAutomaticTasks.length > 0 ? 'metric-alert' : ''}>
          <Clock3 size={18} aria-hidden="true" /><span><b>{failedAutomaticTasks.length}</b>自动任务异常</span>
        </div>
      </section>

      <section className="page-section">
        <div className="section-heading"><h2>需要处理</h2><span>{handlingCount} 项</span></div>
        {handlingCount === 0 && !alertsError && (
          <div className="empty-state compact">当前没有等待批准、执行或恢复核对的事项</div>
        )}
        {alertsError && <div className="inline-error compact"><AlertTriangle size={16} />告警数据不可用</div>}
        {handlingCount > 0 && (
          <div className="task-list compact-list">
            {alerts.map((alert) => {
              const alertURL = alert.grafanaUrl || alertsURL
              const content = <>
                <span><strong>{alert.service}</strong><small>{alert.alertName}</small></span>
                <span className="task-summary">{alert.summary || '活动告警需要处理'}</span>
                <time>{formatTime(alert.startsAt)}</time>
                <span className={`badge badge-alert-${alert.severity || 'unknown'}`}>
                  <BellRing size={12} aria-hidden="true" />{alert.severity === 'critical' ? '严重' : '警告'}
                </span>
              </>
              return alertURL ? (
                <a key={alert.fingerprint} className="task-row alert-row"
                  href={alertURL} target="_blank" rel="noreferrer">{content}</a>
              ) : (
                <div key={alert.fingerprint} className="task-row alert-row static">{content}</div>
              )
            })}
            {failedAutomaticTasks.map((task) => (
              <button key={task.objectId} type="button" className="task-row" onClick={onAutomaticTasks}>
                <span><strong>{task.displayName}</strong><small>{task.schedule}</small></span>
                <span className="task-summary">{task.statusError || '任务结果已超过新鲜度窗口'}</span>
                <time>{formatTime(task.status?.lastSuccessAt as string | undefined)}</time>
                <StatusBadge kind="health" value="error" label="任务异常" />
              </button>
            ))}
            {actionablePlans.map((plan) => (
              <button key={plan.id} type="button" className="task-row" onClick={() => onPlan(plan)}>
                <span><strong>{plan.service}</strong><small>{plan.action}{plan.target ? ` · ${plan.target}` : ''}</small></span>
                <span className="task-summary">{plan.state === 'approved' ? '批准已绑定，等待执行' : plan.state === 'scheduled' ? `等待 ${formatTime(plan.scheduleAt)} 调度` : '等待明确批准'}</span>
                <time>{formatTime(plan.updatedAt)}</time>
                <StatusBadge kind="plan" value={plan.state} />
              </button>
            ))}
            {observingPlans.map((plan) => (
              <button key={plan.id} type="button" className="task-row" onClick={() => onPlan(plan)}>
                <span><strong>{plan.service}</strong><small>{plan.action}{plan.target ? ` · ${plan.target}` : ''}</small></span>
                <span className="task-summary">{plan.closureReason || `观察至 ${formatTime(plan.observationEndsAt)}`}</span>
                <time>{formatTime(plan.updatedAt)}</time>
                <StatusBadge kind="plan" value={plan.state} />
              </button>
            ))}
            {attentionTasks.map((task) => (
              <button key={task.id} type="button" className="task-row" onClick={() => onTask(task)}>
                <span><strong>{task.service}</strong><small>{task.action}{task.target ? ` · ${task.target}` : ''}</small></span>
                <span className="task-summary">{task.summary || task.error || '需要人工核对'}</span>
                <time>{formatTime(task.finishedAt ?? task.createdAt)}</time>
                <StatusBadge kind="state" value={task.state} />
              </button>
            ))}
            {orphanedAttentionPlans.map((plan) => (
              <div key={plan.id} className="task-row static">
                <span><strong>{plan.service}</strong><small>{plan.action}{plan.target ? ` · ${plan.target}` : ''}</small></span>
                <span className="task-summary">{plan.closureReason || '计划需要人工处理'}</span>
                <time>{formatTime(plan.updatedAt)}</time>
                <StatusBadge kind="plan" value={plan.state} />
              </div>
            ))}
          </div>
        )}
      </section>

      <section className="page-section">
        <div className="section-heading"><h2>执行门禁</h2><span>{services.length} 个受控对象</span></div>
        <div className="service-table" role="table" aria-label="执行门禁">
          {services.map((service) => {
            const gate = serviceGate(service)
            const discovery = service.releaseDiscovery
            const releaseState = discovery?.prepared ? '发布已准备' : discovery ? '发布待准备' : '尚未检查发布'
            return (
              <button key={service.name} className="service-row" type="button" onClick={() => onService(service.name)}>
                <span className={`service-indicator ${gate}`} aria-hidden="true" />
                <span className="service-main"><strong>{service.displayName}</strong><small>{service.description}</small></span>
                <span className="service-version">v{service.status?.currentVersion ?? '—'}</span>
                <span className="service-dependencies">{releaseState}</span>
                <StatusBadge kind="health" value={gate}
                  label={gate === 'healthy' ? '门禁可用' : gate === 'warning' ? '任务占用' : '门禁异常'} />
              </button>
            )
          })}
        </div>
      </section>

      <section className="page-section">
        <div className="section-heading"><h2>最近执行</h2><span>最近 6 项</span></div>
        {tasks.length === 0 && <div className="empty-state">暂无执行记录</div>}
        <div className="task-list compact-list">
          {tasks.slice(0, 6).map((task) => (
            <button key={task.id} type="button" className="task-row" onClick={() => onTask(task)}>
              <span><strong>{task.service}</strong><small>{task.action}{task.target ? ` · ${task.target}` : ''}</small></span>
              <span className="task-summary">{task.summary || task.currentPhase || '等待执行'}</span>
              <time>{formatTime(task.createdAt)}</time>
              <StatusBadge kind="state" value={task.state} />
            </button>
          ))}
        </div>
      </section>
    </div>
  )
}
