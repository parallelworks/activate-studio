import { useMemo, useState } from 'react'
import {
  ChatProvider, ChatLayout, ChatThread, ChatEmptyState,
} from '@parallelworks/ai-chat'
import '@parallelworks/ai-chat/styles.css'
import { createStudioAdapter } from '../adapter'

const SUGGESTED = [
  'What is in the revenue pipeline right now?',
  'Summarize our standard past-performance references.',
  'Which proposals are in process and what are their next deadlines?',
  'Show the DAG of the complexdag workflow.',
]

export function ChatPanel() {
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
      <ChatLayout>
        {activeId ? <ChatThread conversationId={activeId} /> : <ChatEmptyState />}
      </ChatLayout>
    </ChatProvider>
  )
}
