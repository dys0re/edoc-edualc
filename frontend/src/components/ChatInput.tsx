import { useRef, useEffect, type KeyboardEvent } from 'react'

interface Props {
  value: string
  onChange: (v: string) => void
  onSubmit: () => void
  disabled: boolean
  onStop?: () => void
}

export function ChatInput({ value, onChange, onSubmit, disabled, onStop }: Props) {
  const ref = useRef<HTMLTextAreaElement>(null)

  // Auto-resize textarea
  useEffect(() => {
    const el = ref.current
    if (!el) return
    el.style.height = 'auto'
    el.style.height = Math.min(el.scrollHeight, 200) + 'px'
  }, [value])

  function handleKey(e: KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && !e.shiftKey) {
      e.preventDefault()
      if (!disabled && value.trim()) onSubmit()
    }
  }

  return (
    <div className="border-t border-[#1e2030] bg-[#0a0b10] px-4 py-3">
      <div className="max-w-3xl mx-auto">
        <div className="flex items-end gap-2 bg-[#13141c] border border-[#2e3044] rounded-2xl px-4 py-2 focus-within:border-[#4f46e5] transition-colors">
          <textarea
            ref={ref}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            onKeyDown={handleKey}
            placeholder="Message edoc… (Enter to send, Shift+Enter for newline)"
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
        <p className="text-center text-[10px] text-[#374151] mt-2">
          edoc-edualc · AI may make mistakes
        </p>
      </div>
    </div>
  )
}
