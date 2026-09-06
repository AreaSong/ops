import { AlertCircle, LoaderCircle, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { isFeatureUnavailable, OpsAPI } from "./api";
import { ConfirmationDialog } from "./components/ConfirmationDialog";
import { Shell, type ViewName } from "./components/Shell";
import { TaskDrawer } from "./components/TaskDrawer";
import type {
  ActiveAlert,
  AccessChange,
  AccessControlView,
  AccessControlUpdate,
  ActionDefinition,
  AuditEntry,
  AutomaticTaskView,
  AutoUpdateEvaluation,
  AutoUpdatePolicyInput,
  AutoUpdatePolicyView,
  BatchTask,
  ComposeServiceView,
  CredentialProfile,
  CredentialRotation,
  ExtensionPlan,
  ExtensionManifest,
  ExtensionPolicyView,
  Fleet,
  FleetRunnerUpdatePlan,
  FleetRunnerUpdatePlanInput,
  FleetRunnerUpdateStatus,
  KubernetesConfigView,
  KubernetesPlan,
  ManagedObjectView,
  ManagedFileProposal,
  ManagedFileView,
  NavigationLinks,
  OpsEvent,
  RecoveryCenterView,
  ReleaseDiscovery,
  ReleasePlan,
  RunnerUpdatePrepareInput,
  RunnerUpdateResolutionEvidence,
  RunnerUpdateStatus,
  ServiceState,
  ServiceView,
  Task,
  TerminalCommand,
  TerminalOutput,
  TerminalShellPlan,
} from "./types";
import { Audit } from "./views/Audit";
import { AutomaticTasks } from "./views/AutomaticTasks";
import { Credentials } from "./views/Credentials";
import { AccessControl } from "./views/AccessControl";
import { AutoUpdates } from "./views/AutoUpdates";
import { Batches } from "./views/Batches";
import { Configuration } from "./views/Configuration";
import { FleetView } from "./views/Fleet";
import { Lifecycle } from "./views/Lifecycle";
import { Overview } from "./views/Overview";
import { RecoveryCenter } from "./views/RecoveryCenter";
import { RunnerUpdate } from "./views/RunnerUpdate";
import { RunnerFleetUpdate } from "./views/RunnerFleetUpdate";
import { Services } from "./views/Services";
import { Tasks } from "./views/Tasks";
import { Files } from "./views/Files";
import { Terminal } from "./views/Terminal";

function mergeTasks(primary: Task[], secondary: Task[]): Task[] {
  const seen = new Set(primary.map((task) => task.id));
  return [...primary, ...secondary.filter((task) => !seen.has(task.id))];
}

function mergeAudit(
  primary: AuditEntry[],
  secondary: AuditEntry[],
): AuditEntry[] {
  const seen = new Set(primary.map((entry) => entry.sequence));
  return [
    ...primary,
    ...secondary.filter((entry) => !seen.has(entry.sequence)),
  ];
}

function errorMessage(reason: unknown, fallback: string): string {
  return reason instanceof Error ? reason.message : fallback;
}

function isReleasePlan(value: unknown): value is ReleasePlan {
  return (
    typeof value === "object" &&
    value !== null &&
    "id" in value &&
    "approvalSummary" in value &&
    "state" in value
  );
}

async function loadFeature<T>(
  loader: () => Promise<T>,
  setData: (value: T) => void,
  setLoading: (value: boolean) => void,
  setAvailable: (value: boolean) => void,
  setFeatureError: (value: string) => void,
  fallback: string,
): Promise<void> {
  setLoading(true);
  setFeatureError("");
  try {
    setData(await loader());
    setAvailable(true);
  } catch (reason) {
    setAvailable(!isFeatureUnavailable(reason));
    setFeatureError(errorMessage(reason, fallback));
  } finally {
    setLoading(false);
  }
}

export default function App() {
  const api = useRef(new OpsAPI()).current;
  const [email, setEmail] = useState("");
  const [currentActorHash, setCurrentActorHash] = useState("");
  const [environment, setEnvironment] = useState<"production" | "development">(
    "production",
  );
  const [links, setLinks] = useState<NavigationLinks>({});
  const [view, setView] = useState<ViewName>("overview");
  const [services, setServices] = useState<ServiceView[]>([]);
  const [automaticTasks, setAutomaticTasks] = useState<AutomaticTaskView[]>([]);
  const [alerts, setAlerts] = useState<ActiveAlert[]>([]);
  const [alertsError, setAlertsError] = useState("");
  const [credentialProfile, setCredentialProfile] =
    useState<CredentialProfile | null>(null);
  const [credentialLoading, setCredentialLoading] = useState(false);
  const [tasks, setTasks] = useState<Task[]>([]);
  const tasksRef = useRef<Task[]>([]);
  const [audit, setAudit] = useState<AuditEntry[]>([]);
  const auditRef = useRef<AuditEntry[]>([]);
  const [tasksHasMore, setTasksHasMore] = useState(false);
  const [auditHasMore, setAuditHasMore] = useState(false);
  const [tasksLoadingMore, setTasksLoadingMore] = useState(false);
  const [auditLoadingMore, setAuditLoadingMore] = useState(false);
  const [discoveries, setDiscoveries] = useState<
    Record<string, ReleaseDiscovery>
  >({});
  const [plans, setPlans] = useState<ReleasePlan[]>([]);
  const [selectedService, setSelectedService] = useState("areaforge");
  const [selectedComposeService, setSelectedComposeService] = useState("");
  const [selectedTask, setSelectedTask] = useState<Task | null>(null);
  const [taskEvents, setTaskEvents] = useState<OpsEvent[]>([]);
  const [taskEventsLoading, setTaskEventsLoading] = useState(false);
  const [taskEventsHasMore, setTaskEventsHasMore] = useState(false);
  const [taskEventsLoadingMore, setTaskEventsLoadingMore] = useState(false);
  const [selectedPlan, setSelectedPlan] = useState<ReleasePlan | null>(null);
  const [serviceStates, setServiceStates] = useState<ServiceState[]>([]);
  const [statesLoading, setStatesLoading] = useState(false);
  const [statesAvailable, setStatesAvailable] = useState(true);
  const [statesError, setStatesError] = useState("");
  const [fleet, setFleet] = useState<Fleet | null>(null);
  const [fleetLoading, setFleetLoading] = useState(false);
  const [fleetAvailable, setFleetAvailable] = useState(true);
  const [fleetError, setFleetError] = useState("");
  const [batches, setBatches] = useState<BatchTask[]>([]);
  const [batchesLoading, setBatchesLoading] = useState(false);
  const [batchesAvailable, setBatchesAvailable] = useState(true);
  const [batchesError, setBatchesError] = useState("");
  const [recoveryCenter, setRecoveryCenter] = useState<RecoveryCenterView[]>(
    [],
  );
  const [recoveryLoading, setRecoveryLoading] = useState(false);
  const [recoveryAvailable, setRecoveryAvailable] = useState(true);
  const [recoveryError, setRecoveryError] = useState("");
  const [composeByService, setComposeByService] = useState<
    Record<string, ComposeServiceView>
  >({});
  const [composeLoading, setComposeLoading] = useState(false);
  const [composeAvailable, setComposeAvailable] = useState(true);
  const [composeError, setComposeError] = useState("");
  const [kubernetes, setKubernetes] = useState<KubernetesConfigView | null>(
    null,
  );
  const [kubernetesLoading, setKubernetesLoading] = useState(false);
  const [kubernetesAvailable, setKubernetesAvailable] = useState(true);
  const [kubernetesError, setKubernetesError] = useState("");
  const [extensions, setExtensions] = useState<ExtensionPolicyView | null>(
    null,
  );
  const [extensionsLoading, setExtensionsLoading] = useState(false);
  const [extensionsAvailable, setExtensionsAvailable] = useState(true);
  const [extensionsError, setExtensionsError] = useState("");
  const [runnerUpdate, setRunnerUpdate] = useState<RunnerUpdateStatus | null>(
    null,
  );
  const [runnerUpdateLoading, setRunnerUpdateLoading] = useState(false);
  const [runnerUpdateAvailable, setRunnerUpdateAvailable] = useState(true);
  const [runnerUpdateError, setRunnerUpdateError] = useState("");
  const [fleetRunnerUpdate, setFleetRunnerUpdate] =
    useState<FleetRunnerUpdateStatus | null>(null);
  const [fleetRunnerUpdateLoading, setFleetRunnerUpdateLoading] =
    useState(false);
  const [fleetRunnerUpdateAvailable, setFleetRunnerUpdateAvailable] =
    useState(true);
  const [fleetRunnerUpdateError, setFleetRunnerUpdateError] = useState("");
  const [access, setAccess] = useState<AccessControlView | null>(null);
  const [accessLoading, setAccessLoading] = useState(false);
  const [accessAvailable, setAccessAvailable] = useState(true);
  const [accessError, setAccessError] = useState("");
  const [autoUpdates, setAutoUpdates] = useState<AutoUpdatePolicyView[]>([]);
  const [autoUpdateEvaluations, setAutoUpdateEvaluations] = useState<
    AutoUpdateEvaluation[]
  >([]);
  const [autoUpdatesLoading, setAutoUpdatesLoading] = useState(false);
  const [autoUpdatesAvailable, setAutoUpdatesAvailable] = useState(true);
  const [autoUpdatesError, setAutoUpdatesError] = useState("");
  const [terminalCommands, setTerminalCommands] = useState<TerminalCommand[]>(
    [],
  );
  const [terminalPlans, setTerminalPlans] = useState<TerminalShellPlan[]>([]);
  const [terminalOutput, setTerminalOutput] = useState<TerminalOutput | null>(
    null,
  );
  const [terminalLoading, setTerminalLoading] = useState(false);
  const [terminalAvailable, setTerminalAvailable] = useState(true);
  const [terminalShellAvailable, setTerminalShellAvailable] = useState(true);
  const [terminalError, setTerminalError] = useState("");
  const [managedFile, setManagedFile] = useState<ManagedFileView | null>(null);
  const [fileProposals, setFileProposals] = useState<ManagedFileProposal[]>([]);
  const [filesLoading, setFilesLoading] = useState(false);
  const [filesAvailable, setFilesAvailable] = useState(true);
  const [filesError, setFilesError] = useState("");
  const [pending, setPending] = useState(false);
  const [busyAction, setBusyAction] = useState("");
  const [connected, setConnected] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");

  const updateTasks = useCallback((next: Task[]) => {
    tasksRef.current = next;
    setTasks(next);
  }, []);

  const updateAudit = useCallback((next: AuditEntry[]) => {
    auditRef.current = next;
    setAudit(next);
  }, []);

  const refresh = useCallback(async () => {
    const [
      serviceData,
      automaticTaskData,
      taskData,
      auditData,
      planData,
      alertData,
    ] = await Promise.all([
      api.services(),
      api.automaticTasks(),
      api.tasks(),
      api.audit(),
      api.plans(),
      api
        .alerts()
        .then((items) => ({ items, error: "" }))
        .catch((reason) => ({
          items: [] as ActiveAlert[],
          error:
            reason instanceof Error
              ? reason.message
              : "Alertmanager 活动告警不可用",
        })),
    ]);
    const previousTasks = tasksRef.current;
    const previousAudit = auditRef.current;
    const nextTasks = mergeTasks(taskData.items, previousTasks);
    const nextAudit = mergeAudit(auditData.items, previousAudit);
    setServices(serviceData);
    setSelectedComposeService((current) =>
      serviceData.some(
        (service) => service.name === current && service.managedCompose,
      )
        ? current
        : (serviceData.find((service) => service.managedCompose)?.name ?? ""),
    );
    setAutomaticTasks(automaticTaskData);
    setAlerts(alertData.items);
    setAlertsError(alertData.error);
    setPlans(planData.items);
    setDiscoveries(
      Object.fromEntries(
        serviceData
          .filter((service) => service.releaseDiscovery)
          .map((service) => [
            service.name,
            service.releaseDiscovery as ReleaseDiscovery,
          ]),
      ),
    );
    updateTasks(nextTasks);
    updateAudit(nextAudit);
    if (previousTasks.length === 0) setTasksHasMore(taskData.hasMore);
    if (previousAudit.length === 0) setAuditHasMore(auditData.hasMore);
    setSelectedTask((current) =>
      current
        ? (nextTasks.find((item) => item.id === current.id) ?? current)
        : null,
    );
    setSelectedPlan((current) =>
      current
        ? (planData.items.find(
            (item) =>
              item.id === current.id &&
              [
                "pending_approval",
                "scheduled",
                "approved",
                "observing",
              ].includes(item.state),
          ) ?? null)
        : null,
    );
  }, [api, updateAudit, updateTasks]);

  const refreshCredential = useCallback(async () => {
    setCredentialLoading(true);
    try {
      setCredentialProfile(await api.credentialProfile());
    } finally {
      setCredentialLoading(false);
    }
  }, [api]);

  const refreshStates = useCallback(
    () =>
      loadFeature(
        () => api.states(),
        setServiceStates,
        setStatesLoading,
        setStatesAvailable,
        setStatesError,
        "生命周期状态读取失败",
      ),
    [api],
  );

  const refreshFleet = useCallback(
    () =>
      loadFeature(
        () => api.fleet(),
        setFleet,
        setFleetLoading,
        setFleetAvailable,
        setFleetError,
        "Fleet 读取失败",
      ),
    [api],
  );

  const refreshBatches = useCallback(
    () =>
      loadFeature(
        async () => (await api.batches()).items,
        setBatches,
        setBatchesLoading,
        setBatchesAvailable,
        setBatchesError,
        "批量作业读取失败",
      ),
    [api],
  );

  const refreshRecoveryCenter = useCallback(
    () =>
      loadFeature(
        () => api.recoveryCenter(),
        setRecoveryCenter,
        setRecoveryLoading,
        setRecoveryAvailable,
        setRecoveryError,
        "恢复中心读取失败",
      ),
    [api],
  );

  const refreshCompose = useCallback(() => {
    if (!selectedComposeService) return Promise.resolve();
    return loadFeature(
      () => api.compose(selectedComposeService),
      (value) =>
        setComposeByService((current) => ({
          ...current,
          [selectedComposeService]: value,
        })),
      setComposeLoading,
      setComposeAvailable,
      setComposeError,
      "Compose 配置读取失败",
    );
  }, [api, selectedComposeService]);

  const refreshKubernetes = useCallback(
    () =>
      loadFeature(
        () => api.kubernetes(),
        setKubernetes,
        setKubernetesLoading,
        setKubernetesAvailable,
        setKubernetesError,
        "Kubernetes 配置读取失败",
      ),
    [api],
  );

  const refreshExtensions = useCallback(
    () =>
      loadFeature(
        () => api.extensions(),
        setExtensions,
        setExtensionsLoading,
        setExtensionsAvailable,
        setExtensionsError,
        "扩展策略读取失败",
      ),
    [api],
  );

  const refreshRunnerUpdate = useCallback(
    () =>
      loadFeature(
        () => api.runnerUpdateStatus(),
        setRunnerUpdate,
        setRunnerUpdateLoading,
        setRunnerUpdateAvailable,
        setRunnerUpdateError,
        "Runner 更新状态读取失败",
      ),
    [api],
  );

  const refreshFleetRunnerUpdate = useCallback(
    () =>
      loadFeature(
        () => api.fleetRunnerUpdateStatus(),
        setFleetRunnerUpdate,
        setFleetRunnerUpdateLoading,
        setFleetRunnerUpdateAvailable,
        setFleetRunnerUpdateError,
        "Runner Fleet 更新状态读取失败",
      ),
    [api],
  );

  const refreshAccess = useCallback(
    () =>
      loadFeature(
        () => api.access(),
        setAccess,
        setAccessLoading,
        setAccessAvailable,
        setAccessError,
        "访问控制策略读取失败",
      ),
    [api],
  );

  const refreshAutoUpdates = useCallback(
    () =>
      loadFeature(
        () => api.autoUpdates(),
        setAutoUpdates,
        setAutoUpdatesLoading,
        setAutoUpdatesAvailable,
        setAutoUpdatesError,
        "自动更新策略读取失败",
      ),
    [api],
  );

  const refreshTerminal = useCallback(async () => {
    setTerminalLoading(true);
    setTerminalError("");
    try {
      const commands = await api.terminalCommands();
      setTerminalCommands(commands);
      setTerminalAvailable(true);
      try {
        setTerminalPlans(await api.terminalShellPlans());
        setTerminalShellAvailable(true);
      } catch (reason) {
        if (isFeatureUnavailable(reason)) {
          setTerminalPlans([]);
          setTerminalShellAvailable(false);
        } else {
          setTerminalShellAvailable(true);
          setTerminalError(errorMessage(reason, "终端计划读取失败"));
        }
      }
    } catch (reason) {
      setTerminalAvailable(!isFeatureUnavailable(reason));
      setTerminalShellAvailable(false);
      setTerminalError(errorMessage(reason, "终端状态读取失败"));
    } finally {
      setTerminalLoading(false);
    }
  }, [api]);

  const refreshFiles = useCallback(
    () =>
      loadFeature(
        async () => ({ proposals: await api.managedFileProposals() }),
        (value) => setFileProposals(value.proposals),
        setFilesLoading,
        setFilesAvailable,
        setFilesError,
        "受管文件状态读取失败",
      ),
    [api],
  );

  useEffect(() => {
    let active = true;
    void (async () => {
      try {
        const session = await api.session();
        if (!active) return;
        setEmail(session.email);
        setCurrentActorHash(session.actorHash);
        setEnvironment(session.environment);
        setLinks(session.links ?? {});
        await refresh();
      } catch (reason) {
        if (active)
          setError(reason instanceof Error ? reason.message : "初始化失败");
      } finally {
        if (active) setLoading(false);
      }
    })();
    return () => {
      active = false;
    };
  }, [api, refresh]);

  useEffect(() => {
    if (!email) return undefined;
    return api.events((event) => {
      const task = tasksRef.current.find((item) => item.id === event.taskId);
      if (task) {
        const terminalState = event.data?.state;
        const state: Task["state"] =
          event.phase === "queued"
            ? "queued"
            : event.phase === "terminal" && typeof terminalState === "string"
              ? (terminalState as Task["state"])
              : event.phase === "terminal"
                ? task.state
                : "running";
        const updated = {
          ...task,
          state,
          currentPhase: event.phase ?? task.currentPhase,
          summary:
            event.phase === "queued" || event.phase === "terminal"
              ? task.summary
              : event.message,
        };
        updateTasks(
          tasksRef.current.map((item) =>
            item.id === updated.id ? updated : item,
          ),
        );
        setSelectedTask((current) =>
          current?.id === updated.id ? updated : current,
        );
      }
      if (event.phase === "discover" && task && event.data) {
        setDiscoveries((current) => ({
          ...current,
          [task.service]: event.data as ReleaseDiscovery,
        }));
      }
      if (selectedTask?.id === event.taskId) {
        setTaskEvents((current) =>
          current.some((item) => item.sequence === event.sequence)
            ? current
            : [...current, event],
        );
      }
      if (event.phase === "terminal") {
        setServices((current) =>
          current.map((service) =>
            service.activeTaskId === event.taskId
              ? { ...service, activeTaskId: undefined }
              : service,
          ),
        );
        setAutomaticTasks((current) =>
          current.map((task) =>
            task.activeTaskId === event.taskId
              ? { ...task, activeTaskId: undefined }
              : task,
          ),
        );
        void refresh().catch((reason) =>
          setError(reason instanceof Error ? reason.message : "刷新失败"),
        );
      }
    }, setConnected);
  }, [api, email, refresh, selectedTask?.id, updateTasks]);

  useEffect(() => {
    if (!email) return undefined;
    const timer = window.setInterval(() => {
      void refresh().catch((reason) =>
        setError(reason instanceof Error ? reason.message : "刷新失败"),
      );
    }, 60_000);
    return () => window.clearInterval(timer);
  }, [email, refresh]);

  useEffect(() => {
    if (view !== "credentials" || credentialProfile || credentialLoading)
      return;
    void refreshCredential().catch((reason) =>
      setError(reason instanceof Error ? reason.message : "凭据状态读取失败"),
    );
  }, [credentialLoading, credentialProfile, refreshCredential, view]);

  useEffect(() => {
    if (view === "lifecycle") void refreshStates();
    if (view === "fleet") void refreshFleet();
    if (view === "batches") void refreshBatches();
    if (view === "recovery") void refreshRecoveryCenter();
    if (view === "configuration") {
      void refreshCompose();
      void refreshKubernetes();
      void refreshExtensions();
    }
    if (view === "auto-updates") void refreshAutoUpdates();
    if (view === "terminal") void refreshTerminal();
    if (view === "files") void refreshFiles();
    if (view === "runner-update") void refreshRunnerUpdate();
    if (view === "runner-fleet-update") void refreshFleetRunnerUpdate();
    if (view === "access") void refreshAccess();
  }, [
    refreshAccess,
    refreshAutoUpdates,
    refreshBatches,
    refreshCompose,
    refreshExtensions,
    refreshFiles,
    refreshFleet,
    refreshFleetRunnerUpdate,
    refreshKubernetes,
    refreshRecoveryCenter,
    refreshRunnerUpdate,
    refreshStates,
    refreshTerminal,
    view,
  ]);

  useEffect(() => {
    if (view !== "runner-update") return undefined;
    const hasActiveUpdate =
      runnerUpdate?.pending?.some((item) => item.state === "activating") ??
      false;
    const timer = window.setInterval(
      () => {
        void refreshRunnerUpdate();
      },
      hasActiveUpdate ? 2_000 : 15_000,
    );
    return () => window.clearInterval(timer);
  }, [refreshRunnerUpdate, runnerUpdate?.pending, view]);

  useEffect(() => {
    if (view !== "runner-fleet-update") return undefined;
    const hasActiveUpdate =
      fleetRunnerUpdate?.plans.some((plan) =>
        ["running", "observing", "rolling_back"].includes(plan.state),
      ) ?? false;
    const timer = window.setInterval(
      () => {
        void refreshFleetRunnerUpdate();
      },
      hasActiveUpdate ? 2_000 : 15_000,
    );
    return () => window.clearInterval(timer);
  }, [fleetRunnerUpdate?.plans, refreshFleetRunnerUpdate, view]);

  async function beginAction(
    service: ManagedObjectView,
    action: ActionDefinition,
    target = "",
  ) {
    const key = `${service.name}/${action.name}`;
    setBusyAction(key);
    setError("");
    try {
      const plan = await api.createPlan(service.name, action.name, target);
      setPlans((current) => [
        plan,
        ...current.filter((item) => item.id !== plan.id),
      ]);
      if (plan.requiresConfirmation) {
        setSelectedPlan(plan);
      } else {
        const approved = await api.approvePlan(plan);
        const task = await api.executePlan(approved.id);
        registerTask(task);
        void openTask(task);
      }
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "发布计划创建失败");
    } finally {
      setBusyAction("");
    }
  }

  async function confirmAction(value: string) {
    if (!selectedPlan) return;
    setPending(true);
    try {
      if (selectedPlan.state === "pending_approval") {
        const approved = await api.approvePlan(selectedPlan, value);
        setSelectedPlan(approved);
        setPlans((current) =>
          current.map((item) => (item.id === approved.id ? approved : item)),
        );
      } else if (selectedPlan.state === "approved") {
        const task = await api.executePlan(selectedPlan.id);
        registerTask(task);
        setSelectedPlan(null);
        void openTask(task);
      }
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "执行提交失败");
    } finally {
      setPending(false);
    }
  }

  async function closePlan() {
    if (!selectedPlan || selectedPlan.state !== "observing") return;
    setPending(true);
    setError("");
    try {
      const closed = await api.closePlan(selectedPlan.id);
      setPlans((current) =>
        current.map((item) => (item.id === closed.id ? closed : item)),
      );
      setSelectedPlan(null);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "计划收口失败");
      await refresh().catch(() => undefined);
    } finally {
      setPending(false);
    }
  }

  async function openTask(task: Task) {
    setSelectedTask(task);
    setTaskEvents([]);
    setTaskEventsLoading(true);
    setTaskEventsHasMore(false);
    try {
      const [detail, page] = await Promise.all([
        api.task(task.id),
        api.taskEvents(task.id),
      ]);
      setSelectedTask(detail);
      updateTasks(
        tasksRef.current.map((item) => (item.id === detail.id ? detail : item)),
      );
      setTaskEvents((current) => {
        const seen = new Set(page.items.map((event) => event.sequence));
        return [
          ...page.items,
          ...current.filter((event) => !seen.has(event.sequence)),
        ];
      });
      setTaskEventsHasMore(page.hasMore);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "执行记录读取失败");
    } finally {
      setTaskEventsLoading(false);
    }
  }

  async function recoverTask(task: Task, action: string) {
    setPending(true);
    setError("");
    try {
      const plan = await api.recoverTask(task.id, action);
      setPlans((current) => [
        plan,
        ...current.filter((item) => item.id !== plan.id),
      ]);
      if (plan.requiresConfirmation) {
        setSelectedPlan(plan);
      } else {
        const approved = await api.approvePlan(plan);
        const recovered = await api.executePlan(approved.id);
        registerTask(recovered);
        void openTask(recovered);
      }
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "恢复动作创建失败");
    } finally {
      setPending(false);
    }
  }

  function registerTask(task: Task) {
    updateTasks([
      task,
      ...tasksRef.current.filter((item) => item.id !== task.id),
    ]);
    setServices((current) =>
      current.map((service) =>
        service.name === task.service
          ? { ...service, activeTaskId: task.id }
          : service,
      ),
    );
    setAutomaticTasks((current) =>
      current.map((item) =>
        item.name === task.service ? { ...item, activeTaskId: task.id } : item,
      ),
    );
  }

  async function loadMoreTasks() {
    setTasksLoadingMore(true);
    try {
      const page = await api.tasks(tasksRef.current.length);
      updateTasks(mergeTasks(tasksRef.current, page.items));
      setTasksHasMore(page.hasMore);
    } catch (reason) {
      setError(
        reason instanceof Error ? reason.message : "更多执行记录读取失败",
      );
    } finally {
      setTasksLoadingMore(false);
    }
  }

  async function loadMoreAudit() {
    setAuditLoadingMore(true);
    try {
      const page = await api.audit(auditRef.current.length);
      updateAudit(mergeAudit(auditRef.current, page.items));
      setAuditHasMore(page.hasMore);
    } catch (reason) {
      setError(
        reason instanceof Error ? reason.message : "更多审计记录读取失败",
      );
    } finally {
      setAuditLoadingMore(false);
    }
  }

  async function loadMoreTaskEvents() {
    if (!selectedTask || taskEvents.length === 0) return;
    setTaskEventsLoadingMore(true);
    try {
      const page = await api.taskEvents(
        selectedTask.id,
        taskEvents[taskEvents.length - 1].sequence,
      );
      setTaskEvents((current) => {
        const seen = new Set(current.map((event) => event.sequence));
        return [
          ...current,
          ...page.items.filter((event) => !seen.has(event.sequence)),
        ];
      });
      setTaskEventsHasMore(page.hasMore);
    } catch (reason) {
      setError(
        reason instanceof Error ? reason.message : "更多执行记录读取失败",
      );
    } finally {
      setTaskEventsLoadingMore(false);
    }
  }

  function openService(name: string) {
    setSelectedService(name);
    navigateTo("services");
  }

  function navigateTo(nextView: ViewName) {
    setError("");
    setView(nextView);
  }

  async function reconcileService(service: string) {
    setBusyAction(service);
    setError("");
    try {
      const result = await api.reconcileService(service);
      setServiceStates((current) => [
        result.state,
        ...current.filter((item) => item.service !== service),
      ]);
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "状态核对失败"));
    } finally {
      setBusyAction("");
    }
  }

  async function registerServer(node: Parameters<OpsAPI["registerServer"]>[0]) {
    setBusyAction("fleet");
    setError("");
    try {
      await api.registerServer(node);
      await refreshFleet();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "服务器登记失败"));
    } finally {
      setBusyAction("");
    }
  }

  async function registerRunner(node: Parameters<OpsAPI["registerRunner"]>[0]) {
    setBusyAction("fleet");
    setError("");
    try {
      await api.registerRunner(node);
      await refreshFleet();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Runner 登记失败"));
    } finally {
      setBusyAction("");
    }
  }

  async function createBatch(task: BatchTask) {
    setBusyAction("create");
    setError("");
    try {
      const created = await api.createBatch(task);
      setBatches((current) => [
        created,
        ...current.filter((item) => item.id !== created.id),
      ]);
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "批量作业创建失败"));
    } finally {
      setBusyAction("");
    }
  }

  async function runBatch(id: string) {
    setBusyAction(id);
    setError("");
    try {
      const updated = await api.runBatch(id);
      setBatches((current) =>
        current.map((item) => (item.id === updated.id ? updated : item)),
      );
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "批量作业启动失败"));
    } finally {
      setBusyAction("");
    }
  }

  async function approveBatch(
    id: string,
    digest: string,
    confirmation: string,
  ) {
    setBusyAction(id);
    setError("");
    try {
      const approved = await api.approveBatch(id, digest, confirmation);
      setBatches((current) =>
        current.map((item) => (item.id === approved.id ? approved : item)),
      );
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "批量作业批准失败"));
    } finally {
      setBusyAction("");
    }
  }

  async function runRecoveryAction(
    service: string,
    action: string,
    recoveryPointId = "",
  ) {
    const key = `${service}/${action}`;
    setBusyAction(key);
    setError("");
    try {
      if (action === "inspect") {
        await api.recoveryCenterForService(service);
      } else {
        await api.recoveryAction(service, action, recoveryPointId);
      }
      await refreshRecoveryCenter();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "恢复动作提交失败"));
    } finally {
      setBusyAction("");
    }
  }

  async function restoreRecoveryPoint(
    service: string,
    recoveryPointId: string,
    mode: "isolated" | "production",
    confirmation: string,
  ) {
    const key = `${service}/restore`;
    setBusyAction(key);
    setError("");
    try {
      const result = await api.restoreRecoveryPoint({
        service,
        recoveryPointId,
        mode,
        confirmation,
      });
      if (isReleasePlan(result)) {
        setPlans((current) => [
          result,
          ...current.filter((item) => item.id !== result.id),
        ]);
        setSelectedPlan(result);
      }
      await refreshRecoveryCenter();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "恢复请求提交失败"));
    } finally {
      setBusyAction("");
    }
  }

  async function saveCompose(body: Parameters<OpsAPI["editCompose"]>[1]) {
    setBusyAction("compose");
    setError("");
    try {
      const updated = await api.editCompose(selectedComposeService, body);
      setComposeByService((current) => ({
        ...current,
        [selectedComposeService]: updated,
      }));
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Compose 配置提交失败"));
    } finally {
      setBusyAction("");
    }
  }

  async function approveComposeRevision(
    revision: Parameters<OpsAPI["approveComposeRevision"]>[0],
    confirmation: string,
  ) {
    setBusyAction(`compose-approve:${revision.id}`);
    setError("");
    try {
      await api.approveComposeRevision(revision, confirmation);
      await refreshCompose();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Compose 提案批准失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function applyComposeRevision(
    revision: Parameters<OpsAPI["applyComposeRevision"]>[0],
  ) {
    setBusyAction(`compose-apply:${revision.id}`);
    setError("");
    try {
      await api.applyComposeRevision(revision);
      await refreshCompose();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Compose 提案应用失败"));
      await refreshCompose().catch(() => undefined);
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function rollbackComposeRevision(
    revision: Parameters<OpsAPI["rollbackComposeRevision"]>[0],
    confirmation: string,
  ) {
    setBusyAction(`compose-rollback:${revision.id}`);
    setError("");
    try {
      await api.rollbackComposeRevision(revision, confirmation);
      await refreshCompose();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Compose 提案回滚失败"));
      await refreshCompose().catch(() => undefined);
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function createKubernetesOperation(
    body: Parameters<OpsAPI["createKubernetesOperation"]>[0],
  ) {
    setBusyAction("kubernetes");
    setError("");
    try {
      const operation = await api.createKubernetesOperation(body);
      setKubernetes((current) =>
        current
          ? {
              ...current,
              operations: [operation, ...(current.operations ?? [])],
            }
          : current,
      );
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Kubernetes 操作提交失败"));
    } finally {
      setBusyAction("");
    }
  }

  async function createKubernetesPlan(
    body: Parameters<OpsAPI["createKubernetesPlan"]>[0],
  ) {
    setBusyAction("kubernetes-plan");
    setError("");
    try {
      await api.createKubernetesPlan(body);
      await refreshKubernetes();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Kubernetes 计划创建失败"));
    } finally {
      setBusyAction("");
    }
  }

  async function approveKubernetesPlan(
    plan: KubernetesPlan,
    confirmation: string,
  ) {
    setBusyAction(`kubernetes-approve:${plan.id}`);
    setError("");
    try {
      await api.approveKubernetesPlan(plan, confirmation);
      await refreshKubernetes();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Kubernetes 计划批准失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function executeKubernetesPlan(plan: KubernetesPlan) {
    setBusyAction(`kubernetes-execute:${plan.id}`);
    setError("");
    try {
      const operation = await api.executeKubernetesPlan(plan);
      setKubernetes((current) =>
        current
          ? {
              ...current,
              operations: [
                operation,
                ...(current.operations ?? []).filter(
                  (item) => item.id !== operation.id,
                ),
              ],
            }
          : current,
      );
      await refreshKubernetes();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Kubernetes 计划执行失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function createKubernetesRollbackPlan(
    plan: KubernetesPlan,
    rollbackToPlanId: string,
  ) {
    setBusyAction(`kubernetes-rollback:${plan.id}`);
    setError("");
    try {
      await api.createKubernetesRollbackPlan(plan, rollbackToPlanId);
      await refreshKubernetes();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Kubernetes 回滚计划创建失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function createExtensionPlan(
    body: Parameters<OpsAPI["createExtensionPlan"]>[0],
  ) {
    setBusyAction("extension-plan");
    setError("");
    try {
      await api.createExtensionPlan(body);
      await refreshExtensions();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "扩展计划创建失败"));
    } finally {
      setBusyAction("");
    }
  }

  async function uploadExtension(manifest: ExtensionManifest, content: string) {
    setBusyAction("extension-upload");
    setError("");
    try {
      await api.uploadExtension(manifest, content);
      await refreshExtensions();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "扩展包上传失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function approveExtensionPlan(
    plan: ExtensionPlan,
    confirmation: string,
  ) {
    setBusyAction(`extension-approve:${plan.id}`);
    setError("");
    try {
      await api.approveExtensionPlan(plan, confirmation);
      await refreshExtensions();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "扩展计划批准失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function executeExtensionPlan(plan: ExtensionPlan) {
    setBusyAction(`extension-execute:${plan.id}`);
    setError("");
    try {
      await api.executeExtensionPlan(plan);
      await refreshExtensions();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "扩展计划执行失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function saveAutoUpdatePolicy(
    service: string,
    input: AutoUpdatePolicyInput,
  ) {
    setBusyAction(`auto-updates:${service}`);
    setError("");
    try {
      const updated = await api.updateAutoUpdatePolicy(service, input);
      setAutoUpdates((current) => [
        updated,
        ...current.filter((item) => item.service !== service),
      ]);
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "自动更新策略保存失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function evaluateAutoUpdatePolicies() {
    setBusyAction("auto-updates-evaluate");
    setError("");
    try {
      setAutoUpdateEvaluations(await api.evaluateAutoUpdates());
      await refreshAutoUpdates();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "自动更新策略评估失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function createTerminalPlan(body: {
    objectId: string;
    input: string;
    confirmation: string;
  }) {
    setBusyAction("terminal-create");
    setError("");
    try {
      const plan = await api.createTerminalShellPlan(body);
      setTerminalPlans((current) => [
        plan,
        ...current.filter((item) => item.id !== plan.id),
      ]);
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "终端计划创建失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function approveTerminalPlan(
    plan: TerminalShellPlan,
    confirmation: string,
  ) {
    setBusyAction(`terminal-approve:${plan.id}`);
    setError("");
    try {
      const updated = await api.approveTerminalShellPlan(plan, confirmation);
      setTerminalPlans((current) =>
        current.map((item) => (item.id === updated.id ? updated : item)),
      );
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "终端计划批准失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function executeTerminalPlan(plan: TerminalShellPlan, input: string) {
    setBusyAction(`terminal-execute:${plan.id}`);
    setError("");
    try {
      const updated = await api.executeTerminalShellPlan(plan, input);
      setTerminalPlans((current) =>
        current.map((item) => (item.id === updated.id ? updated : item)),
      );
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "终端计划执行失败"));
      await refreshTerminal().catch(() => undefined);
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function runTerminal(body: {
    objectId: string;
    command: string;
    confirmation?: string;
  }) {
    setBusyAction("terminal-run");
    setError("");
    try {
      setTerminalOutput(await api.runTerminal(body));
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "受控命令执行失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function readManagedFile(rootId: string, path: string) {
    setBusyAction("files-read");
    setError("");
    try {
      setManagedFile(await api.managedFile(rootId, path));
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "受管文件读取失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function proposeManagedFile(body: {
    rootId: string;
    path: string;
    content: string;
    expectedDigest: string;
  }) {
    setBusyAction("files-propose");
    setError("");
    try {
      const proposal = await api.proposeManagedFile(body);
      setFileProposals((current) => [
        proposal,
        ...current.filter((item) => item.id !== proposal.id),
      ]);
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "受管文件提案失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function approveManagedFile(
    proposal: ManagedFileProposal,
    confirmation: string,
  ) {
    setBusyAction(`files-approve:${proposal.id}`);
    setError("");
    try {
      const updated = await api.approveManagedFileProposal(
        proposal,
        confirmation,
      );
      setFileProposals((current) =>
        current.map((item) => (item.id === updated.id ? updated : item)),
      );
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "文件提案批准失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function applyManagedFile(proposal: ManagedFileProposal) {
    setBusyAction(`files-apply:${proposal.id}`);
    setError("");
    try {
      const updated = await api.applyManagedFileProposal(proposal);
      setFileProposals((current) =>
        current.map((item) => (item.id === updated.id ? updated : item)),
      );
      if (
        managedFile?.rootId === proposal.rootId &&
        managedFile.path === proposal.path
      ) {
        await readManagedFile(proposal.rootId, proposal.path);
      }
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "文件提案应用失败"));
      await refreshFiles().catch(() => undefined);
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function rollbackManagedFile(
    proposal: ManagedFileProposal,
    confirmation: string,
  ) {
    setBusyAction(`files-rollback:${proposal.id}`);
    setError("");
    try {
      const updated = await api.rollbackManagedFileProposal(
        proposal,
        confirmation,
      );
      setFileProposals((current) =>
        current.map((item) => (item.id === updated.id ? updated : item)),
      );
      if (
        managedFile?.rootId === proposal.rootId &&
        managedFile.path === proposal.path
      ) {
        await readManagedFile(proposal.rootId, proposal.path);
      }
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "文件提案回滚失败"));
      await refreshFiles().catch(() => undefined);
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function createAccessChange(body: AccessControlUpdate) {
    setBusyAction("access-change-create");
    setError("");
    try {
      await api.createAccessChange({
        ...body,
        expectedVersion: access?.version,
      });
      await refreshAccess();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "访问策略审批变更创建失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function approveAccessChange(
    change: AccessChange,
    confirmation: string,
  ) {
    setBusyAction(`access-change-approve:${change.id}`);
    setError("");
    try {
      await api.approveAccessChange(change, confirmation);
      await refreshAccess();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "访问策略变更批准失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function applyAccessChange(change: AccessChange) {
    setBusyAction(`access-change-apply:${change.id}`);
    setError("");
    try {
      await api.applyAccessChange(change);
      await refreshAccess();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "访问策略变更执行失败"));
      await refreshAccess().catch(() => undefined);
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function rejectAccessChange(change: AccessChange, reason: string) {
    setBusyAction(`access-change-reject:${change.id}`);
    setError("");
    try {
      await api.rejectAccessChange(change, reason);
      await refreshAccess();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "访问策略变更拒绝失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function prepareRunnerUpdate(input: RunnerUpdatePrepareInput) {
    setBusyAction("runner-prepare");
    setError("");
    try {
      await api.prepareRunnerUpdate(input);
      await refreshRunnerUpdate();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Runner 更新准备失败"));
      await refreshRunnerUpdate().catch(() => undefined);
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function activateRunnerUpdate(id: string, confirmation: string) {
    setBusyAction(`runner-activate:${id}`);
    setError("");
    try {
      await api.activateRunnerUpdate(id, confirmation);
      await refreshRunnerUpdate();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Runner 更新激活失败"));
      await refreshRunnerUpdate().catch(() => undefined);
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function cancelRunnerUpdate(id: string, confirmation: string) {
    setBusyAction(`runner-cancel:${id}`);
    setError("");
    try {
      await api.cancelRunnerUpdate(id, confirmation);
      await refreshRunnerUpdate();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Runner 更新取消失败"));
      await refreshRunnerUpdate().catch(() => undefined);
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function resolveRunnerUpdate(
    id: string,
    confirmation: string,
    evidence: RunnerUpdateResolutionEvidence,
  ) {
    setBusyAction(`runner-resolve:${id}`);
    setError("");
    try {
      await api.resolveRunnerUpdate(id, confirmation, evidence);
      await refreshRunnerUpdate();
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Runner 更新人工收口失败"));
      await refreshRunnerUpdate().catch(() => undefined);
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function createFleetRunnerUpdate(input: FleetRunnerUpdatePlanInput) {
    setBusyAction("runner-fleet-create");
    setError("");
    try {
      const plan = await api.createFleetRunnerUpdatePlan(input);
      setFleetRunnerUpdate((current) =>
        current
          ? {
              ...current,
              plans: [
                plan,
                ...current.plans.filter((item) => item.id !== plan.id),
              ],
            }
          : current,
      );
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Runner Fleet 更新计划创建失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function approveFleetRunnerUpdate(
    plan: FleetRunnerUpdatePlan,
    confirmation: string,
  ) {
    setBusyAction(`runner-fleet-approve:${plan.id}`);
    setError("");
    try {
      const updated = await api.approveFleetRunnerUpdatePlan(
        plan,
        confirmation,
      );
      setFleetRunnerUpdate((current) =>
        current
          ? {
              ...current,
              plans: [
                updated,
                ...current.plans.filter((item) => item.id !== updated.id),
              ],
            }
          : current,
      );
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Runner Fleet 更新计划批准失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function executeFleetRunnerUpdate(plan: FleetRunnerUpdatePlan) {
    setBusyAction(`runner-fleet-execute:${plan.id}`);
    setError("");
    try {
      const updated = await api.executeFleetRunnerUpdatePlan(plan);
      setFleetRunnerUpdate((current) =>
        current
          ? {
              ...current,
              plans: [
                updated,
                ...current.plans.filter((item) => item.id !== updated.id),
              ],
            }
          : current,
      );
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Runner Fleet 更新计划执行失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function cancelFleetRunnerUpdate(
    plan: FleetRunnerUpdatePlan,
    confirmation: string,
  ) {
    setBusyAction(`runner-fleet-cancel:${plan.id}`);
    setError("");
    try {
      const updated = await api.cancelFleetRunnerUpdatePlan(plan, confirmation);
      setFleetRunnerUpdate((current) =>
        current
          ? {
              ...current,
              plans: [
                updated,
                ...current.plans.filter((item) => item.id !== updated.id),
              ],
            }
          : current,
      );
    } catch (reasonValue) {
      setError(errorMessage(reasonValue, "Runner Fleet 更新计划取消失败"));
      throw reasonValue;
    } finally {
      setBusyAction("");
    }
  }

  async function rotateCredential(
    secret: string,
    expiresAt: string,
    confirmation: string,
  ) {
    setError("");
    try {
      const rotation = await api.rotateCredential(
        secret,
        expiresAt,
        confirmation,
      );
      setCredentialProfile((current) =>
        current ? { ...current, lastRotation: rotation } : current,
      );
      await Promise.all([refreshCredential(), refresh()]);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "凭据轮换失败");
      await refreshCredential().catch(() => undefined);
      throw reason;
    } finally {
      secret = "";
    }
  }

  async function closeCredentialRotation(
    rotation: CredentialRotation,
    confirmation: string,
  ) {
    setError("");
    try {
      await api.closeCredentialRotation(rotation.id, confirmation);
      await Promise.all([refreshCredential(), refresh()]);
    } catch (reason) {
      setError(reason instanceof Error ? reason.message : "凭据轮换收口失败");
      await refreshCredential().catch(() => undefined);
      throw reason;
    }
  }

  if (loading) {
    return (
      <div className="boot-state">
        <LoaderCircle className="spin" size={24} />
        <span>连接控制面</span>
      </div>
    );
  }

  if (!email) {
    return (
      <div className="boot-state error">
        <AlertCircle size={24} />
        <span>{error || "身份验证失败"}</span>
      </div>
    );
  }

  return (
    <Shell
      view={view}
      onView={navigateTo}
      email={email}
      environment={environment}
      connected={connected}
      links={links}
    >
      {error && (
        <div className="toast error-toast" role="alert">
          <AlertCircle size={17} aria-hidden="true" />
          <span>{error}</span>
          <button type="button" onClick={() => setError("")} title="关闭">
            <X size={16} />
          </button>
        </div>
      )}
      {view === "overview" && (
        <Overview
          services={services}
          automaticTasks={automaticTasks}
          tasks={tasks}
          plans={plans}
          alerts={alerts}
          alertsError={alertsError}
          alertsURL={links.alerts}
          onService={openService}
          onTask={openTask}
          onPlan={setSelectedPlan}
          onAutomaticTasks={() => navigateTo("automatic-tasks")}
        />
      )}
      {view === "lifecycle" && (
        <Lifecycle
          states={serviceStates}
          services={services}
          loading={statesLoading}
          available={statesAvailable}
          error={statesError}
          busy={busyAction}
          onRefresh={() => void refreshStates()}
          onAction={beginAction}
          onReconcile={reconcileService}
        />
      )}
      {view === "fleet" && (
        <FleetView
          fleet={fleet}
          loading={fleetLoading}
          available={fleetAvailable}
          error={fleetError}
          busy={busyAction === "fleet"}
          onRefresh={() => void refreshFleet()}
          onRegisterServer={registerServer}
          onRegisterRunner={registerRunner}
        />
      )}
      {view === "batches" && (
        <Batches
          batches={batches}
          loading={batchesLoading}
          available={batchesAvailable}
          error={batchesError}
          busy={busyAction}
          onRefresh={() => void refreshBatches()}
          onCreate={createBatch}
          onApprove={approveBatch}
          onRun={runBatch}
          currentActorHash={currentActorHash}
        />
      )}
      {view === "recovery" && (
        <RecoveryCenter
          items={recoveryCenter}
          loading={recoveryLoading}
          available={recoveryAvailable}
          error={recoveryError}
          busy={busyAction}
          onRefresh={() => void refreshRecoveryCenter()}
          onAction={runRecoveryAction}
          onRestore={restoreRecoveryPoint}
        />
      )}
      {view === "configuration" && (
        <Configuration
          services={services}
          selectedService={selectedComposeService}
          compose={composeByService[selectedComposeService] ?? null}
          composeLoading={composeLoading}
          composeAvailable={composeAvailable}
          composeError={composeError}
          kubernetes={kubernetes}
          kubernetesLoading={kubernetesLoading}
          kubernetesAvailable={kubernetesAvailable}
          kubernetesError={kubernetesError}
          extensions={extensions}
          extensionsLoading={extensionsLoading}
          extensionsAvailable={extensionsAvailable}
          extensionsError={extensionsError}
          currentActorHash={currentActorHash}
          busy={busyAction}
          onService={setSelectedComposeService}
          onComposeRefresh={() => void refreshCompose()}
          onSaveCompose={saveCompose}
          onComposeApprove={approveComposeRevision}
          onComposeApply={applyComposeRevision}
          onComposeRollback={rollbackComposeRevision}
          onKubernetesRefresh={() => void refreshKubernetes()}
          onKubernetesOperation={createKubernetesOperation}
          onKubernetesPlan={createKubernetesPlan}
          onKubernetesApprove={approveKubernetesPlan}
          onKubernetesExecute={executeKubernetesPlan}
          onKubernetesRollbackPlan={createKubernetesRollbackPlan}
          onExtensionsRefresh={() => void refreshExtensions()}
          onExtensionUpload={uploadExtension}
          onExtensionPlan={createExtensionPlan}
          onExtensionApprove={approveExtensionPlan}
          onExtensionExecute={executeExtensionPlan}
        />
      )}
      {view === "auto-updates" && (
        <AutoUpdates
          policies={autoUpdates}
          evaluations={autoUpdateEvaluations}
          loading={autoUpdatesLoading}
          available={autoUpdatesAvailable}
          error={autoUpdatesError}
          busy={busyAction}
          onRefresh={() => void refreshAutoUpdates()}
          onSave={saveAutoUpdatePolicy}
          onEvaluate={evaluateAutoUpdatePolicies}
        />
      )}
      {view === "terminal" && (
        <Terminal
          commands={terminalCommands}
          plans={terminalPlans}
          lastOutput={terminalOutput}
          loading={terminalLoading}
          available={terminalAvailable}
          breakGlassAvailable={terminalShellAvailable}
          error={terminalError}
          busy={busyAction}
          currentActorHash={currentActorHash}
          onRefresh={() => void refreshTerminal()}
          onCreatePlan={createTerminalPlan}
          onApprove={approveTerminalPlan}
          onExecute={executeTerminalPlan}
          onRun={runTerminal}
        />
      )}
      {view === "files" && (
        <Files
          file={managedFile}
          proposals={fileProposals}
          loading={filesLoading}
          available={filesAvailable}
          error={filesError}
          busy={busyAction}
          currentActorHash={currentActorHash}
          onRefresh={() => void refreshFiles()}
          onRead={readManagedFile}
          onPropose={proposeManagedFile}
          onApprove={approveManagedFile}
          onApply={applyManagedFile}
          onRollback={rollbackManagedFile}
        />
      )}
      {view === "runner-update" && (
        <RunnerUpdate
          status={runnerUpdate}
          loading={runnerUpdateLoading}
          available={runnerUpdateAvailable}
          error={runnerUpdateError}
          busy={busyAction}
          onRefresh={() => void refreshRunnerUpdate()}
          onPrepare={prepareRunnerUpdate}
          onActivate={activateRunnerUpdate}
          onCancel={cancelRunnerUpdate}
          onResolve={resolveRunnerUpdate}
        />
      )}
      {view === "runner-fleet-update" && (
        <RunnerFleetUpdate
          status={fleetRunnerUpdate}
          loading={fleetRunnerUpdateLoading}
          available={fleetRunnerUpdateAvailable}
          error={fleetRunnerUpdateError}
          busy={busyAction}
          onRefresh={() => void refreshFleetRunnerUpdate()}
          onCreate={createFleetRunnerUpdate}
          onApprove={approveFleetRunnerUpdate}
          onExecute={executeFleetRunnerUpdate}
          onCancel={cancelFleetRunnerUpdate}
        />
      )}
      {view === "access" && (
        <AccessControl
          access={access}
          loading={accessLoading}
          available={accessAvailable}
          error={accessError}
          busy={busyAction}
          onRefresh={() => void refreshAccess()}
          onCreateChange={createAccessChange}
          onApproveChange={approveAccessChange}
          onApplyChange={applyAccessChange}
          onRejectChange={rejectAccessChange}
        />
      )}
      {view === "automatic-tasks" && (
        <AutomaticTasks
          tasks={automaticTasks}
          busyAction={busyAction}
          onRefresh={() =>
            void refresh().catch((reason) =>
              setError(reason instanceof Error ? reason.message : "刷新失败"),
            )
          }
          onAction={beginAction}
        />
      )}
      {view === "services" && (
        <Services
          services={services}
          selected={selectedService}
          discoveries={discoveries}
          busyAction={busyAction}
          plans={plans}
          onSelect={setSelectedService}
          onRefresh={() =>
            void refresh().catch((reason) =>
              setError(reason instanceof Error ? reason.message : "刷新失败"),
            )
          }
          onAction={beginAction}
          onPlan={setSelectedPlan}
        />
      )}
      {view === "credentials" && (
        <Credentials
          profile={credentialProfile}
          loading={credentialLoading}
          onRefresh={() =>
            void refreshCredential().catch((reason) =>
              setError(
                reason instanceof Error ? reason.message : "凭据状态刷新失败",
              ),
            )
          }
          onRotate={rotateCredential}
          onClose={closeCredentialRotation}
        />
      )}
      {view === "tasks" && (
        <Tasks
          tasks={tasks}
          hasMore={tasksHasMore}
          loadingMore={tasksLoadingMore}
          onTask={openTask}
          onLoadMore={() => void loadMoreTasks()}
        />
      )}
      {view === "audit" && (
        <Audit
          entries={audit}
          hasMore={auditHasMore}
          loadingMore={auditLoadingMore}
          onLoadMore={() => void loadMoreAudit()}
        />
      )}
      {selectedPlan && (
        <ConfirmationDialog
          plan={selectedPlan}
          pending={pending}
          currentActorHash={currentActorHash}
          onCancel={() => setSelectedPlan(null)}
          onConfirm={confirmAction}
          onClosePlan={() => void closePlan()}
        />
      )}
      {selectedTask && (
        <TaskDrawer
          task={selectedTask}
          events={taskEvents}
          loading={taskEventsLoading}
          hasMore={taskEventsHasMore}
          loadingMore={taskEventsLoadingMore}
          pending={pending}
          onRecovery={(action) => void recoverTask(selectedTask, action)}
          onLoadMore={() => void loadMoreTaskEvents()}
          onClose={() => setSelectedTask(null)}
        />
      )}
    </Shell>
  );
}
