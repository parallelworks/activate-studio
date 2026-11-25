import { refreshNotices, setReplying } from './notices'
import type { ChatAdapter, GeneratingReply, ModelsList, StreamCompletion } from '@parallelworks/ui/ai'
import { toast } from '@parallelworks/ui'
import { forgetChat, readChat, takeReadAhead } from './chatRecord'
import { setCacheViewer } from './viewerCache'
import { getChatLibrary, getEffort, getLabelScope, getLastEngine, getPersona, hasEffort, knowEngine, setEffort } from './labelScope'
import { GOES_TO, GOES_TO_SESSION } from './chatLinks'
import { fetchModels } from './api'
import { firstMessagePick } from './pwcode/newChatPick'
import { handFirstMessage } from './pwcode/firstMessage'
import { holdModels, modelHeld, PWCODE_MODEL, pwCodeStandsIn } from './pwcode/ownModel'
import { firstMessageStart } from './pwcode/newChatStart'
import { askedEngine, newConversation, pwcodeAnswers } from './chatEngine'
import { foldRecovered, recoveryTracker } from './recoveredSteps'
import { rememberToolCommands } from './components/GufiCommands'

// pw code stood in for the models last listed.
let stoodIn = false

async function listModels(): Promise<ModelsList> {
  // The shared request is typed loosely; the package's own types apply here.
  const data = await fetchModels().catch(e => {
    if (pwCodeStandsIn() || stoodIn || modelHeld()) return {}
    throw e
  }) as unknown as Partial<ModelsList>
  stoodIn = !data.models?.length && pwCodeStandsIn()
  // A new pw code chat with no model takes no message: the package keeps its
  // box closed while none is listed. pw code's own picker lists them still.
  if (holdModels((data.models ?? []) as ModelsList['models'])) return { models: [], unreachableSessions: [], providerIssues: [] }
  if (stoodIn) return { models: [PWCODE_MODEL] as ModelsList['models'], unreachableSessions: [], providerIssues: [] }
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
/** The connection dropped while the reply runs on in the Studio. */
const RUNS_ON = 'The connection to the reply was lost, but the Studio is still writing it. Open this chat again to follow it.'

/** The conversation's effort; a conversation without one yet (a new
 *  chat's first turn) takes the last one chosen and keeps it from then on. */
function effortFor(conversationId: string | undefined): string {
  const e = getEffort(conversationId)
  if (conversationId && !hasEffort(conversationId)) setEffort(e, conversationId)
  return e
}

const readReply: StreamCompletion = async (req, handlers, signal) => {
  const res = await fetch('/api/chat/stream', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      // pw code standing in for a model is none of the Studio's.
      model: req.model === PWCODE_MODEL.id ? '' : req.model,
      messages: req.messages,
      conversationId: req.conversationId,
      userMessageId: req.userMessageId,
      parentMessageId: req.parentMessageId ?? null,
      allocation: req.allocation ?? null,
      labelScope: getLabelScope(),
      library: getChatLibrary(),
      agent: getPersona(),
      effort: effortFor(req.conversationId) || null,
      // What answers is the conversation's, which the server keeps; the
      // last choice made here is for a chat nothing has answered yet.
      newChatEngine: getLastEngine() || null,
      // A chat started from the Library is pw code's, or says why not.
      ...askedEngine(req.conversationId),
      // The model a new pw code chat was set to before its first message;
      // the server uses it only when this message makes the session.
      ...firstMessagePick(req.conversationId),
      // Where it was set to start, the same way.
      ...firstMessageStart(req.conversationId),
      // The session's view runs the command a chat starts with.
      opensSession: true,
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
  // Set once pw code answers: Stop then interrupts its turn as well.
  let pwcodeSession = ''
  const interruptPWCode = () => {
    if (pwcodeSession && req.conversationId) {
      void fetch(`/api/chat/pwcode/${encodeURIComponent(req.conversationId)}/interrupt`, { method: 'POST' }).catch(() => {})
    }
  }
  signal?.addEventListener('abort', interruptPWCode, { once: true })
  // A failed step a later call recovers is settled when that call ends.
  const recovery = recoveryTracker()

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
          recovery.start(payload.id ?? payload.name, payload.name, payload.args ?? '')
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
          for (const r of recovery.end(payload.id ?? payload.name, payload.summary ?? '', !!payload.error)) {
            handlers.onPart?.({ type: 'tool_end', id: r.id, result: r.result, isError: false })
          }
        }
        break
      case 'approval':
        // A call the server holds until the user answers it; the package
        // draws the request and sends the answer back as req.resume.
        if (payload.approval?.id) handlers.onPart?.({ type: 'approval_request', approval: payload.approval })
        break
      case 'pwcode':
        // pw code answers: its session, and where PW Code on the web shows it.
        pwcodeSession = String(payload.session ?? '')
        if (req.conversationId) pwcodeAnswers(req.conversationId)
        window.dispatchEvent(new CustomEvent('ade-pwcode-session', { detail: { conversationId: req.conversationId, ...payload } }))
        break
      case 'pwcode_first':
        // The session is there, and the message is its view's to run.
        if (req.conversationId) handFirstMessage(req.conversationId, { text: String(payload.text ?? ''), refused: String(payload.refused ?? '') })
        break
      case 'pwcode_approval':
      case 'pwcode_approval_resolved':
        // pw code asks before a command; the chat's approval bar answers.
        window.dispatchEvent(new CustomEvent(event === 'pwcode_approval' ? 'ade-pwcode-approval' : 'ade-pwcode-approval-resolved', { detail: payload }))
        break
      case 'pwcode_fallback':
        // The Studio answered, and the chat goes on with it.
        if (req.conversationId) knowEngine(req.conversationId, 'studio')
        // pw code could not reach the Studio's tools, so the Studio
        // answers this message; the chat says why.
        window.dispatchEvent(new CustomEvent('ade-pwcode-fallback', { detail: payload }))
        break
      case 'engine_fallback':
        // pw code is not for this person here: the Studio answers,
        // and the chat is its own from this reply on. The server stores a
        // note after the reply, which the chat reads once the reply ends.
        if (req.conversationId) knowEngine(req.conversationId, 'studio')
        window.dispatchEvent(new CustomEvent('ade:engine-fallback', { detail: { conversationId: req.conversationId, note: payload.note } }))
        break
      case 'steer':
        // The running reply took a message queued during it (SteerBridge).
        window.dispatchEvent(new CustomEvent('ade-steer-applied', { detail: String(payload.id ?? '') }))
        break
      case 'done':
        finishReason = payload.finishReason ?? 'stop'
        // A reply pw code did not give is the Studio's.
        if (req.conversationId && !pwcodeSession && !errorMessage) knowEngine(req.conversationId, 'studio')
        if (req.conversationId) window.dispatchEvent(new CustomEvent('ade:chat-answered', { detail: { conversationId: req.conversationId } }))
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
      if (req.conversationId && await replyRunning(req.conversationId).catch(() => null)) throw new Error(RUNS_ON)
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
  // The package labels a reply with the picker's model when that model
  // takes the Responses API, and with the model the stream reports
  // otherwise. A reply pw code wrote is labelled with pw code's model, as
  // it is stored ("pw code · claude-code/haiku"), so the request is told
  // it did not use the Responses API, which it never does here.
  if (pwcodeSession && model) req.useResponses = false

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

/** A reply, with its chat marked as being answered until it ends. */
const streamFromServer: StreamCompletion = async (req, handlers, signal) => {
  const id = req.conversationId
  if (id) setReplying(id, true)
  try {
    return await readReply(req, handlers, signal)
  } finally {
    if (id) setReplying(id, false)
    refreshNotices()
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
export function setViewerUsername(u: string): void {
  viewerUsername = u
  setCacheViewer(u)
}

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

interface NoteMessage { id: string; content?: string | null; finishReason?: string; stopped?: boolean; parts?: { kind?: string; id?: string; name?: string; args?: string; status?: string; result?: string }[] }

/** The note that opens a chat continued from a pw code session says where
 *  it came from; it is not a reply, so it has nothing to regenerate. The
 *  chat shows Regenerate on every assistant message, so its link to the
 *  Code page is marked here (the Code page reads only machine and session) and
 *  styles.css hides Regenerate in the message holding that link. Notes
 *  saved before this are marked too. */
export const CONTINUED_NOTE_MARK = 'note=continued'
// It also settles the failed steps a later call recovered (recoveredSteps.ts),
// and marks a reply ended by Stop as stopped.
export function markNotes<T extends { id?: string; messages?: NoteMessage[] }>(c: T): T {
  for (const m of c.messages ?? []) {
    if (Array.isArray(m.parts)) m.parts = foldRecovered(m.parts)
    // A reply ended by Stop is stored so; the chat marks it Stopped.
    if (m.finishReason === 'stopped') m.stopped = true
    if (m.id.startsWith('note-continued-') && m.content && !m.content.includes(CONTINUED_NOTE_MARK)) {
      m.content = m.content.replace('](#view=code?', `](#view=code?${CONTINUED_NOTE_MARK}&`)
    }
    // Where its messages go follows what answers the chat (chatLinks.tsx).
    if (m.id.startsWith('note-continued-') && c.id && m.content?.includes(` ${GOES_TO_SESSION}`) && !m.content.includes(GOES_TO)) {
      m.content = m.content.replace(` ${GOES_TO_SESSION}`, ` [${GOES_TO_SESSION}](${GOES_TO}${c.id})`)
    }
  }
  return c
}

/** The Studio's reply running in a conversation, as far as it
 *  has got: a reply runs on when its page goes (a closed tab, a reload),
 *  and the chat follows it from here until it ends. null when none runs. */
export async function replyRunning(id: string): Promise<GeneratingReply | null> {
  const res = await fetch(`/api/chat/conversations/${encodeURIComponent(id)}/generating`)
  if (res.status === 404) return null
  return json<GeneratingReply | null>(res)
}

/** Stops the reply running in a conversation: the only way it ends early,
 *  as closing the page leaves it running. */
async function stopReply(id: string): Promise<void> {
  await fetch(`/api/chat/conversations/${encodeURIComponent(id)}/stop`, { method: 'POST' }).then(r => json(r))
}

export function createStudioAdapter(): ChatAdapter {
  return {
    // Conversations live server-side beside the index; browser storage is
    // partitioned inside the platform iframe and loses history.
    conversations: {
      list: () => fetch('/api/chat/conversations').then(r => json<OwnedSummary[]>(r)).then(decorateConversations) as ReturnType<ChatAdapter['conversations']['list']>,
      // One read with the chat page's other parts (chatRecord.ts), or the
      // one started when the pointer came to the chat in the list; the
      // thread gets its own copy, since it changes what it is given.
      // A reply still running in it comes with it, so the chat shows it
      // being written and follows it to its end.
      get: id => Promise.all([
        (takeReadAhead(id) ?? readChat(id)).then(c => markNotes(structuredClone(c) as { messages?: NoteMessage[] })),
        replyRunning(id).catch(() => null),
      ]).then(([c, generating]) => (generating ? { ...c, generating } : c)) as ReturnType<ChatAdapter['conversations']['get']>,
      generating: replyRunning,
      stop: stopReply,
      create: title => fetch('/api/chat/conversations', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title }),
      }).then(r => json<Awaited<ReturnType<ChatAdapter['conversations']['create']>>>(r)).then(c => {
        if (c?.id) newConversation(c.id)
        return c
      }),
      rename: async (id, title) => { forgetChat(id); await fetch(`/api/chat/conversations/${id}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ title }),
      }).then(r => json<{ sessionKeeps?: string }>(r)).then(a => { if (a.sessionKeeps) toast.warning(a.sessionKeeps) }) },
      remove: async id => { forgetChat(id); await fetch(`/api/chat/conversations/${id}`, { method: 'DELETE' }).then(r => r.status === 404 ? undefined : json(r)) },
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
        if (!res.ok) {
          // The Studio's reason when it gives one: on a shared Studio,
          // attachments go into the main library, read only for most people.
          const text = await res.text()
          let why = ''
          try { why = (JSON.parse(text) as { error?: unknown }).error as string } catch { /* not JSON */ }
          throw new Error(typeof why === 'string' && why ? why : `upload: ${res.status} ${text}`)
        }
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
