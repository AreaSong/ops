import { ChevronDown, ChevronRight, CircleHelp, LoaderCircle, Play, Plus, RefreshCw, ShieldAlert } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { formatTime } from '../labels'
import type { BatchStrategy, BatchTask, FailurePolicy } from '../types'
import { StatusBadge } from '../components/StatusBadge'

interface BatchesProps {
  batches: BatchTask[]
  loading: boolean
  available: boolean
  error: string
  busy: string
  onRefresh: () => void
  onCreate: (task: BatchTask) => Promise<void>
  onApprove: (id: string, digest: string, confirmation: string) => Promise<void>
  onRun: (id: string) => Promise<void>
}

const stateLabels: Record<BatchTask['state'], string> = {
  pending: '待批准', planning: '规划中', running: '执行中', paused: '已暂停', succeeded: '成功',
  failed: '失败', rolling_back: '回滚中', rolled_back: '已回滚', cancelled: '已取消', needs_attention: '待处理', unknown: '未知',
}

function stateTone(state: BatchTask['state']): 'healthy' | 'warning' | 'error' | 'unknown' {
  if (state === 'succeeded' || state === 'rolled_back') return 'healthy'
  if (state === 'pending' || state === 'planning' || state === 'running' || state === 'paused' || state === 'rolling_back') return 'warning'
  if (state === 'failed' || state === 'needs_attention') return 'error'
  return 'unknown'
}

interface BatchDraft {
  action: string
  targets: string
  strategy: BatchStrategy
  batchSize: number
  concurrency: number
  failurePolicy: FailurePolicy
}

const initialDraft: BatchDraft = {
  action: 'inspect', targets: '', strategy: 'serial', batchSize: 1, concurrency: 1, failurePolicy: 'stop',
}

function makeTask(draft: BatchDraft): BatchTask {
  const targetIds = draft.targets.split(/[\s,]+/).map((item) => item.trim()).filter(Boolean)
  const nodes = targetIds.map((targetId, index) => ({
    id: `node-${index + 1}`,
    action: draft.action,
    targetId,
    state: 'ready' as const,
  }))
  return {
    id: '', action: draft.action.trim(), targetIds, nodes,
    batchPolicy: draft.strategy === 'fixed'
      ? { strategy: 'fixed', batchSize: Math.max(1, draft.batchSize) }
      : { strategy: draft.strategy },
    concurrency: { scope: 'global', maxConcurrent: Math.max(1, draft.concurrency) },
    failurePolicy: draft.failurePolicy,
    failureConfig: { policy: draft.failurePolicy },
    state: 'pending',
  }
}

export function Batches({ batches, loading, available, error, busy, onRefresh, onCreate, onApprove, onRun }: BatchesProps) {
  const [draft, setDraft] = useState<BatchDraft>(initialDraft)
  const [formOpen, setFormOpen] = useState(false)
  const [expanded, setExpanded] = useState<string | null>(null)
  const [formError, setFormError] = useState('')
  const [approval, setApproval] = useState<{ id: string; value: string } | null>(null)

  async function submit(event: FormEvent) {
    event.preventDefault()
    setFormError('')
    const task = makeTask(draft)
    if (!task.action || task.targetIds?.length === 0) {
      setFormError('请填写动作和至少一个目标')
      return
    }
    try {
      await onCreate(task)
      setDraft(initialDraft)
      setFormOpen(false)
    } catch {
      // App 统一展示 API 错误；表单保留输入，便于修正后重试。
    }
  }

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">并行执行与故障策略</span><h1>批量作业</h1></div>
        <div className="page-header-actions">
          <button className="button secondary" type="button" onClick={() => setFormOpen((value) => !value)}><Plus size={15} />新建作业</button>
          <button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新批量作业" disabled={loading}><RefreshCw size={17} /></button>
        </div>
      </header>

      {!available && !loading && <div className="empty-state feature-empty"><CircleHelp size={19} aria-hidden="true" />{error || '批量作业能力尚未启用'}</div>}
      {available && error && <div className="inline-error" role="alert"><ShieldAlert size={16} />{error}</div>}
      {available && formOpen && <form className="batch-form" onSubmit={(event) => void submit(event)}>
        <div className="batch-form-heading"><h2>创建批量作业</h2><span>只提交计划，不直接执行目标</span></div>
        <label><span>动作</span><input required value={draft.action} onChange={(event) => setDraft({ ...draft, action: event.target.value })} /></label>
        <label className="batch-target-field"><span>目标 ID（空格或逗号分隔）</span><input required value={draft.targets} onChange={(event) => setDraft({ ...draft, targets: event.target.value })} /></label>
        <label><span>批次策略</span><select value={draft.strategy} onChange={(event) => setDraft({ ...draft, strategy: event.target.value as BatchStrategy })}>
          <option value="serial">串行</option><option value="fixed">固定批次</option><option value="percentage">按比例</option><option value="canary">金丝雀</option>
        </select></label>
        {draft.strategy === 'fixed' && <label><span>批次大小</span><input type="number" min={1} value={draft.batchSize} onChange={(event) => setDraft({ ...draft, batchSize: Number(event.target.value) })} /></label>}
        <label><span>最大并发</span><input type="number" min={1} value={draft.concurrency} onChange={(event) => setDraft({ ...draft, concurrency: Number(event.target.value) })} /></label>
        <label><span>失败策略</span><select value={draft.failurePolicy} onChange={(event) => setDraft({ ...draft, failurePolicy: event.target.value as FailurePolicy })}>
          <option value="stop">停止</option><option value="continue">继续</option><option value="rollback">回滚</option><option value="pause">暂停</option><option value="needs_attention">人工处理</option>
        </select></label>
        {formError && <div className="inline-error batch-form-error">{formError}</div>}
        <div className="form-actions"><button className="button secondary" type="button" onClick={() => setFormOpen(false)}>取消</button><button className="button danger" type="submit" disabled={busy === 'create'}>{busy === 'create' ? '提交中' : '提交作业'}</button></div>
      </form>}

      {available && loading && batches.length === 0 && <div className="empty-state"><LoaderCircle className="spin" size={18} />读取批量作业</div>}
      {available && !loading && batches.length === 0 && <div className="empty-state">暂无批量作业</div>}
      {available && batches.length > 0 && <section className="page-section batch-section">
        <div className="section-heading"><h2>作业队列</h2><span>{batches.length} 项</span></div>
        <div className="batch-list">
          {batches.map((batch) => {
            const isExpanded = expanded === batch.id
            const canRun = batch.state === 'pending' || batch.state === 'planning' || batch.state === 'paused'
            return <article className="batch-card" key={batch.id}>
              <button className="batch-card-header" type="button" onClick={() => setExpanded(isExpanded ? null : batch.id)} aria-expanded={isExpanded}>
                {isExpanded ? <ChevronDown size={17} /> : <ChevronRight size={17} />}
                <span className="batch-title"><strong>{batch.name || batch.id}</strong><small>{batch.action || batch.capability || '未命名动作'} · {batch.targetIds?.length ?? batch.nodes.length} 个目标</small></span>
                <span className="batch-summary">{batch.summary || batch.error || '等待调度'}</span>
                <time>{formatTime(batch.createdAt)}</time>
                <StatusBadge kind="health" value={stateTone(batch.state)} label={stateLabels[batch.state]} />
              </button>
              {isExpanded && <div className="batch-card-detail">
                <dl className="batch-facts"><div><dt>批次策略</dt><dd>{batch.batchPolicy.strategy}{batch.batchPolicy.batchSize ? ` · ${batch.batchPolicy.batchSize}` : ''}</dd></div><div><dt>并发</dt><dd>{batch.concurrency.scope} · {batch.concurrency.maxConcurrent}</dd></div><div><dt>失败策略</dt><dd>{batch.failurePolicy}</dd></div><div><dt>开始时间</dt><dd>{formatTime(batch.startedAt)}</dd></div></dl>
                <div className="batch-nodes">{batch.nodes.map((node) => <span className={`batch-node batch-node-${node.state}`} key={node.id}><b>{node.targetId || node.id}</b><small>{node.state}{node.error ? ` · ${node.error}` : ''}</small></span>)}</div>
                <div className="form-actions">
                  {batch.operationState === 'pending_approval' && <>
                    <label className="batch-approval-field"><span>确认短语</span><code>{batch.confirmationPhrase || '后端未返回确认短语'}</code><input value={approval?.id === batch.id ? approval.value : ''} onChange={(event) => setApproval({ id: batch.id, value: event.target.value })} placeholder="输入确认短语" /></label>
                    <button className="button danger" type="button" disabled={!batch.digest || approval?.id !== batch.id || approval.value !== batch.confirmationPhrase || busy === batch.id} onClick={() => void onApprove(batch.id, batch.digest ?? '', approval?.value ?? '')}><ShieldAlert size={14} />{busy === batch.id ? '批准中' : '批准作业'}</button>
                  </>}
                  <button className="button secondary" type="button" disabled={!canRun || batch.operationState === 'pending_approval' || busy === batch.id} title={!canRun ? '当前状态不可启动' : '启动下一批'} onClick={() => void onRun(batch.id)}><Play size={14} />{busy === batch.id ? '启动中' : '启动下一批'}</button>
                </div>
              </div>}
            </article>
          })}
        </div>
      </section>}
    </div>
  )
}
