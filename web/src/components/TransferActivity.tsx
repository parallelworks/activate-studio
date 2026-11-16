import { memo, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import {
  Button, Callout, Drawer, EmptyState, Loader, ProgressBar, SearchInput, Select, StatusDot, StatusLabel, Tabs, type BadgeVariant,
} from '@parallelworks/ui'
import {
  ACTION_TEXT, KB_CHANGED_EVENT, STATE_TEXT, bytesText, destLine, durationText, isActive, plural, sourceLine, touchesMainLibrary,
  transferApi, transferTitle,
  type Outcome, type Transfer, type TransferState,
} from '../transferApi'
import { ReadySummary, useTransfersReady } from './TransfersReady'

/** Every 2 seconds while anything is under way, else every 15, as the
 *  contract asks. */
export const POLL_ACTIVE_MS = 2000
export const POLL_IDLE_MS = 15000

const TONE: Record<TransferState, { variant?: BadgeVariant; off?: boolean; pulse?: boolean }> = {
  'awaiting-approval': { variant: 'warning' },
  queued: { off: true },
  running: { variant: 'info', pulse: true },
  verifying: { variant: 'info', pulse: true },
  succeeded: { variant: 'success' },
  failed: { variant: 'error' },
  cancelled: { off: true },
  interrupted: { variant: 'warning' },
}

export function StateLabel({ state }: { state: TransferState }) {
  const t = TONE[state]
  return <StatusLabel variant={t.variant} off={t.off} pulse={t.pulse}>{STATE_TEXT[state]}</StatusLabel>
}

function timeText(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const sameDay = d.toDateString() === new Date().toDateString()
  return sameDay
    ? d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })
    : d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}

/** The amounts behind a progress bar: files and bytes done of total, and
 *  while running the rate and time left. */
export function progressText(t: Transfer): string {
  const files = t.files.total < 0 ? `${plural(t.files.done, 'file')}, counting` : `${t.files.done.toLocaleString()} of ${plural(t.files.total, 'file')}`
  const bytes = t.bytes.total < 0 ? `${bytesText(t.bytes.done)}` : `${bytesText(t.bytes.done)} of ${bytesText(t.bytes.total)}`
  const parts = [files, bytes]
  if (t.state === 'running' && t.rate) parts.push(`${bytesText(t.rate)}/s`)
  if (t.state === 'running' && t.etaSeconds != null) parts.push(`${durationText(t.etaSeconds)} left`)
  return parts.join(' · ')
}

function Progress({ t }: { t: Transfer }) {
  const known = t.bytes.total > 0
  const value = known ? t.bytes.done : t.files.total > 0 ? t.files.done : null
  const max = known ? t.bytes.total : t.files.total > 0 ? t.files.total : undefined
  const tone = t.state === 'failed' ? 'error' : t.state === 'succeeded' ? 'success' : t.state === 'cancelled' || t.state === 'interrupted' ? 'muted' : 'info'
  const finished = !isActive(t)
  return (
    <ProgressBar className="xfer-progress" size="sm" tone={tone}
      value={finished && t.state === 'succeeded' ? (max ?? 1) : t.state === 'awaiting-approval' || t.state === 'queued' ? 0 : value}
      max={max ?? 1} aria-label={`Progress of ${transferTitle(t)}`}
      showValue="none" detail={progressText(t)} />
  )
}

function StartedBy({ t }: { t: Transfer }) {
  if (t.startedBy !== 'assistant') return <span>You</span>
  return t.conversationId
    ? <span>Assistant, <a href={`#chat=${t.conversationId}`}>open the conversation</a></span>
    : <span>Assistant</span>
}

function Row({ t, highlight, onOpen }: { t: Transfer; highlight: boolean; onOpen: (t: Transfer) => void }) {
  return (
    <li className={`xfer-row${highlight ? ' new' : ''}`} data-transfer={t.id}>
      <button type="button" className="xfer-row-open" onClick={() => onOpen(t)} aria-label={`Details of ${transferTitle(t)}`}>
        <span className="xfer-row-title">{transferTitle(t)}</span>
      </button>
      <div className="xfer-row-state"><StateLabel state={t.state} /></div>
      <div className="xfer-row-route xfer-mono" title={`${sourceLine(t.source)} → ${destLine(t.destination)}`}>
        {sourceLine(t.source)} <span aria-label="to">→</span> {destLine(t.destination)}
      </div>
      <div className="xfer-row-meta">
        <span>{ACTION_TEXT[t.action]}</span>
        <span>{t.mover}</span>
        <StartedBy t={t} />
        <span title={t.requestedAt}>{t.endedAt ? `Ended ${timeText(t.endedAt)}` : t.startedAt ? `Started ${timeText(t.startedAt)}` : `Requested ${timeText(t.requestedAt)}`}</span>
      </div>
      {t.state === 'awaiting-approval'
        ? (
          <div className="xfer-row-await">
            {t.conversationId
              ? <>Waiting for approval in the chat. <a href={`#chat=${t.conversationId}`}>Approve or deny it there</a>.</>
              : 'Waiting for approval in the chat.'}
          </div>
        )
        : <Progress t={t} />}
      {t.state === 'failed' && t.error && <div className="xfer-row-error">{t.error}</div>}
    </li>
  )
}

type Tab = 'all' | 'active' | 'done'
type Who = 'user' | 'assistant'

/** How often newly finished transfers are asked for when nothing has been
 *  seen to finish: the active list catches most, this catches one that
 *  started and finished between two reads. */
export const SINCE_EVERY_MS = 15000

/** The day a finished transfer belongs to, as its group heading. */
export function dayLabel(iso: string | undefined, now = new Date()): string {
  const d = iso ? new Date(iso) : null
  if (!d || Number.isNaN(d.getTime())) return 'Earlier'
  const start = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime()
  const days = Math.round((start(now) - start(d)) / 86_400_000)
  if (days <= 0) return 'Today'
  if (days === 1) return 'Yesterday'
  return d.toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric', year: d.getFullYear() === now.getFullYear() ? undefined : 'numeric' })
}

const endOf = (t: Transfer) => t.endedAt ?? t.startedAt ?? t.requestedAt
const byNewest = (a: Transfer, b: Transfer) => Date.parse(endOf(b)) - Date.parse(endOf(a))

/** Whether a finished transfer fits the list's search and filters, for one
 *  that arrives through a since read, which is asked for unfiltered. */
export function matchesFilters(t: Transfer, f: { q: string; outcome: Outcome | null; startedBy: Who | null }): boolean {
  if (f.outcome && t.state !== f.outcome) return false
  if (f.startedBy && t.startedBy !== f.startedBy) return false
  const q = f.q.trim().toLowerCase()
  if (!q) return true
  return [transferTitle(t), t.source.label, t.destination.label, t.destination.path, t.mover, ...t.source.paths]
    .some(v => v?.toLowerCase().includes(q))
}

/** A finished transfer on one line: state, name, route, mover, who, when
 *  it ended and its size. Memoized, so loading another page renders only
 *  the rows it adds. */
const Line = memo(function Line({ t, highlight, onOpen }: { t: Transfer; highlight: boolean; onOpen: (t: Transfer) => void }) {
  const tone = TONE[t.state]
  const size = t.bytes.total >= 0 ? t.bytes.total : t.bytes.done
  return (
    <li className={`xfer-line${highlight ? ' new' : ''}`} data-transfer={t.id}>
      <button type="button" className="xfer-line-open" onClick={() => onOpen(t)} aria-label={`Details of ${transferTitle(t)}, ${STATE_TEXT[t.state]}`}>
        <StatusDot variant={tone.variant} off={tone.off} title={STATE_TEXT[t.state]} />
        <span className="xfer-line-title" title={transferTitle(t)}>{transferTitle(t)}</span>
      </button>
      <span className="xfer-line-route xfer-mono" title={`${sourceLine(t.source)} → ${destLine(t.destination)}`}>
        {sourceLine(t.source)} → {destLine(t.destination)}
      </span>
      <span className="xfer-line-mover">{t.mover}</span>
      <span className="xfer-line-who">{t.startedBy === 'assistant'
        ? (t.conversationId ? <a href={`#chat=${t.conversationId}`} title="Open the conversation">Assistant</a> : 'Assistant')
        : 'You'}</span>
      <span className="xfer-line-time" title={endOf(t)}>{timeText(endOf(t))}</span>
      <span className="xfer-line-size">{bytesText(size)}</span>
    </li>
  )
})

interface History { rows: Transfer[]; next: string | null; loading: boolean; loaded: boolean; error: string }

/**
 * The transfers, yours and the assistant's. Those under way are cards at
 * the top, read every 2 seconds while any run. Finished ones are one line
 * each, by day, 50 at a time, searched and filtered on the server; after
 * the first page only newly finished ones are asked for (since the newest
 * seen), never the whole history again.
 */
/** No transfers yet: what is ready to use, and the two ways on. */
function EmptyTransfers({ onNew }: { onNew: () => void }) {
  const ready = useTransfersReady()
  return (
    <EmptyState variant="plain" title="No transfers yet"
      description="Transfers you start here, and the ones the assistant starts from a chat, are listed here with their progress."
      action={
        <div className="xfer-empty-ready">
          {ready && <ReadySummary ready={ready} compact />}
          <div className="xfer-empty-actions">
            <Button variant="primary" size="sm" onClick={onNew}>New transfer</Button>
            <Button variant="secondary" size="sm" href="#view=settings:transfers">Add a location</Button>
          </div>
        </div>
      } />
  )
}

export function TransferActivity({
  highlightId, added, onLanded, onNew,
}: {
  /** A transfer just started from the page, marked and kept at the top. */
  highlightId: string | null
  /** That transfer as the start returned it, shown before the next read. */
  added: Transfer | null
  /** Called once for each transfer seen to finish with success. */
  onLanded: (t: Transfer) => void
  onNew: () => void
}) {
  const [tab, setTab] = useState<Tab>('all')
  const [active, setActive] = useState<Transfer[] | null>(null)
  const [activeError, setActiveError] = useState('')
  const [hist, setHist] = useState<History>({ rows: [], next: null, loading: false, loaded: false, error: '' })
  const [q, setQ] = useState('')
  const [query, setQuery] = useState('')
  const [outcome, setOutcome] = useState<Outcome | null>(null)
  const [who, setWho] = useState<Who | null>(null)
  const [open, setOpen] = useState<Transfer | null>(null)
  // A link to one transfer, as the assistant's reply gives it
  // (#view=transfers?transfer=<id>), opens its details.
  useEffect(() => {
    const show = () => {
      const m = location.hash.match(/^#view=transfers\?(?:[^#]*&)?transfer=([^&#]+)/)
      if (m) transferApi.get(decodeURIComponent(m[1])).then(setOpen, () => {})
    }
    show()
    // The sidebar's Transfers item closes the details too.
    const home = () => setOpen(null)
    window.addEventListener('hashchange', show)
    window.addEventListener('ade:transfers-home', home)
    return () => { window.removeEventListener('hashchange', show); window.removeEventListener('ade:transfers-home', home) }
  }, [])
  const closeDetail = () => {
    setOpen(null)
    if (/[?&]transfer=/.test(location.hash)) history.replaceState(null, '', location.pathname + location.search + '#view=transfers')
  }
  const activeIds = useRef<Set<string>>(new Set())
  const seen = useRef<Set<string>>(new Set())
  const newest = useRef<string | null>(null)
  const lastSince = useRef(0)
  const histSeq = useRef(0)
  const filters = useRef({ q: '', outcome: null as Outcome | null, startedBy: null as Who | null })
  filters.current = { q: query, outcome, startedBy: who }
  const onLandedRef = useRef(onLanded)
  onLandedRef.current = onLanded

  // The search waits for a pause in typing.
  useEffect(() => { const t = setTimeout(() => setQuery(q), 300); return () => clearTimeout(t) }, [q])

  const note = (rows: Transfer[]) => {
    for (const r of rows) {
      seen.current.add(r.id)
      const e = endOf(r)
      if (e && (!newest.current || Date.parse(e) > Date.parse(newest.current))) newest.current = e
    }
  }

  const loadPage = useCallback(async (reset: boolean) => {
    const mine = ++histSeq.current
    setHist(h => ({ ...h, loading: true, error: '', ...(reset ? { rows: [], next: null, loaded: false } : {}) }))
    try {
      const f = filters.current
      const before = reset ? null : histRef.current.next
      const r = await transferApi.list({ state: 'done', before, q: f.q, outcome: f.outcome, startedBy: f.startedBy })
      if (mine !== histSeq.current) return
      note(r.transfers)
      setHist(h => {
        const have = new Set(reset ? [] : h.rows.map(x => x.id))
        return { rows: [...(reset ? [] : h.rows), ...r.transfers.filter(x => !have.has(x.id))], next: r.next, loading: false, loaded: true, error: '' }
      })
    } catch (e) {
      if (mine !== histSeq.current) return
      setHist(h => ({ ...h, loading: false, loaded: true, error: (e as Error).message }))
    }
  }, [])
  const histRef = useRef(hist)
  histRef.current = hist

  useEffect(() => { void loadPage(true) }, [loadPage, query, outcome, who])

  /** Transfers finished since the newest one seen: unfiltered, so one that
   *  landed is reported whatever the list shows, then kept if it fits. */
  const loadSince = useCallback(async () => {
    lastSince.current = Date.now()
    if (!newest.current && !histRef.current.loaded) return
    try {
      const since = newest.current ?? new Date(Date.now() - 60_000).toISOString()
      const r = await transferApi.list({ state: 'done', since })
      const fresh = r.transfers.filter(t => !seen.current.has(t.id))
      if (!fresh.length) return
      note(fresh)
      for (const t of fresh) if (t.state === 'succeeded') onLandedRef.current(t)
      // The Library tree and its details read the main library again when
      // a transfer wrote there or moved files out of it. Any ending counts:
      // a move that failed part way may have removed some originals.
      if (fresh.some(touchesMainLibrary)) window.dispatchEvent(new Event(KB_CHANGED_EVENT))
      const keep = fresh.filter(t => matchesFilters(t, filters.current))
      if (keep.length) setHist(h => ({ ...h, rows: [...keep, ...h.rows.filter(x => !keep.some(k => k.id === x.id))].sort(byNewest) }))
    } catch { /* the next tick asks again */ }
  }, [])

  const loadActive = useCallback(async (): Promise<boolean> => {
    try {
      const rows = (await transferApi.list({ state: 'active' })).transfers.filter(isActive)
      setActive(rows); setActiveError('')
      const now = new Set(rows.map(r => r.id))
      const left = [...activeIds.current].filter(id => !now.has(id))
      activeIds.current = now
      if (left.length || Date.now() - lastSince.current >= SINCE_EVERY_MS) void loadSince()
      return rows.some(r => r.state !== 'awaiting-approval')
    } catch (e) {
      setActiveError((e as Error).message); setActive(a => a ?? [])
      return false
    }
  }, [loadSince])

  // Reads the active list now and then on the next tick; a transfer just
  // started cuts short a slow (idle) wait.
  const pollSoon = useRef<() => void>(() => {})
  useEffect(() => {
    let stop = false
    let timer: ReturnType<typeof setTimeout>
    lastSince.current = Date.now()
    const tick = async () => {
      clearTimeout(timer)
      const busy = await loadActive()
      if (!stop) { clearTimeout(timer); timer = setTimeout(tick, busy || activeIds.current.size ? POLL_ACTIVE_MS : POLL_IDLE_MS) }
    }
    pollSoon.current = () => { clearTimeout(timer); timer = setTimeout(tick, POLL_ACTIVE_MS) }
    void tick()
    return () => { stop = true; clearTimeout(timer); pollSoon.current = () => {} }
  }, [loadActive])

  // The transfer just started shows at once, before the next read lists it.
  useEffect(() => {
    if (!added) return
    setActive(a => (a?.some(r => r.id === added.id) ? a : [added, ...(a ?? [])]))
    if (isActive(added)) { activeIds.current.add(added.id); pollSoon.current() }
  }, [added])

  // Scrolling to the end of the list loads the next page.
  const sentinel = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const el = sentinel.current
    if (!el || typeof IntersectionObserver !== 'function') return
    const io = new IntersectionObserver(es => {
      const h = histRef.current
      if (es.some(e => e.isIntersecting) && h.next && !h.loading) void loadPage(false)
    }, { rootMargin: '400px' })
    io.observe(el)
    return () => io.disconnect()
  }, [loadPage, hist.next, tab])

  const removed = (id: string) => { setHist(h => ({ ...h, rows: h.rows.filter(r => r.id !== id) })); setOpen(null) }

  const activeRows = highlightId
    ? [...(active ?? []).filter(r => r.id === highlightId), ...(active ?? []).filter(r => r.id !== highlightId)]
    : active ?? []
  const finished = useMemo(() => {
    const ids = new Set((active ?? []).map(a => a.id))
    return hist.rows.filter(r => !ids.has(r.id))
  }, [hist.rows, active])
  const days = useMemo(() => {
    const out: { day: string; rows: Transfer[] }[] = []
    for (const t of finished) {
      const day = dayLabel(endOf(t))
      if (out.at(-1)?.day !== day) out.push({ day, rows: [] })
      out.at(-1)!.rows.push(t)
    }
    return out
  }, [finished])
  const filtered = !!(query.trim() || outcome || who)
  const nothingAtAll = active !== null && hist.loaded && !activeRows.length && !finished.length && !filtered && !hist.error

  return (
    <div className="xfer-activity">
      <div className="xfer-activity-head">
        <Tabs<Tab> label="Which transfers" variant="segmented" size="sm" value={tab} onValueChange={setTab}
          items={[
            { value: 'all', label: 'All' },
            { value: 'active', label: 'Active', count: activeRows.length || undefined },
            { value: 'done', label: 'Finished' },
          ]} />
        <Button variant="primary" size="sm" onClick={onNew}>New transfer</Button>
      </div>
      {activeError && <Callout tone="warning" size="sm" className="xfer-callout">{activeError}</Callout>}
      {nothingAtAll && (
        <EmptyTransfers onNew={onNew} />
      )}

      {!nothingAtAll && tab !== 'done' && (
        <section className="xfer-group" aria-label="Active">
          <h2 className="xfer-group-label">Active</h2>
          {active === null
            ? <div className="muted xfer-pad"><Loader inline size={16} label="Reading transfers…" /></div>
            : activeRows.length
              ? <ul className="xfer-rows">{activeRows.map(t => <Row key={t.id} t={t} highlight={t.id === highlightId} onOpen={setOpen} />)}</ul>
              : <p className="muted xfer-pad">Nothing under way.</p>}
        </section>
      )}

      {!nothingAtAll && tab !== 'active' && (
        <section className="xfer-group" aria-label="Finished">
          <div className="xfer-finished-head">
            <h2 className="xfer-group-label">Finished</h2>
            <SearchInput size="sm" value={q} onValueChange={setQ} label="Search finished transfers" placeholder="Search" className="xfer-search" />
            <Select<Outcome | 'any'> aria-label="Outcome" size="sm" value={outcome ?? 'any'}
              onValueChange={v => setOutcome(v === 'any' || !v ? null : v)}
              items={[{ value: 'any', label: 'Any outcome' }, { value: 'succeeded', label: 'Succeeded' }, { value: 'failed', label: 'Failed' }, { value: 'cancelled', label: 'Cancelled' }, { value: 'interrupted', label: 'Interrupted' }]} />
            <Select<Who | 'any'> aria-label="Started by" size="sm" value={who ?? 'any'}
              onValueChange={v => setWho(v === 'any' || !v ? null : v)}
              items={[{ value: 'any', label: 'Anyone' }, { value: 'user', label: 'You' }, { value: 'assistant', label: 'The assistant' }]} />
          </div>
          {hist.error && <Callout tone="warning" size="sm" className="xfer-callout">{hist.error}</Callout>}
          {!hist.loaded && hist.loading && <div className="muted xfer-pad"><Loader inline size={16} label="Reading finished transfers…" /></div>}
          {hist.loaded && !finished.length && !hist.error && (
            <p className="muted xfer-pad">{filtered ? 'No finished transfer matches.' : 'Nothing finished yet.'}</p>
          )}
          {days.map(d => (
            <div key={d.day} className="xfer-day" role="group" aria-label={d.day}>
              <h3 className="xfer-day-label">{d.day}</h3>
              <ul className="xfer-lines">{d.rows.map(t => <Line key={t.id} t={t} highlight={t.id === highlightId} onOpen={setOpen} />)}</ul>
            </div>
          ))}
          <div ref={sentinel} className="xfer-sentinel" />
          {hist.next && (
            <Button variant="secondary" size="sm" className="xfer-more" loading={hist.loading} onClick={() => void loadPage(false)}>Show more</Button>
          )}
        </section>
      )}
      {open && <TransferDetail key={open.id} first={open} onClose={closeDetail} onRemoved={removed} onChanged={() => void loadActive()} />}
    </div>
  )
}

/** One transfer in full: where, how far, its events and its error, with
 *  Cancel while it runs and Remove once it has finished. */
export function TransferDetail({
  first, onClose, onRemoved, onChanged,
}: { first: Transfer; onClose: () => void; onRemoved: (id: string) => void; onChanged: () => void }) {
  const [t, setT] = useState<Transfer>(first)
  const [refused, setRefused] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    let stop = false
    let timer: ReturnType<typeof setTimeout>
    const read = async () => {
      let next: Transfer | null = null
      try { next = await transferApi.get(first.id); if (!stop) { setT(next); setError('') } } catch (e) { if (!stop) setError((e as Error).message) }
      if (!stop && next && isActive(next)) timer = setTimeout(read, POLL_ACTIVE_MS)
    }
    void read()
    return () => { stop = true; clearTimeout(timer) }
  }, [first.id])

  const cancel = async () => {
    setBusy(true); setRefused('')
    try {
      const r = await transferApi.cancel(t.id)
      if (!r.cancelled) setRefused(r.why ?? 'The mover did not stop it.')
      else { onChanged(); setT(await transferApi.get(t.id)) }
    } catch (e) { setRefused((e as Error).message) } finally { setBusy(false) }
  }
  const remove = async () => {
    setBusy(true)
    try { await transferApi.remove(t.id); onRemoved(t.id) } catch (e) { setError((e as Error).message) } finally { setBusy(false) }
  }

  const events = t.events ?? []
  return (
    // Contained: it covers the page below its header, so its close button
    // sits under the header's Status pane toggle, never on top of it.
    <Drawer open contained onClose={onClose} title={transferTitle(t)} width={520}
      description={<StateLabel state={t.state} />}
      footer={
        <>
          {isActive(t) && t.state !== 'awaiting-approval' && <Button variant="danger-outline" onClick={() => void cancel()} loading={busy}>Cancel transfer</Button>}
          {!isActive(t) && <Button variant="secondary" onClick={() => void remove()} loading={busy}>Remove from list</Button>}
        </>
      }>
      <div className="xfer-detail">
        {t.error && <Callout tone="error" size="sm" title="Why it failed">{t.error}</Callout>}
        {refused && <Callout tone="warning" size="sm" live title="Not cancelled">{refused}</Callout>}
        {error && <Callout tone="warning" size="sm">{error}</Callout>}
        {t.state === 'awaiting-approval' && (
          <Callout tone="warning" size="sm" title="Awaiting approval">
            The assistant asked for this transfer. {t.conversationId ? <a href={`#chat=${t.conversationId}`}>Approve or deny it in the conversation.</a> : 'Approve or deny it in the chat.'}
          </Callout>
        )}
        {t.state !== 'awaiting-approval' && <Progress t={t} />}
        {(t.indexNotes ?? []).map(n => <Callout key={n} tone="info" size="sm">{n}.</Callout>)}
        <dl className="xfer-plan-facts">
          <dt>Action</dt><dd>{ACTION_TEXT[t.action]}</dd>
          <dt>From</dt>
          <dd className="xfer-mono">{t.source.paths.length <= 1 ? sourceLine(t.source) : t.source.label}{t.source.paths.length > 1 && (
            <ul className="xfer-names">{t.source.paths.slice(0, 20).map(p => <li key={p}>{p || '/'}</li>)}
              {t.source.paths.length > 20 && <li className="muted">and {t.source.paths.length - 20} more</li>}</ul>
          )}</dd>
          <dt>To</dt><dd className="xfer-mono">{destLine(t.destination)}</dd>
          <dt>Mover</dt><dd>{t.mover}</dd>
          <dt>Started by</dt><dd><StartedBy t={t} /></dd>
          <dt>Requested</dt><dd>{timeText(t.requestedAt)}</dd>
          {t.startedAt && <><dt>Started</dt><dd>{timeText(t.startedAt)}</dd></>}
          {t.endedAt && <><dt>Ended</dt><dd>{timeText(t.endedAt)}</dd></>}
          {(t.replaced > 0 || t.deleted > 0 || t.skipped > 0) && (
            <><dt>Also</dt><dd>{[t.replaced ? `${t.replaced.toLocaleString()} replaced` : '', t.deleted ? `${t.deleted.toLocaleString()} deleted` : '', t.skipped ? `${t.skipped.toLocaleString()} skipped` : ''].filter(Boolean).join(', ')}</dd></>
          )}
        </dl>
        <h3 className="xfer-events-head">Events</h3>
        {events.length
          ? <ol className="xfer-events">{events.map((e, i) => <li key={i}><time dateTime={e.at}>{timeText(e.at)}</time><span>{e.text}</span></li>)}</ol>
          : <p className="muted">No events yet.</p>}
      </div>
    </Drawer>
  )
}
