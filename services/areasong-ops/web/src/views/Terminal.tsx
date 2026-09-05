import { AlertTriangle, CheckCircle2, CircleHelp, LoaderCircle, Play, RefreshCw, ShieldCheck, TerminalSquare } from 'lucide-react'
import { useState, type FormEvent } from 'react'
import { runAction } from '../action'
import { formatTime, shortHash } from '../labels'
import type { TerminalCommand, TerminalOutput, TerminalShellPlan } from '../types'

interface TerminalProps {
  commands: TerminalCommand[]
  plans: TerminalShellPlan[]
  lastOutput?: TerminalOutput | null
  loading: boolean
  available: boolean
  breakGlassAvailable: boolean
  error: string
  busy: string
  onRefresh: () => void
  onCreatePlan: (body: { objectId: string; input: string; confirmation: string }) => Promise<void>
  onApprove: (plan: TerminalShellPlan, confirmation: string) => Promise<void>
  onExecute: (plan: TerminalShellPlan, input: string) => Promise<void>
  onRun?: (body: { objectId: string; command: string; confirmation?: string }) => Promise<void>
}

const stateLabel: Record<string, string> = {
  pending_approval: '等待第一批准',
  pending_second_approval: '等待第二批准',
  approved: '等待执行',
  running: '执行中',
  succeeded: '成功',
  failed: '失败',
  timed_out: '执行超时',
  expired: '已过期',
  needs_attention: '需要人工核对',
}

function stateTone(state: string): 'healthy' | 'warning' | 'error' | 'unknown' {
  if (state === 'succeeded') return 'healthy'
  if (state === 'pending_approval' || state === 'pending_second_approval' || state === 'approved' || state === 'running') return 'warning'
  if (state === 'failed' || state === 'timed_out' || state === 'needs_attention') return 'error'
  return 'unknown'
}

export function Terminal({
  commands, plans, lastOutput, loading, available, error, busy,
  breakGlassAvailable, onRefresh, onCreatePlan, onApprove, onExecute, onRun,
}: TerminalProps) {
  const [objectId, setObjectId] = useState('')
  const [shellInput, setShellInput] = useState('')
  const [requestConfirmation, setRequestConfirmation] = useState('')
  const [planConfirmations, setPlanConfirmations] = useState<Record<string, string>>({})
  const [executionInputs, setExecutionInputs] = useState<Record<string, string>>({})
  const [command, setCommand] = useState('')
  const [commandConfirmation, setCommandConfirmation] = useState('')

  const requestPhrase = objectId ? `申请紧急终端 ${objectId}` : ''

  function setPlanConfirmation(id: string, value: string) {
    setPlanConfirmations((current) => ({ ...current, [id]: value }))
  }

  function setExecutionInput(id: string, value: string) {
    setExecutionInputs((current) => ({ ...current, [id]: value }))
  }

  async function createPlan(event: FormEvent) {
    event.preventDefault()
    if (!objectId || !shellInput || requestConfirmation !== requestPhrase) return
    try {
      await onCreatePlan({ objectId, input: shellInput, confirmation: requestConfirmation })
      setShellInput('')
      setRequestConfirmation('')
    } catch {
      // The parent owns the visible API error and keeps the object id for retry.
    }
  }

  async function approve(plan: TerminalShellPlan) {
    const confirmation = planConfirmations[plan.id] ?? ''
    try {
      await onApprove(plan, confirmation)
      setPlanConfirmation(plan.id, '')
    } catch {
      // The parent owns the visible API error.
    }
  }

  async function execute(plan: TerminalShellPlan) {
    const input = executionInputs[plan.id] ?? ''
    if (!input) return
    try {
      await onExecute(plan, input)
    } catch {
      // The parent owns the visible API error.
    }
  }

  async function runCommand(event: FormEvent) {
    event.preventDefault()
    if (!onRun || !objectId || !command) return
    try {
      await onRun({ objectId, command, confirmation: commandConfirmation || undefined })
      setCommandConfirmation('')
    } catch {
      // The parent owns the visible API error.
    }
  }

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">Break-glass 受控执行</span><h1>紧急终端</h1></div>
        <button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新终端计划" disabled={loading}>
          {loading ? <LoaderCircle className="spin" size={17} /> : <RefreshCw size={17} />}
        </button>
      </header>

      {!available && !loading && <div className="empty-state feature-empty"><TerminalSquare size={19} />{error || '紧急终端尚未启用'}</div>}
      {available && error && <div className="inline-error" role="alert">{error}</div>}
      {available && loading && plans.length === 0 && <div className="empty-state"><LoaderCircle className="spin" size={18} />读取终端计划</div>}

      {available && <>
        <section className="page-section no-top-gap">
          <div className="section-heading"><h2>允许的命令</h2><span>{commands.length} 项</span></div>
          {commands.length === 0 ? <div className="empty-state compact"><CircleHelp size={16} />暂无命令目录</div> : <div className="operation-list">{commands.map((item) => <button className="operation-row" type="button" key={item.name} onClick={() => setShellInput((current) => current || `${item.name}${item.arguments?.length ? ` ${item.arguments.join(' ')}` : ''}`)} title="将命令模板放入 Shell 计划">
            <span><strong>{item.name}</strong><small>{item.executable} · {item.readOnly ? '只读' : '受控写操作'}</small></span><code>{item.arguments?.join(' ') || '无参数'}</code><span>{item.timeoutSeconds}s</span><span>{item.readOnly ? '只读' : '高风险'}</span>
          </button>)}</div>}
        </section>

        {breakGlassAvailable && <section className="page-section">
          <div className="section-heading"><h2>创建 Shell 计划</h2><span>两名独立批准人确认后才能执行</span></div>
          <form className="runner-update-form" onSubmit={(event) => runAction(createPlan(event))}>
            <label><span>对象 ID</span><input required value={objectId} onChange={(event) => setObjectId(event.target.value)} placeholder="受控对象 objectId" /></label>
            <label className="wide"><span>Shell 输入</span><textarea required rows={5} value={shellInput} onChange={(event) => setShellInput(event.target.value)} spellCheck={false} placeholder="仅填写必要的受控命令" /></label>
            <label className="wide runner-prepare-confirm"><span>申请确认</span><code>{requestPhrase || '先填写对象 ID'}</code><input required value={requestConfirmation} onChange={(event) => setRequestConfirmation(event.target.value)} /></label>
            <div className="form-actions"><button className="button danger" type="submit" disabled={!objectId || !shellInput || requestConfirmation !== requestPhrase || busy === 'terminal-create'}><ShieldCheck size={14} />{busy === 'terminal-create' ? '提交中' : '创建待批准计划'}</button></div>
          </form>
        </section>}

        {!breakGlassAvailable && <div className="recovery-warning"><ShieldCheck size={15} />Break-glass Shell 当前关闭；只读命令仍可按权限执行。</div>}

        {onRun && <section className="page-section">
          <div className="section-heading"><h2>受控命令会话</h2><span>只使用命令目录中的固定能力</span></div>
          <form className="runner-update-form" onSubmit={(event) => runAction(runCommand(event))}>
            <label><span>对象 ID</span><input required value={objectId} onChange={(event) => setObjectId(event.target.value)} /></label>
            <label className="wide"><span>命令</span><select required value={command} onChange={(event) => setCommand(event.target.value)}><option value="">选择允许的命令</option>{commands.map((item) => <option key={item.name} value={item.name}>{item.name}</option>)}</select></label>
            <label className="wide"><span>直接会话确认（若后端要求）</span><input value={commandConfirmation} onChange={(event) => setCommandConfirmation(event.target.value)} /></label>
            <div className="form-actions"><button className="button secondary" type="submit" disabled={!objectId || !command || busy === 'terminal-run'}><Play size={14} />{busy === 'terminal-run' ? '执行中' : '运行受控命令'}</button></div>
          </form>
          {lastOutput && <pre className="terminal-output"><code>{lastOutput.output || '(无输出)'}</code></pre>}
        </section>}

        {breakGlassAvailable && <section className="page-section">
          <div className="section-heading"><h2>Shell 计划</h2><span>{plans.length} 项</span></div>
          {plans.length === 0 ? <div className="empty-state compact">暂无终端计划</div> : <div className="runner-update-list">{plans.map((plan) => {
            const confirmation = planConfirmations[plan.id] ?? ''
            const input = executionInputs[plan.id] ?? ''
            const phrase = plan.confirmationPhrase ?? ''
            const approval = plan.state === 'pending_approval' || plan.state === 'pending_second_approval'
            const approvalLabel = plan.state === 'pending_second_approval' ? '第二批准' : '第一批准'
            const executable = plan.state === 'approved'
            return <article className={`runner-update-card runner-update-${stateTone(plan.state)}`} key={plan.id}>
              <header><div className="runner-update-title"><span className={`service-indicator ${stateTone(plan.state)}`} /><div><strong>{plan.objectId}</strong><small>{stateLabel[plan.state] || plan.state} · {formatTime(plan.createdAt)}</small></div></div><span className={`credential-health ${stateTone(plan.state)}`}>{stateLabel[plan.state] || plan.state}</span></header>
              <dl className="runner-update-detail"><div><dt>输入摘要</dt><dd><code>{shortHash(plan.inputDigest)}</code></dd></div><div><dt>计划 ID</dt><dd><code>{shortHash(plan.id)}</code></dd></div><div><dt>第一批准人</dt><dd><code>{shortHash(plan.approvedByHash)}</code></dd></div><div><dt>第二批准人</dt><dd><code>{shortHash(plan.secondApprovedByHash)}</code></dd></div><div><dt>结束时间</dt><dd>{formatTime(plan.finishedAt)}</dd></div></dl>
              {approval && <div className="runner-update-actions attention"><label><span>{approvalLabel}确认</span><code>{phrase}</code><input value={confirmation} onChange={(event) => setPlanConfirmation(plan.id, event.target.value)} /></label><button className="button danger" type="button" disabled={!phrase || confirmation !== phrase || busy === `terminal-approve:${plan.id}`} onClick={() => runAction(approve(plan))}><ShieldCheck size={14} />{busy === `terminal-approve:${plan.id}` ? '批准中' : approvalLabel}</button></div>}
              {executable && <div className="runner-update-actions attention"><label><span>执行时再次提供原始输入</span><textarea rows={3} value={input} onChange={(event) => setExecutionInput(plan.id, event.target.value)} spellCheck={false} /></label><button className="button danger" type="button" disabled={!input || busy === `terminal-execute:${plan.id}`} onClick={() => runAction(execute(plan))}><Play size={14} />{busy === `terminal-execute:${plan.id}` ? '执行中' : '独立执行'}</button></div>}
              {plan.state === 'running' && <div className="runner-update-progress"><LoaderCircle className="spin" size={16} />终端命令执行中</div>}
              {plan.output && <pre className="terminal-output"><code>{plan.output}</code></pre>}
              {plan.error && <div className="runner-update-error"><AlertTriangle size={15} />{plan.error}</div>}
              {plan.state === 'succeeded' && <div className="runner-update-progress"><CheckCircle2 size={15} />命令已完成</div>}
            </article>
          })}</div>}
        </section>}
      </>}
    </div>
  )
}
