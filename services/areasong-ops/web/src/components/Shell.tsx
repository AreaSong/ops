import {
  Activity,
  BellRing,
  ChartNoAxesCombined,
  ClipboardList,
  History,
  LayoutDashboard,
  ServerCog,
  Wifi,
  WifiOff,
} from 'lucide-react'
import type { ReactNode } from 'react'
import type { NavigationLinks } from '../types'

export type ViewName = 'overview' | 'services' | 'tasks' | 'audit'

interface ShellProps {
  view: ViewName
  onView: (view: ViewName) => void
  email: string
  connected: boolean
  links: NavigationLinks
  children: ReactNode
}
const navigation = [
  { id: 'overview' as const, label: '操作总览', icon: LayoutDashboard },
  { id: 'services' as const, label: '服务操作', icon: ServerCog },
  { id: 'tasks' as const, label: '执行记录', icon: ClipboardList },
  { id: 'audit' as const, label: '变更审计', icon: History },
]

export function Shell({ view, onView, email, connected, links, children }: ShellProps) {
  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark"><Activity size={19} aria-hidden="true" /></span>
          <span>
            <strong>AreaSong Ops</strong>
            <small>LosAngeles</small>
          </span>
        </div>
        <nav className="navigation" aria-label="主导航">
          {navigation.map(({ id, label, icon: Icon }) => (
            <button
              key={id}
              type="button"
              className={view === id ? 'nav-item active' : 'nav-item'}
              onClick={() => onView(id)}
              aria-current={view === id ? 'page' : undefined}
            >
              <Icon size={18} aria-hidden="true" />
              <span>{label}</span>
            </button>
          ))}
        </nav>
        <div className="sidebar-status">
          {connected ? <Wifi size={16} aria-hidden="true" /> : <WifiOff size={16} aria-hidden="true" />}
          <span>{connected ? '实时连接正常' : '实时连接重试中'}</span>
        </div>
      </aside>
      <div className="workspace">
        <header className="topbar">
          <div>
            <span className="environment-dot" aria-hidden="true" />
            <span className="environment-name">生产环境</span>
          </div>
          <div className="topbar-right">
            <nav className="external-navigation" aria-label="观测入口">
              {links.grafana && (
                <a href={links.grafana} target="_blank" rel="noreferrer" title="在 Grafana 查看运行观测">
                  <ChartNoAxesCombined size={16} aria-hidden="true" /><span>运行观测</span>
                </a>
              )}
              {links.alerts && (
                <a href={links.alerts} target="_blank" rel="noreferrer" title="在 Grafana Alertmanager 查看活动告警">
                  <BellRing size={16} aria-hidden="true" /><span>活动告警</span>
                </a>
              )}
            </nav>
            <div className="user-identity" title={email}>{email}</div>
          </div>
        </header>
        <main className="content">{children}</main>
      </div>
    </div>
  )
}
