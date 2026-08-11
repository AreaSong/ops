export type Risk = 'read_only' | 'low' | 'medium' | 'high'
export type TaskState =
  | 'queued'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'rolled_back'
  | 'recovery_uncertain'

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
