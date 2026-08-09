import {
  Activity,
  ClipboardList,
  History,
  LayoutDashboard,
  ServerCog,
  Wifi,
  WifiOff,
} from 'lucide-react'
import type { ReactNode } from 'react'

export type ViewName = 'overview' | 'services' | 'tasks' | 'audit'

interface ShellProps {
  view: ViewName
  onView: (view: ViewName) => void
  email: string
  connected: boolean
  children: ReactNode
}
const navigation = [
  { id: 'overview' as const, label: '总览', icon: LayoutDashboard },
  { id: 'services' as const, label: '服务', icon: ServerCog },
  { id: 'tasks' as const, label: '任务', icon: ClipboardList },
  { id: 'audit' as const, label: '审计', icon: History },
]

export function Shell({ view, onView, email, connected, children }: ShellProps) {
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
          <div className="user-identity" title={email}>{email}</div>
        </header>
        <main className="content">{children}</main>
      </div>
    </div>
  )
}
