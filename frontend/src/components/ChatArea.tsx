import { useEffect, useRef, useState, useCallback } from 'react'
import { MessageBubble, type ChatMessage, type ContentBlock } from './MessageBubble'
import { ChatInput } from './ChatInput'
import { streamChat, streamSessionChat, loadSession, executeCommand, compactSession } from '../api'
import type { SSEEvent, Model } from '../types'

interface Props {
  sessionId: string | null
  model: string
  models: Model[]
  onModelChange: (m: string) => void
  onSessionCreated?: (id: string) => void
  onNew?: () => void
}

// 把后端 message.ContentBlock[] 格式转成前端 ChatMessage
// 后端消息格式:
//   - user text:    { role:"user", content:[{ type:"text", text:{text:"..."} }] }
//   - assistant:    { role:"assistant", content:[{ type:"text",... }, { type:"tool_use",... }] }
//   - tool result:  { role:"user", content:[{ type:"tool_result", tool_result:{tool_use_id, content, is_error} }] }
// 一次 agent loop 产生多轮 assistant + tool_result 消息，合并为单个 ChatMessage 保持顺序。
// eslint-disable-next-line @typescript-eslint/no-explicit-any
function convertMessages(raw: any[]): ChatMessage[] {
  const result: ChatMessage[] = []
  let currentAssistant: ChatMessage | null = null
  const toolBlockMap = new Map<string, number>() // tool_use_id → block index

  // 判断一条 user 消息是否是 tool_result（而非普通 user text）
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  function isToolResultMsg(msg: any): boolean {
    const blocks = msg.content ?? []
    return Array.isArray(blocks) && blocks.some((b: any) => b.type === 'tool_result')
  }

  // 判断一条 user 消息是否是 agent loop 内部注入的系统消息
  // （snippet 注入、hook context、task notification 等，不应展示给用户）
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  function isSystemInjectedMsg(msg: any): boolean {
    const blocks = msg.content ?? []
    if (!Array.isArray(blocks) || blocks.length === 0) return false
    const text = blocks[0]?.text?.text ?? ''
    if (!text) return false
    return text.startsWith('<system-reminder>') ||
      text.startsWith('<teammate-message') ||
      text.startsWith('Output token limit hit') ||
      text.startsWith('Stop hook blocking error') ||
      text.startsWith('PostToolUse hook blocking error')
  }

  for (const msg of raw) {
    if (msg.role === 'user' && !isToolResultMsg(msg) && !isSystemInjectedMsg(msg)) {
      // 普通 user 消息 → flush assistant, 新建 user ChatMessage
      if (currentAssistant) {
        result.push(currentAssistant)
        currentAssistant = null
        toolBlockMap.clear()
      }
      const blocks: ContentBlock[] = []
      const contentBlocks = msg.content ?? []
      if (Array.isArray(contentBlocks)) {
        for (const block of contentBlocks) {
          if (block.type === 'text' && block.text?.text) {
            blocks.push({ type: 'text', text: block.text.text })
          }
        }
      }
      if (blocks.length === 0) {
        blocks.push({ type: 'text', text: typeof msg.content === 'string' ? msg.content : '' })
      }
      result.push({ role: 'user', blocks })
    } else if (msg.role === 'user' && isToolResultMsg(msg)) {
      // tool_result 消息 — 合并到当前 assistant
      if (!currentAssistant) continue
      const contentBlocks = msg.content ?? []
      for (const block of contentBlocks) {
        if (block.type === 'tool_result' && block.tool_result) {
          const toolUseId = block.tool_result.tool_use_id ?? ''
          const idx = toolBlockMap.get(toolUseId)
          if (idx !== undefined && currentAssistant.blocks[idx]) {
            currentAssistant.blocks[idx] = {
              ...currentAssistant.blocks[idx],
              toolResult: block.tool_result.content,
              toolIsError: block.tool_result.is_error,
            }
          }
        }
      }
    } else if (msg.role === 'assistant') {
      if (!currentAssistant) {
        currentAssistant = { role: 'assistant', blocks: [] }
      }
      const contentBlocks = msg.content ?? []
      for (const block of contentBlocks) {
        if (block.type === 'text' && block.text?.text) {
          currentAssistant.blocks.push({ type: 'text', text: block.text.text })
        } else if (block.type === 'tool_use' && block.tool_use) {
          const idx = currentAssistant.blocks.length
          const inputStr = block.tool_use.input != null
            ? (typeof block.tool_use.input === 'string' ? block.tool_use.input : JSON.stringify(block.tool_use.input))
            : undefined
          currentAssistant.blocks.push({
            type: 'tool_call',
            toolName: block.tool_use.name,
            toolInput: inputStr,
            toolUseId: block.tool_use.id,
          })
          if (block.tool_use.id) {
            toolBlockMap.set(block.tool_use.id, idx)
          }
        }
      }
    }
    // skip system / compact_boundary messages
  }

  if (currentAssistant) {
    result.push(currentAssistant)
  }

  return result
}

export function ChatArea({ sessionId, model, models, onModelChange, onSessionCreated, onNew }: Props) {
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

  // 添加系统消息（命令输出）
  const addSystemMessage = useCallback((text: string) => {
    setMessages(prev => [...prev, {
      role: 'assistant',
      blocks: [{ type: 'text', text }],
    }])
  }, [])

  // SSE event handlers (shared between normal chat and /review)
  const buildOnEvent = useCallback(() => (evt: SSEEvent) => {
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const e = evt as any
    if (e.type === 'text_delta') {
      setMessages(prev => {
        const next = [...prev]
        const last = next[next.length - 1]
        if (last?.role !== 'assistant') return prev
        const blocks = [...last.blocks]
        const lastBlock = blocks[blocks.length - 1]
        if (lastBlock?.type === 'text') {
          blocks[blocks.length - 1] = { ...lastBlock, text: (lastBlock.text ?? '') + e.delta }
        } else {
          blocks.push({ type: 'text', text: e.delta })
        }
        next[next.length - 1] = { ...last, blocks }
        return next
      })
    } else if (e.type === 'tool_use') {
      setMessages(prev => {
        const next = [...prev]
        const last = next[next.length - 1]
        if (last?.role !== 'assistant') return prev
        const blocks = [...last.blocks, {
          type: 'tool_call' as const,
          toolName: e.tool_name,
          toolInput: e.tool_input,
          toolUseId: e.tool_use_id,
        }]
        next[next.length - 1] = { ...last, blocks }
        return next
      })
    } else if (e.type === 'tool_result') {
      setMessages(prev => {
        const next = [...prev]
        const last = next[next.length - 1]
        if (last?.role !== 'assistant') return prev
        const blocks = [...last.blocks]
        let matched = false
        if (e.tool_use_id) {
          for (let i = 0; i < blocks.length; i++) {
            if (blocks[i].type === 'tool_call' && blocks[i].toolUseId === e.tool_use_id) {
              blocks[i] = { ...blocks[i], toolResult: e.content, toolIsError: e.is_error }
              matched = true
              break
            }
          }
        }
        if (!matched) {
          for (let i = 0; i < blocks.length; i++) {
            if (blocks[i].type === 'tool_call' && blocks[i].toolResult === undefined) {
              blocks[i] = { ...blocks[i], toolResult: e.content, toolIsError: e.is_error }
              break
            }
          }
        }
        next[next.length - 1] = { ...last, blocks }
        return next
      })
    } else if (e.type === 'error') {
      setMessages(prev => {
        const next = [...prev]
        const last = next[next.length - 1]
        if (last?.role !== 'assistant') return prev
        const blocks = [...last.blocks]
        blocks.push({ type: 'text', text: `Error: ${e.error || 'Unknown error'}` })
        next[next.length - 1] = { ...last, blocks, isStreaming: false }
        return next
      })
    }
  }, [])

  const buildOnDone = useCallback(() => () => {
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
  }, [])

  const buildOnError = useCallback(() => (err: string) => {
    setStreaming(false)
    setMessages(prev => {
      const next = [...prev]
      const last = next[next.length - 1]
      if (last?.role === 'assistant') {
        next[next.length - 1] = {
          ...last,
          blocks: [...last.blocks, { type: 'text', text: `Error: ${err}` }],
          isStreaming: false,
        }
      }
      return next
    })
    abortRef.current = null
  }, [])

  const handleSubmit = useCallback(() => {
    const prompt = input.trim()
    if (!prompt || streaming) return
    setInput('')

    // --- 斜杠命令拦截 ---
    if (prompt.startsWith('/')) {
      const parts = prompt.split(/\s+/)
      const cmd = parts[0]

      // 前端直接处理
      if (cmd === '/clear') {
        setMessages([])
        return
      }
      if (cmd === '/help') {
        addSystemMessage(`Commands:
  /new                  Start a new session
  /clear                Clear conversation history
  /compact              Compact conversation context
  /model <name>         Switch model
  /cost                 Show token usage estimate
  /config               Show current configuration
  /doctor               Check environment and config
  /diff [args]          Show git diff
  /branch [name]        List or create git branch
  /commit <msg>         Stage all and commit (git)
  /review [ref]         Review git diff with AI
  /mcp                  List MCP servers
  /hooks                List configured hooks
  /permissions          Show permission mode and rules
  /session              Show current session info
  /memory               Show loaded memory
  /tasks                List background tasks
  /init                 Initialize .edoc/settings.json
  /fast                 Toggle fast (backup) model
  /effort <low|med|high> Switch effort level`)
        return
      }
      if (cmd === '/model' && parts.length > 1) {
        onModelChange(parts[1])
        addSystemMessage(`Model switched to: ${parts[1]}`)
        return
      }
      if (cmd === '/new') {
        onNew?.()
        return
      }

      // /compact — SSE
      if (cmd === '/compact') {
        if (!sessionId) {
          addSystemMessage('No active session to compact.')
          return
        }
        addSystemMessage('Compacting...')
        setStreaming(true)
        compactSession(
          sessionId,
          (evt) => {
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
            const e = evt as any
            if (e.type === 'compact_complete') {
              setMessages(prev => {
                const next = [...prev]
                next[next.length - 1] = {
                  role: 'assistant',
                  blocks: [{ type: 'text', text: `Compacted: ~${e.pre_compact_tokens} → ~${e.post_compact_tokens} tokens` }],
                }
                return next
              })
            } else if (e.type === 'error') {
              addSystemMessage(`Compact error: ${e.error}`)
            }
          },
          () => {
            setStreaming(false)
            // 重新加载 session 消息
            if (sessionId) {
              loadSession(sessionId).then(data => {
                setMessages(convertMessages(data.messages ?? []))
              }).catch(() => {})
            }
          },
          (err) => {
            setStreaming(false)
            addSystemMessage(`Compact error: ${err}`)
          },
        )
        return
      }

      // /commit 和 /review — 走模型（通过 skill 系统）
      // 模型看到 system-reminder 里的 skill 列表，会自动调用 Skill("commit") 或 Skill("review")
      if (cmd === '/commit' || cmd === '/review') {
        const args = parts.slice(1).join(' ')
        const skillPrompt = cmd === '/commit'
          ? (args ? `Please commit my changes with message: ${args}` : 'Please commit my current changes to git.')
          : (args ? `Please review the changes: ${args}` : 'Please review my current code changes.')
        // 作为普通 prompt 发送，模型会通过 skill 系统处理
        setMessages(prev => [...prev, { role: 'user', blocks: [{ type: 'text', text: prompt }] }])
        setMessages(prev => [...prev, { role: 'assistant', blocks: [], isStreaming: true }])
        setStreaming(true)
        const onEvent = buildOnEvent()
        const onDone = buildOnDone()
        const onError = buildOnError()
        if (sessionId) {
          abortRef.current = streamSessionChat(sessionId, skillPrompt, model, onEvent, onDone, onError)
        } else {
          abortRef.current = streamChat(skillPrompt, model, onEvent, onDone, onError)
        }
        onSessionCreated?.(sessionId ?? '')
        return
      }

      // 其他命令 — 走后端 /api/command
      setMessages(prev => [...prev, { role: 'user', blocks: [{ type: 'text', text: prompt }] }])
      ;(async () => {
        const resp = await executeCommand(prompt, sessionId ?? undefined, model)
        const text = resp.error ? `Error: ${resp.error}` : (resp.output || '(no output)')
        addSystemMessage(text)
        // 处理 action
        if (resp.action === 'model_changed' && resp.data?.model) {
          onModelChange(resp.data.model)
        }
      })()
      return
    }

    // --- 正常 prompt ---

    // Add user message
    setMessages(prev => [...prev, { role: 'user', blocks: [{ type: 'text', text: prompt }] }])

    // Add empty assistant message for streaming
    setMessages(prev => [...prev, { role: 'assistant', blocks: [], isStreaming: true }])
    setStreaming(true)

    const onEvent = buildOnEvent()
    const onDone = buildOnDone()
    const onError = buildOnError()

    if (sessionId) {
      abortRef.current = streamSessionChat(sessionId, prompt, model, onEvent, onDone, onError)
    } else {
      abortRef.current = streamChat(prompt, model, onEvent, onDone, onError)
    }
    onSessionCreated?.(sessionId ?? '')
  }, [input, streaming, sessionId, model, onSessionCreated, onModelChange, onNew, addSystemMessage])

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
