import type {
  ActiveAlert, AuditEntry, AutomaticTaskView, CredentialProfile, CredentialRotation, ManagedObjectView, OpsEvent, Page, Preview,
  AccessChange, AccessControlUpdate, AccessControlView, AutoUpdateEvaluation, AutoUpdatePolicyInput, AutoUpdatePolicyView,
  BatchOperation, BatchTask, ComposeRevision, ComposeServiceView, ExtensionPolicyView, ExtensionView, Fleet, KubernetesConfigView, KubernetesOperation, KubernetesPlan,
  ManagedFileProposal, ManagedFileView, RecoveryCenterView, RecoveryPoint, ReleasePlan, RunnerNode, RunnerUpdate, RunnerUpdatePrepareInput, RunnerUpdateResolutionEvidence, RunnerUpdateStatus,
  TerminalCommand, TerminalOutput, TerminalShellPlan,
  ServiceState, ServerNode, ServiceView, SessionResponse, Task,
} from './types'

class APIError extends Error {
  status: number
  payload: unknown

  constructor(status: number, message: string, payload?: unknown) {
    super(message)
    this.status = status
    this.payload = payload
  }
}

function isBatchOperation(value: BatchTask | BatchOperation): value is BatchOperation {
  return 'task' in value && 'items' in value
}

function normalizeBatch(value: BatchTask | BatchOperation): BatchTask {
  if (isBatchOperation(value)) {
    const operation = value
    const task = operation.task
    return {
      ...task,
      id: operation.id || task.id,
      action: operation.action || task.action,
      nodes: operation.items?.map((item) => ({
        id: item.id,
        action: operation.action || task.action,
        targetId: item.service,
        dependencies: item.dependsOn,
        state: item.state,
        error: item.error,
      })) ?? task.nodes,
      state: operationStateToTaskState(operation.state),
      summary: operation.summary || task.summary,
      error: operation.error || task.error,
      createdAt: operation.createdAt || task.createdAt,
      startedAt: operation.startedAt || task.startedAt,
      finishedAt: operation.finishedAt || task.finishedAt,
      digest: operation.digest,
      confirmationPhrase: operation.confirmationPhrase,
      approvedAt: operation.approvedAt,
      operationState: operation.state,
    }
  }
  return value
}

function operationStateToTaskState(state: BatchOperation['state']): BatchTask['state'] {
  switch (state) {
    case 'pending_approval': return 'pending'
    case 'approved': return 'planning'
    case 'observing': return 'running'
    default: return state
  }
}

interface ExtensionPayload extends Partial<ExtensionView> {
  manifest?: Omit<ExtensionView, 'state' | 'stored' | 'createdAt'>
  storageDigest?: string
}

interface ExtensionPolicyPayload {
  enabled: boolean
  trustedPublishers?: string[]
  requireSignature?: boolean
  sandbox?: string
  extensions?: ExtensionPayload[]
}

function normalizeExtension(value: ExtensionPayload): ExtensionView {
  return {
    ...value.manifest,
    id: value.manifest?.id ?? value.id ?? '',
    version: value.manifest?.version ?? value.version,
    type: value.manifest?.type ?? value.type,
    entrypoint: value.manifest?.entrypoint ?? value.entrypoint,
    digest: value.manifest?.digest ?? value.digest ?? value.storageDigest,
    signature: value.manifest?.signature ?? value.signature,
    permissions: value.manifest?.permissions ?? value.permissions,
    allowedObjects: value.manifest?.allowedObjects ?? value.allowedObjects,
    publisher: value.manifest?.publisher ?? value.publisher,
    state: value.state,
    stored: value.stored,
    createdAt: value.createdAt,
  }
}

function isComposeRevision(value: ComposeServiceView | ComposeRevision): value is ComposeRevision {
  return typeof value.id === 'string' && typeof value.source === 'string' && typeof value.validated === 'boolean'
}

async function parseResponse<T>(response: Response): Promise<T> {
  const payload = (await response.json()) as T & { error?: string }
  if (!response.ok) {
    throw new APIError(response.status, payload.error ?? `请求失败（HTTP ${response.status}）`, payload)
  }
  return payload
}

export class OpsAPI {
  private csrfToken = ''
  private executionKeys = new Map<string, string>()
  private closureKeys = new Map<string, string>()
  private planKeys = new Map<string, string>()
  private credentialRotationKey = ''
  private credentialClosureKeys = new Map<string, string>()
  private runnerPrepareKey = ''
  private runnerActivationKeys = new Map<string, string>()
  private runnerCancellationKeys = new Map<string, string>()
  private runnerResolutionKeys = new Map<string, string>()
  private terminalPlanKeys = new Map<string, string>()
  private terminalExecutionKeys = new Map<string, string>()
  private fileProposalKeys = new Map<string, string>()
  private fileApplyKeys = new Map<string, string>()
  private fileRollbackKeys = new Map<string, string>()
  private composeApplyKeys = new Map<string, string>()
  private composeRollbackKeys = new Map<string, string>()

  async session(): Promise<SessionResponse> {
    const response = await fetch('/api/session', { credentials: 'same-origin' })
    const session = await parseResponse<SessionResponse>(response)
    this.csrfToken = session.csrfToken
    return session
  }

  async services(): Promise<ServiceView[]> {
    const response = await fetch('/api/services')
    return (await parseResponse<{ services: ServiceView[] }>(response)).services ?? []
  }

  async states(): Promise<ServiceState[]> {
    const response = await fetch('/api/states', { cache: 'no-store' })
    const payload = await parseResponse<{ states?: ServiceState[] } | ServiceState[]>(response)
    return Array.isArray(payload) ? payload : payload.states ?? []
  }

  async serviceState(name: string): Promise<ServiceState> {
    const response = await fetch(`/api/services/${encodeURIComponent(name)}/state`, { cache: 'no-store' })
    return parseResponse<ServiceState>(response)
  }

  async reconcileService(name: string): Promise<{ state: ServiceState; actionRequired?: boolean }> {
    return this.mutate<{ state: ServiceState; actionRequired?: boolean }>(
      `/api/services/${encodeURIComponent(name)}/reconcile`, {}, 'POST',
    )
  }

  async fleet(): Promise<Fleet> {
    const response = await fetch('/api/fleet', { cache: 'no-store' })
    const payload = await parseResponse<Fleet & { fleet?: Fleet }>(response)
    return payload.fleet ?? { servers: payload.servers ?? [], runners: payload.runners ?? [] }
  }

  async registerServer(node: ServerNode): Promise<ServerNode> {
    return this.mutate<ServerNode>(`/api/fleet/servers/${encodeURIComponent(node.id)}`, node, 'PUT')
  }

  async registerRunner(node: RunnerNode): Promise<RunnerNode> {
    return this.mutate<RunnerNode>(`/api/fleet/runners/${encodeURIComponent(node.id)}`, node, 'PUT')
  }

  async objects(): Promise<ManagedObjectView[]> {
    const response = await fetch('/api/objects')
    return (await parseResponse<{ objects: ManagedObjectView[] }>(response)).objects ?? []
  }

  async automaticTasks(): Promise<AutomaticTaskView[]> {
    const response = await fetch('/api/automatic-tasks')
    return (await parseResponse<{ automaticTasks: AutomaticTaskView[] }>(response)).automaticTasks ?? []
  }

  async autoUpdates(): Promise<AutoUpdatePolicyView[]> {
    const response = await fetch('/api/auto-updates', { cache: 'no-store' })
    const payload = await parseResponse<{ policies?: AutoUpdatePolicyView[] }>(response)
    return payload.policies ?? []
  }

  async updateAutoUpdatePolicy(service: string, input: AutoUpdatePolicyInput): Promise<AutoUpdatePolicyView> {
    return this.mutate<AutoUpdatePolicyView>(`/api/auto-updates/${encodeURIComponent(service)}`, {
      service, ...input, idempotencyKey: crypto.randomUUID(),
    }, 'PUT')
  }

  async evaluateAutoUpdates(): Promise<AutoUpdateEvaluation[]> {
    const payload = await this.mutate<{ evaluations?: AutoUpdateEvaluation[] }>('/api/auto-updates/evaluate', {})
    return payload.evaluations ?? []
  }

  async alerts(): Promise<ActiveAlert[]> {
    const response = await fetch('/api/alerts')
    return (await parseResponse<{ alerts: ActiveAlert[] }>(response)).alerts ?? []
  }

  async credentialProfile(): Promise<CredentialProfile> {
    const response = await fetch('/api/credentials/github-alertmanager', { cache: 'no-store' })
    return parseResponse<CredentialProfile>(response)
  }

  async rotateCredential(secret: string, expiresAt: string, confirmation: string): Promise<CredentialRotation> {
    if (!this.credentialRotationKey) this.credentialRotationKey = crypto.randomUUID()
    try {
      const rotation = await this.mutate<CredentialRotation>('/api/credentials/github-alertmanager/rotate', {
        credentialType: 'github_alertmanager', secret, expiresAt, confirmation,
        idempotencyKey: this.credentialRotationKey,
      })
      this.credentialRotationKey = ''
      return rotation
    } catch (reason) {
      if (reason instanceof APIError) this.credentialRotationKey = ''
      throw reason
    } finally {
      secret = ''
    }
  }

  async closeCredentialRotation(rotationID: string, confirmation: string): Promise<CredentialRotation> {
    const key = this.credentialClosureKeys.get(rotationID) ?? crypto.randomUUID()
    this.credentialClosureKeys.set(rotationID, key)
    return this.mutate<CredentialRotation>(`/api/credential-rotations/${encodeURIComponent(rotationID)}/close`, {
      confirmation, idempotencyKey: key,
    })
  }

  async tasks(offset = 0): Promise<Page<Task>> {
    const response = await fetch(`/api/tasks?limit=100&offset=${offset}`)
    const payload = await parseResponse<{ tasks: Task[]; hasMore: boolean }>(response)
    return { items: payload.tasks ?? [], hasMore: payload.hasMore }
  }

  async taskEvents(taskID: string, after = 0): Promise<Page<OpsEvent>> {
    const response = await fetch(`/api/tasks/${encodeURIComponent(taskID)}/events?limit=200&after=${after}`)
    const payload = await parseResponse<{ events: OpsEvent[]; hasMore: boolean }>(response)
    return { items: payload.events ?? [], hasMore: payload.hasMore }
  }

  async task(taskID: string): Promise<Task> {
    const response = await fetch(`/api/tasks/${encodeURIComponent(taskID)}`)
    return parseResponse<Task>(response)
  }

  async batches(offset = 0): Promise<Page<BatchTask>> {
    const response = await fetch(`/api/batches?limit=100&offset=${offset}`, { cache: 'no-store' })
    const payload = await parseResponse<{ batches?: Array<BatchTask | BatchOperation>; items?: Array<BatchTask | BatchOperation>; hasMore?: boolean }>(response)
    const items = (payload.batches ?? payload.items ?? []).map(normalizeBatch)
    return { items, hasMore: Boolean(payload.hasMore) }
  }

  async batch(id: string): Promise<BatchTask> {
    const response = await fetch(`/api/batches/${encodeURIComponent(id)}`, { cache: 'no-store' })
    return normalizeBatch(await parseResponse<BatchTask | BatchOperation>(response))
  }

  async createBatch(task: BatchTask): Promise<BatchTask> {
    const payload = {
      action: task.action ?? task.capability ?? '',
      target: (task as BatchTask & { target?: string }).target ?? '',
      targetIds: task.targetIds ?? [],
      targetSelector: task.targetSelector,
      batchPolicy: task.batchPolicy,
      concurrency: task.concurrency,
      failurePolicy: task.failurePolicy,
      changeWindow: task.changeWindow,
      idempotencyKey: crypto.randomUUID(),
    }
    return normalizeBatch(await this.mutate<BatchTask | BatchOperation>('/api/batches', payload))
  }

  async runBatch(id: string): Promise<BatchTask> {
    return normalizeBatch(await this.mutate<BatchTask | BatchOperation>(`/api/batches/${encodeURIComponent(id)}/run`, {
      idempotencyKey: crypto.randomUUID(),
    }))
  }

  async approveBatch(id: string, digest: string, confirmation: string): Promise<BatchTask> {
    return normalizeBatch(await this.mutate<BatchTask | BatchOperation>(`/api/batches/${encodeURIComponent(id)}/approve`, {
      digest,
      confirmation,
    }))
  }

  async plans(offset = 0): Promise<Page<ReleasePlan>> {
    const response = await fetch(`/api/plans?limit=100&offset=${offset}`)
    const payload = await parseResponse<{ plans: ReleasePlan[]; hasMore: boolean }>(response)
    return { items: payload.plans ?? [], hasMore: payload.hasMore }
  }

  async createPlan(service: string, action: string, target = ''): Promise<ReleasePlan> {
    const requestKey = `${service}\u0000${action}\u0000${target}`
    const idempotencyKey = this.planKeys.get(requestKey) ?? crypto.randomUUID()
    this.planKeys.set(requestKey, idempotencyKey)
    const plan = await this.mutate<ReleasePlan>('/api/plans', { service, action, target, idempotencyKey })
    // 服务端确认创建后，后续有意发起同一动作必须使用新键；失败重试才保留旧键。
    this.planKeys.delete(requestKey)
    return plan
  }

  async approvePlan(plan: ReleasePlan, confirmation = ''): Promise<ReleasePlan> {
    return this.mutate<ReleasePlan>(`/api/plans/${encodeURIComponent(plan.id)}/approve`, {
      confirmation,
      digest: plan.digest,
    })
  }

  async executePlan(planID: string): Promise<Task> {
    const idempotencyKey = this.executionKeys.get(planID) ?? crypto.randomUUID()
    this.executionKeys.set(planID, idempotencyKey)
    return this.mutate<Task>(`/api/plans/${encodeURIComponent(planID)}/execute`, {
      idempotencyKey,
    })
  }

  async closePlan(planID: string): Promise<ReleasePlan> {
    const idempotencyKey = this.closureKeys.get(planID) ?? crypto.randomUUID()
    this.closureKeys.set(planID, idempotencyKey)
    return this.mutate<ReleasePlan>(`/api/plans/${encodeURIComponent(planID)}/close`, { idempotencyKey })
  }

  async recoverTask(taskID: string, action: string): Promise<ReleasePlan> {
    return this.mutate<ReleasePlan>(`/api/tasks/${encodeURIComponent(taskID)}/recovery`, { action })
  }

  async recoveryCenter(): Promise<RecoveryCenterView[]> {
    // Runner exposes one recovery view per service. The service list is already
    // tenant-filtered, so fan out only across objects the caller may see.
    const services = await this.services()
    const results = await Promise.allSettled(services.map((service) => this.recoveryCenterForService(service.name)))
    const failures = results.filter((result): result is PromiseRejectedResult => result.status === 'rejected')
    if (results.length > 0 && failures.length === results.length && isFeatureUnavailable(failures[0].reason)) {
      throw failures[0].reason
    }
    return results.flatMap((result) => result.status === 'fulfilled' ? [result.value] : [])
  }

  async recoveryCenterForService(service: string): Promise<RecoveryCenterView> {
    const response = await fetch(`/api/recovery-center/${encodeURIComponent(service)}`, { cache: 'no-store' })
    return parseResponse<RecoveryCenterView>(response)
  }

  async restoreRecoveryPoint(request: {
    service: string
    recoveryPointId: string
    mode: 'isolated' | 'production'
    confirmation: string
    tenantId?: string
    serverId?: string
    expectedBeforeDigest?: string
  }): Promise<RecoveryPoint | Task | ReleasePlan> {
    return this.mutate<RecoveryPoint | Task | ReleasePlan>(`/api/recovery-center/${encodeURIComponent(request.service)}/plan`, {
      ...request,
      idempotencyKey: crypto.randomUUID(),
    })
  }

  async recoveryAction(service: string, action: string, recoveryPointId = ''): Promise<unknown> {
    if (action === 'inspect') return this.recoveryCenterForService(service)
    if (action !== 'restore' && action !== 'restore-drill') {
      throw new Error(`恢复动作 ${action} 没有受支持的后端路由`)
    }
    return this.restoreRecoveryPoint({
      service,
      recoveryPointId,
      mode: action === 'restore' ? 'production' : 'isolated',
      confirmation: action === 'restore'
        ? `创建生产恢复计划 ${service}`
        : `创建隔离恢复演练恢复计划 ${service}`,
    })
  }

  async compose(service: string): Promise<ComposeServiceView> {
    try {
      const response = await fetch(`/api/compose/${encodeURIComponent(service)}`, { cache: 'no-store' })
      return await parseResponse<ComposeServiceView>(response)
    } catch (reason) {
      if (!isFeatureUnavailable(reason)) throw reason
      // Keep a short-lived compatibility path for an older Web proxy during a
      // staged rollout; the current Runner route is the one above.
      const response = await fetch(`/api/compose/services/${encodeURIComponent(service)}`, { cache: 'no-store' })
      return parseResponse<ComposeServiceView>(response)
    }
  }

  async editCompose(service: string, body: {
    content: string
    expectedDigest: string
    mode: 'validate' | 'propose'
    confirmation?: string
  }): Promise<ComposeServiceView> {
    let result: ComposeServiceView | ComposeRevision
    try {
      result = await this.mutate<ComposeServiceView | ComposeRevision>(`/api/compose/${encodeURIComponent(service)}/revisions`, {
        service, ...body, idempotencyKey: crypto.randomUUID(),
      })
    } catch (reason) {
      if (!isFeatureUnavailable(reason)) throw reason
      // Compatibility fallback for the pre-unification Web proxy.
      result = await this.mutate<ComposeServiceView | ComposeRevision>(`/api/compose/services/${encodeURIComponent(service)}`, {
        service, ...body, idempotencyKey: crypto.randomUUID(),
      })
    }
    if (!isComposeRevision(result)) return result
    const current = await this.compose(service)
    return {
      ...current,
      content: result.content ?? current.content,
      source: result.source,
      validated: result.validated,
      revisions: result.id
        ? [result, ...(current.revisions ?? []).filter((revision) => revision.id !== result.id)]
        : current.revisions,
    }
  }

  async approveComposeRevision(revision: ComposeRevision, confirmation: string): Promise<ComposeRevision> {
    return this.composeRevisionMutation(revision, 'approve', {
      digest: revision.digest,
      confirmation,
    })
  }

  async applyComposeRevision(revision: ComposeRevision): Promise<ComposeRevision> {
    const key = this.composeApplyKeys.get(revision.id) ?? crypto.randomUUID()
    this.composeApplyKeys.set(revision.id, key)
    try {
      const result = await this.composeRevisionMutation(revision, 'apply', { idempotencyKey: key })
      this.composeApplyKeys.delete(revision.id)
      return result
    } catch (reason) {
      if (reason instanceof APIError) this.composeApplyKeys.delete(revision.id)
      throw reason
    }
  }

  async rollbackComposeRevision(revision: ComposeRevision, confirmation: string): Promise<ComposeRevision> {
    const key = this.composeRollbackKeys.get(revision.id) ?? crypto.randomUUID()
    this.composeRollbackKeys.set(revision.id, key)
    try {
      const result = await this.composeRevisionMutation(revision, 'rollback', {
        confirmation,
        idempotencyKey: key,
      })
      this.composeRollbackKeys.delete(revision.id)
      return result
    } catch (reason) {
      if (reason instanceof APIError) this.composeRollbackKeys.delete(revision.id)
      throw reason
    }
  }

  private async composeRevisionMutation(
    revision: ComposeRevision,
    action: 'approve' | 'apply' | 'rollback',
    body: unknown,
  ): Promise<ComposeRevision> {
    const service = encodeURIComponent(revision.service)
    const id = encodeURIComponent(revision.id)
    try {
      return await this.mutate<ComposeRevision>(`/api/compose/${service}/revisions/${id}/${action}`, body)
    } catch (reason) {
      if (!isFeatureUnavailable(reason)) throw reason
      // A short-lived compatibility path supports proxies that expose revisions
      // as a top-level collection while the nested route rolls out.
      return this.mutate<ComposeRevision>(`/api/compose/revisions/${id}/${action}`, body)
    }
  }

  async terminalCommands(): Promise<TerminalCommand[]> {
    const response = await fetch('/api/terminal/commands', { cache: 'no-store' })
    const payload = await parseResponse<{ commands?: TerminalCommand[] }>(response)
    return payload.commands ?? []
  }

  async runTerminal(body: {
    objectId: string
    command: string
    confirmation?: string
  }): Promise<TerminalOutput> {
    return this.mutate<TerminalOutput>('/api/terminal/sessions', {
      ...body,
      idempotencyKey: crypto.randomUUID(),
    })
  }

  async terminalShellPlans(): Promise<TerminalShellPlan[]> {
    const response = await fetch('/api/terminal/shell-plans', { cache: 'no-store' })
    const payload = await parseResponse<{ plans?: TerminalShellPlan[] }>(response)
    return payload.plans ?? []
  }

  async createTerminalShellPlan(body: {
    objectId: string
    input: string
    confirmation: string
  }): Promise<TerminalShellPlan> {
    const key = `${body.objectId}:${body.input}`
    const idempotencyKey = this.terminalPlanKeys.get(key) ?? crypto.randomUUID()
    this.terminalPlanKeys.set(key, idempotencyKey)
    try {
      const plan = await this.mutate<TerminalShellPlan>('/api/terminal/shell-plans', {
        ...body, idempotencyKey,
      })
      this.terminalPlanKeys.delete(key)
      return plan
    } catch (reason) {
      if (reason instanceof APIError) this.terminalPlanKeys.delete(key)
      throw reason
    }
  }

  async approveTerminalShellPlan(plan: TerminalShellPlan, confirmation: string): Promise<TerminalShellPlan> {
    return this.mutate<TerminalShellPlan>(`/api/terminal/shell-plans/${encodeURIComponent(plan.id)}/approve`, {
      confirmation,
    })
  }

  async executeTerminalShellPlan(plan: TerminalShellPlan, input: string): Promise<TerminalShellPlan> {
    const key = this.terminalExecutionKeys.get(plan.id) ?? crypto.randomUUID()
    this.terminalExecutionKeys.set(plan.id, key)
    try {
      const result = await this.mutate<TerminalShellPlan>(`/api/terminal/shell-plans/${encodeURIComponent(plan.id)}/execute`, {
        input, idempotencyKey: key,
      })
      this.terminalExecutionKeys.delete(plan.id)
      return result
    } catch (reason) {
      if (reason instanceof APIError) this.terminalExecutionKeys.delete(plan.id)
      throw reason
    }
  }

  async managedFile(rootId: string, path = ''): Promise<ManagedFileView> {
    const query = new URLSearchParams({ root: rootId, path })
    const response = await fetch(`/api/files?${query.toString()}`, { cache: 'no-store' })
    return parseResponse<ManagedFileView>(response)
  }

  async managedFileProposals(): Promise<ManagedFileProposal[]> {
    const response = await fetch('/api/files/proposals', { cache: 'no-store' })
    const payload = await parseResponse<{ proposals?: ManagedFileProposal[] }>(response)
    return payload.proposals ?? []
  }

  async proposeManagedFile(body: {
    rootId: string
    path: string
    content: string
    expectedDigest: string
  }): Promise<ManagedFileProposal> {
    const key = `${body.rootId}:${body.path}:${body.expectedDigest}:${body.content}`
    const idempotencyKey = this.fileProposalKeys.get(key) ?? crypto.randomUUID()
    this.fileProposalKeys.set(key, idempotencyKey)
    try {
      const proposal = await this.mutate<ManagedFileProposal>('/api/files/proposals', {
        ...body, mode: 'propose', idempotencyKey,
      })
      this.fileProposalKeys.delete(key)
      return proposal
    } catch (reason) {
      if (reason instanceof APIError) this.fileProposalKeys.delete(key)
      throw reason
    }
  }

  async approveManagedFileProposal(proposal: ManagedFileProposal, confirmation: string): Promise<ManagedFileProposal> {
    return this.mutate<ManagedFileProposal>(`/api/files/proposals/${encodeURIComponent(proposal.id)}/approve`, {
      digest: proposal.proposedDigest,
      confirmation,
    })
  }

  async applyManagedFileProposal(proposal: ManagedFileProposal): Promise<ManagedFileProposal> {
    const key = this.fileApplyKeys.get(proposal.id) ?? crypto.randomUUID()
    this.fileApplyKeys.set(proposal.id, key)
    try {
      const result = await this.mutate<ManagedFileProposal>(`/api/files/proposals/${encodeURIComponent(proposal.id)}/apply`, {
        idempotencyKey: key,
      })
      this.fileApplyKeys.delete(proposal.id)
      return result
    } catch (reason) {
      if (reason instanceof APIError) this.fileApplyKeys.delete(proposal.id)
      throw reason
    }
  }

  async rollbackManagedFileProposal(proposal: ManagedFileProposal, confirmation: string): Promise<ManagedFileProposal> {
    const key = this.fileRollbackKeys.get(proposal.id) ?? crypto.randomUUID()
    this.fileRollbackKeys.set(proposal.id, key)
    try {
      const result = await this.mutate<ManagedFileProposal>(`/api/files/proposals/${encodeURIComponent(proposal.id)}/rollback`, {
        confirmation,
        idempotencyKey: key,
      })
      this.fileRollbackKeys.delete(proposal.id)
      return result
    } catch (reason) {
      if (reason instanceof APIError) this.fileRollbackKeys.delete(proposal.id)
      throw reason
    }
  }

  async kubernetes(): Promise<KubernetesConfigView> {
    const [response, plans] = await Promise.all([
      fetch('/api/kubernetes', { cache: 'no-store' }),
      this.kubernetesPlans(),
    ])
    return { ...(await parseResponse<KubernetesConfigView>(response)), plans }
  }

  async extensions(): Promise<ExtensionPolicyView> {
    const response = await fetch('/api/extensions', { cache: 'no-store' })
    const payload = await parseResponse<ExtensionPolicyPayload>(response)
    return { ...payload, extensions: (payload.extensions ?? []).map(normalizeExtension).filter((item) => item.id) }
  }

  async runnerUpdateStatus(): Promise<RunnerUpdateStatus> {
    const response = await fetch('/api/runner/update', { cache: 'no-store' })
    return parseResponse<RunnerUpdateStatus>(response)
  }

  async prepareRunnerUpdate(input: RunnerUpdatePrepareInput): Promise<RunnerUpdate> {
    if (!this.runnerPrepareKey) this.runnerPrepareKey = crypto.randomUUID()
    try {
      const update = await this.mutate<RunnerUpdate>('/api/runner/update/prepare', {
        ...input, idempotencyKey: this.runnerPrepareKey,
      })
      this.runnerPrepareKey = ''
      return update
    } catch (reason) {
      if (reason instanceof APIError) this.runnerPrepareKey = ''
      throw reason
    }
  }

  async activateRunnerUpdate(id: string, confirmation: string): Promise<RunnerUpdate> {
    const key = this.runnerActivationKeys.get(id) ?? crypto.randomUUID()
    this.runnerActivationKeys.set(id, key)
    try {
      const update = await this.mutate<RunnerUpdate>(`/api/runner/update/${encodeURIComponent(id)}/activate`, {
        confirmation, idempotencyKey: key,
      })
      this.runnerActivationKeys.delete(id)
      return update
    } catch (reason) {
      if (reason instanceof APIError) this.runnerActivationKeys.delete(id)
      throw reason
    }
  }

  async cancelRunnerUpdate(id: string, confirmation: string): Promise<RunnerUpdate> {
    const key = this.runnerCancellationKeys.get(id) ?? crypto.randomUUID()
    this.runnerCancellationKeys.set(id, key)
    try {
      const update = await this.mutate<RunnerUpdate>(`/api/runner/update/${encodeURIComponent(id)}/cancel`, {
        confirmation, idempotencyKey: key,
      })
      this.runnerCancellationKeys.delete(id)
      return update
    } catch (reason) {
      if (reason instanceof APIError) this.runnerCancellationKeys.delete(id)
      throw reason
    }
  }

  async resolveRunnerUpdate(id: string, confirmation: string, evidence: RunnerUpdateResolutionEvidence): Promise<RunnerUpdate> {
    const key = this.runnerResolutionKeys.get(id) ?? crypto.randomUUID()
    this.runnerResolutionKeys.set(id, key)
    try {
      const update = await this.mutate<RunnerUpdate>(`/api/runner/update/${encodeURIComponent(id)}/resolve`, {
        confirmation, idempotencyKey: key, evidence,
      })
      this.runnerResolutionKeys.delete(id)
      return update
    } catch (reason) {
      if (reason instanceof APIError) this.runnerResolutionKeys.delete(id)
      throw reason
    }
  }

  async createKubernetesOperation(body: {
    target: KubernetesOperation['target']
    manifest?: string
  }): Promise<KubernetesOperation> {
    const payload = await this.mutate<{ operation?: KubernetesOperation; output?: string }>('/api/kubernetes/operations', {
      ...body,
      action: 'validate',
      dryRun: true,
      manifest: body.manifest ?? '',
      idempotencyKey: crypto.randomUUID(),
    })
    if (!payload.operation) throw new Error('Kubernetes 未返回操作记录')
    return payload.operation
  }

  async createKubernetesPlan(body: {
    target: KubernetesOperation['target']
    manifest: string
  }): Promise<KubernetesPlan> {
    return this.mutate<KubernetesPlan>('/api/kubernetes/plans', {
      target: body.target,
      manifest: body.manifest,
      idempotencyKey: crypto.randomUUID(),
    })
  }

  async kubernetesPlans(): Promise<KubernetesPlan[]> {
    const response = await fetch('/api/kubernetes/plans', { cache: 'no-store' })
    const payload = await parseResponse<{ plans?: KubernetesPlan[] }>(response)
    return payload.plans ?? []
  }

  async approveKubernetesPlan(plan: KubernetesPlan, confirmation: string): Promise<KubernetesPlan> {
    return this.mutate<KubernetesPlan>(`/api/kubernetes/plans/${encodeURIComponent(plan.id)}/approve`, {
      digest: plan.manifestDigest,
      confirmation,
    })
  }

  async executeKubernetesPlan(plan: KubernetesPlan): Promise<KubernetesOperation> {
    const payload = await this.mutate<{ operation?: KubernetesOperation }>(`/api/kubernetes/plans/${encodeURIComponent(plan.id)}/execute`, {
      idempotencyKey: crypto.randomUUID(),
    })
    if (!payload.operation) throw new Error('Kubernetes 计划未返回操作记录')
    return payload.operation
  }

  async access(): Promise<AccessControlView> {
    const response = await fetch('/api/access', { cache: 'no-store' })
    return parseResponse<AccessControlView>(response)
  }

  async updateAccess(body: AccessControlUpdate): Promise<AccessControlView> {
    return this.mutate<AccessControlView>('/api/access', {
      ...body,
      idempotencyKey: crypto.randomUUID(),
    }, 'PUT')
  }

  async createAccessChange(body: AccessControlUpdate): Promise<AccessChange> {
    return this.mutate<AccessChange>('/api/access/changes', {
      ...body,
      requiresDualApproval: true,
      idempotencyKey: crypto.randomUUID(),
    })
  }

  async approveAccessChange(change: AccessChange, confirmation: string): Promise<AccessChange> {
    return this.mutate<AccessChange>(`/api/access/changes/${encodeURIComponent(change.id)}/approve`, {
      digest: change.requestDigest,
      confirmation,
    })
  }

  async applyAccessChange(change: AccessChange): Promise<AccessChange> {
    return this.mutate<AccessChange>(`/api/access/changes/${encodeURIComponent(change.id)}/apply`, {})
  }

  async rejectAccessChange(change: AccessChange, reason: string): Promise<AccessChange> {
    return this.mutate<AccessChange>(`/api/access/changes/${encodeURIComponent(change.id)}/reject`, { reason })
  }

  async audit(offset = 0): Promise<Page<AuditEntry>> {
    const response = await fetch(`/api/audit?limit=100&offset=${offset}`)
    const payload = await parseResponse<{ entries: AuditEntry[]; hasMore: boolean }>(response)
    return { items: payload.entries ?? [], hasMore: payload.hasMore }
  }

  async preview(service: string, action: string, target = ''): Promise<Preview> {
    return this.mutate<Preview>('/api/previews', { service, action, target })
  }

  async start(previewID: string, confirmation = ''): Promise<Task> {
    return this.mutate<Task>('/api/tasks', {
      previewId: previewID,
      confirmation,
      idempotencyKey: crypto.randomUUID(),
    })
  }

  events(onEvent: (event: OpsEvent) => void, onState: (connected: boolean) => void): () => void {
    const source = new EventSource('/api/events?tail=1')
    source.addEventListener('open', () => onState(true))
    source.addEventListener('error', () => onState(false))
    source.addEventListener('ops', (message) => {
      onEvent(JSON.parse((message as MessageEvent).data) as OpsEvent)
    })
    return () => source.close()
  }

  private async mutate<T>(url: string, body: unknown, method = 'POST'): Promise<T> {
    const response = await fetch(url, {
      method,
      credentials: 'same-origin',
      headers: {
        'Content-Type': 'application/json',
        'X-AreaSong-Ops-CSRF': this.csrfToken,
      },
      body: JSON.stringify(body),
    })
    return parseResponse<T>(response)
  }
}

export { APIError }

export function isFeatureUnavailable(reason: unknown): boolean {
  if (!(reason instanceof APIError)) return false
  if (reason.status === 404 || reason.status === 405 || reason.status === 501) return true
  return [403, 409, 503].includes(reason.status) && /(尚未启用|未配置|不支持)/u.test(reason.message)
}
