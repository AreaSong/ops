import { AlertCircle, LoaderCircle, X } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { OpsAPI } from './api'
import { ConfirmationDialog } from './components/ConfirmationDialog'
import { Shell, type ViewName } from './components/Shell'
import { TaskDrawer } from './components/TaskDrawer'
import type {
  ActionDefinition,
  AuditEntry,
  OpsEvent,
  Preview,
  ReleaseDiscovery,
  ServiceView,
  Task,
} from './types'
import { Audit } from './views/Audit'
import { Overview } from './views/Overview'
import { Services } from './views/Services'
import { Tasks } from './views/Tasks'

export default function App() {
  const api = useRef(new OpsAPI()).current
  const [email, setEmail] = useState('')
  const [view, setView] = useState<ViewName>('overview')
  const [services, setServices] = useState<ServiceView[]>([])
  const [tasks, setTasks] = useState<Task[]>([])
  const tasksRef = useRef<Task[]>([])
  const [audit, setAudit] = useState<AuditEntry[]>([])
  const [discoveries, setDiscoveries] = useState<Record<string, ReleaseDiscovery>>({})
  const [selectedService, setSelectedService] = useState('areaforge')
  const [selectedTask, setSelectedTask] = useState<Task | null>(null)
  const [taskEvents, setTaskEvents] = useState<OpsEvent[]>([])
  const [taskEventsLoading, setTaskEventsLoading] = useState(false)
  const [preview, setPreview] = useState<Preview | null>(null)
  const [pending, setPending] = useState(false)
  const [busyAction, setBusyAction] = useState('')
  const [connected, setConnected] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const updateTasks = useCallback((next: Task[]) => {
    tasksRef.current = next
    setTasks(next)
  }, [])

  const refresh = useCallback(async () => {
    const [serviceData, taskData, auditData] = await Promise.all([api.services(), api.tasks(), api.audit()])
    setServices(serviceData)
    updateTasks(taskData)
    setAudit(auditData)
    setSelectedTask((current) => current ? taskData.find((item) => item.id === current.id) ?? current : null)
  }, [api, updateTasks])

  useEffect(() => {
    let active = true
    void (async () => {
      try {
        const session = await api.session()
        if (!active) return
        setEmail(session.email)
        await refresh()
      } catch (reason) {
        if (active) setError(reason instanceof Error ? reason.message : '初始化失败')
      } finally {
        if (active) setLoading(false)
      }
    })()
    return () => { active = false }
  }, [api, refresh])

  useEffect(() => {
    if (!email) return undefined
    return api.events((event) => {
      const task = tasksRef.current.find((item) => item.id === event.taskId)
      if (event.phase === 'discover' && task && event.data) {
        setDiscoveries((current) => ({ ...current, [task.service]: event.data as ReleaseDiscovery }))
      }
      if (selectedTask?.id === event.taskId) {
        setTaskEvents((current) => current.some((item) => item.sequence === event.sequence) ? current : [...current, event])
      }
      if (event.phase === 'terminal') {
        void refresh().catch((reason) => setError(reason instanceof Error ? reason.message : '刷新失败'))
      }
    }, setConnected)
  }, [api, email, refresh, selectedTask?.id])

  useEffect(() => {
    if (!email) return undefined
    const timer = window.setInterval(() => {
      void refresh().catch((reason) => setError(reason instanceof Error ? reason.message : '刷新失败'))
    }, 60_000)
    return () => window.clearInterval(timer)
  }, [email, refresh])

  async function beginAction(service: ServiceView, action: ActionDefinition, target = '') {
    const key = `${service.name}/${action.name}`
    setBusyAction(key)
    setError('')
    try {
      const nextPreview = await api.preview(service.name, action.name, target)
      if (nextPreview.requiresConfirmation) {
        setPreview(nextPreview)
      } else {
        const task = await api.start(nextPreview.id)
        updateTasks([task, ...tasksRef.current.filter((item) => item.id !== task.id)])
        void openTask(task)
      }
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '操作预览失败')
    } finally {
      setBusyAction('')
    }
  }

  async function confirmAction(value: string) {
    if (!preview) return
    setPending(true)
    try {
      const task = await api.start(preview.id, value)
      updateTasks([task, ...tasksRef.current.filter((item) => item.id !== task.id)])
      setPreview(null)
      void openTask(task)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '任务提交失败')
    } finally {
      setPending(false)
    }
  }

  async function openTask(task: Task) {
    setSelectedTask(task)
    setTaskEvents([])
    setTaskEventsLoading(true)
    try {
      setTaskEvents(await api.taskEvents(task.id))
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '任务记录读取失败')
    } finally {
      setTaskEventsLoading(false)
    }
  }

  function openService(name: string) {
    setSelectedService(name)
    setView('services')
  }

  if (loading) {
    return <div className="boot-state"><LoaderCircle className="spin" size={24} /><span>连接控制面</span></div>
  }

  if (!email) {
    return <div className="boot-state error"><AlertCircle size={24} /><span>{error || '身份验证失败'}</span></div>
  }

  return (
    <Shell view={view} onView={setView} email={email} connected={connected}>
      {error && (
        <div className="toast error-toast" role="alert">
          <AlertCircle size={17} aria-hidden="true" /><span>{error}</span>
          <button type="button" onClick={() => setError('')} title="关闭"><X size={16} /></button>
        </div>
      )}
      {view === 'overview' && <Overview services={services} tasks={tasks} onService={openService} onTask={openTask} />}
      {view === 'services' && (
        <Services
          services={services}
          selected={selectedService}
          discoveries={discoveries}
          tasks={tasks}
          busyAction={busyAction}
          onSelect={setSelectedService}
          onRefresh={() => void refresh().catch((reason) => setError(reason instanceof Error ? reason.message : '刷新失败'))}
          onAction={beginAction}
        />
      )}
      {view === 'tasks' && <Tasks tasks={tasks} onTask={openTask} />}
      {view === 'audit' && <Audit entries={audit} />}
      {preview && (
        <ConfirmationDialog preview={preview} pending={pending} onCancel={() => setPreview(null)} onConfirm={confirmAction} />
      )}
      {selectedTask && (
        <TaskDrawer task={selectedTask} events={taskEvents} loading={taskEventsLoading} onClose={() => setSelectedTask(null)} />
      )}
    </Shell>
  )
}
