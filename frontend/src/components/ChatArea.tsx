import { useEffect, useRef, useState, useCallback } from 'react'
import { MessageBubble, type ChatMessage } from './MessageBubble'
import { ChatInput } from './ChatInput'
import { streamChat, streamSessionChat, loadSession } from '../api'
import type { SSEEvent, Model } from '../types'

interface Props {
  sessionId: string | null
  model: string
  models: Model[]
  onModelChange: (m: string) => void
  onSessionCreated?: (id: string) => void
}

// 把后端 message.ContentBlock[] 格式转成前端 ChatMessage
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function convertMessages(raw: any[]): ChatMessage[] {
  const result: ChatMessage[] = []
  for (const msg of raw) {
    if (msg.role !== 'user' && msg.role !== 'assistant') continue
    const blocks: { type: string; text?: { text: string }; tool_use?: { name: string; input: unknown }; tool_result?: { content: string; is_error: boolean } }[] = msg.content ?? []

    let text = ''
    const toolCalls: ChatMessage['toolCalls'] = []

    for (const block of blocks) {
      if (block.type === 'text' && block.text?.text) {
        text += block.text.text
      } else if (block.type === 'tool_use' && block.tool_use) {
        toolCalls.push({ name: block.tool_use.name })
      } else if (block.type === 'tool_result' && block.tool_result) {
        // 匹配上一个 tool_use
        if (toolCalls.length > 0) {
          const last = toolCalls[toolCalls.length - 1]
          if (!last.result) {
            toolCalls[toolCalls.length - 1] = {
              ...last,
              result: block.tool_result.content,
              isError: block.tool_result.is_error,
            }
          }
        }
      }
    }

    result.push({
      role: msg.role,
      content: text,
      toolCalls: toolCalls.length > 0 ? toolCalls : undefined,
    })
  }
  return result
}

export function ChatArea({ sessionId, model, models, onModelChange, onSessionCreated }: Props) {
  const [messages, setMessages] = useState<ChatMessage[]>([])
  const [input, setInput] = useState('')
  const [streaming, setStreaming] = useState(false)
  const [loading, setLoading] = useState(false)
  const abortRef = useRef<AbortController | null>(null)
  const bottomRef = useRef<HTMLDivElement>(null)
  const prevSessionId = useRef<string | null>(null)

  // 切换会话时加载历史消息
  useEffect(() => {
    if (prevSessionId.current === sessionId) return
    prevSessionId.current = sessionId
    setMessages([])
    if (!sessionId) return

    setLoading(true)
    loadSession(sessionId)
      .then(data => {
        setMessages(convertMessages(data.messages ?? []))
      })
      .catch(() => { /* session may not exist yet */ })
      .finally(() => setLoading(false))
  }, [sessionId])

  // Auto-scroll to bottom
  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
  }, [messages])

  const handleSubmit = useCallback(() => {
    const prompt = input.trim()
    if (!prompt || streaming) return
    setInput('')

    // Add user message
    setMessages(prev => [...prev, { role: 'user', content: prompt }])

    // Add empty assistant message for streaming
    setMessages(prev => [...prev, { role: 'assistant', content: '', isStreaming: true }])
    setStreaming(true)

    const onEvent = (evt: SSEEvent) => {
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const e = evt as any
      if (e.type === 'text_delta') {
        setMessages(prev => {
          const next = [...prev]
          const last = next[next.length - 1]
          if (last?.role === 'assistant') {
            next[next.length - 1] = { ...last, content: last.content + e.delta }
          }
          return next
        })
      } else if (e.type === 'tool_use') {
        setMessages(prev => {
          const next = [...prev]
          const last = next[next.length - 1]
          if (last?.role === 'assistant') {
            const toolCalls = [...(last.toolCalls ?? []), { name: e.tool_name }]
            next[next.length - 1] = { ...last, toolCalls }
          }
          return next
        })
      } else if (e.type === 'tool_result') {
        setMessages(prev => {
          const next = [...prev]
          const last = next[next.length - 1]
          if (last?.role === 'assistant' && last.toolCalls?.length) {
            const toolCalls = [...last.toolCalls]
            const lastTool = toolCalls[toolCalls.length - 1]
            if (lastTool.name === e.tool_name || !lastTool.result) {
              toolCalls[toolCalls.length - 1] = {
                ...lastTool,
                result: e.content,
                isError: e.is_error,
              }
            }
            next[next.length - 1] = { ...last, toolCalls }
          }
          return next
        })
      }
    }

    const onDone = () => {
      setStreaming(false)
      setMessages(prev => {
        const next = [...prev]
        const last = next[next.length - 1]
        if (last?.role === 'assistant') {
          next[next.length - 1] = { ...last, isStreaming: false }
        }
        return next
      })
      abortRef.current = null
    }

    const onError = (err: string) => {
      setStreaming(false)
      setMessages(prev => {
        const next = [...prev]
        const last = next[next.length - 1]
        if (last?.role === 'assistant') {
          next[next.length - 1] = { ...last, content: `Error: ${err}`, isStreaming: false }
        }
        return next
      })
      abortRef.current = null
    }

    if (sessionId) {
      abortRef.current = streamSessionChat(sessionId, prompt, model, onEvent, onDone, onError)
    } else {
      abortRef.current = streamChat(prompt, model, onEvent, onDone, onError)
    }
    onSessionCreated?.(sessionId ?? '')
  }, [input, streaming, sessionId, model, onSessionCreated])

  const handleStop = useCallback(() => {
    abortRef.current?.abort()
    abortRef.current = null
    setStreaming(false)
    setMessages(prev => {
      const next = [...prev]
      const last = next[next.length - 1]
      if (last?.role === 'assistant') {
        next[next.length - 1] = { ...last, isStreaming: false }
      }
      return next
    })
  }, [])

  return (
    <div className="flex-1 flex flex-col min-w-0">
      {/* Topbar */}
      <div className="flex items-center justify-between px-4 py-2.5 border-b border-[#1e2030] bg-[#0a0b10]">
        <span className="text-sm text-[#6b7280]">
          {sessionId ? `Session ${sessionId.slice(0, 8)}` : 'New chat'}
        </span>
        <select
          value={model}
          onChange={e => onModelChange(e.target.value)}
          className="text-xs bg-[#13141c] border border-[#2e3044] text-[#9ca3af] rounded-md px-2 py-1 outline-none hover:border-[#4f46e5] transition-colors cursor-pointer"
        >
          {models.map(m => (
            <option key={`${m.provider}/${m.model}`} value={m.model}>
              {m.model}
            </option>
          ))}
          {models.length === 0 && (
            <option value={model}>{model}</option>
          )}
        </select>
      </div>

      {/* Messages */}
      <div className="flex-1 overflow-y-auto px-4 py-6">
        {loading ? (
          <div className="h-full flex items-center justify-center">
            <div className="w-5 h-5 border-2 border-[#4f46e5] border-t-transparent rounded-full animate-spin" />
          </div>
        ) : messages.length === 0 ? (
          <div className="h-full flex flex-col items-center justify-center text-center gap-3">
            <div className="w-12 h-12 rounded-2xl bg-[#1e2030] flex items-center justify-center">
              <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="#818cf8" strokeWidth="1.5">
                <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
              </svg>
            </div>
            <p className="text-[#4b5563] text-sm">Start a conversation</p>
          </div>
        ) : (
          <div className="max-w-3xl mx-auto flex flex-col gap-4">
            {messages.map((msg, i) => (
              <MessageBubble key={i} message={msg} />
            ))}
            <div ref={bottomRef} />
          </div>
        )}
      </div>

      {/* Input */}
      <ChatInput
        value={input}
        onChange={setInput}
        onSubmit={handleSubmit}
        disabled={streaming}
        onStop={streaming ? handleStop : undefined}
      />
    </div>
  )
}
