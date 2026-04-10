import { useRef, useEffect, useState, type KeyboardEvent } from 'react'

const COMMANDS = [
  { cmd: '/clear', desc: 'Clear conversation history' },
  { cmd: '/compact', desc: 'Compact conversation context' },
  { cmd: '/config', desc: 'Show current configuration' },
  { cmd: '/cost', desc: 'Show token usage estimate' },
  { cmd: '/commit', desc: 'Stage all and commit (git)' },
  { cmd: '/diff', desc: 'Show git diff' },
  { cmd: '/doctor', desc: 'Check environment and config' },
  { cmd: '/branch', desc: 'List or create git branch' },
  { cmd: '/effort', desc: 'Switch effort level' },
  { cmd: '/fast', desc: 'Toggle fast (backup) model' },
  { cmd: '/help', desc: 'Show all commands' },
  { cmd: '/hooks', desc: 'List configured hooks' },
  { cmd: '/init', desc: 'Initialize .edoc/settings.json' },
  { cmd: '/mcp', desc: 'List MCP servers' },
  { cmd: '/memory', desc: 'Show loaded memory' },
  { cmd: '/model', desc: 'Switch model' },
  { cmd: '/new', desc: 'Start a new session' },
  { cmd: '/permissions', desc: 'Show permission mode and rules' },
  { cmd: '/review', desc: 'Review git diff with AI' },
  { cmd: '/rewind', desc: 'Restore file snapshots' },
  { cmd: '/session', desc: 'Show current session info' },
  { cmd: '/tasks', desc: 'List background tasks' },
]

interface Props {
  value: string
  onChange: (v: string) => void
  onSubmit: () => void
  disabled: boolean
  onStop?: () => void
}

export function ChatInput({ value, onChange, onSubmit, disabled, onStop }: Props) {
  const ref = useRef<HTMLTextAreaElement>(null)
  const menuRef = useRef<HTMLDivElement>(null)
  const [showMenu, setShowMenu] = useState(false)
  const [selectedIdx, setSelectedIdx] = useState(0)

  // Auto-resize textarea
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = Math.min(el.scrollHeight, 200) + 'px'
  }, [value])

  // Filter commands based on input
  const filtered = value.startsWith('/')
    ? COMMANDS.filter(c => c.cmd.startsWith(value.split(/\s/)[0]))
    : []

  // Show/hide menu
  useEffect(() => {
    const shouldShow = value.startsWith('/') && !value.includes(' ') && filtered.length > 0
    setShowMenu(shouldShow)
    if (shouldShow) setSelectedIdx(0)
  }, [value, filtered.length])

  // Scroll selected item into view
  useEffect(() => {
    if (!showMenu || !menuRef.current) return
    const items = menuRef.current.children
    if (items[selectedIdx]) {
      (items[selectedIdx] as HTMLElement).scrollIntoView({ block: 'nearest' })
    }
  }, [selectedIdx, showMenu])

  function selectCommand(cmd: string) {
    onChange(cmd + ' ')
    setShowMenu(false)
    ref.current?.focus()
  }

  function handleKey(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (showMenu) {
      if (e.key === 'ArrowDown') {
        e.preventDefault()
        setSelectedIdx(i => Math.min(i + 1, filtered.length - 1))
        return
      }
      if (e.key === 'ArrowUp') {
        e.preventDefault()
        setSelectedIdx(i => Math.max(i - 1, 0))
        return
      }
      if (e.key === 'Tab' || (e.key === 'Enter' && !e.shiftKey)) {
        e.preventDefault()
        if (filtered[selectedIdx]) {
          const cmd = filtered[selectedIdx].cmd
          // 无参数命令直接提交
          const noArgCmds = ['/clear', '/compact', '/config', '/cost', '/doctor', '/fast', '/help', '/hooks', '/mcp', '/memory', '/new', '/permissions', '/session', '/tasks']
          if (noArgCmds.includes(cmd)) {
            onChange(cmd)
            setShowMenu(false)
            // 延迟提交让 onChange 生效
            setTimeout(() => onSubmit(), 0)
          } else {
            selectCommand(cmd)
          }
        }
        return
      }
      if (e.key === 'Escape') {
        e.preventDefault()
        setShowMenu(false)
        return
      }
    }

    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      if (!disabled && value.trim()) onSubmit()
    }
  }

  return (
    <div className="border-t border-[#1e2030] bg-[#0a0b10] px-4 py-3">
      <div className="max-w-3xl mx-auto">
        <div className="relative">
          {/* Command menu */}
          {showMenu && (
            <div
              ref={menuRef}
              className="absolute bottom-full left-0 right-0 mb-1 max-h-[240px] overflow-y-auto rounded-lg border border-[#2e3044] bg-[#13141c] shadow-xl z-10"
            >
              {filtered.map((c, i) => (
                <div
                  key={c.cmd}
                  onMouseDown={(e) => { e.preventDefault(); selectCommand(c.cmd) }}
                  onMouseEnter={() => setSelectedIdx(i)}
                  className={`flex items-center gap-3 px-3 py-2 cursor-pointer transition-colors ${
                    i === selectedIdx
                      ? 'bg-[#4f46e5]/20 text-[#ececec]'
                      : 'text-[#9ca3af] hover:bg-[#1e2030]'
                  }`}
                >
                  <span className="font-mono text-sm text-[#818cf8] w-28 flex-shrink-0">{c.cmd}</span>
                  <span className="text-xs truncate">{c.desc}</span>
                </div>
              ))}
            </div>
          )}
          <div className="flex items-end gap-2 bg-[#13141c] border border-[#2e3044] rounded-2xl px-4 py-2 focus-within:border-[#4f46e5] transition-colors">
            <textarea
              ref={ref}
              value={value}
              onChange={(e) => onChange(e.target.value)}
              onKeyDown={handleKey}
              placeholder="Message edoc… (Enter to send, / for commands)"
              rows={1}
              disabled={disabled && !onStop}
              className="flex-1 bg-transparent text-sm text-[#ececec] placeholder-[#4b5563] resize-none outline-none py-1 max-h-[200px] leading-relaxed"
            />
            {disabled && onStop ? (
              <button
                onClick={onStop}
                className="flex-shrink-0 w-8 h-8 flex items-center justify-center rounded-lg bg-[#ef4444] hover:bg-[#dc2626] text-white transition-colors"
                title="Stop"
              >
                <svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor">
                  <rect x="4" y="4" width="16" height="16" rx="2" />
                </svg>
              </button>
            ) : (
              <button
                onClick={onSubmit}
                disabled={disabled || !value.trim()}
                className="flex-shrink-0 w-8 h-8 flex items-center justify-center rounded-lg bg-[#4f46e5] hover:bg-[#4338ca] disabled:opacity-30 disabled:cursor-not-allowed text-white transition-colors"
                title="Send"
              >
                <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.5">
                  <path d="M22 2L11 13M22 2L15 22l-4-9-9-4 20-7z" />
                </svg>
              </button>
            )}
          </div>
        </div>
        <p className="text-center text-[10px] text-[#374151] mt-2">
          edoc-edualc · AI may make mistakes
        </p>
      </div>
    </div>
  )
}
