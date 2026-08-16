import type { ChatAdapter, ModelsList, StreamCompletion } from '@parallelworks/ai-chat'
import { getLabelScope } from './labelScope'

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
      parentMessageId: req.parentMessageId ?? null,
      allocation: req.allocation ?? null,
      labelScope: getLabelScope(),
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
        // The server sends a humanized one-liner; fall back to a plain form.
        const line = payload.pretty ?? (payload.phase === 'call'
          ? `\n→ ${payload.name}\n`
          : `\n↳ ${payload.summary}\n`)
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
        if (payload.kind === 'credential') {
          window.dispatchEvent(new CustomEvent('ade-credential-error', { detail: errorMessage }))
        }
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

async function json<T>(res: Response): Promise<T> {
  if (!res.ok) throw new Error(`${res.status} ${await res.text()}`)
  return res.json()
}

/** Chat-rail ownership filter. In a shared deployment every user sees the
 *  same server-side history; the rail can show all of it or only the
 *  viewer's own (legacy chats with no owner always show). Module state so
 *  the adapter's list() can consult it without prop plumbing. */
// Defaults to 'mine': the rail is personal working space, and unowned
// (legacy, standalone) chats always show, so the default only hides other
// users' conversations; the pill widens the view.
let chatFilter: 'all' | 'mine' = (localStorage.getItem('ade-chat-filter') as 'all' | 'mine') || 'mine'
let viewerUsername = ''
export function setChatListFilter(f: 'all' | 'mine'): void {
  chatFilter = f
  localStorage.setItem('ade-chat-filter', f)
}
export function getChatListFilter(): 'all' | 'mine' { return chatFilter }
export function setViewerUsername(u: string): void { viewerUsername = u }

interface OwnedSummary { id: string; title?: string; owner?: string | null }
function decorateConversations<T extends OwnedSummary>(rows: T[]): T[] {
  return rows
    .filter(c => chatFilter === 'all' || !c.owner || !viewerUsername || c.owner === viewerUsername)
    .map(c => c.owner && viewerUsername && c.owner !== viewerUsername
      ? { ...c, title: `${c.title ?? 'Untitled'} (${c.owner})` }
      : c)
}

export function createStudioAdapter(): ChatAdapter {
  return {
    // Conversations live server-side beside the index; browser storage is
    // partitioned inside the platform iframe and loses history.
    conversations: {
      list: () => fetch('/api/chat/conversations').then(r => json<OwnedSummary[]>(r)).then(decorateConversations) as ReturnType<ChatAdapter['conversations']['list']>,
      get: id => fetch(`/api/chat/conversations/${id}`).then(r => json(r)),
      create: title => fetch('/api/chat/conversations', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title }),
      }).then(r => json(r)),
      rename: async (id, title) => { await fetch(`/api/chat/conversations/${id}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title }),
      }).then(r => json(r)) },
      remove: async id => { await fetch(`/api/chat/conversations/${id}`, { method: 'DELETE' }).then(r => json(r)) },
    },
    models: { list: listModels },
    streamCompletion: streamFromServer,
    // onOpen is part of the package's adapter surface as of 0.1.0; it was
    // AttachmentsAdapter type, hence the cast. Attachments live in the KB
    // under uploads/chat/; the id is the base64url rel path, so a tile
    // click opens the file in the Library via the #open deep link.
    attachments: {
      onOpen: (att: { id: string }) => {
        try {
          const rel = atob(att.id.replace(/-/g, '+').replace(/_/g, '/'))
          location.hash = `#open=file:${encodeURIComponent(rel).replace(/%2F/gi, '/')}`
        } catch { /* malformed id: ignore */ }
      },
      list: async ({ limit, offset }: { limit: number; offset: number }) => {
        const res = await fetch(`/api/chat/attachments?limit=${limit}&offset=${offset}`)
        if (!res.ok) throw new Error(`attachments: ${res.status}`)
        return res.json()
      },
      upload: async (file, _conversationId) => {
        const fd = new FormData()
        fd.append('file', file, file.name)
        const res = await fetch('/api/chat/attachments', { method: 'POST', body: fd })
        if (!res.ok) throw new Error(`upload: ${res.status} ${await res.text()}`)
        return res.json()
      },
      remove: async id => {
        const res = await fetch(`/api/chat/attachments/${id}`, { method: 'DELETE' })
        if (!res.ok) throw new Error(`remove: ${res.status}`)
      },
      downloadUrl: (id: string) => `/api/chat/attachments/${id}/download`,
    } as ChatAdapter['attachments'],
  }
}
