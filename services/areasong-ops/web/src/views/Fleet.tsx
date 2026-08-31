import { Cpu, HardDrive, LoaderCircle, Plus, RefreshCw, Server, Signal, WifiOff } from 'lucide-react'
import { useMemo, useState, type FormEvent } from 'react'
import { formatTime } from '../labels'
import type { Fleet, FleetNodeState, RunnerNode, ServerNode } from '../types'
import { StatusBadge } from '../components/StatusBadge'

interface FleetProps {
  fleet: Fleet | null
  loading: boolean
  available: boolean
  error: string
  busy: boolean
  onRefresh: () => void
  onRegisterServer: (node: ServerNode) => Promise<void>
  onRegisterRunner: (node: RunnerNode) => Promise<void>
}

const nodeStateLabel: Record<FleetNodeState, string> = {
  unknown: '未知', online: '在线', offline: '离线', draining: '排空中', disabled: '已禁用',
}

function nodeHealth(state: FleetNodeState): 'healthy' | 'warning' | 'error' | 'unknown' {
  if (state === 'online') return 'healthy'
  if (state === 'draining' || state === 'unknown') return 'warning'
  if (state === 'offline' || state === 'disabled') return 'error'
  return 'unknown'
}

function labelsText(labels?: Record<string, string>): string {
  if (!labels || Object.keys(labels).length === 0) return '无标签'
  return Object.entries(labels).map(([key, value]) => `${key}=${value}`).join(' · ')
}

export function FleetView({
  fleet, loading, available, error, busy, onRefresh, onRegisterServer, onRegisterRunner,
}: FleetProps) {
  const [serverOpen, setServerOpen] = useState(false)
  const [runnerOpen, setRunnerOpen] = useState(false)
  const [server, setServer] = useState({ id: '', hostname: '', environment: 'production', region: '', address: '' })
  const [runner, setRunner] = useState({ id: '', serverId: '', hostname: '', version: '', state: 'unknown' as FleetNodeState })
  const servers = useMemo(() => fleet?.servers ?? [], [fleet?.servers])
  const runners = useMemo(() => fleet?.runners ?? [], [fleet?.runners])
  const onlineServers = servers.filter((node) => node.state === 'online').length
  const onlineRunners = runners.filter((node) => node.state === 'online').length
  const groupedRunners = useMemo(() => {
    const result = new Map<string, RunnerNode[]>()
    for (const runnerNode of runners) result.set(runnerNode.serverId, [...(result.get(runnerNode.serverId) ?? []), runnerNode])
    return result
  }, [runners])

  async function submitServer(event: FormEvent) {
    event.preventDefault()
    if (!server.id || !server.hostname) return
    await onRegisterServer({ ...server, state: 'unknown', region: server.region || undefined, address: server.address || undefined })
    setServer({ id: '', hostname: '', environment: 'production', region: '', address: '' })
    setServerOpen(false)
  }

  async function submitRunner(event: FormEvent) {
    event.preventDefault()
    if (!runner.id || !runner.serverId || !runner.version) return
    await onRegisterRunner({ ...runner, hostname: runner.hostname || undefined })
    setRunner({ id: '', serverId: '', hostname: '', version: '', state: 'unknown' })
    setRunnerOpen(false)
  }

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">节点与执行器</span><h1>Fleet 多服务器</h1></div>
        <button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新 Fleet" disabled={loading}>
          {loading ? <LoaderCircle className="spin" size={17} /> : <RefreshCw size={17} />}
        </button>
      </header>

      {!available && !loading && <div className="empty-state feature-empty"><WifiOff size={19} aria-hidden="true" />{error || '多服务器能力尚未启用'}</div>}
      {available && loading && !fleet && <div className="empty-state"><LoaderCircle className="spin" size={18} />读取 Fleet</div>}
      {available && error && <div className="inline-error" role="alert">{error}</div>}
      {available && fleet && (
        <>
          <section className="metric-strip fleet-metrics" aria-label="Fleet 概览">
            <div><Server size={18} aria-hidden="true" /><span><b>{servers.length}</b>服务器</span></div>
            <div><Signal size={18} aria-hidden="true" /><span><b>{onlineServers}</b>服务器在线</span></div>
            <div><Cpu size={18} aria-hidden="true" /><span><b>{runners.length}</b>Runner</span></div>
            <div><HardDrive size={18} aria-hidden="true" /><span><b>{onlineRunners}</b>Runner 在线</span></div>
          </section>

          <section className="page-section fleet-section">
            <div className="section-heading"><h2>服务器清单</h2>{fleet.canManage && <div className="section-actions">
              <button className="button secondary" type="button" onClick={() => setServerOpen((value) => !value)}><Plus size={14} />登记服务器</button>
              <button className="button secondary" type="button" onClick={() => setRunnerOpen((value) => !value)}><Plus size={14} />登记 Runner</button>
            </div>}</div>

            {fleet.canManage && serverOpen && <form className="inline-form fleet-form" onSubmit={(event) => void submitServer(event)}>
              <label><span>服务器 ID</span><input required pattern="[A-Za-z][A-Za-z0-9_.:-]{0,127}" value={server.id} onChange={(event) => setServer({ ...server, id: event.target.value })} /></label>
              <label><span>主机名</span><input required value={server.hostname} onChange={(event) => setServer({ ...server, hostname: event.target.value })} /></label>
              <label><span>环境</span><input required value={server.environment} onChange={(event) => setServer({ ...server, environment: event.target.value })} /></label>
              <label><span>地域</span><input value={server.region} onChange={(event) => setServer({ ...server, region: event.target.value })} /></label>
              <label><span>地址</span><input value={server.address} onChange={(event) => setServer({ ...server, address: event.target.value })} /></label>
              <button className="button secondary" type="submit" disabled={busy}>保存服务器</button>
            </form>}

            {fleet.canManage && runnerOpen && <form className="inline-form fleet-form" onSubmit={(event) => void submitRunner(event)}>
              <label><span>Runner ID</span><input required pattern="[A-Za-z][A-Za-z0-9_.:-]{0,127}" value={runner.id} onChange={(event) => setRunner({ ...runner, id: event.target.value })} /></label>
              <label><span>服务器 ID</span><input required value={runner.serverId} onChange={(event) => setRunner({ ...runner, serverId: event.target.value })} /></label>
              <label><span>版本</span><input required value={runner.version} onChange={(event) => setRunner({ ...runner, version: event.target.value })} /></label>
              <label><span>主机名</span><input value={runner.hostname} onChange={(event) => setRunner({ ...runner, hostname: event.target.value })} /></label>
              <button className="button secondary" type="submit" disabled={busy}>保存 Runner</button>
            </form>}

            {servers.length === 0 && <div className="empty-state compact">暂无服务器登记</div>}
            <div className="fleet-table" role="table" aria-label="服务器列表">
              {servers.map((node) => {
                const serverRunners = groupedRunners.get(node.id) ?? []
                return <article className="fleet-server-row" role="row" key={node.id}>
                  <div className="fleet-node-main"><span className={`service-indicator ${nodeHealth(node.state)}`} aria-hidden="true" /><div><strong>{node.hostname}</strong><small><code>{node.id}</code> · {node.environment}{node.region ? ` · ${node.region}` : ''}</small></div></div>
                  <div className="fleet-node-facts"><span>{node.address || '地址未登记'}</span><small>{labelsText(node.labels)}</small></div>
                  <div className="fleet-node-runners"><span>{serverRunners.length} Runner</span><small>{node.runnerId || '未绑定'}</small></div>
                  <div className="fleet-node-time">{formatTime(node.lastHeartbeat)}</div>
                  <StatusBadge kind="health" value={nodeHealth(node.state)} label={nodeStateLabel[node.state]} />
                </article>
              })}
            </div>
          </section>

          <section className="page-section">
            <div className="section-heading"><h2>Runner 状态</h2><span>{runners.length} 项</span></div>
            {runners.length === 0 ? <div className="empty-state compact">暂无 Runner 心跳</div> : <div className="runner-table" role="table" aria-label="Runner 列表">
              {runners.map((node) => <article className="runner-row" role="row" key={node.id}>
                <div><strong>{node.id}</strong><small>{node.hostname || '主机名未上报'} · 绑定 {node.serverId}</small></div>
                <span><code>{node.version}</code></span>
                <span>{labelsText(node.labels)}</span>
                <span>{formatTime(node.lastHeartbeat)}</span>
                <StatusBadge kind="health" value={nodeHealth(node.state)} label={nodeStateLabel[node.state]} />
              </article>)}
            </div>}
          </section>
        </>
      )}
    </div>
  )
}
