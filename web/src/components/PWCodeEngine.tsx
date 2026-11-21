import { type CSSProperties, useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Button, Callout, ExternalLink, Popover } from '@parallelworks/ui'
import { useChat } from '@parallelworks/ui/ai'
import { type Engine, getEngine, setEngine } from '../labelScope'

/** GET /api/chat/engine: what answers by default, and whether pw code is
 *  offered to this person (with why not). */
interface EngineInfo { setting: 'auto' | 'pwcode' | 'studio'; default: 'pwcode' | 'studio'; pwcode: boolean; reason: string | null; toolsDown?: string | null }

/** How the Studio's tools stand in the conversation's pw code session. */
interface ToolsState { connected: boolean; count: number; error?: string }

/** continued: the chat continues a session from the Code page, on the
 *  platform's machine `machine`. */
interface SessionInfo { url?: string; session?: string; model?: string; continued?: boolean; machine?: string }

/** The address of a pw code session on the Code page. */
export function codePageLink(machine: string, session: string): string {
  return `#view=code?${new URLSearchParams({ machine, session })}`
}

const ENGINES: { value: 'pwcode' | 'studio'; label: string; hint: string }[] = [
  { value: 'pwcode', label: 'pw code', hint: 'The ACTIVATE agent harness: works in files, runs commands you approve, searches these libraries, and shows in PW Code on the web.' },
  { value: 'studio', label: 'Studio assistant', hint: "Answers from the libraries with this Studio's own tools and the model chosen below." },
]

/** The pw code session a conversation has, from the conversation itself
 *  (GET /api/chat/conversations/:id) or from a reply as it streams. */
function useSession(conversationId: string | null): SessionInfo {
  const [s, setS] = useState<SessionInfo>({})
  useEffect(() => {
    setS({})
    if (!conversationId) return
    let gone = false
    fetch(`/api/chat/conversations/${encodeURIComponent(conversationId)}`)
      .then(r => (r.ok ? r.json() : null))
      .then(c => { if (!gone && c?.pwcode) setS({ url: c.pwcode.url, session: c.pwcode.session, model: c.pwcode.model, continued: !!c.pwcode.continued, machine: c.pwcode.machine }) })
      .catch(() => {})
    const on = (e: Event) => {
      const d = (e as CustomEvent).detail as SessionInfo & { conversationId?: string }
      if (d.conversationId === conversationId) setS(prev => ({ ...prev, url: d.url, session: d.session, model: d.model }))
    }
    window.addEventListener('ade-pwcode-session', on)
    return () => { gone = true; window.removeEventListener('ade-pwcode-session', on) }
  }, [conversationId])
  return s
}

/**
 * The chat bar's choice of what answers this conversation: pw code or the
 * Studio assistant. Shown where pw code is offered (or chosen for the
 * Studio and unavailable, to say why); the conversation's PW Code on the
 * web link sits in its menu once pw code has answered.
 */
export function PWCodeEngine({ conversationId }: { conversationId: string | null }) {
  const [info, setInfo] = useState<EngineInfo | null>(null)
  const [chosen, setChosen] = useState<Engine>(() => getEngine(conversationId))
  const [open, setOpen] = useState(false)
  const session = useSession(conversationId)

  const read = useCallback(() => {
    fetch('/api/chat/engine').then(r => (r.ok ? r.json() : null)).then(setInfo, () => setInfo(null))
  }, [])
  useEffect(() => {
    read()
    // A fallback changes what answers by default for a while.
    window.addEventListener('ade:model-access-changed', read)
    window.addEventListener('ade-pwcode-fallback', read)
    return () => {
      window.removeEventListener('ade:model-access-changed', read)
      window.removeEventListener('ade-pwcode-fallback', read)
    }
  }, [read])
  useEffect(() => { setChosen(getEngine(conversationId)) }, [conversationId])
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

  if (!info || (!info.pwcode && info.setting !== 'pwcode' && !session.url)) return null
  const current: 'pwcode' | 'studio' = chosen || info.default
  // pw code answers with its own model, which the composer's picker does not set.
  const pwModel = session.model ? `This chat runs on ${session.model}.` : "It uses pw code's model, set in Settings, not the one chosen below."
  const pick = (v: 'pwcode' | 'studio') => {
    // The Studio's default is kept as '', so a change of setting reaches it.
    const value: Engine = v === info.default ? '' : v
    setEngine(value, conversationId)
    setChosen(value)
    setOpen(false)
  }
  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      title="What answers this conversation"
      align="end"
      className="scope-menu persona-menu engine-menu"
      trigger={
        <button className={`scope-btn ${current === 'pwcode' ? 'active' : ''}`} title="What answers this conversation: pw code or the Studio assistant">
          <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true"><path d="M5 4.5 1.5 8 5 11.5M11 4.5 14.5 8 11 11.5M9.2 3 6.8 13"/></svg>
          <span className="persona-label">{current === 'pwcode' ? 'pw code' : 'Studio'}</span>
        </button>
      }
    >
      {ENGINES.map(e => {
        const off = e.value === 'pwcode' && !info.pwcode
        return (
          <button key={e.value} className={`persona-row ${current === e.value ? 'on' : ''}`} disabled={off} aria-pressed={current === e.value} onClick={() => pick(e.value)}>
            <span className="persona-name">{e.label}{e.value === info.default && <span className="persona-shared">default</span>}</span>
            <span className="muted">{off && info.reason ? info.reason : e.value === 'pwcode' ? `${e.hint} ${pwModel}` : e.hint}</span>
          </button>
        )
      })}
      {tools && (
        <p className={`engine-tools ${tools.connected ? '' : 'engine-tools-off'}`}>
          {tools.connected
            ? `This chat's pw code session has the Studio's tools: ${tools.count} of them, the index included.`
            : session.continued
              ? `This chat's pw code session does not have the Studio's tools${tools.error ? `: ${tools.error}` : ''}. The next message tries to reconnect them.`
              : `This chat's pw code session does not have the Studio's tools${tools.error ? `: ${tools.error}` : ''}. The next message reconnects them or starts a session that has them.`}
        </p>
      )}
      {!tools && session.continued && (
        <p className="engine-tools engine-tools-off">This session began outside the Studio, so it works with its own tools and not the Studio's library tools: pw code cannot add them to a session that already exists.</p>
      )}
      {!tools && !session.continued && info.toolsDown && <p className="engine-tools engine-tools-off">{info.toolsDown}</p>}
      {session.continued && session.machine && session.session && (
        <p className="engine-web">
          This chat continues a pw code session. <a href={codePageLink(session.machine, session.session)} onClick={() => setOpen(false)}>See it on the Code page</a>
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

interface Approval { id: string; kind?: string; tool?: string; command?: string; reason?: string; plan?: string }
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

/**
 * pw code's requests for approval in this conversation (a command, a plan),
 * answered here or in PW Code on the web. Each leaves when pw code says
 * it was answered.
 */
export function PWCodeApprovals({ conversationId }: { conversationId: string | null }) {
  const asked = usePending()
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')
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
  const answer = async (a: Asked, allowed: boolean) => {
    setBusy(a.approval.id + allowed); setError('')
    try {
      const res = await fetch(`/api/chat/pwcode/${encodeURIComponent(a.conversationId)}/approvals/${encodeURIComponent(a.approval.id)}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ allowed }),
      })
      if (!res.ok) throw new Error((await res.json().catch(() => ({}))).error ?? `${res.status}`)
      settle(a.approval.id)
    } catch (e) {
      setError(`Could not send the answer: ${String((e as Error).message ?? e)}`)
    } finally { setBusy('') }
  }
  const a = mine[0]
  const what = a.approval.kind === 'plan' ? 'pw code has a plan for you to approve'
    : `pw code asks to run ${a.approval.tool ?? 'a tool'}`
  return (
    <div ref={ref} className="cred-banner pwcode-approval" style={place}>
      <Callout variant="bar" tone="warning" live title={what}
        action={<>
          <Button variant="primary" size="sm" loading={busy === a.approval.id + 'true'} disabled={!!busy} onClick={() => void answer(a, true)}>Allow</Button>
          <Button variant="secondary" size="sm" loading={busy === a.approval.id + 'false'} disabled={!!busy} onClick={() => void answer(a, false)}>Deny</Button>
        </>}>
        {a.approval.command && <code className="pwcode-command">{a.approval.command}</code>}
        {a.approval.kind === 'plan' && a.approval.plan && <p className="pwcode-plan">{a.approval.plan.slice(0, 600)}</p>}
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
