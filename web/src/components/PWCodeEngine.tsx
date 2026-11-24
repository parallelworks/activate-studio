import { type CSSProperties, useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { SIGN_IN_TO_USE, accountsHref, needsActivate } from '../accounts'
import { Button, Callout, ExternalLink, Popover } from '@parallelworks/ui'
import { useChat } from '@parallelworks/ui/ai'
import { getLastEngine, switchEngine } from '../labelScope'
import { sessionToolsSaid } from './studioTools'
import { liveModel, onModel } from '../pwcode/liveModel'
import { cachedChat, readChat, type ChatRecord } from '../chatRecord'

/** GET /api/chat/engine: what answers by default, and whether pw code is
 *  offered to this person (with why not). */
interface EngineInfo {
  setting: 'auto' | 'pwcode' | 'studio'
  /** What a new chat starts with. */
  default: 'pwcode' | 'studio'
  /** What this conversation answers with (its own, or a new chat's). */
  current?: 'pwcode' | 'studio'
  /** Something has answered the conversation. */
  answered?: boolean
  pwcode: boolean
  reason: string | null
  toolsDown?: string | null
}

/** How the Studio's tools stand in the conversation's pw code session. */
interface ToolsState { connected: boolean; count: number; error?: string; relay?: boolean; machineHas?: boolean }

/** continued: the chat continues a session from the Code page, on the
 *  platform's machine `machine`. */
interface SessionInfo { url?: string; session?: string; model?: string; modelNote?: string; modelFrom?: string; continued?: boolean; machine?: string; machineName?: string; relay?: boolean }

/** A model id's last part, as the chat bar names it: "haiku" for
 *  claude-code/haiku, "gpt-6.2-sol" for owner:codex/gpt-6.2-sol. */
export function shortModel(id?: string): string {
  return id ? id.slice(id.lastIndexOf('/') + 1) : ''
}

/** The address of a pw code session on the Code page. */
export function codePageLink(machine: string, session: string): string {
  return `#view=code?${new URLSearchParams({ machine, session })}`
}

const ENGINES: { value: 'pwcode' | 'studio'; label: string; hint: string }[] = [
  { value: 'pwcode', label: 'pw code', hint: 'The ACTIVATE agent harness: works in files, runs commands you approve, searches these libraries, and shows in PW Code on the web.' },
  { value: 'studio', label: 'Studio assistant', hint: "Answers from the libraries with this Studio's own tools and the model chosen below." },
]

function sessionInfo(c: ChatRecord | undefined): SessionInfo {
  const p = c?.pwcode as Record<string, unknown> | undefined
  if (!p) return {}
  const str = (k: string) => (typeof p[k] === 'string' ? p[k] as string : undefined)
  return { url: str('url'), session: str('session'), model: str('model'), modelNote: str('modelNote'), modelFrom: str('modelFrom'), continued: !!p.continued, machine: str('machine'), machineName: str('machineName'), relay: !!p.relay }
}

/** The pw code session a conversation has, from the conversation itself
 *  (GET /api/chat/conversations/:id) or from a reply as it streams. */
function useSession(conversationId: string | null): SessionInfo {
  const [s, setS] = useState<SessionInfo>({})
  // The model the session runs on now (pwcode/liveModel.ts), over the one
  // the chat was linked with.
  const [live, setLive] = useState<string | undefined>()
  useEffect(() => {
    setLive(conversationId ? liveModel(conversationId) : undefined)
    return conversationId ? onModel(conversationId, setLive) : undefined
  }, [conversationId])
  useEffect(() => {
    if (!conversationId) { setS({}); return }
    let gone = false
    // The kept record first (chatRecord.ts), then the read the chat's other
    // parts share.
    setS(sessionInfo(cachedChat(conversationId)))
    readChat(conversationId)
      .then(c => { if (!gone && c.pwcode) setS(sessionInfo(c)) })
      .catch(() => {})
    const on = (e: Event) => {
      const d = (e as CustomEvent).detail as SessionInfo & { conversationId?: string }
      // The note is the session's as the server has it now: a new session
      // has none, so an old session's note does not stay.
      if (d.conversationId === conversationId) setS(prev => ({ ...prev, url: d.url, session: d.session, model: d.model, modelNote: d.modelNote, modelFrom: d.modelFrom }))
    }
    window.addEventListener('ade-pwcode-session', on)
    return () => { gone = true; window.removeEventListener('ade-pwcode-session', on) }
  }, [conversationId])
  return live ? { ...s, model: live } : s
}

/**
 * The chat bar's choice of what answers this conversation: pw code or the
 * Studio assistant. Shown where pw code is offered (or chosen for the
 * Studio and unavailable, to say why); the conversation's PW Code on the
 * web link sits in its menu once pw code has answered.
 */
export function PWCodeEngine({ conversationId }: { conversationId: string | null }) {
  const [info, setInfo] = useState<EngineInfo | null>(null)
  const [open, setOpen] = useState(false)
  const session = useSession(conversationId)
  const { isStreaming } = useChat()

  // Read for this conversation, by the rule its next message is sent by.
  const read = useCallback(() => {
    const q = new URLSearchParams()
    if (conversationId) q.set('conversation', conversationId)
    const last = getLastEngine()
    if (last) q.set('last', last)
    fetch(`/api/chat/engine?${q}`).then(r => (r.ok ? r.json() : null)).then(setInfo, () => setInfo(null))
  }, [conversationId])
  useEffect(() => {
    read()
    // A sign-in (in this tab or another), a fallback, or a switch changes
    // what answers.
    const onEngine = (e: Event) => {
      const d = (e as CustomEvent).detail as { conversationId?: string | null }
      if ((d?.conversationId ?? null) === conversationId) read()
    }
    window.addEventListener('ade:model-access-changed', read)
    window.addEventListener('ade-pwcode-fallback', read)
    window.addEventListener('ade:chat-engine', onEngine)
    return () => {
      window.removeEventListener('ade:model-access-changed', read)
      window.removeEventListener('ade-pwcode-fallback', read)
      window.removeEventListener('ade:chat-engine', onEngine)
    }
  }, [read, conversationId])
  // Read again each time the menu opens and when a reply ends: cheap, and
  // never stale.
  useEffect(() => { if (open) read() }, [open, read])
  useEffect(() => { if (!isStreaming) read() }, [isStreaming, read])
  const [tools, setTools] = useState<ToolsState | null>(null)
  useEffect(() => {
    setTools(null)
    if (!open || !conversationId || !session.session) return
    let gone = false
    fetch(`/api/chat/pwcode/${encodeURIComponent(conversationId)}`)
      .then(r => (r.ok ? r.json() : null))
      .then(st => { if (!gone) setTools(st?.tools ?? null) })
      .catch(() => {})
    return () => { gone = true }
  }, [open, conversationId, session.session])

  // pw code waiting only for an ACTIVATE sign-in shows too, saying so.
  const shown = !!info && (info.pwcode || info.setting === 'pwcode' || !!session.url || needsActivate(info.reason) || info.current === 'pwcode')
  const current: 'pwcode' | 'studio' = info?.current ?? info?.default ?? 'studio'
  return shown && info ? <EngineMenu {...{ info, current, session, tools, open, setOpen, conversationId, running: isStreaming, reread: read }} /> : null
}

/** The note on which model pw code runs a chat on, its setting a link and
 *  the models' full ids on hover. */
function ModelNote({ session, close }: { session: SessionInfo; close: () => void }) {
  const note = session.modelNote ?? ''
  const at = note.indexOf('Settings, External access')
  const ids = [session.modelFrom, session.model].filter(Boolean).join(' → ')
  const text = at < 0 ? <>{note}</> : <>{note.slice(0, at)}<a href="#view=settings:rag" onClick={close}>Settings, External access</a>{note.slice(at + 'Settings, External access'.length)}</>
  return <span title={ids || undefined}>{text}</span>
}

/** The chat bar's engine button and its menu. */
function EngineMenu({ info, current, session, tools, open, setOpen, conversationId, running, reread }: {
  info: EngineInfo; current: 'pwcode' | 'studio'; session: SessionInfo; tools: ToolsState | null; open: boolean
  setOpen: (v: boolean) => void; conversationId: string | null; running: boolean; reread: () => void
}) {
  // The canvas says what answers, so the composer's model picker, which
  // sets the Studio assistant's model only, is hidden while pw code does.
  useEffect(() => {
    const canvas = document.querySelector('.chat-canvas')
    canvas?.setAttribute('data-engine', current)
    return () => canvas?.removeAttribute('data-engine')
  }, [current])
  const [error, setError] = useState('')
  useEffect(() => { if (!open) setError('') }, [open])
  const close = () => setOpen(false)
  // pw code answers with its own model, which the composer's picker does
  // not set; the picker is hidden while pw code answers (styles.css).
  const pwModel = session.modelNote
    ? <ModelNote session={session} close={close} />
    : session.model ? <span title={session.model}>This chat runs on {shortModel(session.model)}.</span> : <>It runs on pw code&apos;s own model, or the one Settings names.</>
  // A relayed chat moves to a new session on its machine, which has the
  // tools pw code there has now; its next message carries the chat over.
  const [starting, setStarting] = useState(false)
  const startNew = async () => {
    if (!conversationId) return
    setStarting(true); setError('')
    try {
      const res = await fetch(`/api/chat/pwcode/${encodeURIComponent(conversationId)}/new-session`, { method: 'POST' })
      const d = (await res.json().catch(() => ({}))) as { session?: string; url?: string; model?: string; error?: string }
      if (!res.ok) throw new Error(d.error ?? `${res.status}`)
      window.dispatchEvent(new CustomEvent('ade-pwcode-session', { detail: { conversationId, session: d.session, url: d.url, model: d.model } }))
      reread(); close()
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setStarting(false) }
  }
  const pick = (v: 'pwcode' | 'studio') => {
    if (v === current || running) { close(); return }
    switchEngine(v, conversationId).then(() => { reread(); close() }, e => setError(String((e as Error).message ?? e)))
  }
  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      title="What answers this conversation"
      align="end"
      className="scope-menu persona-menu engine-menu"
      trigger={
        <button className={`scope-btn engine-btn ${current === 'pwcode' ? 'active' : ''}`} title="What answers this conversation: pw code or the Studio assistant">
          <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true"><path d="M5 4.5 1.5 8 5 11.5M11 4.5 14.5 8 11 11.5M9.2 3 6.8 13"/></svg>
          <span className="persona-label">{current === 'pwcode' ? (session.model ? `pw code · ${shortModel(session.model)}` : 'pw code') : 'Studio'}</span>
        </button>
      }
    >
      {ENGINES.map(e => {
        const off = e.value === 'pwcode' && !info.pwcode
        const busy = running && e.value !== current
        return (
          <button key={e.value} className={`persona-row ${current === e.value ? 'on' : ''}`} disabled={off || busy} aria-pressed={current === e.value} onClick={() => pick(e.value)}>
            <span className="persona-name">{e.label}{e.value === info.default && <span className="persona-shared" title="What a new chat starts with">default</span>}</span>
            <span className="muted">{off && info.reason ? (needsActivate(info.reason) ? `${SIGN_IN_TO_USE}.` : info.reason) : e.value === 'pwcode' ? <>{e.hint} {pwModel}</> : e.hint}</span>
          </button>
        )
      })}
      {running && <p className="engine-tools engine-tools-off">A reply is being written; switch what answers when it ends.</p>}
      {error && <p className="engine-tools engine-tools-off" role="alert">{error}</p>}
      {!info.pwcode && needsActivate(info.reason) && (
        <p className="engine-tools engine-tools-off">
          {info.reason} <a href={accountsHref('activate')} onClick={close}>Open Accounts</a>
        </p>
      )}
      {(tools || session.continued) && current === 'pwcode' && (() => {
        const said = sessionToolsSaid({ tools, continued: !!session.continued, relay: !!session.relay || !!tools?.relay, machine: session.machineName || session.machine })
        return (
          <p className={`engine-tools ${said.ok ? '' : 'engine-tools-off'}`}>
            {said.text}
            {said.offerNew && conversationId && <>{' '}<button type="button" className="code-approval-more" disabled={running || starting} onClick={() => void startNew()}>{starting ? 'Starting…' : 'Start a new session there with them'}</button></>}
          </p>
        )
      })()}
      {!tools && !session.continued && info.toolsDown && <p className="engine-tools engine-tools-off">{info.toolsDown}</p>}
      {session.continued && session.machine && session.session && (
        <p className="engine-web">
          This chat continues a pw code session. <a href={codePageLink(session.machine, session.session)} onClick={close}>See it on the Code page</a>
        </p>
      )}
      {session.url && (
        <p className="engine-web">
          <ExternalLink href={session.url}>Open this chat in PW Code on the web</ExternalLink>
        </p>
      )}
    </Popover>
  )
}

/**
 * In a chat both engines have answered, each Studio assistant reply says
 * so beside its model (pw code's already read "pw code · model"), and the
 * replies' details stay in view, so a chat that switched reads clearly.
 */
export function EngineMarks() {
  const { currentConversation } = useChat()
  const messages = (currentConversation?.messages ?? []) as { id: string; role: string; model?: string }[]
  const replies = messages.filter(m => m.role === 'assistant' && !m.id.startsWith('note-'))
  const pw = (m: { id: string; model?: string }) => m.id.startsWith('pwc-') || m.id.startsWith('pwh-') || !!m.model?.startsWith('pw code')
  const mixed = replies.some(pw) && replies.some(m => !pw(m))
  useEffect(() => {
    const canvas = document.querySelector('.chat-canvas')
    if (!canvas) return
    if (!mixed) { canvas.removeAttribute('data-engines-mixed'); return }
    canvas.setAttribute('data-engines-mixed', '')
    const mark = () => {
      for (const meta of canvas.querySelectorAll('.chat-meta')) {
        const model = meta.children[1] as HTMLElement | undefined
        if (!model || model.dataset.engineLabel !== undefined || /^\d/.test(model.textContent ?? '')) continue
        model.dataset.engineLabel = (model.textContent ?? '').startsWith('pw code') ? '' : 'Studio'
      }
    }
    mark()
    const mo = new MutationObserver(mark)
    mo.observe(canvas, { childList: true, subtree: true })
    return () => { mo.disconnect(); canvas.removeAttribute('data-engines-mixed') }
  }, [mixed, currentConversation?.id])
  return null
}

/** A question pw code asks (an ask): its options are picked by label. */
interface Question { question: string; header?: string; multiSelect?: boolean; options?: { label: string; description?: string }[] }
interface Approval { id: string; kind?: string; tool?: string; command?: string; reason?: string; plan?: string; questions?: Question[] }
/** One question's answer as the person gives it: the options picked, and
 *  what they typed. */
interface Answer { picked: string[]; text: string }
interface Asked { conversationId: string; url?: string; approval: Approval }

/* pw code's open requests for approval, by conversation. They live outside
   React, so a remount of the chat (a model's status changing, say) keeps
   them; a reply's events and the watcher's look at the session fill them. */
let pending: Asked[] = []
const listeners = new Set<() => void>()
const changed = () => listeners.forEach(f => f())
function ask(a: Asked) { pending = [...pending.filter(x => x.approval.id !== a.approval.id), a]; changed() }
function settle(id: string) { if (pending.some(x => x.approval.id === id)) { pending = pending.filter(x => x.approval.id !== id); changed() } }
/** The session's open requests as pw code reports them, for one conversation. */
function replacePending(conversationId: string, url: string | undefined, approvals: Approval[]) {
  pending = [...pending.filter(x => x.conversationId !== conversationId), ...approvals.map(approval => ({ conversationId, url, approval }))]
  changed()
}
window.addEventListener('ade-pwcode-approval', e => {
  const d = (e as CustomEvent).detail as Asked
  if (d?.approval?.id) ask(d)
})
window.addEventListener('ade-pwcode-approval-resolved', e => settle(String((e as CustomEvent).detail?.approvalId ?? '')))
function usePending(): Asked[] {
  const [, bump] = useState(0)
  useEffect(() => {
    const f = () => bump(n => n + 1)
    listeners.add(f)
    return () => { listeners.delete(f) }
  }, [])
  return pending
}

/** How often to look at a turn that runs on without its reply's stream. */
const WATCH_MS = 3_000

/**
 * A pw code turn runs on in its session when the page loses the reply's
 * stream (a reload, a remount, a dropped connection). While the open
 * conversation has such a turn, this looks at the session every few
 * seconds, shows what it waits to be approved, and reads the conversation
 * again when the turn has ended, so the reply shows without a reload.
 */
export function PWCodeWatcher() {
  const { currentConversation, isStreaming, loadConversation } = useChat()
  const id = currentConversation?.id ?? null
  useEffect(() => {
    if (!id || isStreaming) return
    let stop = false
    let timer: ReturnType<typeof setTimeout> | undefined
    let wasRunning = false
    const look = async () => {
      if (stop) return
      let running = false
      try {
        const res = await fetch(`/api/chat/pwcode/${encodeURIComponent(id)}`)
        if (stop || !res.ok) return
        const st = await res.json() as { status?: string; answering?: boolean; url?: string; approvals?: Approval[] }
        if (stop) return
        running = !!st.answering || st.status === 'running' || st.status === 'awaiting_approval'
        replacePending(id, st.url, st.approvals ?? [])
      } catch { return }
      if (wasRunning && !running) { void loadConversation(id); return }
      wasRunning = running
      if (running) timer = setTimeout(() => void look(), WATCH_MS)
    }
    void look()
    return () => { stop = true; if (timer) clearTimeout(timer) }
  }, [id, isStreaming, loadConversation])
  return null
}

// PLAN_FOLD is how much of a plan shows before Show all.
const PLAN_FOLD = 600

/**
 * pw code's requests for approval in this conversation (a command, a plan),
 * answered here or in PW Code on the web. Each leaves when pw code says
 * it was answered.
 */
export function PWCodeApprovals({ conversationId }: { conversationId: string | null }) {
  const asked = usePending()
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
  // An ask's answers so far, by approval and question.
  const [answers, setAnswers] = useState<Record<string, Answer[]>>({})
  // What to change in a plan, sent with Reject; and the long plans shown
  // whole, which Approve waits for.
  const [feedback, setFeedback] = useState<Record<string, string>>({})
  const [whole, setWhole] = useState<Record<string, boolean>>({})
  const ref = useRef<HTMLDivElement>(null)
  const [place, setPlace] = useState<CSSProperties>({})
  const mine = asked.filter(a => a.conversationId === conversationId)
  // Over the composer, as wide as it, wherever the layout puts it.
  useLayoutEffect(() => {
    if (!mine.length) return
    const measure = () => {
      const canvas = ref.current?.offsetParent as HTMLElement | null
      const input = canvas?.querySelector('textarea')
      const box = (input?.closest('form') ?? input) as HTMLElement | null
      if (!canvas || !box) return
      const c = canvas.getBoundingClientRect(), i = box.getBoundingClientRect()
      setPlace({ left: i.left - c.left + i.width / 2, width: i.width, bottom: c.bottom - i.top + 8 })
    }
    measure()
    window.addEventListener('resize', measure)
    return () => window.removeEventListener('resize', measure)
  }, [mine.length])
  if (!mine.length) return null
  const answer = async (a: Asked, allowed: boolean, extra?: Record<string, unknown>) => {
    setBusy(a.approval.id + (extra?.chat ? 'chat' : allowed)); setError('')
    const kind = a.approval.kind === 'ask' || a.approval.kind === 'plan' ? { kind: a.approval.kind } : {}
    try {
      const res = await fetch(`/api/chat/pwcode/${encodeURIComponent(a.conversationId)}/approvals/${encodeURIComponent(a.approval.id)}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ allowed, ...kind, ...extra }),
      })
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error ?? `${res.status}`)
      settle(a.approval.id)
    } catch (e) {
      setError(`Could not send the answer: ${String((e as Error).message ?? e)}`)
    } finally { setBusy('') }
  }
  const a = mine[0]
  const questions = a.approval.kind === 'ask' ? a.approval.questions ?? [] : []
  const given = answers[a.approval.id] ?? questions.map(() => ({ picked: [], text: '' }))
  const setGiven = (i: number, next: Answer) => setAnswers(m => ({ ...m, [a.approval.id]: given.map((g, j) => (j === i ? next : g)) }))
  const complete = questions.length > 0 && given.every(g => g.picked.length > 0 || g.text.trim() !== '')
  const plan = a.approval.kind === 'plan' ? a.approval.plan ?? '' : ''
  const folded = plan.length > PLAN_FOLD && !whole[a.approval.id]
  const change = (feedback[a.approval.id] ?? '').trim()
  const what = a.approval.kind === 'plan' ? 'pw code has a plan for you to approve'
    : a.approval.kind === 'ask' ? (questions.length === 1 ? 'pw code asks you a question' : 'pw code asks you some questions')
    : `pw code asks to run ${a.approval.tool ?? 'a tool'}`
  const action = a.approval.kind === 'ask' ? <>
    <Button variant="primary" size="sm" loading={busy === a.approval.id + 'true'} disabled={!!busy || !complete}
      onClick={() => void answer(a, true, { answers: questions.map((q, i) => ({ question: q.question, picked: given[i].picked, text: given[i].text.trim() })) })}>Send</Button>
    <Button variant="secondary" size="sm" loading={busy === a.approval.id + 'chat'} disabled={!!busy} onClick={() => void answer(a, false, { chat: true })}>Talk it through</Button>
  </> : a.approval.kind === 'plan' ? <>
    <Button variant="primary" size="sm" loading={busy === a.approval.id + 'true'} disabled={!!busy || folded}
      title={folded ? 'Show the whole plan to approve it' : undefined} onClick={() => void answer(a, true)}>Approve</Button>
    <Button variant="secondary" size="sm" loading={busy === a.approval.id + 'false'} disabled={!!busy}
      onClick={() => void answer(a, false, change ? { message: change } : undefined)}>Reject</Button>
  </> : <>
    <Button variant="primary" size="sm" loading={busy === a.approval.id + 'true'} disabled={!!busy} onClick={() => void answer(a, true)}>Allow</Button>
    <Button variant="secondary" size="sm" loading={busy === a.approval.id + 'false'} disabled={!!busy} onClick={() => void answer(a, false)}>Deny</Button>
  </>
  return (
    <div ref={ref} className="cred-banner pwcode-approval" style={place}>
      <Callout variant="bar" tone="warning" live title={what} action={action}>
        {questions.map((q, i) => (
          <fieldset key={i} className="pwcode-question">
            <legend>{q.header ? <strong>{q.header}: </strong> : null}{q.question}</legend>
            {(q.options ?? []).map(o => {
              const on = given[i].picked.includes(o.label)
              return (
                <label key={o.label} className="pwcode-option">
                  <input type={q.multiSelect ? 'checkbox' : 'radio'} name={`${a.approval.id}-${i}`} checked={on}
                    onChange={() => setGiven(i, { ...given[i], picked: q.multiSelect ? (on ? given[i].picked.filter(x => x !== o.label) : [...given[i].picked, o.label]) : [o.label] })} />
                  <span>{o.label}{o.description ? <span className="muted"> · {o.description}</span> : null}</span>
                </label>
              )
            })}
            <input className="pwcode-answer" type="text" aria-label={`Your own answer to: ${q.question}`} placeholder={q.options?.length ? 'Or your own answer' : 'Your answer'}
              value={given[i].text} onChange={e => setGiven(i, { ...given[i], text: e.target.value })} />
          </fieldset>
        ))}
        {a.approval.command && <code className="pwcode-command">{a.approval.command}</code>}
        {plan && <>
          <p className={folded ? 'pwcode-plan' : 'pwcode-plan pwcode-plan-whole'}>{folded ? plan.slice(0, PLAN_FOLD) + '…' : plan}</p>
          {folded && <>
            <button type="button" className="code-approval-more" onClick={() => setWhole(w => ({ ...w, [a.approval.id]: true }))}>Show all ({plan.length} characters)</button>
            <p className="muted code-approval-why">Show the whole plan to approve it.</p>
          </>}
        </>}
        {a.approval.kind === 'plan' && (
          <input className="pwcode-answer pwcode-plan-change" type="text" aria-label="What to change in the plan" placeholder="What to change, sent with Reject"
            value={feedback[a.approval.id] ?? ''} onChange={e => setFeedback(f => ({ ...f, [a.approval.id]: e.target.value }))} />
        )}
        {a.url && <p className="muted key-note"><ExternalLink href={a.url}>See it in PW Code on the web</ExternalLink></p>}
        {error && <p role="alert">{error}</p>}
      </Callout>
    </div>
  )
}

/**
 * Says why the Studio assistant answered a message pw code was to answer:
 * pw code could not reach the Studio's tools. Shown in that conversation
 * until it is dismissed or pw code answers there again.
 */
export function PWCodeNotice({ conversationId }: { conversationId: string | null }) {
  const [notes, setNotes] = useState<Record<string, string>>({})
  useEffect(() => {
    const on = (e: Event) => {
      const d = (e as CustomEvent).detail as { conversationId?: string; reason?: string }
      if (d?.conversationId && d.reason) setNotes(n => ({ ...n, [d.conversationId as string]: d.reason as string }))
    }
    const answered = (e: Event) => {
      const id = (e as CustomEvent).detail?.conversationId as string | undefined
      if (id) setNotes(n => { const { [id]: _, ...rest } = n; return rest })
    }
    window.addEventListener('ade-pwcode-fallback', on)
    window.addEventListener('ade-pwcode-session', answered)
    return () => {
      window.removeEventListener('ade-pwcode-fallback', on)
      window.removeEventListener('ade-pwcode-session', answered)
    }
  }, [])
  const note = conversationId ? notes[conversationId] : undefined
  if (!note || !conversationId) return null
  const close = () => setNotes(n => { const { [conversationId]: _, ...rest } = n; return rest })
  return (
    <div className="cred-banner pwcode-notice">
      <Callout variant="bar" tone="info" title="The Studio assistant answered this message"
        action={<Button variant="ghost" size="sm" onClick={close}>Dismiss</Button>}>
        <p>{note}</p>
      </Callout>
    </div>
  )
}

/**
 * Says which model pw code runs a chat on when the Studio moved its
 * session there: pw code's own model is one the person's AI connection
 * does not offer. Shown in that conversation until it is dismissed.
 */
export function PWCodeModelNotice({ conversationId }: { conversationId: string | null }) {
  const [notes, setNotes] = useState<Record<string, { text: string; stuck: boolean }>>({})
  useEffect(() => {
    const on = (e: Event) => {
      const d = (e as CustomEvent).detail as { conversationId?: string; modelMoved?: boolean; modelStuck?: boolean; modelNote?: string }
      if (d?.conversationId && (d.modelMoved || d.modelStuck) && d.modelNote) setNotes(n => ({ ...n, [d.conversationId as string]: { text: d.modelNote as string, stuck: !!d.modelStuck } }))
    }
    window.addEventListener('ade-pwcode-session', on)
    return () => window.removeEventListener('ade-pwcode-session', on)
  }, [])
  const note = conversationId ? notes[conversationId] : undefined
  if (!note || !conversationId) return null
  const close = () => setNotes(n => { const { [conversationId]: _, ...rest } = n; return rest })
  return (
    <div className="cred-banner pwcode-notice">
      <Callout variant="bar" tone={note.stuck ? 'warning' : 'info'} title={note.stuck ? 'pw code could not move this chat to another model' : 'pw code runs this chat on another model'}
        action={<Button variant="ghost" size="sm" onClick={close}>Dismiss</Button>}>
        <p>{note.text}</p>
      </Callout>
    </div>
  )
}
