export type Risk = 'read_only' | 'low' | 'medium' | 'high'
export type TaskState =
  | 'waiting_confirmation'
  | 'queued'
  | 'running'
  | 'rolling_back'
  | 'succeeded'
  | 'failed'
  | 'failed_recoverable'
  | 'needs_attention'
  | 'rolled_back'
  | 'recovery_uncertain'

export type PlanState = 'pending_approval' | 'approved' | 'executing' | 'completed' | 'invalidated'
export type StageState = 'pending' | 'running' | 'succeeded' | 'failed' | 'skipped' | 'rolled_back'

export interface ActionDefinition {
  name: string
  displayName: string
  enabled: boolean
  risk: Risk
  targetMode: 'none' | 'signed_release_tag' | 'allowlist' | 'controlled_rollback'
  allowedTargets?: string[]
  steps: string[]
  timeoutSeconds: number
  disabledReason?: string
  readinessGate?: 'prepared_release'
  impact: string
  rollback: string
  scope: string
}
export interface ServiceStatus {
  currentVersion?: string
  currentImage?: string
  currentImageId?: string
  runtimeIdentityHash?: string
  gitCommit?: string
  appState?: string
  postgresState?: string
  redisState?: string
  migrations?: number
  health?: Record<string, unknown>
  [key: string]: unknown
}

export interface ServiceView {
  name: string
  displayName: string
  description: string
  actions: Record<string, ActionDefinition>
  status?: ServiceStatus
  statusError?: string
  activeTaskId?: string
  releaseDiscovery?: ReleaseDiscovery
  rollbackSourceTaskId?: string
}

export interface Preview {
  id: string
  service: string
  action: string
  target?: string
  risk: Risk
  impact: string
  rollback: string
  scope: string
  steps: string[]
  requiresConfirmation: boolean
  confirmationPhrase?: string
  snapshot?: ServiceStatus
  expiresAt: string
}

export interface ApprovalSummary {
  schemaVersion: number
  service: string
  action: string
  target?: string
  risk: Risk
  impact: string
  rollback: string
  scope: string
  steps: string[]
  phaseSemantics?: Record<string, {
    effect: 'observe' | 'artifact_write' | 'runtime_mutation' | 'data_mutation'
    producesRecoveryPoint?: boolean
    requiresRecoveryPoint?: boolean
    failurePolicy: 'fail' | 'rollback' | 'needs_attention'
    recoveryPhase?: string
  }>
  confirmationPhrase?: string
  expectedBefore: ServiceStatus
  targetEvidence?: ReleaseDiscovery
}

export interface ReleasePlan {
  id: string
  actorHash: string
  service: string
  action: string
  target?: string
  risk: Risk
  state: PlanState
  digest: string
  approvalSummary: ApprovalSummary
  confirmationPhrase?: string
  requiresConfirmation: boolean
  approvedByHash?: string
  approvedAt?: string
  invalidatedReason?: string
  taskId?: string
  createdAt: string
  updatedAt: string
}

export interface TaskStage {
  name: string
  state: StageState
  summary?: string
  startedAt?: string
  finishedAt?: string
}

export interface RecoveryAction {
  name: 'inspect' | 'retry' | 'rollback' | 'reconcile'
  label: string
  enabled: boolean
  reason?: string
}

export interface Task {
  id: string
  actorHash: string
  service: string
  action: string
  target?: string
  risk: Risk
  state: TaskState
  currentPhase?: string
  summary?: string
  error?: string
  planId?: string
  planDigest?: string
  stages?: TaskStage[]
  heartbeatAt?: string
  productionChanged: boolean
  retryable: boolean
  failureCode?: string
  rollbackAvailable: boolean
  rollbackReason?: string
  recoveryPointId?: string
  recoveryActions?: RecoveryAction[]
  createdAt: string
  startedAt?: string
  finishedAt?: string
}

export interface OpsEvent {
  sequence: number
  taskId?: string
  occurredAt: string
  level: 'info' | 'warning' | 'error'
  phase?: string
  message: string
  data?: Record<string, unknown>
}

export interface AuditEntry {
  sequence: number
  occurredAt: string
  actorHash: string
  event: string
  resource: string
  outcome: string
  detail?: Record<string, unknown>
}

export interface ReleaseDiscovery {
  currentVersion?: string
  latestTag?: string
  manifestVersion?: string
  publishedAt?: string
  prepared?: boolean
  updateAvailable?: boolean
  webImageDigest?: string
  blockers?: string[]
  preparationSteps?: Array<{ name: string; state: 'pending' | 'running' | 'succeeded' | 'failed'; detail?: string }>
}

export interface Page<T> {
  items: T[]
  hasMore: boolean
}
