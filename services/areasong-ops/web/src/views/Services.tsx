import {
  ArchiveRestore,
  DatabaseBackup,
  History,
  RefreshCw,
  RotateCcw,
  SearchCheck,
  ShieldCheck,
} from 'lucide-react'
import { shortHash } from '../labels'
import type { ActionDefinition, ReleaseDiscovery, ServiceView, Task } from '../types'
import { StatusBadge } from '../components/StatusBadge'

interface ServicesProps {
  services: ServiceView[]
  selected: string
  discoveries: Record<string, ReleaseDiscovery>
  tasks: Task[]
  busyAction: string
  onSelect: (name: string) => void
  onRefresh: () => void
  onAction: (service: ServiceView, action: ActionDefinition, target?: string) => void
}

const actionIcons = {
  check: SearchCheck,
  backup: DatabaseBackup,
  restart: RotateCcw,
  update: RefreshCw,
  rollback: History,
  'restore-drill': ArchiveRestore,
}

const actionOrder = ['check', 'backup', 'restart', 'update', 'rollback', 'restore-drill']

export function Services({
  services,
  selected,
  discoveries,
  tasks,
  busyAction,
  onSelect,
  onRefresh,
  onAction,
}: ServicesProps) {
  const service = services.find((item) => item.name === selected) ?? services[0]
  if (!service) return <div className="empty-state">没有已声明的服务</div>
  const discovery = discoveries[service.name]
  const discoveredTarget = discovery?.latestTag ?? (discovery?.manifestVersion ? `v${discovery.manifestVersion}` : '')
  const updateAvailable = Boolean(discoveredTarget && discoveredTarget.replace(/^v/, '') !== service.status?.currentVersion)
  const rollbackSource = tasks.find((task) =>
    task.service === service.name && task.action === 'update' && task.state === 'succeeded')

  return (
    <div className="page service-page">
      <header className="page-header">
        <div><span className="eyebrow">受控能力</span><h1>服务运维</h1></div>
        <button className="icon-button bordered" type="button" onClick={onRefresh} title="刷新全部服务状态">
          <RefreshCw size={18} aria-hidden="true" />
        </button>
      </header>
      <div className="service-layout">
        <nav className="service-selector" aria-label="服务列表">
          {services.map((item) => (
            <button
              key={item.name}
              type="button"
              className={item.name === service.name ? 'active' : ''}
              onClick={() => onSelect(item.name)}
            >
              <span className={item.statusError ? 'service-indicator error' : 'service-indicator healthy'} />
              <span><strong>{item.displayName}</strong><small>v{item.status?.currentVersion ?? '—'}</small></span>
            </button>
          ))}
        </nav>
        <div className="service-detail">
          <section className="service-identity">
            <div className="identity-heading">
              <div><h2>{service.displayName}</h2><p>{service.description}</p></div>
              <StatusBadge
                kind="health"
                value={service.statusError ? 'error' : service.activeTaskId ? 'warning' : 'healthy'}
                label={service.statusError ? '检查失败' : service.activeTaskId ? '任务执行中' : '运行正常'}
              />
            </div>
            {service.statusError ? (
              <div className="error-block">{service.statusError}</div>
            ) : (
              <dl className="identity-grid">
                <div><dt>版本</dt><dd>{service.status?.currentVersion ?? '—'}</dd></div>
                <div><dt>应用状态</dt><dd>{service.status?.appState ?? '—'}</dd></div>
                <div><dt>镜像 ID</dt><dd title={service.status?.currentImageId}>{shortHash(service.status?.currentImageId)}</dd></div>
                <div><dt>Git Commit</dt><dd title={service.status?.gitCommit}>{shortHash(service.status?.gitCommit)}</dd></div>
                {service.status?.migrations !== undefined && <div><dt>迁移数量</dt><dd>{service.status.migrations}</dd></div>}
                {service.status?.postgresState && <div><dt>PostgreSQL</dt><dd>{service.status.postgresState}</dd></div>}
                {service.status?.redisState && <div><dt>Redis</dt><dd>{service.status.redisState}</dd></div>}
                <div><dt>运行身份</dt><dd title={service.status?.runtimeIdentityHash}>{shortHash(service.status?.runtimeIdentityHash)}</dd></div>
              </dl>
            )}
          </section>

          {discovery && (
            <section className="release-band">
              <div><ShieldCheck size={19} aria-hidden="true" /><span><strong>最新发布</strong><small>{discovery.latestTag ?? `v${discovery.manifestVersion}`}</small></span></div>
              <span>{updateAvailable ? '有新版本' : '已是最新'}</span>
              {service.name === 'sub2api' && <span>{discovery.prepared ? '迁移门禁已准备' : '等待迁移演练'}</span>}
            </section>
          )}

          <section className="page-section action-section">
            <div className="section-heading"><h2>允许的操作</h2><span>逐项授权</span></div>
            <div className="action-grid">
              {Object.values(service.actions)
                .filter((action) => action.name !== 'inspect')
                .sort((left, right) => actionOrder.indexOf(left.name) - actionOrder.indexOf(right.name))
                .map((action) => {
                  const Icon = actionIcons[action.name as keyof typeof actionIcons] ?? RefreshCw
                  let target = ''
                  let unavailable = !action.enabled || Boolean(service.activeTaskId)
                  let disabledReason = !action.enabled ? '该能力尚未开放' : service.activeTaskId ? '服务已有活动任务' : ''
                  if (action.name === 'update') {
                    target = discoveredTarget
                    if (!target || !updateAvailable) {
                      unavailable = true
                      disabledReason = target ? '当前已是最新版本' : '请先执行检查更新'
                    }
                    if (service.name === 'sub2api' && !discovery?.prepared) {
                      unavailable = true
                      disabledReason = '目标尚未通过迁移演练门禁'
                    }
                  }
                  if (action.name === 'rollback') {
                    target = rollbackSource?.id ?? ''
                    if (!target) {
                      unavailable = true
                      disabledReason = '没有本控制面产生的成功更新记录'
                    }
                  }
                  const isBusy = busyAction === `${service.name}/${action.name}`
                  return (
                    <button
                      key={action.name}
                      type="button"
                      className={`action-item risk-${action.risk}`}
                      disabled={unavailable || isBusy}
                      onClick={() => onAction(service, action, target)}
                      title={unavailable ? disabledReason : action.impact}
                    >
                      <Icon size={20} aria-hidden="true" />
                      <span><strong>{isBusy ? '处理中' : action.displayName}</strong><small>{action.scope}</small></span>
                      <StatusBadge kind="risk" value={action.risk} />
                    </button>
                  )
                })}
            </div>
          </section>
        </div>
      </div>
    </div>
  )
}
