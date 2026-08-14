import { useMemo, useState } from 'react'
import {
  ChatProvider, ChatLayout, ChatThread, ChatEmptyState,
} from '@parallelworks/ai-chat'
import '@parallelworks/ai-chat/styles.css'
import { createStudioAdapter } from '../adapter'
import { useAppConfig } from '../config'

export function ChatView() {
  const [activeId, setActiveId] = useState<string | null>(null)
  const adapter = useMemo(() => createStudioAdapter(), [])
  const cfg = useAppConfig()

  // The empty state freezes its greeting and starter prompts at mount, so
  // wait for the deployment config before mounting the chat tree.
  if (!cfg.loaded) return <div className="chat-canvas" />

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
      <div className="chat-canvas">
        <ChatLayout>
          {activeId ? <ChatThread conversationId={activeId} /> : <ChatEmptyState />}
        </ChatLayout>
      </div>
    </ChatProvider>
  )
}
