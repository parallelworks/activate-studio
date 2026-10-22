import { useSyncExternalStore } from 'react'
import { api, type IndexJob, type IndexPhase, type IndexStatus } from './api'

/**
 * The index's status, read once for every view that shows it: the footer's
 * index line, the status panel, Settings, Libraries and Statistics all
 * subscribe here, so they agree. While a job runs it is read every second;
 * otherwise every 15 seconds, and not while the tab is hidden. When a job
 * ends, or a first build makes the names searchable, the views that read
 * the index are told (ade-kb-changed), as after any other change.
 */

let state: IndexStatus | null = null
const subs = new Set<() => void>()
let inflight: Promise<void> | null = null
let loop = 0
let seen: { running: boolean; searchable: string | null } = { running: false, searchable: null }

let FAST_MS = 1000
let SLOW_MS = 15_000

/** Tests poll faster. */
export function setIndexPollingForTests(fastMs: number, slowMs: number): void { FAST_MS = fastMs; SLOW_MS = slowMs }

function notify(): void { for (const fn of subs) fn() }

/** Read the status now; resolves to it (or the last one, on a failure). */
export function refreshIndexStatus(): Promise<IndexStatus | null> {
  if (!inflight) {
    inflight = api.indexStatus().then(s => {
      const searchable = s.job?.searchableAt ?? null
      const changed = (seen.running && !s.running) || (!!searchable && searchable !== seen.searchable)
      seen = { running: s.running, searchable }
      state = s
      if (changed) window.dispatchEvent(new Event('ade-kb-changed'))
    }).catch(() => { /* the next read */ }).finally(() => { inflight = null; notify() })
  }
  return inflight.then(() => state)
}

function startLoop(): void {
  const mine = ++loop
  const tick = async (): Promise<void> => {
    if (mine !== loop || !subs.size) return
    if (document.visibilityState !== 'hidden' || state?.running) await refreshIndexStatus()
    if (mine !== loop || !subs.size) return
    setTimeout(() => { void tick() }, state?.running ? FAST_MS : SLOW_MS)
  }
  void tick()
}

/** After starting or stopping something: read now, and at the fast pace. */
export function kickIndexStatus(): void { if (subs.size) startLoop(); else void refreshIndexStatus() }

function subscribe(fn: () => void): () => void {
  subs.add(fn)
  if (subs.size === 1) {
    startLoop()
    document.addEventListener('visibilitychange', onVisible)
  }
  return () => {
    subs.delete(fn)
    if (!subs.size) { loop++; document.removeEventListener('visibilitychange', onVisible) }
  }
}

function onVisible(): void { if (document.visibilityState === 'visible') startLoop() }

export function useIndexStatus(): IndexStatus | null {
  return useSyncExternalStore(subscribe, () => state)
}

// ---- what to say about it ----

export type Tone = 'info' | 'success' | 'warning' | 'error' | 'muted'

export interface IndexSummary {
  tone: Tone
  pulse: boolean
  /** The short line: the footer, the status panel, a tile. */
  text: string
  /** The job or condition behind it, in a sentence. */
  detail: string | null
  /** How far the job has come, 0 to 100, or null when not known. */
  percent: number | null
  running: boolean
}

const SHORT: Record<IndexPhase['id'], string> = { names: 'names', documents: 'documents', text: 'text', meaning: 'meaning' }

/** The phase a job is in now, if any. */
export function currentPhase(job: IndexJob | null | undefined): IndexPhase | null {
  return job?.phases?.find(p => p.state === 'running') ?? null
}

/** How far a job has come, counting each phase it runs equally. */
export function jobPercent(job: IndexJob | null | undefined): number | null {
  const phases = (job?.phases ?? []).filter(p => p.state !== 'skipped')
  if (!phases.length) return null
  let sum = 0
  for (const p of phases) {
    if (p.state === 'done') sum += 1
    else if (p.state === 'running' && p.total > 0) sum += Math.min(1, p.done / p.total)
  }
  return Math.round((sum / phases.length) * 100)
}

/** The running phase as a short line: "meaning 21%". */
export function phaseLine(job: IndexJob | null | undefined): string | null {
  const ph = currentPhase(job)
  if (!ph) return null
  return `${SHORT[ph.id]}${ph.total > 0 ? ` ${Math.min(100, Math.round((ph.done / ph.total) * 100))}%` : ''}`
}

export function indexSummary(s: IndexStatus | null, files?: number): IndexSummary {
  const base = { pulse: false, detail: null, percent: null, running: false }
  if (!s) return { ...base, tone: 'muted', text: 'index loading' }
  const job = s.job ?? null
  if (job) {
    const ph = currentPhase(job)
    const pct = jobPercent(job)
    const what = job.kind === 'delete' ? 'Deleting the index' : job.kind === 'move' ? 'Moving the index' : 'Indexing'
    const text = ph ? `${what}: ${SHORT[ph.id]}${ph.total > 0 ? ` ${Math.min(100, Math.round((ph.done / ph.total) * 100))}%` : ''}` : `${what}…`
    return { tone: 'info', pulse: true, text, detail: job.label ?? null, percent: pct, running: true }
  }
  if (s.running || (s.queued?.length ?? 0) > 0) return { ...base, tone: 'info', pulse: true, text: 'Indexing…', running: true }
  if (s.lastError) return { ...base, tone: 'warning', text: 'index needs attention', detail: s.lastError }
  if (s.incomplete) {
    return { ...base, tone: 'warning', text: 'index incomplete',
      detail: s.lastBuild?.state === 'cancelled' ? 'The last build was stopped before it finished. Update the index to finish it.' : 'The last build did not finish. Update the index to finish it.' }
  }
  if (s.indexed === false) return { ...base, tone: 'muted', text: 'not indexed', detail: 'Build the index under Settings, Libraries.' }
  return { ...base, tone: 'success', text: files != null ? `${files.toLocaleString()} files indexed` : 'indexed' }
}

/** A duration as people read it: 850 ms, 12 s, 4 min 12 s, 1 h 3 min. */
export function fmtDuration(ms: number | null | undefined): string {
  if (ms == null || !Number.isFinite(ms)) return ''
  if (ms < 1000) return `${Math.max(0, Math.round(ms))} ms`
  const s = ms / 1000
  if (s < 60) return `${s < 10 ? s.toFixed(1) : Math.round(s)} s`
  const m = Math.floor(s / 60)
  if (m < 60) return `${m} min ${Math.round(s - m * 60)} s`
  return `${Math.floor(m / 60)} h ${m % 60} min`
}

/** The time a running job has taken so far. */
export function elapsed(job: IndexJob | null | undefined, now = Date.now()): number | null {
  const from = job?.runningAt ?? job?.startedAt
  return from ? Math.max(0, now - Date.parse(from)) : null
}

/** One line on a finished job's phases: "names 0.1 s, text 1 min 5 s, …". */
export function phaseTimes(job: IndexJob | null | undefined): string {
  return (job?.phases ?? []).filter(p => p.ms != null).map(p => `${SHORT[p.id]} ${fmtDuration(p.ms)}`).join(', ')
}
