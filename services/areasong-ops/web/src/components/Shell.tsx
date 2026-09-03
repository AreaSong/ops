import {
  Activity,
  BellRing,
  ChartNoAxesCombined,
  ClipboardList,
  Boxes,
  FileArchive,
  History,
  LayoutDashboard,
  ServerCog,
  KeyRound,
  Network,
  PackageCheck,
  TerminalSquare,
  FileCode2,
  CalendarClock,
  Settings2,
  Shield,
  TimerReset,
  Workflow,
  Wifi,
  WifiOff,
} from 'lucide-react'
import { useEffect, useRef, type ReactNode } from 'react'
import type { NavigationLinks } from '../types'

export type ViewName = 'overview' | 'lifecycle' | 'services' | 'fleet' | 'batches' | 'recovery' | 'configuration' | 'auto-updates' | 'terminal' | 'files' | 'runner-update' | 'runner-fleet-update' | 'access' | 'automatic-tasks' | 'credentials' | 'tasks' | 'audit'

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
  { id: 'lifecycle' as const, label: '生命周期', icon: Workflow },
  { id: 'services' as const, label: '服务操作', icon: ServerCog },
  { id: 'fleet' as const, label: '多服务器', icon: Network },
  { id: 'batches' as const, label: '批量作业', icon: Boxes },
  { id: 'recovery' as const, label: '恢复中心', icon: FileArchive },
  { id: 'configuration' as const, label: '配置中心', icon: Settings2 },
  { id: 'auto-updates' as const, label: '自动更新', icon: CalendarClock },
  { id: 'terminal' as const, label: '受控终端', icon: TerminalSquare },
  { id: 'files' as const, label: '受管文件', icon: FileCode2 },
  { id: 'runner-update' as const, label: 'Runner 更新', icon: PackageCheck },
  { id: 'runner-fleet-update' as const, label: 'Runner Fleet 更新', icon: PackageCheck },
  { id: 'access' as const, label: '访问控制', icon: Shield },
  { id: 'automatic-tasks' as const, label: '自动任务', icon: TimerReset },
  { id: 'credentials' as const, label: '凭据轮换', icon: KeyRound },
  { id: 'tasks' as const, label: '执行记录', icon: ClipboardList },
  { id: 'audit' as const, label: '变更审计', icon: History },
]

export function Shell({ view, onView, email, connected, links, children }: ShellProps) {
  const activeNavigation = useRef<HTMLButtonElement | null>(null)

  useEffect(() => {
    activeNavigation.current?.scrollIntoView({ block: 'nearest', inline: 'center' })
  }, [view])

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
              ref={view === id ? activeNavigation : undefined}
              type="button"
              className={view === id ? 'nav-item active' : 'nav-item'}
              onClick={() => onView(id)}
              title={label}
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
