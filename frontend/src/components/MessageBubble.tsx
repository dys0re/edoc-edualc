import { useState } from 'react'
import { Markdown } from './Markdown'

export interface ContentBlock {
  type: 'text' | 'tool_call'
  // text
  text?: string
  // tool_call
  toolName?: string
  toolInput?: string  // JSON string of tool input params
  toolUseId?: string
  toolResult?: string
  toolIsError?: boolean
}

export interface ChatMessage {
  role: 'user' | 'assistant'
  blocks: ContentBlock[]
  isStreaming?: boolean
}

interface Props {
  message: ChatMessage
}

export function MessageBubble({ message }: Props) {
  const isUser = message.role === 'user'

  // user messages: just show the first text block
  if (isUser) {
    const text = message.blocks.find(b => b.type === 'text')?.text ?? ''
    return (
      <div className="flex gap-3 flex-row-reverse">
        <div className="w-7 h-7 flex-shrink-0 rounded-full flex items-center justify-center text-xs font-semibold bg-[#4f46e5] text-white">
          U
        </div>
        <div className="flex flex-col gap-1 max-w-[80%] items-end">
          <div className="rounded-2xl px-4 py-2.5 text-sm bg-[#4f46e5] text-white rounded-tr-sm">
            <p className="whitespace-pre-wrap">{text}</p>
          </div>
        </div>
      </div>
    )
  }

  // assistant messages: render blocks in order
  const hasAnyContent = message.blocks.some(b =>
    (b.type === 'text' && b.text) || b.type === 'tool_call'
  )

  return (
    <div className="flex gap-3 flex-row">
      <div className="w-7 h-7 flex-shrink-0 rounded-full flex items-center justify-center text-xs font-semibold bg-[#1e2030] text-[#818cf8]">
        A
      </div>
      <div className="flex flex-col gap-1.5 max-w-[80%] items-start min-w-0">
        {message.blocks.map((block, i) => {
          if (block.type === 'text' && block.text) {
            return (
              <div
                key={i}
                className="rounded-2xl px-4 py-2.5 text-sm bg-[#13141c] text-[#ececec] rounded-tl-sm border border-[#1e2030]"
              >
                <Markdown content={block.text} />
                {/* Show cursor on the last text block while streaming */}
                {message.isStreaming && i === message.blocks.length - 1 && (
                  <span className="inline-block w-1.5 h-4 bg-[#818cf8] ml-0.5 animate-pulse rounded-sm" />
                )}
              </div>
            )
          }
          if (block.type === 'tool_call') {
            return <ToolCallBlock key={i} block={block} />
          }
          return null
        })}

        {/* Streaming with no content yet */}
        {!hasAnyContent && message.isStreaming && (
          <div className="bg-[#13141c] border border-[#1e2030] rounded-2xl rounded-tl-sm px-4 py-2.5">
            <span className="inline-block w-1.5 h-4 bg-[#818cf8] animate-pulse rounded-sm" />
          </div>
        )}
      </div>
    </div>
  )
}

// 从 tool input JSON 中提取摘要（文件名、pattern 等）
function toolInputSummary(toolName?: string, toolInput?: string): string {
  if (!toolInput) return ''
  try {
    const input = JSON.parse(toolInput)
    switch (toolName) {
      case 'Read': return input.file_path ?? ''
      case 'Write': return input.file_path ?? ''
      case 'Edit': return input.file_path ?? ''
      case 'Glob': return input.pattern ?? ''
      case 'Grep': return input.pattern ?? ''
      case 'Bash': {
        const cmd = input.command ?? ''
        return cmd.length > 60 ? cmd.slice(0, 60) + '…' : cmd
      }
      default: {
        // fallback: show file_path or pattern if present
        return input.file_path ?? input.pattern ?? input.query ?? ''
      }
    }
  } catch {
    return ''
  }
}

function ToolCallBlock({ block }: { block: ContentBlock }) {
  const [collapsed, setCollapsed] = useState(true)
  const summary = toolInputSummary(block.toolName, block.toolInput)

  return (
    <div className="text-xs rounded-lg border border-[#2e3044] bg-[#0f1117] overflow-hidden w-full">
      <button
        type="button"
        onClick={() => setCollapsed(c => !c)}
        className="flex items-center gap-2 px-3 py-1.5 bg-[#1e2030] text-[#9ca3af] w-full text-left hover:bg-[#252738] transition-colors"
      >
        <svg
          width="10"
          height="10"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="2"
          className={`transition-transform ${collapsed ? '' : 'rotate-90'}`}
        >
          <path d="M9 18l6-6-6-6" />
        </svg>
        <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
          <path d="M14.7 6.3a1 1 0 0 0 0 1.4l1.6 1.6a1 1 0 0 0 1.4 0l3.77-3.77a6 6 0 0 1-7.94 7.94l-6.91 6.91a2.12 2.12 0 0 1-3-3l6.91-6.91a6 6 0 0 1 7.94-7.94l-3.76 3.76z" />
        </svg>
        <span className="font-mono font-medium text-[#c084fc]">{block.toolName}</span>
        {summary && (
          <span className="text-[#6b7280] truncate" title={summary}>{summary}</span>
        )}
        {block.toolIsError && (
          <span className="ml-auto text-[#ef4444]">error</span>
        )}
        {!block.toolIsError && block.toolResult !== undefined && (
          <span className="ml-auto text-[#22c55e]">done</span>
        )}
      </button>
      {!collapsed && block.toolResult && (
        <div className={`px-3 py-2 font-mono text-[11px] leading-relaxed max-h-48 overflow-y-auto ${
          block.toolIsError ? 'text-[#ef4444]' : 'text-[#6b7280]'
        }`}>
          {block.toolResult.length > 500 ? block.toolResult.slice(0, 500) + '…' : block.toolResult}
        </div>
      )}
    </div>
  )
}
