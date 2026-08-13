import type { ChatAdapter, ModelsList, StreamCompletion } from '@parallelworks/ai-chat'
import { createMemoryConversationStore } from '@parallelworks/ai-chat/adapters/mock'

async function listModels(): Promise<ModelsList> {
  const res = await fetch('/api/chat/models')
  if (!res.ok) throw new Error(`models: ${res.status}`)
  const data = await res.json()
  return { models: data.models ?? [], unreachableSessions: data.unreachableSessions ?? [] }
}

/** Stream one completion from the server's tool-loop endpoint over SSE. */
const streamFromServer: StreamCompletion = async (req, handlers, signal) => {
  const res = await fetch('/api/chat/stream', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      model: req.model,
      messages: req.messages,
      conversationId: req.conversationId,
      userMessageId: req.userMessageId,
      allocation: req.allocation ?? null,
    }),
    signal,
  })
  if (!res.ok || !res.body) throw new Error(`chat stream: ${res.status} ${await res.text()}`)

  let content = ''
  let reasoning = ''
  let finishReason: string | null = null
  let model: string | null = null
  let messageId = `local-${Date.now()}`
  let errorMessage: string | null = null

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  const handleBlock = (block: string) => {
    let event = 'message'
    let data = ''
    for (const line of block.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim()
      else if (line.startsWith('data:')) data += line.slice(5).trim()
    }
    if (!data) return
    let payload: any
    try { payload = JSON.parse(data) } catch { return }
    switch (event) {
      case 'content':
        content += payload.text
        handlers.onContent(payload.text)
        break
      case 'reasoning':
        reasoning += payload.text
        handlers.onReasoning?.(payload.text)
        break
      case 'tool': {
        const line = payload.phase === 'call'
          ? `\ncalling ${payload.name}(${payload.args ?? ''})\n`
          : `\n${payload.name}: ${payload.summary}\n`
        reasoning += line
        handlers.onReasoning?.(line)
        break
      }
      case 'done':
        finishReason = payload.finishReason ?? 'stop'
        model = payload.model ?? null
        if (payload.messageId) { messageId = payload.messageId; handlers.onMessageId?.(payload.messageId) }
        break
      case 'error':
        errorMessage = payload.message ?? 'stream error'
        break
    }
  }
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    let sep
    while ((sep = buf.indexOf('\n\n')) >= 0) {
      handleBlock(buf.slice(0, sep))
      buf = buf.slice(sep + 2)
    }
  }
  if (buf.trim()) handleBlock(buf)
  if (errorMessage && !content) throw new Error(errorMessage)

  return {
    content,
    messageId,
    toolCalls: [],
    finishReason,
    model,
    reasoning,
    responsesOutput: [],
  }
}

export function createStudioAdapter(): ChatAdapter {
  const store = createMemoryConversationStore({ storageKey: 'ade-studio-chat' })
  return {
    conversations: store.conversations,
    models: { list: listModels },
    streamCompletion: store.recordingStream(streamFromServer),
  }
}
