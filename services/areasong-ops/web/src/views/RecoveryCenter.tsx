import { ArchiveRestore, CheckCircle2, CircleAlert, CircleHelp, FileCheck2, LoaderCircle, RefreshCw, RotateCcw, SearchCheck, ShieldAlert } from 'lucide-react'
import { useState } from 'react'
import { runAction } from '../action'
import { formatTime, shortHash } from '../labels'
import type { RecoveryCenterView, RecoveryAction } from '../types'

interface RecoveryCenterProps {
  items: RecoveryCenterView[]
  loading: boolean
  available: boolean
  error: string
  busy: string
  onRefresh: () => void
  onAction: (service: string, action: string, recoveryPointId?: string) => Promise<void>
  onRestore: (service: string, recoveryPointId: string, mode: 'isolated' | 'production', confirmation: string) => Promise<void>
}

function actionIcon(name: string) {
  if (name === 'inspect') return SearchCheck
  if (name === 'rollback' || name === 'restore-drill') return RotateCcw
  if (name === 'restore') return ArchiveRestore
  return FileCheck2
}

function actionLabel(action: RecoveryAction): string {
  if (action.label) return action.label
  if (action.name === 'inspect') return '核对证据'
  if (action.name === 'rollback') return '创建回滚计划'
  if (action.name === 'restore-drill') return '隔离恢复演练'
  if (action.name === 'restore') return '生产恢复'
  return action.name
}

export function RecoveryCenter({ items, loading, available, error, busy, onRefresh, onAction, onRestore }: RecoveryCenterProps) {
  const [restore, setRestore] = useState<{ service: string; pointId: string; mode: 'isolated' | 'production'; confirmation: string } | null>(null)
  const restorePhrase = restore
    ? restore.mode === 'production'
      ? `创建生产恢复计划 ${restore.service}`
      : `创建隔离恢复演练计划 ${restore.service}`
    : ''

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">备份证据与恢复演练</span><h1>恢复中心</h1></div>
        <button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新恢复中心" disabled={loading}>
          {loading ? <LoaderCircle className="spin" size={17} /> : <RefreshCw size={17} />}
        </button>
      </header>

      {!available && !loading && <div className="empty-state feature-empty"><CircleHelp size={19} aria-hidden="true" />{error || '恢复中心能力尚未启用'}</div>}
      {available && error && <div className="inline-error" role="alert"><ShieldAlert size={16} />{error}</div>}
      {available && loading && items.length === 0 && <div className="empty-state"><LoaderCircle className="spin" size={18} />读取恢复证据</div>}
      {available && !loading && items.length === 0 && <div className="empty-state">暂无恢复点或演练记录</div>}
      {available && items.length > 0 && <section className="page-section recovery-center-section">
        <div className="section-heading"><h2>服务恢复状态</h2><span>{items.length} 项</span></div>
        <div className="recovery-grid">
          {items.map((item) => {
            const point = item.latest
            const pointId = point?.id ?? ''
            const pointFresh = Boolean(point?.recoverableUntil && new Date(point.recoverableUntil).getTime() > Date.now())
            const drillTone = item.drillFresh ? 'healthy' : 'warning'
            return <article className="recovery-card" key={item.service}>
              <header className="recovery-card-header"><div><span className="eyebrow">受控服务</span><h2>{item.service}</h2></div><span className={`recovery-health ${drillTone}`}><span className="service-indicator" />{item.drillFresh ? '演练新鲜' : '演练过期'}</span></header>
              <dl className="recovery-facts"><div><dt>最近演练</dt><dd>{formatTime(item.drillLastSuccessAt)}</dd></div><div><dt>最近恢复点</dt><dd>{point ? shortHash(point.id) : '—'}</dd></div><div><dt>状态</dt><dd>{point?.status || '暂无证据'}</dd></div><div><dt>可恢复至</dt><dd>{formatTime(point?.recoverableUntil)}</dd></div></dl>
              {!item.drillFresh && <div className="recovery-warning"><CircleAlert size={15} aria-hidden="true" />{item.drillReason || '恢复演练不在新鲜窗口内'}</div>}
              {point && <div className="recovery-evidence"><span><strong>{point.evidence?.artifacts?.length ?? 0}</strong> 个受控产物</span><code title={point.evidenceDigest}>{shortHash(point.evidenceDigest)}</code><span>{pointFresh ? '仍在恢复窗口' : '恢复窗口已过期'}</span></div>}
              <div className="recovery-actions">
                {(item.availableActions ?? []).map((action) => {
                  const Icon = actionIcon(action.name)
                  const actionBusy = busy === `${item.service}/${action.name}`
                  return <button className="button secondary" type="button" key={action.name} disabled={!action.enabled || actionBusy || ((action.name === 'restore' || action.name === 'restore-drill') && !pointFresh)} title={action.reason || actionLabel(action)} onClick={() => (action.name === 'restore' || action.name === 'restore-drill') && point
                    ? setRestore({ service: item.service, pointId, mode: action.name === 'restore' ? 'production' : 'isolated', confirmation: '' })
                    : runAction(onAction(item.service, action.name, pointId))}><Icon size={14} />{actionBusy ? '提交中' : actionLabel(action)}</button>
                })}
                {point && <button className="button danger" type="button" disabled={!pointFresh || busy === `${item.service}/restore`} title={!pointFresh ? '恢复点已过期' : '打开恢复确认'} onClick={() => setRestore({ service: item.service, pointId, mode: 'isolated', confirmation: '' })}><ArchiveRestore size={14} />恢复</button>}
              </div>
            </article>
          })}
        </div>
      </section>}

      {restore && <div className="modal-backdrop" role="presentation" onMouseDown={(event) => { if (event.currentTarget === event.target) setRestore(null) }}>
        <section className="modal recovery-modal" role="dialog" aria-modal="true" aria-labelledby="restore-title">
          <header className="modal-header"><div className="modal-title-group"><span className="warning-icon"><ArchiveRestore size={19} /></span><div><h2 id="restore-title">创建恢复请求</h2><span>{restore.service} · {shortHash(restore.pointId)}</span></div></div></header>
          <div className="modal-body"><div className="recovery-mode-tabs" role="group" aria-label="恢复模式"><button type="button" className={restore.mode === 'isolated' ? 'active' : ''} onClick={() => setRestore({ ...restore, mode: 'isolated', confirmation: '' })}><CheckCircle2 size={15} />隔离验证</button><button type="button" className={restore.mode === 'production' ? 'active danger' : ''} onClick={() => setRestore({ ...restore, mode: 'production', confirmation: '' })}><ShieldAlert size={15} />生产恢复</button></div><label className="confirmation-field"><span>确认短语</span><code>{restorePhrase}</code><input autoFocus value={restore.confirmation} onChange={(event) => setRestore({ ...restore, confirmation: event.target.value })} autoComplete="off" spellCheck={false} /></label></div>
          <footer className="modal-footer"><button className="button secondary" type="button" onClick={() => setRestore(null)}>取消</button><button className={restore.mode === 'production' ? 'button danger' : 'button secondary'} type="button" disabled={busy === `${restore.service}/restore` || restore.confirmation !== restorePhrase} onClick={() => { const value = restore; setRestore(null); runAction(onRestore(value.service, value.pointId, value.mode, value.confirmation)) }}>{busy === `${restore.service}/restore` ? '提交中' : '提交恢复请求'}</button></footer>
        </section>
      </div>}
    </div>
  )
}
