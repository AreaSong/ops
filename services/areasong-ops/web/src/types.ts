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
  | 'scheduled'
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
  tenantId?: string
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
  canManage: boolean
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
  scheduleAt?: string
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
  scheduleAt?: string
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
  timeoutSeconds?: number
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
  tenantId: string
  serverId: string
  scheduleAt?: string
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
  name: 'inspect' | 'retry' | 'rollback' | 'reconcile' | 'restore-drill' | 'restore'
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

/** 生命周期控制面状态。后端新增字段通过 data 保留，避免旧 Runner 阻断页面。 */
export type DesiredState = 'running' | 'stopped' | 'maintenance' | 'drained'
export type ActualState = 'unknown' | 'running' | 'stopped' | 'maintenance' | 'drained'
export type HealthState = 'unknown' | 'healthy' | 'degraded' | 'unhealthy'

export interface StateDrift {
  detected: boolean
  expected: string
  observed: string
  reason?: string
  detectedAt?: string
}

export interface ServiceState {
  service: string
  objectId?: string
  tenantId?: string
  desired: DesiredState
  actual: ActualState
  health: HealthState
  observedAt?: string
  desiredUpdatedAt?: string
  maintenanceUntil?: string
  reason?: string
  generation?: number
  data?: Record<string, unknown>
  drift?: StateDrift
}

export interface Capability {
  name: string
  version?: string
  readOnly?: boolean
  parameters?: Record<string, string>
}

export type FleetNodeState = 'unknown' | 'online' | 'offline' | 'draining' | 'disabled'

export interface ServerNode {
  id: string
  hostname: string
  environment: string
  region?: string
  address?: string
  labels?: Record<string, string>
  capabilities?: string[]
  capabilityDefinitions?: Capability[]
  runnerId?: string
  state: FleetNodeState
  maxConcurrency?: number
  lastHeartbeat?: string
  disabledReason?: string
}

export interface RunnerNode {
  id: string
  serverId: string
  hostname?: string
  version: string
  labels?: Record<string, string>
  capabilities?: string[]
  capabilityDefinitions?: Capability[]
  state: FleetNodeState
  maxConcurrency?: number
  lastHeartbeat?: string
  leaseExpiresAt?: string
  disabledReason?: string
}

export interface Fleet {
  servers: ServerNode[]
  runners: RunnerNode[]
  canManage: boolean
}

export type BatchTaskState =
  | 'pending'
  | 'planning'
  | 'running'
  | 'paused'
  | 'succeeded'
  | 'failed'
  | 'rolling_back'
  | 'rolled_back'
  | 'cancelled'
  | 'needs_attention'
  | 'unknown'

export type BatchNodeState =
  | 'pending'
  | 'ready'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'skipped'
  | 'blocked'
  | 'cancelled'
  | 'rolling_back'
  | 'rolled_back'
  | 'unknown'

export type BatchStrategy = 'serial' | 'fixed' | 'percentage' | 'canary'
export type ConcurrencyScope = 'global' | 'per_runner' | 'per_server'
export type FailurePolicy = 'stop' | 'continue' | 'rollback' | 'pause' | 'needs_attention'

export interface NodeSelector {
  ids?: string[]
  matchLabels?: Record<string, string>
  matchCapabilities?: string[]
  excludeIds?: string[]
}

export interface BatchPolicy {
  strategy: BatchStrategy
  batchSize?: number
  batchPercentage?: number
  canarySize?: number
  canaryPercentage?: number
  pauseSeconds?: number
  observationSeconds?: number
}

export interface ConcurrencyPolicy {
  scope: ConcurrencyScope
  maxConcurrent: number
  perRunner?: number
  perServer?: number
  queueLimit?: number
}

export interface FailurePolicyConfig {
  policy: FailurePolicy
  maxFailures?: number
  rollbackOnFailure?: boolean
}

export interface ChangeWindow {
  id?: string
  startAt: string
  endAt: string
  timezone?: string
  reason?: string
  approvalRequired?: boolean
  emergencyOverride?: boolean
}

export interface DAGNode {
  id: string
  action?: string
  capability?: string
  targetId?: string
  dependencies?: string[]
  dependsOn?: string[]
  state: BatchNodeState
  attempts?: number
  error?: string
  startedAt?: string
  finishedAt?: string
}

export interface BatchTask {
  id: string
  name?: string
  action?: string
  capability?: string
  targetSelector?: NodeSelector
  targetIds?: string[]
  nodes: DAGNode[]
  batchPolicy: BatchPolicy
  concurrency: ConcurrencyPolicy
  failurePolicy: FailurePolicy
  failureConfig?: FailurePolicyConfig
  changeWindow?: ChangeWindow
  state: BatchTaskState
  summary?: string
  error?: string
  createdAt?: string
  startedAt?: string
  finishedAt?: string
  digest?: string
  confirmationPhrase?: string
  approvedAt?: string
  operationState?: 'pending_approval' | 'approved' | 'running' | 'paused' | 'observing' | 'succeeded' | 'failed' | 'rolling_back' | 'rolled_back' | 'needs_attention' | 'cancelled'
}

export interface BatchOperation {
  id: string
  action: string
  target?: string
  task: BatchTask
  digest: string
  confirmationPhrase?: string
  state: 'pending_approval' | 'approved' | 'running' | 'paused' | 'observing' | 'succeeded' | 'failed' | 'rolling_back' | 'rolled_back' | 'needs_attention' | 'cancelled'
  items: Array<{
    id: string
    objectId: string
    service: string
    serverId?: string
    runnerId?: string
    batchIndex: number
    dependsOn?: string[]
    state: BatchNodeState
    planId?: string
    taskId?: string
    error?: string
    updatedAt?: string
  }>
  approvedAt?: string
  startedAt?: string
  finishedAt?: string
  summary?: string
  error?: string
  createdAt?: string
  updatedAt?: string
}

export interface RecoveryArtifact {
  role: string
  path: string
  sizeBytes: number
  sha256: string
}

export interface RecoveryPoint {
  id: string
  taskId: string
  service: string
  status: string
  evidence?: {
    schemaVersion?: number
    service?: string
    taskId?: string
    createdAt?: string
    artifacts?: RecoveryArtifact[]
  }
  evidenceDigest?: string
  expectedBeforeDigest?: string
  requiredArtifactRoles?: string[]
  createdAt?: string
  verifiedAt?: string
  recoverableUntil?: string
}

export interface RecoveryCenterView {
  service: string
  latest?: RecoveryPoint
  drillLastSuccessAt?: string
  drillFresh: boolean
  drillReason?: string
  availableActions?: RecoveryAction[]
}

export interface ComposeRevision {
  id: string
  service: string
  digest: string
  expectedDigest?: string
  source: string
  content?: string
  validated: boolean
  state?: ComposeRevisionState
  actorHash?: string
  confirmationPhrase?: string
  approvedBy?: string
  secondApprovedByHash?: string
  appliedByHash?: string
  backupControlledPath?: string
  backupRuntimePath?: string
  error?: string
  createdAt?: string
  approvedAt?: string
  secondApprovedAt?: string
  appliedAt?: string
  finishedAt?: string
}

export type ComposeRevisionState =
  | 'proposed'
  | 'pending_second_approval'
  | 'approved'
  | 'applying'
  | 'applied'
  | 'rolling_back'
  | 'rolled_back'
  | 'failed'
  | 'needs_attention'

export type AutoUpdateChannel = 'stable' | 'candidate' | 'security'

/** 自动更新策略只描述调度和审批门禁，不包含 Runner/适配器内部路径。 */
export interface AutoUpdatePolicyView {
  service: string
  objectId: string
  tenantId: string
  enabled: boolean
  channel: AutoUpdateChannel | string
  maintenanceWindow?: string
  canaryPercent?: number
  maxUnavailable?: number
  requireBackup: boolean
  requireApproval: boolean
  rollbackOnAlert: boolean
  observationSeconds: number
  nextEvaluationAt?: string
  lastEvaluationAt?: string
  lastPlanId?: string
  lastError?: string
}

export interface AutoUpdatePolicyInput {
  enabled: boolean
  channel: AutoUpdateChannel
  maintenanceWindow?: string
  canaryPercent: number
  maxUnavailable: number
  requireBackup: boolean
  requireApproval: boolean
  rollbackOnAlert: boolean
  observationSeconds: number
}

export interface AutoUpdateEvaluation {
  service: string
  evaluatedAt: string
  eligible: boolean
  reason?: string
  planId?: string
  target?: string
  updateCreated: boolean
}

export interface TerminalCommand {
  name: string
  executable: string
  arguments?: string[]
  readOnly: boolean
  timeoutSeconds: number
}

export interface TerminalOutput {
  sessionId: string
  exitCode: number
  output: string
  state: string
}

export interface TerminalSession {
  id: string
  objectId: string
  command: string
  state: string
  actorHash: string
  exitCode: number
  output?: string
  expiresAt: string
  createdAt: string
}

export type TerminalShellPlanState = 'pending_approval' | 'approved' | 'running' | 'succeeded' | 'failed' | 'needs_attention'

export interface TerminalShellPlan {
  id: string
  objectId: string
  state: TerminalShellPlanState | string
  actorHash: string
  inputDigest: string
  confirmationPhrase?: string
  approvedByHash?: string
  exitCode?: number
  output?: string
  error?: string
  createdAt: string
  expiresAt: string
  approvedAt?: string
  startedAt?: string
  finishedAt?: string
}

export interface ManagedFileEntry {
  name: string
  path: string
  size: number
  isDirectory: boolean
  modifiedAt: string
}

export interface ManagedFileView {
  rootId: string
  path: string
  digest?: string
  size: number
  content?: string
  readOnly: boolean
  isDirectory: boolean
  entries?: ManagedFileEntry[]
}

export type ManagedFileProposalState =
  | 'proposed'
  | 'pending_second_approval'
  | 'approved'
  | 'applying'
  | 'applied'
  | 'rolling_back'
  | 'rolled_back'
  | 'failed'
  | 'needs_attention'

export interface ManagedFileProposal {
  id: string
  idempotencyKey?: string
  actorHash: string
  rootId: string
  path: string
  expectedDigest: string
  proposedDigest: string
  content?: string
  state: ManagedFileProposalState | string
  confirmationPhrase?: string
  approvedByHash?: string
  secondApprovedByHash?: string
  appliedByHash?: string
  backupPath?: string
  rolledBackByHash?: string
  error?: string
  createdAt: string
  approvedAt?: string
  secondApprovedAt?: string
  appliedAt?: string
  finishedAt?: string
  rolledBackAt?: string
}

export interface ComposeServiceView {
  service: string
  current?: ComposeRevision
  revisions?: ComposeRevision[]
  runtime?: ServiceStatus
  digest?: string
  source?: string
  content?: string
  validated?: boolean
  validationError?: string
  controlledCompose?: string
  runtimeCompose?: string
  envFile?: string
  applicationService?: string
  controlledPath?: string
  runtimePath?: string
  applicationContainer?: string
  dependencyContainers?: string[]
  healthUrl?: string
  availableActions?: string[]
  [key: string]: unknown
}

export interface KubernetesTarget {
  cluster: string
  context: string
  namespace: string
  tenantId?: string
  allowlist?: string[]
  resourceKinds?: string[]
}

export interface KubernetesOperation {
  id: string
  target: KubernetesTarget
  action: string
  manifestDigest?: string
  dryRun: boolean
  state: string
  createdAt?: string
}

export interface KubernetesPlan {
  id: string
  idempotencyKey?: string
  tenantId?: string
  target: KubernetesTarget
  manifestDigest: string
  action: 'apply'
  state: string
  confirmationPhrase?: string
  approvedByHash?: string
  secondApprovedByHash?: string
  requiresDualApproval: boolean
  operationId?: string
  error?: string
  createdAt: string
  approvedAt?: string
  secondApprovedAt?: string
  startedAt?: string
  finishedAt?: string
}

export interface KubernetesConfigView {
  enabled?: boolean
  targets?: KubernetesTarget[] | Record<string, KubernetesTarget>
  operations?: KubernetesOperation[]
  plans?: KubernetesPlan[]
  manifest?: string
  [key: string]: unknown
}

export interface ExtensionPolicyView {
  enabled: boolean
  trustedPublishers?: string[]
  requireSignature?: boolean
  sandbox?: string
  extensions?: ExtensionView[]
  [key: string]: unknown
}

export interface ExtensionView {
    id: string
    version?: string
    type?: string
    entrypoint?: string
    digest?: string
    signature?: string
    permissions?: string[]
    allowedObjects?: string[]
    publisher?: string
    state?: string
    stored?: boolean
    createdAt?: string
}

export type RunnerUpdateState =
  | 'prepared'
  | 'activating'
  | 'succeeded'
  | 'rolled_back'
  | 'failed'
  | 'needs_attention'
  | 'cancelled'

export interface RunnerUpdate {
  id: string
  runnerId: string
  targetVersion: string
  artifactPath?: string
  artifactDigest: string
  artifactRevision: string
  publisher: string
  state: RunnerUpdateState
  phase?: string
  preparedByHash?: string
  previousVersion?: string
  previousRevision?: string
  previousDigest?: string
  confirmationPhrase?: string
  approvedByHash?: string
  resolvedByHash?: string
  cancelledByHash?: string
  rollbackPath?: string
  error?: string
  createdAt: string
  activatedAt?: string
  finishedAt?: string
  resolutionDecision?: RunnerUpdateResolutionDecision
  resolutionEvidence?: RunnerUpdateResolutionEvidence
}

export type RunnerUpdateResolutionDecision = 'keep' | 'rollback' | 'abort'

export interface RunnerUpdateResolutionEvidence {
  decision: RunnerUpdateResolutionDecision
  observedVersion: string
  observedRevision: string
  observedDigest: string
  observedPid?: number
  reason: string
}

export interface RunnerUpdateStatus {
  runnerId: string
  currentVersion: string
  revision: string
  publisher: string
  manifestPurpose: string
  manifestSchema: number
  manifestGoos: string
  manifestGoarch: string
  currentActorHash: string
  canManage: boolean
  pending?: RunnerUpdate[]
  recent?: RunnerUpdate[]
}

export interface RunnerUpdatePrepareInput {
  manifest: RunnerUpdateManifest
  manifestPurpose: string
  manifestSchema: number
  manifestGoos: string
  manifestGoarch: string
  runnerId: string
  targetVersion: string
  artifactPath: string
  artifactDigest: string
  artifactRevision: string
  publisher: string
  artifactSignature: string
  confirmation: string
}

export interface RunnerUpdateManifest {
  purpose: string
  schema: number
  goos: string
  goarch: string
  runnerId: string
  targetVersion: string
  artifactDigest: string
  artifactRevision: string
  publisher: string
}

export interface AccessTenant {
  id: string
  displayName: string
  status: string
  createdAt?: string
  updatedAt?: string
  createdBy?: string
}

export interface AccessRole {
  id: string
  displayName: string
  permissions: string[]
  builtIn: boolean
  createdBy?: string
}

export interface AccessBinding {
  id: string
  subject: string
  tenantId: string
  roleId: string
  objectIds?: string[]
  expiresAt?: string
  jit?: boolean
  requiresDualApproval?: boolean
  approvalState?: string
  approvedByHash?: string
  secondApprovedByHash?: string
  createdAt?: string
  createdBy?: string
}

export interface AccessPrincipal {
  subject: string
  email?: string
  emailHash?: string
  tenantId: string
  roles: string[]
  status?: string
  expiresAt?: string
  jit?: boolean
  createdBy?: string
}

export type AccessChangeState = 'pending_approval' | 'approved' | 'applied' | 'rejected'

export interface AccessChange {
  id: string
  requestDigest: string
  actorHash: string
  state: AccessChangeState
  confirmationPhrase: string
  approvedByHash?: string
  secondApprovedByHash?: string
  requiresDualApproval: boolean
  version?: number
  error?: string
  createdAt: string
  approvedAt?: string
  secondApprovedAt?: string
  appliedAt?: string
}

export interface AccessControlView {
  enforced: boolean
  canManage: boolean
  defaultTenant?: string
  principals?: Record<string, { email?: string; emailHash?: string; tenantId: string; roles: string[] }>
  tenants?: AccessTenant[] | Record<string, AccessTenant>
  roles?: AccessRole[] | Record<string, AccessRole>
  bindings?: AccessBinding[]
  principalList?: AccessPrincipal[]
  currentSubject?: { email?: string; subject?: string; tenantId?: string }
  version?: number
  digest?: string
  pendingChanges?: AccessChange[]
  [key: string]: unknown
}

export interface AccessControlUpdate {
  enforced?: boolean
  tenants?: AccessTenant[]
  roles?: AccessRole[]
  principals?: AccessPrincipal[]
  bindings?: AccessBinding[]
  removeTenantIds?: string[]
  removeRoleIds?: string[]
  removePrincipalSubjects?: string[]
  removeBindingIds?: string[]
  requiresDualApproval?: boolean
  expectedVersion?: number
}

export interface FeatureResult<T> {
  available: boolean
  data?: T
  error?: string
}
