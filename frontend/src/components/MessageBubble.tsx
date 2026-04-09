import { Markdown } from './Markdown'

interface ToolCall {
  name: string
  input?: unknown
  result?: string
  isError?: boolean
}

export interface ChatMessage {
  role: 'user' | 'assistant'
  content: string
  toolCalls?: ToolCall[]
  isStreaming?: boolean
}

interface Props {
  message: ChatMessage
}

export function MessageBubble({ message }: Props) {
  const isUser = message.role === 'user'

  return (
    <div className={`flex gap-3 ${isUser ? 'flex-row-reverse' : 'flex-row'}`}>
      {/* Avatar */}
      <div className={`w-7 h-7 flex-shrink-0 rounded-full flex items-center justify-center text-xs font-semibold ${
        isUser ? 'bg-[#4f46e5] text-white' : 'bg-[#1e2030] text-[#818cf8]'
      }`}>
        {isUser ? 'U' : 'A'}
      </div>

      <div className={`flex flex-col gap-1 max-w-[80%] ${isUser ? 'items-end' : 'items-start'}`}>
        {/* Tool calls */}
        {message.toolCalls && message.toolCalls.length > 0 && (
          <div className="flex flex-col gap-1 w-full">
            {message.toolCalls.map((tc, i) => (
              <ToolCallBlock key={i} toolCall={tc} />
            ))}
          </div>
        )}

        {/* Text content */}
        {message.content && (
          <div className={`rounded-2xl px-4 py-2.5 text-sm ${
            isUser
              ? 'bg-[#4f46e5] text-white rounded-tr-sm'
              : 'bg-[#13141c] text-[#ececec] rounded-tl-sm border border-[#1e2030]'
          }`}>
            {isUser ? (
              <p className="whitespace-pre-wrap">{message.content}</p>
            ) : (
              <Markdown content={message.content} />
            )}
            {message.isStreaming && (
              <span className="inline-block w-1.5 h-4 bg-[#818cf8] ml-0.5 animate-pulse rounded-sm" />
            )}
          </div>
        )}
        {/* Streaming with no content yet */}
        {!message.content && message.isStreaming && (
          <div className="bg-[#13141c] border border-[#1e2030] rounded-2xl rounded-tl-sm px-4 py-2.5">
            <span className="inline-block w-1.5 h-4 bg-[#818cf8] animate-pulse rounded-sm" />
          </div>
        )}
      </div>
    </div>
  )
}

function ToolCallBlock({ toolCall }: { toolCall: ToolCall }) {
  return (
    <div className="text-xs rounded-lg border border-[#2e3044] bg-[#0f1117] overflow-hidden">
      <div className="flex items-center gap-2 px-3 py-1.5 bg-[#1e2030] text-[#9ca3af]">
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z" />
        </svg>
        <span className="font-mono font-medium text-[#c084fc]">{toolCall.name}</span>
        {toolCall.isError && (
          <span className="ml-auto text-[#ef4444]">error</span>
        )}
      </div>
      {toolCall.result && (
        <div className={`px-3 py-2 font-mono text-[11px] leading-relaxed max-h-32 overflow-y-auto ${
          toolCall.isError ? 'text-[#ef4444]' : 'text-[#6b7280]'
        }`}>
          {toolCall.result.length > 300 ? toolCall.result.slice(0, 300) + '…' : toolCall.result}
        </div>
      )}
    </div>
  )
}
