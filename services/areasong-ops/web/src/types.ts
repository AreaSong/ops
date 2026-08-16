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

export type PlanState =
  | 'pending_approval'
  | 'approved'
  | 'executing'
  | 'observing'
  | 'needs_attention'
  | 'completed'
  | 'invalidated'
export type StageState = 'pending' | 'running' | 'succeeded' | 'failed' | 'skipped' | 'rolled_back'
export type CredentialRotationState =
  | 'running'
  | 'failed'
  | 'rolled_back'
  | 'needs_attention'
  | 'switched_pending_revocation'
  | 'revocation_verified'
  | 'completed'

export interface NavigationLinks {
  grafana?: string
  alerts?: string
}

export interface ActiveAlert {
  fingerprint: string
  objectId: string
  service: string
  alertName: string
  severity: string
  summary: string
  runbookUrl?: string
  grafanaUrl?: string
  silenced: boolean
  startsAt: string
}

export interface SessionResponse {
  email: string
  csrfToken: string
  links?: NavigationLinks
}

export interface CredentialRotation {
  id: string
  actorHash: string
  credentialType: 'github_alertmanager'
  target: string
  state: CredentialRotationState
  fingerprint: string
  expiresAt: string
  validationResult?: string
  outcome?: string
  rollbackResult?: string
  createdAt: string
  finishedAt?: string
  closedAt?: string
}

export interface CredentialProfile {
  type: 'github_alertmanager'
  displayName: string
  target: string
  repository: string
  risk: Risk
  confirmationPhrase: string
  configured: boolean
  fingerprint?: string
  expiresAt?: string
  lastRotation?: CredentialRotation
}

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
  observationSeconds?: number
  impact: string
  rollback: string
  scope: string
}

export interface AlertPolicyDefinition {
  matchers?: Record<string, string>
  blockingAlerts?: string[]
  maintenanceAlerts?: string[]
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
  health?: Record<string, unknown> | 'healthy' | 'stale'
  [key: string]: unknown
}

export interface ObjectMetadata {
  type: 'service' | 'automatic_task'
  environment: 'production'
  owner: string
  criticality: 'standard' | 'important' | 'critical'
  lifecycle: 'proposed' | 'onboarding' | 'active' | 'maintenance' | 'retiring' | 'retired'
  maturity: 'disabled' | 'inspect_only' | 'shadow' | 'manual_approval' | 'automated'
}

export interface ManagedObjectView {
  name: string
  objectId: string
  metadata: ObjectMetadata
  displayName: string
  description: string
  actions: Record<string, ActionDefinition>
  status?: ServiceStatus
  statusError?: string
  activeTaskId?: string
}

export interface ServiceView extends ManagedObjectView {
  releaseDiscovery?: ReleaseDiscovery
  rollbackSourceTaskId?: string
}

export interface AutomaticTaskView extends ManagedObjectView {
  schedule: string
  scheduleSource: 'cron'
  freshnessSeconds: number
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
  observationSeconds?: number
  alertPolicy?: AlertPolicyDefinition
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
  observationSeconds?: number
  observationStartedAt?: string
  observationEndsAt?: string
  closureReason?: string
  maintenanceSilenceId?: string
  maintenanceSilenceEndsAt?: string
  maintenanceSilenceReleasedAt?: string
  blockingAlertFingerprints?: string[]
  closedAt?: string
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
