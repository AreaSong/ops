import { AlertTriangle, CheckCircle2, CircleHelp, LoaderCircle, RefreshCw, RotateCcw, Save, Wrench } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'
import { runAction } from '../action'
import { formatTime } from '../labels'
import type { ActionDefinition, DesiredState, ServiceState, ServiceView } from '../types'
import { StatusBadge } from '../components/StatusBadge'

interface LifecycleProps {
  states: ServiceState[]
  services: ServiceView[]
  loading: boolean
  available: boolean
  error: string
  busy: string
  onRefresh: () => void
  onAction: (service: ServiceView, action: ActionDefinition) => Promise<void>
  onReconcile: (service: string) => Promise<void>
}

const desiredLabels: Record<DesiredState, string> = {
  running: '运行',
  stopped: '停止',
  maintenance: '维护',
  drained: '排空',
}

const actualLabels: Record<ServiceState['actual'], string> = {
  unknown: '未知',
  running: '运行',
  stopped: '停止',
  maintenance: '维护',
  drained: '排空',
}

const healthLabels: Record<ServiceState['health'], string> = {
  unknown: '未知',
  healthy: '健康',
  degraded: '降级',
  unhealthy: '不健康',
}

function healthBadge(health: ServiceState['health']): 'healthy' | 'warning' | 'error' | 'unknown' {
  if (health === 'healthy') return 'healthy'
  if (health === 'degraded') return 'warning'
  if (health === 'unhealthy') return 'error'
  return 'unknown'
}

function stateTone(state: ServiceState): 'healthy' | 'warning' | 'error' | 'unknown' {
  if (state.drift?.detected) return 'warning'
  if (state.health === 'unhealthy') return 'error'
  if (state.health === 'healthy' && state.actual === state.desired) return 'healthy'
  if (state.actual === 'unknown' || state.health === 'unknown') return 'unknown'
  return 'warning'
}

export function Lifecycle({
  states, services, loading, available, error, busy, onRefresh, onAction, onReconcile,
}: LifecycleProps) {
  const [drafts, setDrafts] = useState<Record<string, DesiredState>>({})

  useEffect(() => {
    setDrafts((current) => {
      const next = { ...current }
      for (const state of states) next[state.service] = next[state.service] ?? state.desired
      return next
    })
  }, [states])

  const serviceNames = useMemo<ServiceState[]>(() => {
    const known = new Set(states.map((state) => state.service))
    return [
      ...states,
      ...services.filter((service) => !known.has(service.name)).map((service) => ({
        service: service.name,
        objectId: service.objectId,
        desired: 'running' as DesiredState,
        actual: 'unknown' as const,
        health: 'unknown' as const,
        reason: '尚未收到生命周期状态',
      })),
    ]
  }, [services, states])

  const driftCount = states.filter((state) => state.drift?.detected).length
  const healthyCount = states.filter((state) => state.health === 'healthy').length
  const unknownCount = states.filter((state) => state.health === 'unknown').length

  return (
    <div className="page">
      <header className="page-header">
        <div><span className="eyebrow">目标与观测状态</span><h1>生命周期状态</h1></div>
        <button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新生命周期状态" disabled={loading}>
          {loading ? <LoaderCircle className="spin" size={17} /> : <RefreshCw size={17} />}
        </button>
      </header>

      <section className="metric-strip operation-metrics lifecycle-metrics" aria-label="生命周期概览">
        <div><CheckCircle2 size={18} aria-hidden="true" /><span><b>{healthyCount}</b>健康</span></div>
        <div className={driftCount > 0 ? 'metric-alert' : ''}><Wrench size={18} aria-hidden="true" /><span><b>{driftCount}</b>状态漂移</span></div>
        <div><CircleHelp size={18} aria-hidden="true" /><span><b>{unknownCount}</b>未知观测</span></div>
        <div><span><b>{serviceNames.length}</b>受控服务</span></div>
      </section>

      {!available && !loading && (
        <div className="empty-state feature-empty">
          <CircleHelp size={19} aria-hidden="true" /><span>{error || '生命周期状态能力尚未启用'}</span>
        </div>
      )}
      {available && error && <div className="inline-error" role="alert"><AlertTriangle size={16} aria-hidden="true" />{error}</div>}
      {available && loading && states.length === 0 && <div className="empty-state"><LoaderCircle className="spin" size={18} />读取生命周期状态</div>}
      {available && !loading && serviceNames.length === 0 && <div className="empty-state">暂无可展示的生命周期对象</div>}

      {available && serviceNames.length > 0 && (
        <section className="page-section lifecycle-section">
          <div className="section-heading"><h2>服务状态</h2><span>{serviceNames.length} 项</span></div>
          <div className="lifecycle-table" role="table" aria-label="服务生命周期状态">
            <div className="lifecycle-head" role="row">
              <span>服务</span><span>目标</span><span>实际</span><span>健康</span><span>观测</span><span>操作</span>
            </div>
            {serviceNames.map((state) => {
              const service = services.find((item) => item.name === state.service)
              const draft = drafts[state.service] ?? state.desired
              const changed = draft !== state.desired
              const actionNames: Record<DesiredState, string> = {
                running: state.actual === 'stopped' ? 'start' : 'resume-traffic',
                stopped: 'stop',
                maintenance: 'enter-maintenance',
                drained: 'drain',
              }
              const lifecycleActionName = actionNames[draft]
              const lifecycleAction = service?.actions[lifecycleActionName]
              const actionBusy = lifecycleAction !== undefined && busy === `${state.service}/${lifecycleAction.name}`
              const reconcileBusy = busy === state.service
              const availableDesiredStates = (Object.keys(desiredLabels) as DesiredState[]).filter((desired) =>
                service?.actions[actionNames[desired]] !== undefined)
              return (
                <article className="lifecycle-row" role="row" key={state.service}>
                  <div className="lifecycle-service">
                    <span className={`service-indicator ${stateTone(state)}`} aria-hidden="true" />
                    <span><strong>{service?.displayName ?? state.service}</strong><small>{service?.description ?? state.service}</small></span>
                  </div>
                  <div className="lifecycle-desired">
                    <select aria-label={`${state.service} 目标状态`} value={draft}
                      onChange={(event) => setDrafts((current) => ({ ...current, [state.service]: event.target.value as DesiredState }))}>
                      {(availableDesiredStates.length > 0 ? availableDesiredStates : [draft]).map((value) =>
                        <option value={value} key={value}>{desiredLabels[value]}</option>)}
                    </select>
                  </div>
                  <span className="lifecycle-value">{actualLabels[state.actual]}</span>
                  <StatusBadge kind="health" value={healthBadge(state.health)} label={healthLabels[state.health]} />
                  <div className="lifecycle-observed">
                    <span>{formatTime(state.observedAt)}</span>
                    {state.drift?.detected && <small title={state.drift.reason}>漂移：{state.drift.expected} ≠ {state.drift.observed}</small>}
                    {!state.drift?.detected && state.reason && <small>{state.reason}</small>}
                  </div>
                  <div className="lifecycle-actions">
                    <button className="icon-button bordered" type="button" title="重新核对状态" disabled={reconcileBusy || actionBusy}
                      onClick={() => runAction(onReconcile(state.service))}>
                      {reconcileBusy ? <LoaderCircle className="spin" size={15} /> : <RotateCcw size={15} />}
                    </button>
                    <button className="button secondary lifecycle-save" type="button"
                      disabled={!changed || reconcileBusy || actionBusy || !service || !lifecycleAction?.enabled}
                      title={!lifecycleAction ? '该目标状态没有对应的受控动作' : lifecycleAction.impact}
                      onClick={() => { if (service && lifecycleAction) runAction(onAction(service, lifecycleAction)) }}>
                      {actionBusy ? <LoaderCircle className="spin" size={14} /> : <Save size={14} aria-hidden="true" />}创建计划
                    </button>
                  </div>
                </article>
              )
            })}
          </div>
        </section>
      )}
    </div>
  )
}
