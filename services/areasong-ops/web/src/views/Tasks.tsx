import { formatTime, phaseLabel } from '../labels'
import type { Task } from '../types'
import { StatusBadge } from '../components/StatusBadge'

interface TasksProps {
  tasks: Task[]
  onTask: (task: Task) => void
}
export function Tasks({ tasks, onTask }: TasksProps) {
  return (
    <div className="page">
      <header className="page-header"><div><span className="eyebrow">状态机</span><h1>任务记录</h1></div><span>{tasks.length} 项</span></header>
      <section className="page-section no-top-gap">
        {tasks.length === 0 && <div className="empty-state">暂无任务记录</div>}
        <div className="data-table task-table" role="table" aria-label="任务记录">
          <div className="data-table-head" role="row">
            <span>服务 / 操作</span><span>当前阶段</span><span>创建时间</span><span>结果</span>
          </div>
          {tasks.map((task) => (
            <button key={task.id} className="data-table-row" type="button" role="row" onClick={() => onTask(task)}>
              <span><strong>{task.service}</strong><small>{task.action}{task.target ? ` · ${task.target}` : ''}</small></span>
              <span>{phaseLabel[task.currentPhase ?? ''] ?? task.currentPhase ?? '等待'}</span>
              <time>{formatTime(task.createdAt)}</time>
              <StatusBadge kind="state" value={task.state} />
            </button>
          ))}
        </div>
      </section>
    </div>
  )
}
