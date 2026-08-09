import { CircleCheck, CircleDot, CircleX, X } from 'lucide-react'
import { formatTime, phaseLabel } from '../labels'
import type { OpsEvent, Task } from '../types'
import { StatusBadge } from './StatusBadge'

interface TaskDrawerProps {
  task: Task
  events: OpsEvent[]
  loading: boolean
  onClose: () => void
}
export function TaskDrawer({ task, events, loading, onClose }: TaskDrawerProps) {
  return (
    <div className="drawer-backdrop" role="presentation" onMouseDown={(event) => {
      if (event.currentTarget === event.target) onClose()
    }}>
      <aside className="task-drawer" role="dialog" aria-modal="true" aria-labelledby="task-title">
        <header className="drawer-header">
          <div>
            <span className="eyebrow">{task.service} · {task.action}</span>
            <h2 id="task-title">任务详情</h2>
          </div>
          <button className="icon-button" type="button" onClick={onClose} title="关闭">
            <X size={18} aria-hidden="true" />
          </button>
        </header>
        <div className="drawer-summary">
          <StatusBadge kind="state" value={task.state} />
          <dl>
            <div><dt>任务 ID</dt><dd><code>{task.id}</code></dd></div>
            <div><dt>目标</dt><dd>{task.target || '—'}</dd></div>
            <div><dt>创建时间</dt><dd>{formatTime(task.createdAt)}</dd></div>
          </dl>
        </div>
        {task.error && <div className="error-block">{task.error}</div>}
        <section className="timeline-section">
          <h3>执行记录</h3>
          {loading && <div className="empty-state compact">读取中</div>}
          {!loading && events.length === 0 && <div className="empty-state compact">暂无阶段记录</div>}
          <ol className="timeline">
            {events.map((event) => {
              const Icon = event.level === 'error' ? CircleX : event.level === 'warning' ? CircleDot : CircleCheck
              return (
                <li key={event.sequence} className={`timeline-${event.level}`}>
                  <Icon size={17} aria-hidden="true" />
                  <div>
                    <div className="timeline-heading">
                      <strong>{phaseLabel[event.phase ?? ''] ?? event.phase ?? '事件'}</strong>
                      <time>{formatTime(event.occurredAt)}</time>
                    </div>
                    <p>{event.message}</p>
                    {event.data && Object.keys(event.data).length > 0 && (
                      <pre>{JSON.stringify(event.data, null, 2)}</pre>
                    )}
                  </div>
                </li>
              )
            })}
          </ol>
        </section>
      </aside>
    </div>
  )
}
