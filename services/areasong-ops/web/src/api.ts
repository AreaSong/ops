import type {
  ActiveAlert, AuditEntry, AutomaticTaskView, CredentialProfile, CredentialRotation, ManagedObjectView, OpsEvent, Page, Preview,
  ReleasePlan, ServiceView, SessionResponse, Task,
} from './types'

class APIError extends Error {
  status: number

  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function parseResponse<T>(response: Response): Promise<T> {
  const payload = (await response.json()) as T & { error?: string }
  if (!response.ok) {
    throw new APIError(response.status, payload.error ?? `请求失败（HTTP ${response.status}）`)
  }
  return payload
}

export class OpsAPI {
  private csrfToken = ''
  private executionKeys = new Map<string, string>()
  private closureKeys = new Map<string, string>()
  private credentialRotationKey = ''
  private credentialClosureKeys = new Map<string, string>()

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

  async objects(): Promise<ManagedObjectView[]> {
    const response = await fetch('/api/objects')
    return (await parseResponse<{ objects: ManagedObjectView[] }>(response)).objects ?? []
  }

  async automaticTasks(): Promise<AutomaticTaskView[]> {
    const response = await fetch('/api/automatic-tasks')
    return (await parseResponse<{ automaticTasks: AutomaticTaskView[] }>(response)).automaticTasks ?? []
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

  async plans(offset = 0): Promise<Page<ReleasePlan>> {
    const response = await fetch(`/api/plans?limit=100&offset=${offset}`)
    const payload = await parseResponse<{ plans: ReleasePlan[]; hasMore: boolean }>(response)
    return { items: payload.plans ?? [], hasMore: payload.hasMore }
  }

  async createPlan(service: string, action: string, target = ''): Promise<ReleasePlan> {
    return this.mutate<ReleasePlan>('/api/plans', { service, action, target })
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

  private async mutate<T>(url: string, body: unknown): Promise<T> {
    const response = await fetch(url, {
      method: 'POST',
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
