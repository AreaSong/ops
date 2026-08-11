import { AlertTriangle, CheckCheck, Clock3, ShieldCheck } from 'lucide-react'
import { StatusBadge } from '../components/StatusBadge'
import { formatTime } from '../labels'
import type { ReleasePlan, ServiceView, Task } from '../types'

interface OverviewProps {
  services: ServiceView[]
  tasks: Task[]
  plans: ReleasePlan[]
  onService: (name: string) => void
  onTask: (task: Task) => void
  onPlan: (plan: ReleasePlan) => void
}

const activeTaskStates = new Set<Task['state']>(['queued', 'running', 'rolling_back'])
const attentionTaskStates = new Set<Task['state']>(['failed_recoverable', 'needs_attention', 'recovery_uncertain'])

function serviceGate(service: ServiceView): 'healthy' | 'error' | 'warning' {
  if (service.statusError || !service.status) return 'error'
  if (service.activeTaskId) return 'warning'
  return 'healthy'
}

export function Overview({ services, tasks, plans, onService, onTask, onPlan }: OverviewProps) {
  const pendingPlans = plans.filter((plan) => plan.state === 'pending_approval')
  const approvedPlans = plans.filter((plan) => plan.state === 'approved')
  const activeTasks = tasks.filter((task) => activeTaskStates.has(task.state))
  const attentionTasks = tasks.filter((task) => attentionTaskStates.has(task.state))
  const actionablePlans = [...pendingPlans, ...approvedPlans]

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">受控生产操作</span><h1>操作总览</h1></div>
        <span className="last-updated">自动刷新 · SSE 实时事件</span>
      </header>
      <section className="metric-strip" aria-label="操作状态">
        <div><ShieldCheck size={18} aria-hidden="true" /><span><b>{pendingPlans.length}</b>待批准计划</span></div>
        <div><CheckCheck size={18} aria-hidden="true" /><span><b>{approvedPlans.length}</b>已批准待执行</span></div>
        <div><Clock3 size={18} aria-hidden="true" /><span><b>{activeTasks.length}</b>活动执行</span></div>
        <div className={attentionTasks.length > 0 ? 'metric-alert' : ''}>
          <AlertTriangle size={18} aria-hidden="true" /><span><b>{attentionTasks.length}</b>恢复待处理</span>
        </div>
      </section>

      <section className="page-section">
        <div className="section-heading"><h2>需要处理</h2><span>{actionablePlans.length + attentionTasks.length} 项</span></div>
        {actionablePlans.length === 0 && attentionTasks.length === 0 && (
          <div className="empty-state compact">当前没有等待批准、执行或恢复核对的事项</div>
        )}
        {(actionablePlans.length > 0 || attentionTasks.length > 0) && (
          <div className="task-list compact-list">
            {actionablePlans.map((plan) => (
              <button key={plan.id} type="button" className="task-row" onClick={() => onPlan(plan)}>
                <span><strong>{plan.service}</strong><small>{plan.action}{plan.target ? ` · ${plan.target}` : ''}</small></span>
                <span className="task-summary">{plan.state === 'approved' ? '批准已绑定，等待执行' : '等待明确批准'}</span>
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
