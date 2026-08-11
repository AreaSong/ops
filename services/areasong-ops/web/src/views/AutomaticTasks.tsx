import { RefreshCw, RotateCw, TimerReset } from 'lucide-react'
import { formatTime } from '../labels'
import type { ActionDefinition, AutomaticTaskView } from '../types'
import { StatusBadge } from '../components/StatusBadge'

interface AutomaticTasksProps {
  tasks: AutomaticTaskView[]
  busyAction: string
  onRefresh: () => void
  onAction: (task: AutomaticTaskView, action: ActionDefinition) => void
}

function taskHealth(task: AutomaticTaskView): 'healthy' | 'warning' | 'error' {
  if (task.statusError || !task.status) return 'error'
  if (task.activeTaskId) return 'warning'
  if (task.status.health === 'healthy') return 'healthy'
  if (task.status.health === 'stale') return 'warning'
  return 'error'
}

export function AutomaticTasks({ tasks, busyAction, onRefresh, onAction }: AutomaticTasksProps) {
  return (
    <div className="page automatic-task-page">
      <header className="page-header">
        <div><span className="eyebrow">系统调度保持权威</span><h1>自动任务</h1></div>
        <button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新自动任务状态">
          <RefreshCw size={18} aria-hidden="true" />
        </button>
      </header>
      <section className="page-section no-top-gap">
        <div className="section-heading"><h2>已登记关键任务</h2><span>{tasks.length} 项</span></div>
        {tasks.length === 0 && <div className="empty-state">暂无已登记自动任务</div>}
        <div className="automatic-task-list">
          {tasks.map((task) => {
            const health = taskHealth(task)
            const rerun = task.actions.rerun
            const busy = busyAction === `${task.name}/rerun`
            const disabled = !rerun?.enabled || Boolean(task.activeTaskId) || busy
            return (
              <article className="automatic-task-row" key={task.objectId}>
                <div className="automatic-task-main">
                  <span className="automatic-task-icon"><TimerReset size={18} aria-hidden="true" /></span>
                  <span><strong>{task.displayName}</strong><small>{task.description}</small></span>
                </div>
                <dl className="automatic-task-facts">
                  <div><dt>调度</dt><dd>{task.schedule}</dd></div>
                  <div><dt>最近成功</dt><dd>{formatTime(task.status?.lastSuccessAt as string | undefined)}</dd></div>
                  <div><dt>生命周期</dt><dd>{task.metadata.lifecycle === 'active' ? '运行中' : task.metadata.lifecycle}</dd></div>
                </dl>
                <StatusBadge kind="health" value={health}
                  label={health === 'healthy' ? '新鲜' : health === 'warning' ? '执行中或陈旧' : '状态异常'} />
                <button className="button secondary automatic-task-action" type="button" disabled={disabled}
                  title={disabled ? task.activeTaskId ? '任务已有活动执行' : rerun?.disabledReason ?? '补跑不可用' : rerun.impact}
                  onClick={() => onAction(task, rerun)}>
                  <RotateCw size={15} aria-hidden="true" />{busy ? '提交中' : '立即补跑'}
                </button>
              </article>
            )
          })}
        </div>
      </section>
    </div>
  )
}
