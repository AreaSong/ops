import { formatTime, shortHash } from '../labels'
import type { AuditEntry } from '../types'

interface AuditProps {
  entries: AuditEntry[]
  hasMore: boolean
  loadingMore: boolean
  onLoadMore: () => void
}
export function Audit({ entries, hasMore, loadingMore, onLoadMore }: AuditProps) {
  return (
    <div className="page">
      <header className="page-header"><div><span className="eyebrow">365 天摘要</span><h1>操作审计</h1></div><span>{entries.length} 项</span></header>
      <section className="page-section no-top-gap">
        {entries.length === 0 && <div className="empty-state">暂无审计记录</div>}
        <div className="data-table audit-table" role="table" aria-label="操作审计">
          <div className="data-table-head" role="row">
            <span>时间</span><span>事件</span><span>资源</span><span>操作者哈希</span><span>结果</span>
          </div>
          {entries.map((entry) => (
            <div key={entry.sequence} className="data-table-row static" role="row">
              <time>{formatTime(entry.occurredAt)}</time>
              <span>{entry.event}</span>
              <span><code>{entry.resource}</code></span>
              <span title={entry.actorHash}><code>{shortHash(entry.actorHash)}</code></span>
              <span className={`outcome outcome-${entry.outcome}`}>{entry.outcome}</span>
            </div>
          ))}
        </div>
        {hasMore && (
          <div className="load-more-row">
            <button className="button secondary" type="button" onClick={onLoadMore} disabled={loadingMore}>
              {loadingMore ? '读取中' : '加载更多'}
            </button>
          </div>
        )}
      </section>
    </div>
  )
}
