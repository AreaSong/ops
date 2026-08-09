import { AlertTriangle, CheckCircle2, Clock3, Server } from 'lucide-react'
import { formatTime } from '../labels'
import type { ServiceView, Task } from '../types'
import { StatusBadge } from '../components/StatusBadge'

interface OverviewProps {
  services: ServiceView[]
  tasks: Task[]
  onService: (name: string) => void
  onTask: (task: Task) => void
}
function serviceHealth(service: ServiceView): 'healthy' | 'error' | 'warning' {
  if (service.statusError || !service.status) return 'error'
  if (service.activeTaskId) return 'warning'
  return 'healthy'
}

export function Overview({ services, tasks, onService, onTask }: OverviewProps) {
  const active = tasks.filter((task) => task.state === 'queued' || task.state === 'running')
  const uncertain = tasks.filter((task) => task.state === 'recovery_uncertain')
  const healthy = services.filter((service) => serviceHealth(service) === 'healthy').length

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">生产态势</span><h1>运行总览</h1></div>
        <span className="last-updated">自动刷新 · SSE 实时事件</span>
      </header>
      <section className="metric-strip" aria-label="运行指标">
        <div><Server size={18} aria-hidden="true" /><span><b>{services.length}</b>受控服务</span></div>
        <div><CheckCircle2 size={18} aria-hidden="true" /><span><b>{healthy}</b>状态正常</span></div>
        <div><Clock3 size={18} aria-hidden="true" /><span><b>{active.length}</b>活动任务</span></div>
        <div className={uncertain.length > 0 ? 'metric-alert' : ''}>
          <AlertTriangle size={18} aria-hidden="true" /><span><b>{uncertain.length}</b>待人工核对</span>
        </div>
      </section>
      <section className="page-section">
        <div className="section-heading"><h2>服务状态</h2><span>{healthy}/{services.length} 正常</span></div>
        <div className="service-table" role="table" aria-label="服务状态">
          {services.map((service) => {
            const health = serviceHealth(service)
            return (
              <button key={service.name} className="service-row" type="button" onClick={() => onService(service.name)}>
                <span className={`service-indicator ${health}`} aria-hidden="true" />
                <span className="service-main"><strong>{service.displayName}</strong><small>{service.description}</small></span>
                <span className="service-version">v{service.status?.currentVersion ?? '—'}</span>
                <span className="service-dependencies">
                  {service.name === 'sub2api' ? `PG ${service.status?.postgresState ?? '—'} · Redis ${service.status?.redisState ?? '—'}` : `Web ${service.status?.appState ?? '—'}`}
                </span>
                <StatusBadge kind="health" value={health} label={health === 'healthy' ? '正常' : health === 'warning' ? '变更中' : '异常'} />
              </button>
            )
          })}
        </div>
      </section>
      <section className="page-section">
        <div className="section-heading"><h2>最近任务</h2><span>最近 6 项</span></div>
        {tasks.length === 0 && <div className="empty-state">暂无受控任务</div>}
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
