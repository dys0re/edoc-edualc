import { useEffect, useState, useCallback } from 'react'
import { Sidebar } from './components/Sidebar'
import { ChatArea } from './components/ChatArea'
import { fetchSessions, fetchModels, createSession, deleteSession } from './api'
import type { Session, Model } from './types'

export default function App() {
  const [sessions, setSessions] = useState<Session[]>([])
  const [activeId, setActiveId] = useState<string | null>(null)
  const [models, setModels] = useState<Model[]>([])
  const [model, setModel] = useState('claude-sonnet-4-20250514')

  useEffect(() => {
    fetchModels().then(res => {
      setModels(res.models)
      if (res.default) setModel(res.default)
      else if (res.models.length > 0) setModel(res.models[0].model)
    })
    fetchSessions().then(setSessions)
  }, [])

  const handleNew = useCallback(async () => {
    const sess = await createSession(model)
    setSessions(prev => [sess, ...prev])
    setActiveId(sess.id)
  }, [model])

  const handleSelect = useCallback((id: string) => {
    setActiveId(id)
  }, [])

  const handleDelete = useCallback(async (id: string) => {
    await deleteSession(id)
    setSessions(prev => prev.filter(s => s.id !== id))
    if (activeId === id) setActiveId(null)
  }, [activeId])

  return (
    <div className="flex h-full w-full">
      <Sidebar
        sessions={sessions}
        activeId={activeId}
        onSelect={handleSelect}
        onNew={handleNew}
        onDelete={handleDelete}
      />
      <ChatArea
        sessionId={activeId}
        model={model}
        models={models}
        onModelChange={setModel}
        onNew={handleNew}
      />
    </div>
  )
}
