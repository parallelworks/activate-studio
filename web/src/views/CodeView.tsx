import { type ReactElement, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, Callout, EmptyState, Loader, SearchInput, StatusDot } from '@parallelworks/ui'
import { Streamdown } from 'streamdown'

/**
 * The viewer's pw code sessions on the platform (PW Code on the web): every
 * machine's sessions, as the platform's Code page lists them, and the one
 * chosen with its transcript. Read only for now; each session opens on the
 * platform, where it is answered, approved and steered.
 */

interface Machine { id: string; name: string; kind: string; hostname?: string; status: string; error?: string }
export interface CodeSession {
  machineId: string; machine: string; id: string; title: string; model?: string; workspace?: string
  createdAt?: string; updatedAt?: string; messages: number; status: string; permissionMode?: string
  branch?: string; pullRequests?: string[]; archived?: boolean; pinned?: boolean; waiting?: number
  lastError?: string; url: string
}
interface Item { kind: string; text?: string; toolName?: string; toolArgs?: string }
interface Detail { session: CodeSession; history: Item[]; earlier: number }

const STATUS_TEXT: Record<string, string> = {
  awaiting_approval: 'Needs you', running: 'Working', idle: 'Ready', live_elsewhere: 'Open elsewhere',
}

/** The list's groups, in order: what needs an answer, what is working,
 *  the rest, and archived sessions last. */
export function groupSessions(rows: CodeSession[]): { key: string; label: string; rows: CodeSession[] }[] {
  const by = (t?: string) => (t ? Date.parse(t) : 0)
  const sorted = [...rows].sort((a, b) => (Number(!!b.pinned) - Number(!!a.pinned)) || by(b.updatedAt) - by(a.updatedAt))
  const groups = [
    { key: 'needs', label: 'Needs you', rows: sorted.filter(s => !s.archived && (s.status === 'awaiting_approval' || (s.waiting ?? 0) > 0)) },
    { key: 'working', label: 'Working', rows: sorted.filter(s => !s.archived && s.status === 'running' && !(s.waiting ?? 0)) },
    { key: 'recent', label: 'Recent', rows: sorted.filter(s => !s.archived && s.status !== 'running' && s.status !== 'awaiting_approval' && !(s.waiting ?? 0)) },
    { key: 'archived', label: 'Archived', rows: sorted.filter(s => s.archived) },
  ]
  return groups.filter(g => g.rows.length)
}

export function ago(iso: string | undefined, now = Date.now()): string {
  const t = iso ? Date.parse(iso) : NaN
  if (!Number.isFinite(t)) return ''
  const s = Math.max(0, Math.round((now - t) / 1000))
  if (s < 60) return 'just now'
  if (s < 3600) return `${Math.floor(s / 60)}m`
  if (s < 86400) return `${Math.floor(s / 3600)}h`
  return `${Math.floor(s / 86400)}d`
}

/** The last two parts of a workspace path, which name it in a row. */
export function shortPath(p?: string): string {
  if (!p) return ''
  const parts = p.split('/').filter(Boolean)
  return parts.length <= 2 ? p : '…/' + parts.slice(-2).join('/')
}

function dotFor(s: CodeSession): { variant?: 'info' | 'warning' | 'error' | 'success'; off?: boolean; pulse?: boolean } {
  if (s.status === 'awaiting_approval' || (s.waiting ?? 0) > 0) return { variant: 'warning' }
  if (s.status === 'running') return { variant: 'info', pulse: true }
  if (s.lastError) return { variant: 'error' }
  return { off: true }
}

async function getJSON<T>(url: string): Promise<T> {
  const res = await fetch(url)
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((body as { error?: string; message?: string }).error ?? (body as { message?: string }).message ?? `HTTP ${res.status}`)
  return body as T
}

export function CodeView() {
  const [machines, setMachines] = useState<Machine[]>([])
  const [rows, setRows] = useState<CodeSession[] | null>(null)
  const [codeUrl, setCodeUrl] = useState('')
  const [error, setError] = useState('')
  const [filter, setFilter] = useState('')
  const [showArchived, setShowArchived] = useState(false)
  const [open, setOpen] = useState<CodeSession | null>(null)
  const [detail, setDetail] = useState<Detail | null>(null)
  const [detailError, setDetailError] = useState('')
  // A session opens at its latest messages, as a chat does; later reads
  // of the same session leave the scroll where the reader put it.
  const detailRef = useRef<HTMLDivElement>(null)
  const scrolledFor = useRef('')

  const load = useCallback(async () => {
    try {
      const d = await getJSON<{ machines: Machine[]; sessions: CodeSession[]; codeUrl: string }>('/api/code-sessions')
      setMachines(d.machines); setRows(d.sessions); setCodeUrl(d.codeUrl); setError('')
    } catch (e) {
      setError((e as Error).message); setRows(r => r ?? [])
    }
  }, [])
  // The list follows the platform's every 20 seconds while the page is open.
  useEffect(() => {
    void load()
    const t = setInterval(() => { if (document.visibilityState === 'visible') void load() }, 20_000)
    return () => clearInterval(t)
  }, [load])

  const loadDetail = useCallback(async (s: CodeSession) => {
    try {
      const q = new URLSearchParams({ machine: s.machineId, id: s.id, machineName: s.machine })
      setDetail(await getJSON<Detail>(`/api/code-sessions/item?${q}`)); setDetailError('')
    } catch (e) {
      setDetailError((e as Error).message)
    }
  }, [])
  // A session at work is read again every 5 seconds.
  useEffect(() => {
    if (!open) return
    setDetail(null); setDetailError('')
    void loadDetail(open)
    const t = setInterval(() => {
      const live = rows?.find(r => r.id === open.id && r.machineId === open.machineId)
      if (document.visibilityState === 'visible' && live && (live.status === 'running' || live.status === 'awaiting_approval')) void loadDetail(open)
    }, 5_000)
    return () => clearInterval(t)
  }, [open?.id, open?.machineId]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    const key = detail ? `${detail.session.machineId}/${detail.session.id}` : ''
    const el = detailRef.current
    if (!key || !el || scrolledFor.current === key) return
    scrolledFor.current = key
    requestAnimationFrame(() => { el.scrollTop = el.scrollHeight })
  }, [detail])

  const groups = useMemo(() => {
    const q = filter.trim().toLowerCase()
    const shown = (rows ?? []).filter(s => !q || [s.title, s.machine, s.workspace, s.model, s.branch].some(v => v?.toLowerCase().includes(q)))
    return groupSessions(shown)
  }, [rows, filter])
  const connected = machines.filter(m => m.status === 'connected')
  const archivedCount = groups.find(g => g.key === 'archived')?.rows.length ?? 0

  if (rows === null) return <div className="code-view"><div className="muted pad"><Loader inline size={16} label="Reading your pw code sessions…" /></div></div>

  return (
    <div className="code-view">
      <div className="code-list">
        <div className="code-list-head">
          <SearchInput value={filter} onValueChange={setFilter} label="Filter sessions" />
          {codeUrl && <Button variant="secondary" size="sm" href={codeUrl} target="_blank" rel="noopener noreferrer">New session in ACTIVATE</Button>}
        </div>
        {error && <Callout tone="warning" size="sm" className="code-callout">{error}</Callout>}
        {!error && (
          <p className="muted code-machines" title={machines.map(m => `${m.name}: ${m.status.replace(/_/g, ' ')}`).join('\n')}>
            {connected.length
              ? `${connected.length} machine${connected.length === 1 ? '' : 's'} connected: ${connected.map(m => m.name).join(', ')}`
              : 'No machine has a pw code daemon running.'}
          </p>
        )}
        {!error && !groups.length && (
          <EmptyState variant="bare" title="No sessions" description="Sessions you start with pw code on a machine, or from the Code page in ACTIVATE, are listed here." />
        )}
        {groups.filter(g => g.key !== 'archived' || showArchived).map(g => (
          <section key={g.key} className="code-group" aria-label={g.label}>
            <h3 className="code-group-label">{g.label}</h3>
            <ul>
              {g.rows.map(s => {
                const dot = dotFor(s)
                const on = open?.id === s.id && open.machineId === s.machineId
                return (
                  <li key={`${s.machineId}/${s.id}`}>
                    <button type="button" className={`code-row${on ? ' on' : ''}`} onClick={() => setOpen(s)} aria-current={on || undefined}>
                      <StatusDot variant={dot.variant} off={dot.off} pulse={dot.pulse} />
                      <span className="code-row-main">
                        <span className="code-row-title">{s.title}</span>
                        <span className="code-row-meta">{s.machine}{s.workspace ? ` · ${shortPath(s.workspace)}` : ''}</span>
                      </span>
                      <span className="code-row-when">{ago(s.updatedAt)}</span>
                    </button>
                  </li>
                )
              })}
            </ul>
          </section>
        ))}
        {archivedCount > 0 && (
          <Button variant="ghost" size="sm" onClick={() => setShowArchived(v => !v)} className="code-archived-toggle">
            {showArchived ? 'Hide archived' : `Show archived (${archivedCount})`}
          </Button>
        )}
      </div>
      <div className="code-detail" ref={detailRef}>
        {!open && <EmptyState variant="bare" title="Choose a session" description="Its conversation shows here, read only. Open it in ACTIVATE to answer, approve or steer it." />}
        {open && <SessionDetail s={detail?.session ?? open} detail={detail} error={detailError} />}
      </div>
    </div>
  )
}

function SessionDetail({ s, detail, error }: { s: CodeSession; detail: Detail | null; error: string }) {
  const dot = dotFor(s)
  return (
    <>
      <div className="code-detail-head">
        <div className="code-detail-title">
          <h2>{s.title}</h2>
          <div className="code-detail-meta">
            <span className="code-status"><StatusDot variant={dot.variant} off={dot.off} pulse={dot.pulse} />{STATUS_TEXT[s.status] ?? s.status}{(s.waiting ?? 0) > 0 ? `, ${s.waiting} to approve` : ''}</span>
            <span>{s.machine}</span>
            {s.workspace && <span className="code-mono" title={s.workspace}>{s.workspace}</span>}
            {s.model && <span>{s.model}</span>}
            {s.branch && <span className="code-mono">{s.branch}</span>}
            {s.pullRequests?.map(u => <a key={u} href={u} target="_blank" rel="noopener noreferrer">{u.replace(/^https?:\/\/(www\.)?github\.com\//, '')}</a>)}
          </div>
        </div>
        <Button variant="primary" size="sm" href={s.url} target="_blank" rel="noopener noreferrer">Open in ACTIVATE</Button>
      </div>
      {s.lastError && <Callout tone="error" size="sm" className="code-callout">{s.lastError}</Callout>}
      {error && <Callout tone="warning" size="sm" className="code-callout">{error}</Callout>}
      {!detail && !error && <div className="muted pad"><Loader inline size={16} label="Reading the conversation…" /></div>}
      {detail && <Transcript items={detail.history} earlier={detail.earlier} />}
    </>
  )
}

/** The conversation: the user's messages and the agent's replies in full,
 *  each tool call as a line with its result folded under it. */
export function Transcript({ items, earlier }: { items: Item[]; earlier: number }) {
  const out: ReactElement[] = []
  for (let i = 0; i < items.length; i++) {
    const it = items[i]
    if (it.kind === 'tool_call') {
      const next = items[i + 1]
      const result = next?.kind === 'tool_result' ? next : null
      if (result) i++
      out.push(
        <details key={i} className="code-tool">
          <summary><span className="code-tool-name">{it.toolName}</span> <span className="code-tool-args">{argSummary(it.toolArgs)}</span></summary>
          {result?.text && <pre>{result.text}</pre>}
        </details>,
      )
      continue
    }
    if (it.kind === 'tool_result') continue
    if (it.kind === 'user') { out.push(<div key={i} className="code-msg user"><div className="code-msg-body">{it.text}</div></div>); continue }
    if (it.kind === 'assistant' || it.kind === 'recap' || it.kind === 'plan_view' || it.kind === 'goal_view') {
      out.push(<div key={i} className="code-msg assistant md-body"><Streamdown>{it.text ?? ''}</Streamdown></div>)
      continue
    }
    if (it.text) out.push(<div key={i} className={`code-note ${it.kind}`}>{it.text}</div>)
  }
  return (
    <div className="code-transcript">
      {earlier > 0 && <p className="muted">{earlier} earlier items are not shown; open the session in ACTIVATE for all of it.</p>}
      {out.length ? out : <p className="muted">No messages yet.</p>}
    </div>
  )
}

/** A tool call's arguments as a short line: its path, pattern or command
 *  when it has one, else the JSON. */
export function argSummary(args?: string): string {
  if (!args) return ''
  try {
    const o = JSON.parse(args) as Record<string, unknown>
    for (const k of ['path', 'file_path', 'pattern', 'command', 'query', 'url']) {
      if (typeof o[k] === 'string') return (o[k] as string).slice(0, 160)
    }
  } catch { /* not JSON */ }
  return args.slice(0, 160)
}
