import type { AuditEntry, OpsEvent, Page, Preview, ServiceView, Task } from './types'

interface SessionResponse {
  email: string
  csrfToken: string
}

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
