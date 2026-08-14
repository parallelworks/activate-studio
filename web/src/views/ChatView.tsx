import { useMemo, useRef, useState } from 'react'
import {
  ChatProvider, ChatLayout, ChatThread, ChatEmptyState,
} from '@parallelworks/ai-chat'
import '@parallelworks/ai-chat/styles.css'
import { createStudioAdapter } from '../adapter'
import { useAppConfig } from '../config'

export function ChatView() {
  const [activeId, setActiveId] = useState<string | null>(null)
  const [rail, setRail] = useState(() => Number(localStorage.getItem('ade-chat-rail')) || 260)
  const adapter = useMemo(() => createStudioAdapter(), [])
  const cfg = useAppConfig()

  const canvasRef = useRef<HTMLDivElement>(null)
  const handleRef = useRef<HTMLDivElement>(null)

  // Drag writes straight to the DOM; React state commits once on release,
  // so the provider subtree does not re-render per mousemove.
  const onRailDrag = (e: React.MouseEvent) => {
    e.preventDefault()
    const startX = e.clientX
    const startW = rail
    let w = startW
    const onMove = (ev: MouseEvent) => {
      w = Math.min(460, Math.max(180, startW + (ev.clientX - startX)))
      canvasRef.current?.style.setProperty('--ade-chat-rail', `${w}px`)
      if (handleRef.current) handleRef.current.style.left = `${w - 3}px`
    }
    const onUp = () => {
      document.body.classList.remove('resizing')
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
      setRail(w)
      localStorage.setItem('ade-chat-rail', String(w))
    }
    document.body.classList.add('resizing')
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }

  // The empty state freezes its greeting and starter prompts at mount, so
  // wait for the deployment config before mounting the chat tree.
  if (!cfg.loaded) return <div className="chat-wrap"><div className="chat-canvas card" /></div>

  return (
    <ChatProvider
      adapter={adapter}
      currentUser={cfg.user}
      navigation={{
        toConversation: id => setActiveId(id),
        toNewChat: () => setActiveId(null),
        toAttachments: () => {},
      }}
      notify={{
        success: () => {},
        error: (m: string) => console.error(m),
        info: () => {},
      }}
      activeConversationId={activeId}
      config={{ variant: 'modern', suggestedPrompts: cfg.suggestedPrompts }}
    >
      <div className="chat-wrap">
        <div ref={canvasRef} className="chat-canvas card" style={{ ['--ade-chat-rail' as string]: `${rail}px` }}>
          <ChatLayout>
            {activeId ? <ChatThread conversationId={activeId} /> : <ChatEmptyState />}
          </ChatLayout>
          <div ref={handleRef} className="chat-rail-handle" style={{ left: rail - 3 }} onMouseDown={onRailDrag} title="Drag to resize conversations" />
        </div>
      </div>
    </ChatProvider>
  )
}
