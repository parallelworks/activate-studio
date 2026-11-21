import { type ReactElement, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, Callout, ConfirmModal, EmptyState, Loader, SearchInput, StatusDot } from '@parallelworks/ui'
import { Streamdown } from 'streamdown'
import { keepEngineFor } from '../labelScope'

/**
 * The viewer's pw code sessions on the platform (PW Code on the web): every
 * machine's sessions, as the platform's Code page lists them, and the one
 * chosen with its transcript. The page reads; a session on the machine
 * whose pw code this Studio's chat reaches for the viewer can be continued
 * in a Studio chat, and every session opens on the platform, where it is
 * answered, approved and steered.
 */

interface Machine { id: string; name: string; kind: string; hostname?: string; status: string; error?: string; daemonId?: string }

/** GET /api/chat/pwcode/here: the daemon this viewer's pw code chat
 *  reaches (or why there is none), and their chats that run in a session. */
export interface Here {
  available: boolean; reason?: string; daemonId?: string; hostname?: string
  chats: { session: string; conversationId: string; title?: string; continued?: boolean }[]
}

/** Whether the Studio can continue s in a chat for this viewer, and why
 *  not: it must be on the machine whose daemon the chat reaches. */
export function continuable(s: CodeSession, machines: Machine[], here: Here | null): { ok: boolean; why: string } {
  if (!here) return { ok: false, why: 'Checking where this Studio runs pw code…' }
  if (!here.available) return { ok: false, why: here.reason || 'pw code chat is not offered to you on this Studio.' }
  const m = machines.find(x => x.id === s.machineId)
  // By the daemon id the platform keeps for the machine, else its host name.
  const same = !!m && (m.daemonId || !here.daemonId
    ? !!m.daemonId && m.daemonId === here.daemonId
    : !!m.hostname && m.hostname.toLowerCase() === (here.hostname ?? '').toLowerCase())
  if (!same) return { ok: false, why: `This session is on ${s.machine}, and this Studio's pw code runs on ${here.hostname || 'another machine'}, so it is read only here.` }
  if (s.archived) return { ok: false, why: 'This session is archived. Restore it in ACTIVATE to continue it here.' }
  return { ok: true, why: '' }
}
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

/** The session the address names, as a chat's link back gives it:
 *  #view=code?machine=<id>&session=<id>. */
export function sessionInHash(hash: string): { machine: string; session: string } | null {
  const m = hash.match(/^#view=code\?(.*)$/)
  if (!m) return null
  const q = new URLSearchParams(m[1])
  const machine = q.get('machine') ?? '', session = q.get('session') ?? ''
  return machine && session ? { machine, session } : null
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
  const [here, setHere] = useState<Here | null>(null)

  const load = useCallback(async () => {
    try {
      const d = await getJSON<{ machines: Machine[]; sessions: CodeSession[]; codeUrl: string }>('/api/code-sessions')
      setMachines(d.machines); setRows(d.sessions); setCodeUrl(d.codeUrl); setError('')
    } catch (e) {
      setError((e as Error).message); setRows(r => r ?? [])
    }
  }, [])
  const loadHere = useCallback(async () => {
    try { setHere(await getJSON<Here>('/api/chat/pwcode/here')) } catch (e) { setHere({ available: false, reason: (e as Error).message, chats: [] }) }
  }, [])
  // The list follows the platform's every 20 seconds while the page is open.
  useEffect(() => {
    void load(); void loadHere()
    const t = setInterval(() => { if (document.visibilityState === 'visible') { void load(); void loadHere() } }, 20_000)
    return () => clearInterval(t)
  }, [load, loadHere])
  // A session named in the address opens once the list has it.
  const [asked, setAsked] = useState(() => sessionInHash(location.hash))
  useEffect(() => {
    const on = () => { const a = sessionInHash(location.hash); if (a) setAsked(a) }
    window.addEventListener('hashchange', on)
    return () => window.removeEventListener('hashchange', on)
  }, [])
  useEffect(() => {
    if (!asked || !rows) return
    const s = rows.find(r => r.machineId === asked.machine && r.id === asked.session)
    if (s) { setOpen(s); if (s.archived) setShowArchived(true) }
    setAsked(null)
  }, [asked, rows])
  const chatFor = useMemo(() => new Map((here?.chats ?? []).map(c => [c.session, c.conversationId])), [here])

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
                        <span className="code-row-meta">{s.machine}{s.workspace ? ` · ${shortPath(s.workspace)}` : ''}{chatFor.has(s.id) ? ' · in a Studio chat' : ''}</span>
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
        {!open && <EmptyState variant="bare" title="Choose a session" description="Its conversation shows here. Continue it in a Studio chat, or open it in ACTIVATE to answer, approve or steer it." />}
        {open && <SessionDetail s={detail?.session ?? open} detail={detail} error={detailError}
          can={continuable(detail?.session ?? open, machines, here)} chat={chatFor.get(open.id)} onContinued={() => void loadHere()} />}
      </div>
    </div>
  )
}

/** Opens a Studio chat by its id. */
function openChat(id: string) { location.hash = `#chat=${id}` }

async function postJSON<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const out = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((out as { error?: string }).error ?? `HTTP ${res.status}`)
  return out as T
}

interface ContinueAnswer { confirm?: string; conversationId?: string; existing?: boolean }

/**
 * Continue in Studio: asks the Studio, which checks the session is the
 * viewer's and on the machine its pw code reaches, and says what to ask;
 * once the person agrees, the Studio makes a chat linked to the session,
 * which opens. A session that already has a chat opens that chat.
 */
function ContinueButton({ s, can, chat, onContinued }: { s: CodeSession; can: { ok: boolean; why: string }; chat?: string; onContinued: () => void }) {
  const [busy, setBusy] = useState(false)
  const [ask, setAsk] = useState('')
  const [error, setError] = useState('')
  // What the Studio said when the person confirmed and it could not go on:
  // shown in the dialog, which stays open so they can read it.
  const [refused, setRefused] = useState('')
  if (chat) return <Button variant="primary" size="sm" onClick={() => openChat(chat)}>Open the Studio chat</Button>
  const body = { machine: s.machineId, session: s.id }
  const start = async () => {
    setBusy(true); setError('')
    try {
      const a = await postJSON<ContinueAnswer>('/api/chat/pwcode/continue', body)
      if (a.conversationId) { onContinued(); openChat(a.conversationId); return }
      setAsk(a.confirm ?? 'Your messages from this chat go to the same session.')
    } catch (e) { setError((e as Error).message) } finally { setBusy(false) }
  }
  const confirm = async () => {
    setRefused('')
    let id = ''
    try {
      const a = await postJSON<ContinueAnswer>('/api/chat/pwcode/continue', { ...body, confirmed: true })
      id = a.conversationId ?? ''
      if (!id) throw new Error('The Studio made no chat for this session.')
    } catch (e) {
      setRefused((e as Error).message || 'The Studio could not continue this session.')
      return
    }
    setAsk('')
    keepEngineFor(id, 'pwcode')
    onContinued()
    window.dispatchEvent(new CustomEvent('ade:chats-changed'))
    openChat(id)
  }
  return (
    <>
      <Button variant="primary" size="sm" disabled={!can.ok} loading={busy} onClick={() => void start()} title={can.why || undefined}>Continue in Studio</Button>
      {error && <span className="code-continue-error" role="alert">{error}</span>}
      <ConfirmModal open={!!ask} onClose={() => { setAsk(''); setRefused('') }} title="Continue this session in a Studio chat?" description={ask}
        confirmLabel="Continue in Studio" onConfirm={confirm} closeOnConfirm={false} error={refused || undefined} />
    </>
  )
}

function SessionDetail({ s, detail, error, can, chat, onContinued }: {
  s: CodeSession; detail: Detail | null; error: string; can: { ok: boolean; why: string }; chat?: string; onContinued: () => void
}) {
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
        <div className="code-detail-actions">
          <ContinueButton s={s} can={can} chat={chat} onContinued={onContinued} />
          <Button variant="secondary" size="sm" href={s.url} target="_blank" rel="noopener noreferrer">Open in ACTIVATE</Button>
        </div>
      </div>
      {!chat && !can.ok && <p className="muted code-continue-why">{can.why}</p>}
      {s.lastError && <Callout tone="error" size="sm" className="code-callout">{s.lastError}</Callout>}
      {error && <Callout tone="warning" size="sm" className="code-callout">{error}</Callout>}
      {!detail && !error && <div className="muted pad"><Loader inline size={16} label="Reading the conversation…" /></div>}
      {detail && <Transcript items={detail.history} earlier={detail.earlier} />}
    </>
  )
}

/** One tool call with its result folded under it. */
function ToolCall({ call, result }: { call: Item; result: Item | null }) {
  return (
    <details className="code-tool">
      <summary><span className="code-tool-name">{call.toolName}</span> <span className="code-tool-args">{argSummary(call.toolArgs, call.toolName)}</span></summary>
      {result?.text && <pre>{result.text}</pre>}
    </details>
  )
}

/** A run of tool calls in one line, "6 tool calls: ReadFile 3, EditFile 2,
 *  Bash 1", that opens to each call. */
function ToolRun({ calls }: { calls: { call: Item; result: Item | null }[] }) {
  const counts = new Map<string, number>()
  for (const { call } of calls) counts.set(call.toolName ?? 'tool', (counts.get(call.toolName ?? 'tool') ?? 0) + 1)
  const names = [...counts].sort((a, b) => b[1] - a[1]).map(([n, c]) => (c > 1 ? `${n} ${c}` : n)).join(', ')
  return (
    <details className="code-tool code-tool-run">
      <summary><span className="code-tool-name">{calls.length} tool calls</span> <span className="code-tool-args">{names}</span></summary>
      <div className="code-tool-run-body">
        {calls.map((c, j) => <ToolCall key={j} call={c.call} result={c.result} />)}
      </div>
    </details>
  )
}

/** The conversation: the user's messages and the agent's replies in full;
 *  a tool call as a line with its result folded under it, and several in a
 *  row as one line that opens to them. */
export function Transcript({ items, earlier }: { items: Item[]; earlier: number }) {
  const out: ReactElement[] = []
  let run: { call: Item; result: Item | null }[] = []
  let runKey = 0
  const flush = () => {
    if (run.length === 1) out.push(<ToolCall key={`t${runKey}`} call={run[0].call} result={run[0].result} />)
    else if (run.length > 1) out.push(<ToolRun key={`t${runKey}`} calls={run} />)
    run = []
  }
  for (let i = 0; i < items.length; i++) {
    const it = items[i]
    if (it.kind === 'tool_call') {
      if (!run.length) runKey = i
      const next = items[i + 1]
      const result = next?.kind === 'tool_result' ? next : null
      if (result) i++
      run.push({ call: it, result })
      continue
    }
    if (it.kind === 'tool_result') continue
    flush()
    if (it.kind === 'user') { out.push(<div key={i} className="code-msg user"><div className="code-msg-body">{it.text}</div></div>); continue }
    if (it.kind === 'assistant' || it.kind === 'recap' || it.kind === 'plan_view' || it.kind === 'goal_view') {
      out.push(<div key={i} className="code-msg assistant md-body"><Streamdown>{it.text ?? ''}</Streamdown></div>)
      continue
    }
    if (it.text) out.push(<div key={i} className={`code-note ${it.kind}`}>{it.text}</div>)
  }
  flush()
  return (
    <div className="code-transcript">
      {earlier > 0 && <p className="muted">{earlier} earlier items are not shown; open the session in ACTIVATE for all of it.</p>}
      {out.length ? out : <p className="muted">No messages yet.</p>}
    </div>
  )
}

const ARG_KEYS = ['path', 'file_path', 'pattern', 'command', 'query', 'url', 'description', 'prompt']
const LINE = 160

/** A string field's value from arguments that may be cut short (the server
 *  keeps the first 600 characters, so long ones are no longer JSON). */
function fieldOf(raw: string, key: string): string | undefined {
  const m = new RegExp(`"${key}"\\s*:\\s*"((?:[^"\\\\]|\\\\.)*)`).exec(raw)
  if (!m) return undefined
  try { return JSON.parse(`"${m[1]}"`) as string } catch { return m[1].replace(/\\n/g, '\n').replace(/\\(.)/g, '$1') }
}

/** The first line of a value, cut to a line's length. */
function firstLine(v: string): string {
  const lines = v.split('\n').map(l => l.trim()).filter(Boolean)
  const head = lines[0] ?? ''
  const more = lines.length > 1 || head.length > LINE
  return head.slice(0, LINE) + (more ? ' …' : '')
}

/** A tool call's arguments as a short line: the to-do item under way for a
 *  to-do list, the pattern and where for a search, else its path, command
 *  or query, else its first text value. Never the raw arguments. */
export function argSummary(args?: string, tool?: string): string {
  if (!args) return ''
  let o: Record<string, unknown> | null = null
  try { const v = JSON.parse(args); if (v && typeof v === 'object' && !Array.isArray(v)) o = v as Record<string, unknown> } catch { /* cut short */ }
  const get = (k: string) => (o ? (typeof o[k] === 'string' ? o[k] as string : undefined) : fieldOf(args, k))
  if (/todo/i.test(tool ?? '') || /"todos"\s*:/.test(args)) {
    const todos = o && Array.isArray(o.todos) ? o.todos as { status?: string; activeForm?: string; content?: string }[] : null
    const doing = todos?.find(t => t.status === 'in_progress') ?? todos?.[0]
    // Cut short: each item is a {...} of its own; the one under way first.
    const parts = todos ? [] : (args.match(/\{[^{}]*/g) ?? []).filter(p => /"(activeForm|content)"/.test(p))
    const part = parts.find(p => /"status"\s*:\s*"in_progress"/.test(p)) ?? parts[0]
    const what = doing?.activeForm ?? doing?.content ?? (part ? fieldOf(part, 'activeForm') ?? fieldOf(part, 'content') : undefined)
    return what ? firstLine(what) : 'the to-do list'
  }
  const pattern = get('pattern')
  if (pattern) {
    const where = get('path') ?? get('file_path')
    return firstLine(where ? `${pattern} in ${where}` : pattern)
  }
  for (const k of ARG_KEYS) {
    const v = get(k)
    if (v) return firstLine(v)
  }
  const first = o
    ? Object.values(o).find(v => typeof v === 'string' && v.trim()) as string | undefined
    : fieldOf(args, '[A-Za-z_]+')
  return first ? firstLine(first) : ''
}
