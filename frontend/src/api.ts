import type { Session, Model, SSEEvent } from './types'

const BASE = ''

export async function fetchModels(): Promise<Model[]> {
  const res = await fetch(`${BASE}/api/models`)
  const data = await res.json()
  return data.models ?? []
}

export async function fetchSessions(): Promise<Session[]> {
  const res = await fetch(`${BASE}/api/sessions`)
  if (!res.ok) return []
  const data = await res.json()
  return data.sessions ?? []
}

export async function createSession(model: string): Promise<Session> {
  const res = await fetch(`${BASE}/api/sessions`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ model }),
  })
  return res.json()
}

export async function deleteSession(id: string): Promise<void> {
  await fetch(`${BASE}/api/sessions/${id}`, { method: 'DELETE' })
}

export async function updateSessionTitle(id: string, title: string): Promise<void> {
  await fetch(`${BASE}/api/sessions/${id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title }),
  })
}

export async function loadSession(id: string): Promise<{ session: Session; messages: unknown[] }> {
  const res = await fetch(`${BASE}/api/sessions/${id}`)
  return res.json()
}

// streamChat sends a prompt and calls onEvent for each SSE event.
// Returns an AbortController so the caller can cancel.
export function streamChat(
  prompt: string,
  model: string,
  onEvent: (evt: SSEEvent) => void,
  onDone: () => void,
  onError: (err: string) => void,
): AbortController {
  const ctrl = new AbortController()
  ;(async () => {
    try {
      const res = await fetch(`${BASE}/api/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt, model }),
        signal: ctrl.signal,
      })
      if (!res.ok) {
        onError(`HTTP ${res.status}`)
        return
      }
      await readSSE(res, onEvent, onDone)
    } catch (e: unknown) {
      if (e instanceof Error && e.name !== 'AbortError') onError(e.message)
    }
  })()
  return ctrl
}

export function streamSessionChat(
  sessionId: string,
  prompt: string,
  model: string,
  onEvent: (evt: SSEEvent) => void,
  onDone: () => void,
  onError: (err: string) => void,
): AbortController {
  const ctrl = new AbortController()
  ;(async () => {
    try {
      const res = await fetch(`${BASE}/api/sessions/${sessionId}/chat`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ prompt, model }),
        signal: ctrl.signal,
      })
      if (!res.ok) {
        onError(`HTTP ${res.status}`)
        return
      }
      await readSSE(res, onEvent, onDone)
    } catch (e: unknown) {
      if (e instanceof Error && e.name !== 'AbortError') onError(e.message)
    }
  })()
  return ctrl
}

async function readSSE(
  res: Response,
  onEvent: (evt: SSEEvent) => void,
  onDone: () => void,
) {
  const reader = res.body!.getReader()
  const decoder = new TextDecoder()
  let buf = ''

  while (true) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    const lines = buf.split('\n')
    buf = lines.pop() ?? ''
    for (const line of lines) {
      if (!line.startsWith('data: ')) continue
      const raw = line.slice(6).trim()
      if (raw === '[DONE]') { onDone(); return }
      try {
        onEvent(JSON.parse(raw) as SSEEvent)
      } catch { /* ignore malformed */ }
    }
  }
  onDone()
}
