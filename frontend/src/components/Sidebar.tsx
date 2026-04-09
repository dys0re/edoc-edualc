import type { Session } from '../types'

interface Props {
  sessions: Session[]
  activeId: string | null
  onSelect: (id: string) => void
  onNew: () => void
  onDelete: (id: string) => void
}

export function Sidebar({ sessions, activeId, onSelect, onNew, onDelete }: Props) {
  return (
    <aside className="w-64 flex-shrink-0 flex flex-col bg-[#0a0b10] border-r border-[#1e2030]">
      {/* Header */}
      <div className="flex items-center justify-between px-4 py-3 border-b border-[#1e2030]">
        <span className="text-sm font-semibold text-[#ececec]">edoc</span>
        <button
          onClick={onNew}
          title="New chat"
          className="w-7 h-7 flex items-center justify-center rounded-md text-[#9ca3af] hover:text-[#ececec] hover:bg-[#1e2030] transition-colors"
        >
          <svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
            <path d="M12 5v14M5 12h14" />
          </svg>
        </button>
      </div>

      {/* Session list */}
      <div className="flex-1 overflow-y-auto py-2">
        {sessions.length === 0 && (
          <p className="px-4 py-3 text-xs text-[#4b5563]">No conversations yet</p>
        )}
        {sessions.map((s) => (
          <div
            key={s.id}
            onClick={() => onSelect(s.id)}
            className={`group flex items-center gap-2 px-3 py-2 mx-2 rounded-md cursor-pointer transition-colors ${
              s.id === activeId
                ? 'bg-[#1e2030] text-[#ececec]'
                : 'text-[#9ca3af] hover:bg-[#13141c] hover:text-[#ececec]'
            }`}
          >
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" className="flex-shrink-0 opacity-60">
              <path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z" />
            </svg>
            <span className="flex-1 text-xs truncate">
              {s.title || s.id.slice(0, 8)}
            </span>
            <button
              onClick={(e) => { e.stopPropagation(); onDelete(s.id) }}
              className="opacity-0 group-hover:opacity-100 w-5 h-5 flex items-center justify-center rounded text-[#6b7280] hover:text-[#ef4444] transition-all"
            >
              <svg width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2">
                <path d="M18 6L6 18M6 6l12 12" />
              </svg>
            </button>
          </div>
        ))}
      </div>
    </aside>
  )
}
