import { lazy, Suspense, useEffect, useMemo, useRef, useState } from 'react'
import { useChat } from '@parallelworks/ui/ai'
import { initializeValues } from '@parallelworks/ui/form'
import { useWorkflowFields } from './WorkflowFields'
import { insidePlatform } from './SessionView'
import { openBeside } from '../sideBySide'

/**
 * The run the assistant launched, beside the conversation: the workflow's
 * form as it was submitted, the platform's live graph of the run, and the
 * web session the run opened, served through the Studio so it can sit in
 * the pane. It opens when a reply launches a run and follows that run to
 * the end; it moves from the form to the graph to the session as each
 * becomes worth watching, unless the viewer picks a tab.
 */

const DynamicForm = lazy(() => import('@parallelworks/ui/form').then(m => ({ default: m.DynamicForm })))
const DependencyGraph = lazy(() => import('@parallelworks/ui/graph').then(m => ({ default: m.DependencyGraph })))

/** Tools whose summaries name a run, and how each names it: a launch says
 *  "run submitted: <id>", the watchers start with "<id>: <status>". The id
 *  is taken as written, since its shape varies: a run of a workflow file
 *  from GitHub has no number on the end. */
const RUN_TOOLS = new Set(['run_workflow', 'watch_run', 'workflow_run_detail'])
const RUN_ID = /^[A-Za-z0-9][A-Za-z0-9_.-]{0,120}$/
function runIdOf(tool: string, firstLine: string): string | null {
  const id = tool === 'run_workflow'
    ? /^run submitted: (\S+)/.exec(firstLine)?.[1]
    : /^(\S+): /.exec(firstLine)?.[1]
  return id && RUN_ID.test(id) ? id : null
}

interface PartLike { kind?: string; name?: string; result?: string | null }
interface MessageLike { parts?: PartLike[] | null }

/** The most recent run the conversation's tool calls launched or followed. */
export function latestRunSlug(messages: MessageLike[], streaming: PartLike[] = []): string | null {
  let found: string | null = null
  const scan = (parts: PartLike[] | null | undefined) => {
    for (const p of parts ?? []) {
      if (p.kind !== 'tool_call' || !RUN_TOOLS.has(String(p.name))) continue
      const id = runIdOf(String(p.name), String(p.result ?? '').split('\n')[0])
      if (id) found = id
    }
  }
  for (const m of messages) scan(m.parts)
  scan(streaming)
  return found
}

interface PaneRun {
  slug: string; status?: string; statusReason?: string; createdAt?: string; completedAt?: string
  workflowName?: string; inputs?: Record<string, unknown>; executedJobs?: Record<string, unknown>; terminal?: boolean
}
interface PaneSession { name: string; status: string; url: string | null; ownHost: boolean }
interface FormDoc { displayName?: string; form?: Record<string, unknown> | null }
type Tab = 'form' | 'graph' | 'session'

const json = async <T,>(r: Response): Promise<T> => {
  if (!r.ok) throw new Error((await r.json().catch(() => ({}))).error ?? `${r.status}`)
  return r.json() as Promise<T>
}

function since(iso?: string): string {
  if (!iso) return ''
  const s = Math.max(0, Math.round((Date.now() - Date.parse(iso)) / 1000))
  return s < 90 ? `${s}s ago` : s < 5400 ? `${Math.round(s / 60)}m ago` : new Date(iso).toLocaleTimeString()
}

export function RunPane({ slug, onClose }: { slug: string; onClose: () => void }) {
  const [run, setRun] = useState<PaneRun | null>(null)
  const [error, setError] = useState('')
  const [session, setSession] = useState<PaneSession | null>(null)
  const [doc, setDoc] = useState<FormDoc | null>(null)
  const [tab, setTab] = useState<Tab>('form')
  const picked = useRef(false)
  const fields = useWorkflowFields()
  const pick = (t: Tab) => { picked.current = true; setTab(t) }

  // The run, every few seconds until it ends.
  useEffect(() => {
    let live = true, timer: ReturnType<typeof setTimeout> | undefined
    picked.current = false; setRun(null); setSession(null); setDoc(null); setError(''); setTab('form')
    const tick = () => fetch(`/api/runs/item?slug=${encodeURIComponent(slug)}`).then(r => json<PaneRun>(r))
      .then(r => { if (!live) return; setRun(r); if (!r.terminal) timer = setTimeout(tick, 4000) })
      .catch(e => { if (live) { setError(String((e as Error).message)); timer = setTimeout(tick, 10000) } })
    tick()
    return () => { live = false; clearTimeout(timer) }
  }, [slug])

  // The session the run opened, once it is up.
  useEffect(() => {
    let live = true, timer: ReturnType<typeof setTimeout> | undefined
    const tick = () => fetch(`/api/sessions?run=${encodeURIComponent(slug)}`).then(r => json<{ sessions: PaneSession[] }>(r))
      .then(r => {
        if (!live) return
        const s = r.sessions.find(x => /running|active|ready/i.test(x.status)) ?? null
        setSession(s)
        if (!s) timer = setTimeout(tick, 8000)
      })
      .catch(() => { if (live) timer = setTimeout(tick, 15000) })
    tick()
    return () => { live = false; clearTimeout(timer) }
  }, [slug])

  // The workflow's form, for the inputs the run was given.
  useEffect(() => {
    if (!run?.workflowName || doc) return
    fetch(`/api/workflows/item/form?w=${encodeURIComponent(run.workflowName)}`).then(r => json<FormDoc>(r)).then(setDoc).catch(() => setDoc({ form: null }))
  }, [run?.workflowName, doc])

  // Form first, then the graph a few seconds on, then the session when it
  // is up: the order a launch happens in. A tab the viewer picks stays.
  useEffect(() => {
    if (picked.current) return
    if (session?.ownHost) { setTab('session'); return }
    const t = setTimeout(() => { if (!picked.current) setTab('graph') }, 4000)
    return () => clearTimeout(t)
  }, [session, slug])

  const initial = useMemo(() => {
    if (!doc?.form || !run?.inputs) return run?.inputs ?? {}
    try { return initializeValues(doc.form as any, run.inputs) ?? run.inputs } catch { return run.inputs }
  }, [doc, run?.inputs])

  const host = session?.ownHost && session.url ? new URL(session.url).host : null
  const status = (run?.status ?? 'starting').toLowerCase()
  return (
    <aside className="run-pane card" aria-label={`Run ${slug}`}>
      <header className="run-pane-head">
        <div className="run-pane-title">
          <span className={`run-dot run-${status}`} />
          <strong>{doc?.displayName ?? run?.workflowName ?? 'Run'}</strong>
          <span className="muted">{slug} · {status}{run?.createdAt ? ` · submitted ${since(run.createdAt)}` : ''}</span>
        </div>
        <button type="button" className="btn-ghost run-pane-close" title="Close the run pane" onClick={onClose}>×</button>
      </header>
      <nav className="run-pane-tabs" role="tablist">
        <button role="tab" aria-selected={tab === 'form'} className={tab === 'form' ? 'on' : ''} onClick={() => pick('form')}>Form</button>
        <button role="tab" aria-selected={tab === 'graph'} className={tab === 'graph' ? 'on' : ''} onClick={() => pick('graph')}>Graph</button>
        <button role="tab" aria-selected={tab === 'session'} className={tab === 'session' ? 'on' : ''} onClick={() => pick('session')} disabled={!session}>
          Session{session ? '' : ' (waiting)'}
        </button>
      </nav>
      <div className="run-pane-body">
        {error && !run && <p className="banner-error">{error}</p>}
        {tab === 'form' && (
          <div className="run-pane-form">
            <p className={`run-executed${run ? ' on' : ''}`}>{run ? `Executed ${run.createdAt ? new Date(run.createdAt).toLocaleTimeString() : ''} from this conversation` : 'Submitting…'}</p>
            {doc?.form && Object.keys(doc.form).length
              ? <Suspense fallback={<p className="muted">Loading form…</p>}>
                  <fieldset disabled className="run-pane-fieldset">
                    <DynamicForm key={`${slug}:${doc.displayName ?? ''}`} formJSONs={doc.form as any} initialValues={initial as any}
                      fields={fields} workflowForm labelPosition="left" contextKey={slug} allowLayoutCSS />
                  </fieldset>
                </Suspense>
              : run?.inputs
                ? <pre className="run-pane-inputs">{JSON.stringify(run.inputs, null, 2)}</pre>
                : <p className="muted">Loading the run…</p>}
          </div>
        )}
        {tab === 'graph' && (
          run?.executedJobs && Object.keys(run.executedJobs).length
            ? <Suspense fallback={<p className="muted">Loading graph…</p>}><div className="run-pane-graph"><DependencyGraph run={run as any} removeBorder /></div></Suspense>
            : <p className="muted pad">The run's jobs appear here as the platform starts them.</p>
        )}
        {tab === 'session' && (
          host
            ? <iframe className="run-pane-frame" title={session!.name} src={`/api/session-proxy/${host}/`} />
            : session
              ? <p className="muted pad">This session is served under the platform's own address. <a href={session.url ?? '#'} target="_blank" rel="noreferrer">Open it in a new tab</a>.</p>
              : <p className="muted pad">The run's session appears here once it is online.</p>
        )}
      </div>
      {session?.url && tab === 'session' && (
        <footer className="run-pane-foot">
          <span className="muted">{session.name}</span>
          <button type="button" className="link-btn" onClick={() => popOut({ kind: 'session', id: `session:${session.name}`, name: session.name, url: session.url!, host, platformUrl: null })}>Open beside the Studio</button>
        </footer>
      )}
    </aside>
  )
}

/** A tab in the side view: a run the conversation launched, or a session
 *  sent there from a session card in the chat. */
export type SideItem =
  | { kind: 'run'; id: string; slug: string }
  | { kind: 'session'; id: string; name: string; url: string; host: string | null; platformUrl?: string | null }

const label = (i: SideItem) => (i.kind === 'run' ? `Run ${i.slug}` : i.name)
const upsert = (xs: SideItem[], item: SideItem) => (xs.some(x => x.id === item.id) ? xs : [...xs, item])

/** A session in the side view, through the Studio's relay. */
function SessionPane({ item }: { item: Extract<SideItem, { kind: 'session' }> }) {
  return (
    <aside className="run-pane card" aria-label={item.name}>
      <header className="run-pane-head">
        <div className="run-pane-title"><span className="run-dot run-running" /><strong>{item.name}</strong><span className="muted">session</span></div>
      </header>
      <div className="run-pane-body">
        {item.host
          ? <iframe className="run-pane-frame" title={item.name} src={`/api/session-proxy/${item.host}/`} />
          : <p className="muted pad">This session is served under the platform's own address, so it cannot show here. Pop it out to open it beside the Studio.</p>}
      </div>
    </aside>
  )
}

/** Opens a side view tab in a window beside the Studio's. A session opens
 *  at its own address, where it signs in the way the platform's pages do;
 *  inside the platform's frame it opens its page on the platform instead. */
export function popOut(item: SideItem): void {
  if (item.kind === 'run') { openBeside(`/?embed=run&slug=${encodeURIComponent(item.slug)}`, item.id); return }
  if (insidePlatform() && item.platformUrl) { window.open(item.platformUrl, '_blank', 'noopener'); return }
  if (!openBeside(item.url, item.id)) window.open(item.url, '_blank', 'noopener')
}

/**
 * The side view beside the chat: tabs for the runs the conversation
 * launched and the sessions sent to it, each able to pop out into a window
 * beside the Studio. A run that appears while the viewer watches opens it;
 * a conversation opened with a run already in it starts with it hidden,
 * with a button at the edge to show it.
 */
export function SideViewHost() {
  const { currentConversation, streamingParts } = useChat()
  const slug = useMemo(
    () => latestRunSlug((currentConversation?.messages ?? []) as MessageLike[], (streamingParts ?? []) as PartLike[]),
    [currentConversation, streamingParts],
  )
  const [items, setItems] = useState<SideItem[]>([])
  const [active, setActive] = useState<string | null>(null)
  const [hidden, setHidden] = useState(true)
  const conv = currentConversation?.id ?? null
  const seen = useRef<{ conv: string | null; slug: string | null } | null>(null)
  useEffect(() => {
    const prev = seen.current
    const run: SideItem | null = slug ? { kind: 'run', id: `run:${slug}`, slug } : null
    if (!prev || prev.conv !== conv) {
      seen.current = { conv, slug }
      setItems(run ? [run] : []); setActive(run?.id ?? null); setHidden(true)
      return
    }
    if (run && slug !== prev.slug) { seen.current = { conv, slug }; setItems(xs => upsert(xs, run)); setActive(run.id); setHidden(false) }
  }, [conv, slug])
  // A session card in a reply sends its session here (it is a same-origin
  // frame inside the chat).
  useEffect(() => {
    const on = (e: MessageEvent) => {
      if (e.origin !== window.location.origin || e.data?.type !== 'ade-side-view') return
      const s = e.data.session as { name?: string; url?: string | null; ownHost?: boolean; platformUrl?: string | null } | undefined
      if (!s?.name || !s.url) return
      let host: string | null = null
      try { host = s.ownHost ? new URL(s.url).host : null } catch { /* no address */ }
      const item: SideItem = { kind: 'session', id: `session:${s.name}`, name: s.name, url: s.url, host, platformUrl: s.platformUrl ?? null }
      setItems(xs => upsert(xs, item)); setActive(item.id); setHidden(false)
    }
    window.addEventListener('message', on)
    return () => window.removeEventListener('message', on)
  }, [])
  if (!items.length) return null
  if (hidden) {
    return (
      <button type="button" className="run-pane-reopen" title="Show the side view beside the chat" onClick={() => setHidden(false)}>
        {items.length === 1 ? label(items[0]) : `Side view (${items.length})`}
      </button>
    )
  }
  const cur = items.find(i => i.id === active) ?? items[items.length - 1]
  const close = (id: string) => {
    const rest = items.filter(i => i.id !== id)
    setItems(rest)
    if (cur.id === id) setActive(rest[rest.length - 1]?.id ?? null)
    if (!rest.length) setHidden(true)
  }
  return (
    <div className="side-view">
      <div className="side-tabs" role="tablist">
        {items.map(i => (
          <div key={i.id} className={`side-tab${i.id === cur.id ? ' on' : ''}`}>
            <button type="button" role="tab" aria-selected={i.id === cur.id} className="side-tab-label" title={label(i)} onClick={() => setActive(i.id)}>{label(i)}</button>
            <button type="button" className="side-tab-x" title="Close this tab" onClick={() => close(i.id)}>×</button>
          </div>
        ))}
        <span className="side-tabs-actions">
          <button type="button" className="btn-ghost" title="Open in a window beside this one" onClick={() => popOut(cur)}>Pop out</button>
          <button type="button" className="btn-ghost" title="Hide the side view" onClick={() => setHidden(true)}>Hide</button>
        </span>
      </div>
      {cur.kind === 'run'
        ? <RunPane key={cur.id} slug={cur.slug} onClose={() => close(cur.id)} />
        : <SessionPane key={cur.id} item={cur} />}
    </div>
  )
}
