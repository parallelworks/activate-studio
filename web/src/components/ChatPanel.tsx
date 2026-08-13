import { useEffect, useMemo, useState } from 'react'
import {
  ChatProvider, ChatThread, ChatEmptyState, useChat,
} from '@parallelworks/ai-chat'
import '@parallelworks/ai-chat/styles.css'
import { createStudioAdapter } from '../adapter'

const SUGGESTED = [
  'What is in the revenue pipeline right now?',
  'Summarize our standard past-performance references.',
  'Which proposals are in process?',
  'Show the DAG of the complexdag workflow.',
]

function DockHeader({ activeId, onSelect, onNew, fullScreen, onToggleFull }: {
  activeId: string | null
  onSelect: (id: string) => void
  onNew: () => void
  fullScreen: boolean
  onToggleFull: () => void
}) {
  const { conversations, loadConversations } = useChat()
  useEffect(() => { loadConversations() }, [loadConversations])
  return (
    <div className="chat-dock-head">
      <select
        value={activeId ?? ''}
        onChange={e => (e.target.value ? onSelect(e.target.value) : onNew())}
        title="Conversations"
      >
        <option value="">New chat</option>
        {conversations.map(c => (
          <option key={c.id} value={c.id}>
            {(c.title || c.preview || c.id).slice(0, 60)}
          </option>
        ))}
      </select>
      <button onClick={onNew}>New</button>
      <button onClick={onToggleFull} title={fullScreen ? 'Back to split view' : 'Expand chat to full screen'}>
        {fullScreen ? '⇥ Split' : '⛶ Full'}
      </button>
    </div>
  )
}

export function ChatPanel({ fullScreen, onToggleFull }: {
  fullScreen: boolean
  onToggleFull: () => void
}) {
  const [activeId, setActiveId] = useState<string | null>(null)
  const adapter = useMemo(() => createStudioAdapter(), [])

  return (
    <ChatProvider
      adapter={adapter}
      currentUser={{ id: 'matthew', username: 'Matthew.Shaxted', name: 'Matthew Shaxted' }}
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
      config={{ variant: 'modern', suggestedPrompts: SUGGESTED }}
    >
      <div className="chat-dock">
        <DockHeader
          activeId={activeId}
          onSelect={setActiveId}
          onNew={() => setActiveId(null)}
          fullScreen={fullScreen}
          onToggleFull={onToggleFull}
        />
        <div className="chat-dock-body">
          {activeId ? <ChatThread conversationId={activeId} /> : <ChatEmptyState />}
        </div>
      </div>
    </ChatProvider>
  )
}
