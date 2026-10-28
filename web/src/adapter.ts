import type { ChatAdapter, ModelsList, StreamCompletion } from '@parallelworks/ui/ai'
import { getChatLibrary, getLabelScope, getPersona } from './labelScope'
import { fetchModels } from './api'
import { rememberToolCommands } from './components/GufiCommands'

async function listModels(): Promise<ModelsList> {
  // The shared request is typed loosely; the package's own types apply here.
  const data = (await fetchModels()) as unknown as Partial<ModelsList>
  // The server marks models whose most recent call failed (an expired
  // provider key fails at call time while listing fine). The picker
  // component is upstream, so the signal rides the display name; it
  // disappears on the first successful call.
  const models = (data.models ?? []).map(m =>
    (m as { callable?: boolean }).callable === false ? { ...m, name: `${m.name || m.id} · last call failed` } : m)
  return { models, unreachableSessions: data.unreachableSessions ?? [], providerIssues: data.providerIssues ?? [] }
}

/** Stream one completion from the server's tool-loop endpoint over SSE. */
const CUT_OFF = 'The reply was cut off before it arrived, for example by a restart of the Studio. Retry sends the message again.'

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
      library: getChatLibrary(),
      agent: getPersona(),
      tools: req.tools,
      // The user's answer to a call held for approval; the server runs what
      // it held for that call, never what the request says to run.
      ...(req.resume ? { resume: req.resume } : {}),
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
      case 'tool':
        // Tool activity streams as structured part deltas; the package
        // renders them as tool blocks (running → resolved) in the thread.
        if (payload.phase === 'call') {
          handlers.onPart?.({
            type: 'tool_start',
            id: payload.id ?? payload.name,
            name: payload.name,
            args: payload.args ?? '',
          })
        } else if (payload.phase === 'wait') {
          // Held for the user's approval; an approval event follows.
          handlers.onPart?.({ type: 'tool_wait', id: payload.id })
        } else {
          // The GUFI commands the call ran, which the package's part does
          // not carry; the tool's block reads them by the call's id.
          if (payload.id && Array.isArray(payload.gufi)) rememberToolCommands(payload.id, payload.gufi)
          handlers.onPart?.({
            type: 'tool_end',
            id: payload.id ?? payload.name,
            result: payload.summary ?? '',
            isError: !!payload.error,
          })
        }
        break
      case 'approval':
        // A call the server holds until the user answers it; the package
        // draws the request and sends the answer back as req.resume.
        if (payload.approval?.id) handlers.onPart?.({ type: 'approval_request', approval: payload.approval })
        break
      case 'steer':
        // The running reply took a message queued during it (SteerBridge).
        window.dispatchEvent(new CustomEvent('ade-steer-applied', { detail: String(payload.id ?? '') }))
        break
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
    let chunk: ReadableStreamReadResult<Uint8Array>
    try { chunk = await reader.read() } catch (e) {
      // Stop aborts the read; anything else is the connection dropping.
      if (signal?.aborted) throw e
      if (content) break
      throw new Error(CUT_OFF)
    }
    const { done, value } = chunk
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
  // The server ends every turn with done or error. A stream that closed with
  // neither and said nothing was cut off (the Studio restarted, the network
  // dropped); an error here gives the message the chat's Retry button.
  if (!finishReason && !content && !signal?.aborted) throw new Error(CUT_OFF)

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

/** Whether the viewer may delete a conversation: their own, or one with
 *  no owner. The server applies the same rule. */
export function isOwnConversation(c: { owner?: string | null }): boolean {
  return !c.owner || !viewerUsername || c.owner === viewerUsername
}

/** Deletes many conversations in one request; ids the viewer may not
 *  delete come back as skipped. */
export async function deleteConversations(ids: string[]): Promise<{ deleted: string[]; skipped: string[] }> {
  return fetch('/api/chat/conversations/delete', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ids }),
  }).then(r => json<{ deleted: string[]; skipped: string[] }>(r))
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
    // The composer's Tools menu: one switch per kind of tool the server
    // offers. The package remembers the reader's choice and sends the ids
    // left on as req.tools.
    tools: {
      list: async () => {
        const res = await fetch('/api/chat/tool-options')
        if (!res.ok) throw new Error(`tool options: ${res.status}`)
        return ((await res.json()) as { tools?: { id: string; label: string; description?: string }[] }).tools ?? []
      },
    },
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
      // ai-chat 0.5 pages by an opaque cursor and asks whether there is
      // more; the server pages by offset, so the cursor is the next offset.
      list: async ({ limit, cursor }: { limit: number; cursor?: string }) => {
        const offset = Number(cursor) || 0
        const res = await fetch(`/api/chat/attachments?limit=${limit}&offset=${offset}`)
        if (!res.ok) throw new Error(`attachments: ${res.status}`)
        const page = await res.json() as { attachments: unknown[]; total: number }
        const next = offset + (page.attachments?.length ?? 0)
        const hasMore = next < (page.total ?? 0)
        return { ...page, hasMore, ...(hasMore ? { nextCursor: String(next) } : {}) }
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
