import { CircleCheck, CircleDot, CircleX, RotateCcw, SearchCheck, X } from 'lucide-react'
import { formatTime, phaseLabel } from '../labels'
import type { OpsEvent, Task } from '../types'
import { StatusBadge } from './StatusBadge'

interface TaskDrawerProps {
  task: Task
  events: OpsEvent[]
  loading: boolean
  hasMore: boolean
  loadingMore: boolean
  pending: boolean
  onClose: () => void
  onLoadMore: () => void
  onRecovery: (action: string) => void
}
export function TaskDrawer({ task, events, loading, hasMore, loadingMore, pending, onClose, onLoadMore, onRecovery }: TaskDrawerProps) {
  return (
    <div className="drawer-backdrop" role="presentation" onMouseDown={(event) => {
      if (event.currentTarget === event.target) onClose()
    }}>
      <aside className="task-drawer" role="dialog" aria-modal="true" aria-labelledby="task-title">
        <header className="drawer-header">
          <div>
            <span className="eyebrow">{task.service} · {task.action}</span>
            <h2 id="task-title">执行详情</h2>
          </div>
          <button className="icon-button" type="button" onClick={onClose} title="关闭">
            <X size={18} aria-hidden="true" />
          </button>
        </header>
        <div className="drawer-summary">
          <StatusBadge kind="state" value={task.state} />
          <dl>
            <div><dt>执行 ID</dt><dd><code>{task.id}</code></dd></div>
            <div><dt>目标</dt><dd>{task.target || '—'}</dd></div>
            <div><dt>创建时间</dt><dd>{formatTime(task.createdAt)}</dd></div>
            {task.planDigest && <div><dt>计划摘要</dt><dd><code>{task.planDigest}</code></dd></div>}
            <div><dt>生产变更</dt><dd>{task.productionChanged ? '已发生或按已发生处理' : '无变更证据'}</dd></div>
          </dl>
        </div>
        {task.error && <div className="error-block">{task.error}</div>}
        {task.stages && task.stages.length > 0 && (
          <section className="task-stage-section">
            <h3>执行阶段</h3>
            <div className="task-stage-grid">
              {task.stages.map((stage) => (
                <div key={stage.name} className={`task-stage task-stage-${stage.state}`}>
                  <span>{phaseLabel[stage.name] ?? stage.name}</span>
                  <strong>{stage.state}</strong>
                </div>
              ))}
            </div>
          </section>
        )}
        {task.recoveryActions && task.recoveryActions.length > 0 && (
          <section className="recovery-section">
            <h3>恢复处理</h3>
            <div className="recovery-actions">
              {task.recoveryActions.map((action) => {
                const Icon = action.name === 'inspect' ? SearchCheck : RotateCcw
                return (
                  <button key={action.name} className="button secondary" type="button"
                    disabled={!action.enabled || pending || action.name === 'reconcile'}
                    title={action.reason || (action.name === 'reconcile' ? '按运行手册人工核对后处置' : action.label)}
                    onClick={() => onRecovery(action.name)}>
                    <Icon size={16} aria-hidden="true" />{action.label}
                  </button>
                )
              })}
            </div>
          </section>
        )}
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
          {hasMore && (
            <div className="load-more-row compact">
              <button className="button secondary" type="button" onClick={onLoadMore} disabled={loadingMore}>
                {loadingMore ? '读取中' : '加载更早之后的记录'}
              </button>
            </div>
          )}
        </section>
      </aside>
    </div>
  )
}
