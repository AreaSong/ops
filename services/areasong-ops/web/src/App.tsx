import { AlertCircle, LoaderCircle, X } from 'lucide-react'
import { useCallback, useEffect, useRef, useState } from 'react'
import { OpsAPI } from './api'
import { ConfirmationDialog } from './components/ConfirmationDialog'
import { Shell, type ViewName } from './components/Shell'
import { TaskDrawer } from './components/TaskDrawer'
import type {
  ActionDefinition,
  AuditEntry,
  NavigationLinks,
  OpsEvent,
  ReleaseDiscovery,
  ReleasePlan,
  ServiceView,
  Task,
} from './types'
import { Audit } from './views/Audit'
import { Overview } from './views/Overview'
import { Services } from './views/Services'
import { Tasks } from './views/Tasks'

function mergeTasks(primary: Task[], secondary: Task[]): Task[] {
  const seen = new Set(primary.map((task) => task.id))
  return [...primary, ...secondary.filter((task) => !seen.has(task.id))]
}

function mergeAudit(primary: AuditEntry[], secondary: AuditEntry[]): AuditEntry[] {
  const seen = new Set(primary.map((entry) => entry.sequence))
  return [...primary, ...secondary.filter((entry) => !seen.has(entry.sequence))]
}

export default function App() {
  const api = useRef(new OpsAPI()).current
  const [email, setEmail] = useState('')
  const [links, setLinks] = useState<NavigationLinks>({})
  const [view, setView] = useState<ViewName>('overview')
  const [services, setServices] = useState<ServiceView[]>([])
  const [tasks, setTasks] = useState<Task[]>([])
  const tasksRef = useRef<Task[]>([])
  const [audit, setAudit] = useState<AuditEntry[]>([])
  const auditRef = useRef<AuditEntry[]>([])
  const [tasksHasMore, setTasksHasMore] = useState(false)
  const [auditHasMore, setAuditHasMore] = useState(false)
  const [tasksLoadingMore, setTasksLoadingMore] = useState(false)
  const [auditLoadingMore, setAuditLoadingMore] = useState(false)
  const [discoveries, setDiscoveries] = useState<Record<string, ReleaseDiscovery>>({})
  const [plans, setPlans] = useState<ReleasePlan[]>([])
  const [selectedService, setSelectedService] = useState('areaforge')
  const [selectedTask, setSelectedTask] = useState<Task | null>(null)
  const [taskEvents, setTaskEvents] = useState<OpsEvent[]>([])
  const [taskEventsLoading, setTaskEventsLoading] = useState(false)
  const [taskEventsHasMore, setTaskEventsHasMore] = useState(false)
  const [taskEventsLoadingMore, setTaskEventsLoadingMore] = useState(false)
  const [selectedPlan, setSelectedPlan] = useState<ReleasePlan | null>(null)
  const [pending, setPending] = useState(false)
  const [busyAction, setBusyAction] = useState('')
  const [connected, setConnected] = useState(false)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')

  const updateTasks = useCallback((next: Task[]) => {
    tasksRef.current = next
    setTasks(next)
  }, [])

  const updateAudit = useCallback((next: AuditEntry[]) => {
    auditRef.current = next
    setAudit(next)
  }, [])

  const refresh = useCallback(async () => {
    const [serviceData, taskData, auditData, planData] = await Promise.all([
      api.services(), api.tasks(), api.audit(), api.plans(),
    ])
    const previousTasks = tasksRef.current
    const previousAudit = auditRef.current
    const nextTasks = mergeTasks(taskData.items, previousTasks)
    const nextAudit = mergeAudit(auditData.items, previousAudit)
    setServices(serviceData)
    setPlans(planData.items)
    setDiscoveries(Object.fromEntries(serviceData
      .filter((service) => service.releaseDiscovery)
      .map((service) => [service.name, service.releaseDiscovery as ReleaseDiscovery])))
    updateTasks(nextTasks)
    updateAudit(nextAudit)
    if (previousTasks.length === 0) setTasksHasMore(taskData.hasMore)
    if (previousAudit.length === 0) setAuditHasMore(auditData.hasMore)
    setSelectedTask((current) => current ? nextTasks.find((item) => item.id === current.id) ?? current : null)
    setSelectedPlan((current) => current
      ? planData.items.find((item) => item.id === current.id && ['pending_approval', 'approved'].includes(item.state)) ?? null
      : null)
  }, [api, updateAudit, updateTasks])

  useEffect(() => {
    let active = true
    void (async () => {
      try {
        const session = await api.session()
        if (!active) return
        setEmail(session.email)
        setLinks(session.links ?? {})
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
      if (task) {
        const terminalState = event.data?.state
        const state: Task['state'] = event.phase === 'queued'
          ? 'queued'
          : event.phase === 'terminal' && typeof terminalState === 'string'
            ? terminalState as Task['state']
            : event.phase === 'terminal' ? task.state : 'running'
        const updated = {
          ...task,
          state,
          currentPhase: event.phase ?? task.currentPhase,
          summary: event.phase === 'queued' || event.phase === 'terminal' ? task.summary : event.message,
        }
        updateTasks(tasksRef.current.map((item) => item.id === updated.id ? updated : item))
        setSelectedTask((current) => current?.id === updated.id ? updated : current)
      }
      if (event.phase === 'discover' && task && event.data) {
        setDiscoveries((current) => ({ ...current, [task.service]: event.data as ReleaseDiscovery }))
      }
      if (selectedTask?.id === event.taskId) {
        setTaskEvents((current) => current.some((item) => item.sequence === event.sequence) ? current : [...current, event])
      }
      if (event.phase === 'terminal') {
        setServices((current) => current.map((service) =>
          service.activeTaskId === event.taskId ? { ...service, activeTaskId: undefined } : service))
        void refresh().catch((reason) => setError(reason instanceof Error ? reason.message : '刷新失败'))
      }
    }, setConnected)
  }, [api, email, refresh, selectedTask?.id, updateTasks])

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
      const plan = await api.createPlan(service.name, action.name, target)
      setPlans((current) => [plan, ...current.filter((item) => item.id !== plan.id)])
      if (plan.requiresConfirmation) {
        setSelectedPlan(plan)
      } else {
        const approved = await api.approvePlan(plan)
        const task = await api.executePlan(approved.id)
        registerTask(task)
        void openTask(task)
      }
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '发布计划创建失败')
    } finally {
      setBusyAction('')
    }
  }

  async function confirmAction(value: string) {
    if (!selectedPlan) return
    setPending(true)
    try {
      if (selectedPlan.state === 'pending_approval') {
        const approved = await api.approvePlan(selectedPlan, value)
        setSelectedPlan(approved)
        setPlans((current) => current.map((item) => item.id === approved.id ? approved : item))
      } else {
        const task = await api.executePlan(selectedPlan.id)
        registerTask(task)
        setSelectedPlan(null)
        void openTask(task)
      }
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '执行提交失败')
    } finally {
      setPending(false)
    }
  }

  async function openTask(task: Task) {
    setSelectedTask(task)
    setTaskEvents([])
    setTaskEventsLoading(true)
    setTaskEventsHasMore(false)
    try {
      const [detail, page] = await Promise.all([api.task(task.id), api.taskEvents(task.id)])
      setSelectedTask(detail)
      updateTasks(tasksRef.current.map((item) => item.id === detail.id ? detail : item))
      setTaskEvents((current) => {
        const seen = new Set(page.items.map((event) => event.sequence))
        return [...page.items, ...current.filter((event) => !seen.has(event.sequence))]
      })
      setTaskEventsHasMore(page.hasMore)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '执行记录读取失败')
    } finally {
      setTaskEventsLoading(false)
    }
  }

  async function recoverTask(task: Task, action: string) {
    setPending(true)
    setError('')
    try {
      const plan = await api.recoverTask(task.id, action)
      setPlans((current) => [plan, ...current.filter((item) => item.id !== plan.id)])
      if (plan.requiresConfirmation) {
        setSelectedPlan(plan)
      } else {
        const approved = await api.approvePlan(plan)
        const recovered = await api.executePlan(approved.id)
        registerTask(recovered)
        void openTask(recovered)
      }
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '恢复动作创建失败')
    } finally {
      setPending(false)
    }
  }

  function registerTask(task: Task) {
    updateTasks([task, ...tasksRef.current.filter((item) => item.id !== task.id)])
    setServices((current) => current.map((service) =>
      service.name === task.service ? { ...service, activeTaskId: task.id } : service))
  }

  async function loadMoreTasks() {
    setTasksLoadingMore(true)
    try {
      const page = await api.tasks(tasksRef.current.length)
      updateTasks(mergeTasks(tasksRef.current, page.items))
      setTasksHasMore(page.hasMore)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '更多执行记录读取失败')
    } finally {
      setTasksLoadingMore(false)
    }
  }

  async function loadMoreAudit() {
    setAuditLoadingMore(true)
    try {
      const page = await api.audit(auditRef.current.length)
      updateAudit(mergeAudit(auditRef.current, page.items))
      setAuditHasMore(page.hasMore)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '更多审计记录读取失败')
    } finally {
      setAuditLoadingMore(false)
    }
  }

  async function loadMoreTaskEvents() {
    if (!selectedTask || taskEvents.length === 0) return
    setTaskEventsLoadingMore(true)
    try {
      const page = await api.taskEvents(selectedTask.id, taskEvents[taskEvents.length - 1].sequence)
      setTaskEvents((current) => {
        const seen = new Set(current.map((event) => event.sequence))
        return [...current, ...page.items.filter((event) => !seen.has(event.sequence))]
      })
      setTaskEventsHasMore(page.hasMore)
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : '更多执行记录读取失败')
    } finally {
      setTaskEventsLoadingMore(false)
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
    <Shell view={view} onView={setView} email={email} connected={connected} links={links}>
      {error && (
        <div className="toast error-toast" role="alert">
          <AlertCircle size={17} aria-hidden="true" /><span>{error}</span>
          <button type="button" onClick={() => setError('')} title="关闭"><X size={16} /></button>
        </div>
      )}
      {view === 'overview' && (
        <Overview services={services} tasks={tasks} plans={plans}
          onService={openService} onTask={openTask} onPlan={setSelectedPlan} />
      )}
      {view === 'services' && (
        <Services
          services={services}
          selected={selectedService}
          discoveries={discoveries}
          busyAction={busyAction}
          plans={plans}
          onSelect={setSelectedService}
          onRefresh={() => void refresh().catch((reason) => setError(reason instanceof Error ? reason.message : '刷新失败'))}
          onAction={beginAction}
          onPlan={setSelectedPlan}
        />
      )}
      {view === 'tasks' && (
        <Tasks tasks={tasks} hasMore={tasksHasMore} loadingMore={tasksLoadingMore}
          onTask={openTask} onLoadMore={() => void loadMoreTasks()} />
      )}
      {view === 'audit' && (
        <Audit entries={audit} hasMore={auditHasMore} loadingMore={auditLoadingMore}
          onLoadMore={() => void loadMoreAudit()} />
      )}
      {selectedPlan && (
        <ConfirmationDialog plan={selectedPlan} pending={pending} onCancel={() => setSelectedPlan(null)} onConfirm={confirmAction} />
      )}
      {selectedTask && (
        <TaskDrawer task={selectedTask} events={taskEvents} loading={taskEventsLoading}
          hasMore={taskEventsHasMore} loadingMore={taskEventsLoadingMore}
          pending={pending} onRecovery={(action) => void recoverTask(selectedTask, action)}
          onLoadMore={() => void loadMoreTaskEvents()} onClose={() => setSelectedTask(null)} />
      )}
    </Shell>
  )
}
