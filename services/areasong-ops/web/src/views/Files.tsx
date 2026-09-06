import { CheckCircle2, FileCode2, FolderOpen, LoaderCircle, RefreshCw, RotateCcw, Save, ShieldCheck } from 'lucide-react'
import { useEffect, useState } from 'react'
import { runAction } from '../action'
import { canCurrentActorApprove, canCurrentActorExecute } from '../approval'
import { formatTime, shortHash } from '../labels'
import type { ManagedFileProposal, ManagedFileView } from '../types'

interface FilesProps {
  file: ManagedFileView | null
  proposals: ManagedFileProposal[]
  loading: boolean
  available: boolean
  error: string
  busy: string
  currentActorHash?: string
  onRefresh: () => void
  onRead: (rootId: string, path: string) => Promise<void>
  onPropose: (body: { rootId: string; path: string; content: string; expectedDigest: string }) => Promise<void>
  onApprove: (proposal: ManagedFileProposal, confirmation: string) => Promise<void>
  onApply: (proposal: ManagedFileProposal) => Promise<void>
  onRollback: (proposal: ManagedFileProposal, confirmation: string) => Promise<void>
}

function proposalLabel(state: string): string {
  switch (state) {
    case 'proposed': return '等待第一批准'
    case 'pending_second_approval': return '等待第二批准'
    case 'approved': return '等待独立应用'
    case 'applying': return '应用中'
    case 'applied': return '已应用'
    case 'rolling_back': return '回滚中'
    case 'rolled_back': return '已回滚'
    case 'needs_attention': return '需要人工核对'
    case 'failed': return '失败'
    default: return state
  }
}

function approvalLabel(proposal: ManagedFileProposal, state: string): string {
  if (proposal.approvalPolicy === 'two_party_v1') {
    if (state === 'proposed') return '等待独立批准'
    if (state === 'approved') return '等待创建人应用'
  }
  return proposalLabel(state)
}

function proposalTone(state: string): 'healthy' | 'warning' | 'error' | 'unknown' {
  if (state === 'applied' || state === 'rolled_back') return 'healthy'
  if (state === 'proposed' || state === 'pending_second_approval' || state === 'approved' || state === 'applying' || state === 'rolling_back') return 'warning'
  if (state === 'failed' || state === 'needs_attention') return 'error'
  return 'unknown'
}

export function Files({
  file, proposals, loading, available, error, busy,
  onRefresh, onRead, onPropose, onApprove, onApply, onRollback, currentActorHash,
}: FilesProps) {
  const [rootId, setRootId] = useState('')
  const [path, setPath] = useState('')
  const [content, setContent] = useState('')
  const [confirmations, setConfirmations] = useState<Record<string, string>>({})

  useEffect(() => {
    if (!file) return
    setRootId(file.rootId)
    setPath(file.path)
    setContent(file.content ?? '')
  }, [file])

  function setConfirmation(id: string, value: string) {
    setConfirmations((current) => ({ ...current, [id]: value }))
  }

  async function read() {
    if (!rootId) return
    try {
      await onRead(rootId, path)
    } catch {
      // The parent owns the visible API error.
    }
  }

  async function propose() {
    if (!file || file.isDirectory || file.readOnly || !file.digest) return
    try {
      await onPropose({ rootId: file.rootId, path: file.path, content, expectedDigest: file.digest })
    } catch {
      // The parent owns the visible API error and retains the editor contents.
    }
  }

  async function approve(proposal: ManagedFileProposal) {
    const confirmation = confirmations[proposal.id] ?? ''
    try {
      await onApprove(proposal, confirmation)
      setConfirmation(proposal.id, '')
    } catch {
      // The parent owns the visible API error.
    }
  }

  async function rollback(proposal: ManagedFileProposal) {
    const confirmation = confirmations[proposal.id] ?? ''
    try {
      await onRollback(proposal, confirmation)
      setConfirmation(proposal.id, '')
    } catch {
      // The parent owns the visible API error.
    }
  }

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">白名单根目录与原子替换</span><h1>受管文件</h1></div>
        <button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新受管文件提案" disabled={loading}>
          {loading ? <LoaderCircle className="spin" size={17} /> : <RefreshCw size={17} />}
        </button>
      </header>

      {!available && !loading && <div className="empty-state feature-empty"><FileCode2 size={19} />{error || '文件管理尚未启用'}</div>}
      {available && error && <div className="inline-error" role="alert">{error}</div>}
      {available && <>
        <section className="page-section no-top-gap">
          <div className="section-heading"><h2>读取白名单路径</h2><span>只读检查先于提案</span></div>
          <div className="config-actions">
            <label className="inline-confirm"><span>Root ID</span><input value={rootId} onChange={(event) => setRootId(event.target.value)} placeholder="配置中的 rootId" /></label>
            <label className="inline-confirm"><span>相对路径</span><input value={path} onChange={(event) => setPath(event.target.value)} placeholder="留空读取根目录" /></label>
            <button className="button secondary" type="button" disabled={!rootId || busy === 'files-read'} onClick={() => runAction(read())}><FolderOpen size={14} />{busy === 'files-read' ? '读取中' : '读取'}</button>
          </div>
        </section>

        {file && <section className="page-section">
          <div className="section-heading"><h2>{file.isDirectory ? '目录内容' : '文件内容'}</h2><span>{file.rootId}/{file.path || '.'}</span></div>
          <dl className="config-facts"><div><dt>摘要</dt><dd><code>{shortHash(file.digest)}</code></dd></div><div><dt>大小</dt><dd>{file.size} bytes</dd></div><div><dt>权限</dt><dd>{file.readOnly ? '只读' : '可提案'}</dd></div><div><dt>类型</dt><dd>{file.isDirectory ? '目录' : '文本文件'}</dd></div></dl>
          {file.isDirectory
            ? <div className="operation-list">{(file.entries ?? []).length === 0 ? <div className="empty-state compact">目录为空</div> : file.entries?.map((entry) => <button className="operation-row" type="button" key={entry.path} onClick={() => { setPath(entry.path); runAction(onRead(file.rootId, entry.path)) }}><span><strong>{entry.name}</strong><small>{entry.isDirectory ? '目录' : '文件'} · {formatTime(entry.modifiedAt)}</small></span><code>{entry.path}</code><span>{entry.size} bytes</span><span>{entry.isDirectory ? '打开' : '读取'}</span></button>)}</div>
            : <><label className="config-editor"><span>当前内容</span><textarea value={content} onChange={(event) => setContent(event.target.value)} readOnly={file.readOnly} spellCheck={false} rows={16} /></label><div className="config-actions"><button className="button danger" type="button" disabled={file.readOnly || !file.digest || !content || busy === 'files-propose'} onClick={() => runAction(propose())}><Save size={14} />{busy === 'files-propose' ? '提案中' : '生成文件提案'}</button></div></>}
        </section>}

        <section className="page-section">
          <div className="section-heading"><h2>文件提案</h2><span>{proposals.length} 项</span></div>
          {proposals.length === 0 ? <div className="empty-state compact">暂无文件提案</div> : <div className="runner-update-list">{proposals.map((proposal) => {
            const state = proposal.state
            const tone = proposalTone(state)
            const confirmation = confirmations[proposal.id] ?? ''
            const approvalPhrase = proposal.confirmationPhrase ?? ''
            const rollbackPhrase = `回滚文件变更 ${proposal.id}`
            const approvalPending = state === 'proposed' || (proposal.approvalPolicy !== 'two_party_v1' && state === 'pending_second_approval')
            const canApprove = approvalPending && canCurrentActorApprove(proposal, currentActorHash)
            const twoParty = proposal.approvalPolicy === 'two_party_v1'
            return <article className={`runner-update-card runner-update-${state}`} key={proposal.id}>
              <header><div className="runner-update-title"><span className={`service-indicator ${tone}`} /><div><strong>{proposal.rootId}/{proposal.path}</strong><small>{approvalLabel(proposal, state)} · {formatTime(proposal.createdAt)}</small></div></div><span className={`credential-health ${tone}`}>{approvalLabel(proposal, state)}</span></header>
              <dl className="runner-update-detail"><div><dt>原摘要</dt><dd><code>{shortHash(proposal.expectedDigest)}</code></dd></div><div><dt>新摘要</dt><dd><code>{shortHash(proposal.proposedDigest)}</code></dd></div><div><dt>第一批准</dt><dd><code>{shortHash(proposal.approvedByHash)}</code></dd></div><div><dt>第二批准</dt><dd><code>{shortHash(proposal.secondApprovedByHash)}</code></dd></div></dl>
              {canApprove && <div className="runner-update-actions attention"><label><span>批准确认</span><code>{approvalPhrase}</code><input value={confirmation} onChange={(event) => setConfirmation(proposal.id, event.target.value)} /></label><button className="button secondary" type="button" disabled={!approvalPhrase || confirmation !== approvalPhrase || busy === `files-approve:${proposal.id}`} onClick={() => runAction(approve(proposal))}><ShieldCheck size={14} />{busy === `files-approve:${proposal.id}` ? '批准中' : twoParty ? '独立批准' : state === 'proposed' ? '第一人批准' : '第二人批准'}</button></div>}
              {approvalPending && !canApprove && <small>等待尚未参与该提案的独立批准账号处理。</small>}
              {state === 'approved' && <div className="runner-update-actions"><span>{twoParty ? '独立批准已完成，由创建人应用。' : '双人批准已完成，执行人需独立于两位批准人。'}</span><button className="button danger" type="button" disabled={busy === `files-apply:${proposal.id}` || !canCurrentActorExecute(proposal, currentActorHash)} onClick={() => runAction(onApply(proposal))}><CheckCircle2 size={14} />{busy === `files-apply:${proposal.id}` ? '应用中' : twoParty ? '创建人应用' : '独立应用'}</button></div>}
              {state === 'applied' && <div className="runner-update-actions attention"><label><span>回滚确认</span><code>{rollbackPhrase}</code><input value={confirmation} onChange={(event) => setConfirmation(proposal.id, event.target.value)} /></label><button className="button danger" type="button" disabled={confirmation !== rollbackPhrase || busy === `files-rollback:${proposal.id}`} onClick={() => runAction(rollback(proposal))}><RotateCcw size={14} />{busy === `files-rollback:${proposal.id}` ? '回滚中' : '回滚'}</button></div>}
              {proposal.error && <div className="runner-update-error">{proposal.error}</div>}
            </article>
          })}</div>}
        </section>
      </>}
    </div>
  )
}
