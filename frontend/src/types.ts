export interface Session {
  id: string
  title: string
  model: string
  created_at: string
  updated_at: string
}

export interface Message {
  role: 'user' | 'assistant'
  content: string | ContentBlock[]
}

export interface ContentBlock {
  type: 'text' | 'tool_use' | 'tool_result'
  text?: string
  name?: string
  input?: unknown
  content?: string
}

export interface Model {
  provider: string
  model: string
}

// SSE event types from backend
export type SSEEvent =
  | { type: 'text_delta'; delta: string }
  | { type: 'thinking_delta'; delta: string }
  | { type: 'tool_use'; tool_name: string; tool_input: unknown }
  | { type: 'tool_result'; tool_name: string; content: string; is_error: boolean }
  | { type: 'error'; error: string }
  | { type: 'warning'; delta: string }
  | { type: 'turn_complete' }
  | { type: 'message_complete'; message: Message }
  | { type: 'compacted' | 'permission_request' | string & Record<never, never> }
