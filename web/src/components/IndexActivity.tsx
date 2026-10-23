import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Button, Callout, ConfirmModal, CopyButton, Modal, ProgressBar, StatusDot, Table, toast, type BadgeVariant } from '@parallelworks/ui'
import { api, type IndexJob, type IndexPhase, type IndexStatus } from '../api'
import { elapsed, fmtDuration, jobPercent, kickIndexStatus, phaseTimes, useIndexStatus } from '../indexActivity'

/**
 * The index's activity as the footer panel, Settings, Libraries and
 * Statistics show it: a job's phases with their counts and times, the
 * controls (build, stop, delete), the index log, and the record of runs.
 * Everything here reads the one status (indexActivity.ts).
 */

const errText = (e: unknown) => String((e as Error)?.message ?? e)
const n = (v: number) => v.toLocaleString()

const PHASE_TONE: Record<IndexPhase['state'], BadgeVariant> = {
  pending: 'muted', running: 'info', done: 'success', skipped: 'muted', failed: 'error',
}

/** "1,234 of 5,000 folders", or what the phase is doing while it counts. */
function phaseCount(p: IndexPhase): string {
  if (p.state === 'skipped') return p.note ?? 'skipped'
  if (p.state === 'pending') return 'waiting'
  const step = p.step === 'chunk' ? 'preparing passages' : p.step === 'write' ? 'writing tables' : null
  if (p.state === 'running' && !p.total) return step ? `${step}…` : 'counting…'
  if (p.total) return `${n(p.done)} of ${n(p.total)} ${p.unit}${p.state === 'running' && step ? `, ${step}` : ''}`
  return p.note ?? ''
}

/** Each phase: its state, its name and how long it took, then what it has
 *  done (and a bar while it runs), so a narrow panel reads as well as a page. */
export function IndexPhaseList({ job }: { job: IndexJob }) {
  const phases = job.phases ?? []
  if (!phases.length) return null
  return (
    <ol className="ix-phases" aria-label="Phases">
      {phases.map(p => {
        const count = phaseCount(p)
        const note = p.state === 'done' && p.note && p.note !== count ? p.note : null
        return (
          <li key={p.id} className={`ix-phase ix-${p.state}`}>
            <div className="ix-phase-head">
              <StatusDot variant={PHASE_TONE[p.state]} pulse={p.state === 'running'} size={7} />
              <span className="ix-phase-label">{p.label}</span>
              {p.ms != null && <span className="ix-phase-time">{fmtDuration(p.ms)}</span>}
            </div>
            <div className="ix-phase-count">{[count, note].filter(Boolean).join(' · ')}</div>
            {p.state === 'running' && (
              <div className="ix-phase-bar">
                <ProgressBar aria-label={p.label} size="sm" value={p.total ? p.done : null} max={p.total || 1} showValue="none" />
              </div>
            )}
          </li>
        )
      })}
    </ol>
  )
}

/** A running job: what it is, how far, how long so far, and its phases. */
export function IndexJobView({ job, onStop, stopping, log = true }: { job: IndexJob; onStop?: () => void; stopping?: boolean; log?: boolean }) {
  const [, tick] = useState(0)
  useEffect(() => {
    const t = window.setInterval(() => tick(x => x + 1), 1000)
    return () => window.clearInterval(t)
  }, [])
  const pct = jobPercent(job)
  // Phases take very different times (embeddings the longest), so the bar
  // says which phase it is in rather than a percentage of the whole.
  const ran = (job.phases ?? []).filter(p => p.state !== 'skipped')
  const at = ran.findIndex(p => p.state === 'running')
  const which = at >= 0 ? `phase ${at + 1} of ${ran.length}` : ''
  return (
    <div className="ix-job" aria-live="polite">
      <ProgressBar label={job.label ?? 'Indexing'} value={pct} max={100} status={which || undefined}
        detail={`${fmtDuration(elapsed(job))} so far, on ${job.threads ?? '?'} threads${job.state === 'queued' ? ', waiting for the job ahead' : ''}`} />
      <IndexPhaseList job={job} />
      {job.searchableAt && job.kind === 'build' && (
        <p className="ix-note">Search by name works now; text and meaning are added as they are read.</p>
      )}
      {(job.warningCount ?? 0) > 0 && (
        <p className="ix-note">{n(job.warningCount!)} file{job.warningCount === 1 ? '' : 's'} could not be read so far; the log names them.</p>
      )}
      {onStop && (
        <div className="ix-actions">
          <Button variant="secondary" size="sm" loading={stopping} onClick={onStop}>Stop</Button>
          {log && <IndexLogButton />}
        </div>
      )}
    </div>
  )
}

/** How a finished job ended, in a sentence. */
export function jobOutcome(job: IndexJob): string {
  const took = fmtDuration(job.ms)
  const times = phaseTimes(job)
  const what = job.kind === 'delete' ? 'Deleted the index' : job.kind === 'move' ? 'Moved the index' : job.label?.replace(/^(Building|Updating) the index$/, (_m, v: string) => (v === 'Building' ? 'Built the index' : 'Updated the index')) ?? 'Indexed'
  if (job.state === 'done') return `${what} in ${took}${times ? ` (${times})` : ''}${job.threads ? `, on ${job.threads} threads` : ''}.`
  if (job.state === 'cancelled') return `${job.label ?? 'Indexing'}: stopped after ${took}.`
  return `${job.label ?? 'Indexing'} failed after ${took}: ${job.error ?? 'see the log'}`
}

/** The index log: the last lines, kept current while a job runs. */
export function IndexLogDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const status = useIndexStatus()
  const [text, setText] = useState<string | null>(null)
  const [error, setError] = useState('')
  const running = !!status?.running
  useEffect(() => {
    if (!open) return
    let live = true
    const load = () => api.indexLog(500).then(t => { if (live) { setText(t); setError('') } }).catch(e => { if (live) setError(errText(e)) })
    void load()
    const t = running ? window.setInterval(load, 2000) : undefined
    return () => { live = false; if (t) window.clearInterval(t) }
  }, [open, running])
  const reveal = async () => {
    try { await api.revealIndexLog() } catch (e) { toast.error(errText(e)) }
  }
  return (
    <Modal open={open} onClose={onClose} size="xl" title="Index log"
      description={status?.log?.path ? <span className="ix-log-path">{status.log.path}</span> : 'What each index job did, newest last.'}
      footer={(
        <>
          {status?.log?.reveal && <Button variant="ghost" onClick={() => void reveal()}>Show in Finder</Button>}
          <a className="ix-log-full" href="/api/index/log?full=1" target="_blank" rel="noopener noreferrer">Open the full log</a>
          <Button variant="secondary" onClick={onClose}>Close</Button>
        </>
      )}>
      {error && <Callout tone="error">{error}</Callout>}
      {text === null ? <p className="muted">Loading…</p>
        : text.trim() ? <LogText text={text} />
          : <p className="muted">Nothing has been logged yet. The log fills as the index is built or synced.</p>}
    </Modal>
  )
}

// One log dialog for the page, held by a host that stays mounted (the
// footer's), so a button inside a popover that closes as the dialog opens
// still opens it. Without a host, a button keeps a dialog of its own.
let hosts = 0
const logOpeners = new Set<() => void>()

/** Holds the page's index log dialog; StatusFooter mounts it. */
export function IndexLogHost() {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    const opener = () => setOpen(true)
    hosts++
    logOpeners.add(opener)
    return () => { hosts--; logOpeners.delete(opener) }
  }, [])
  return <IndexLogDialog open={open} onClose={() => setOpen(false)} />
}

/** The log's lines, newest last, kept scrolled to the end as lines arrive
 *  unless the reader has scrolled up to look at something. */
function LogText({ text }: { text: string }) {
  const ref = useRef<HTMLPreElement>(null)
  const following = useRef(true)
  useLayoutEffect(() => {
    const el = ref.current
    if (el && following.current) el.scrollTop = el.scrollHeight
  }, [text])
  return (
    <div className="ix-log">
      <div className="ix-log-copy"><CopyButton text={text} label="Copy the log" /></div>
      <pre ref={ref} tabIndex={0} aria-label="Index log lines"
        onScroll={e => { const el = e.currentTarget; following.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24 }}>{text}</pre>
    </div>
  )
}

export function IndexLogButton({ size = 'sm', variant = 'ghost', label = 'View log' }: { size?: 'xs' | 'sm'; variant?: 'ghost' | 'secondary'; label?: string }) {
  const [open, setOpen] = useState(false)
  const show = () => {
    if (hosts > 0) { for (const fn of logOpeners) { fn(); break } } else setOpen(true)
  }
  return (
    <>
      <Button variant={variant} size={size} onClick={show}>{label}</Button>
      {hosts === 0 && <IndexLogDialog open={open} onClose={() => setOpen(false)} />}
    </>
  )
}

/** Recent index jobs, newest first, with what each took. */
export function IndexRuns({ runs }: { runs: IndexJob[] }) {
  if (!runs.length) return <p className="muted">No index jobs have run yet.</p>
  return (
    <Table density="compact" wrapperClassName="ix-runs">
      <Table.Header>Finished</Table.Header>
      <Table.Header>Job</Table.Header>
      <Table.Header>Took</Table.Header>
      <Table.Header>Phases</Table.Header>
      <Table.Header>Result</Table.Header>
      {runs.map(j => (
        <tr key={`${j.id}-${j.finishedAt}`}>
          <Table.Item>{j.finishedAt ? new Date(j.finishedAt).toLocaleString() : ''}</Table.Item>
          <Table.Item wrap>{j.label ?? j.kind}</Table.Item>
          <Table.Item>{fmtDuration(j.ms)}</Table.Item>
          <Table.Item wrap className="muted">{phaseTimes(j) || '—'}</Table.Item>
          <Table.Item wrap>{j.state === 'done' ? (j.warningCount ? `done, ${n(j.warningCount)} files unread` : 'done') : j.state === 'cancelled' ? 'stopped' : `failed: ${j.error ?? ''}`}</Table.Item>
        </tr>
      ))}
    </Table>
  )
}

/**
 * The main folder's index controls: build or update it, stop a running
 * job, delete it (a red button, then a dialog that asks first; the
 * folder's files are never touched), and the log. `canDelete` is for a
 * single-user install.
 */
export function useIndexControls() {
  const status = useIndexStatus()
  const [busy, setBusy] = useState<null | 'build' | 'stop' | 'delete'>(null)
  const [confirming, setConfirming] = useState(false)
  const [error, setError] = useState('')
  const [deleteError, setDeleteError] = useState('')
  const act = async (which: 'build' | 'stop' | 'delete', fn: () => Promise<unknown>) => {
    setBusy(which)
    setError('')
    try { await fn(); kickIndexStatus() } catch (e) { setError(errText(e)) } finally { setBusy(null) }
  }
  return {
    status, busy, error, confirming, setConfirming,
    build: () => act('build', () => api.buildIndex()),
    stop: () => act('stop', () => api.stopIndexing()),
    // The dialog closes once the index is gone; a failure shows in it, and
    // it stays open for another try.
    remove: async () => {
      setBusy('delete')
      setDeleteError('')
      try { await api.deleteIndex(); kickIndexStatus(); setConfirming(false) }
      catch (e) { setDeleteError(errText(e)) }
      finally { setBusy(null) }
    },
    deleteError,
    closeConfirm: () => { setConfirming(false); setDeleteError('') },
  }
}

export function IndexControls({ canDelete, canBuild = true, folder, part = 'all' }: {
  canDelete: boolean
  canBuild?: boolean
  folder?: string | null
  /** In a table row: the build or stop button (`primary`), and the
   *  delete and log controls under the row (`secondary`). */
  part?: 'all' | 'primary' | 'secondary'
}) {
  const c = useIndexControls()
  const s = c.status
  const running = !!s?.running
  const indexed = !!s?.indexed
  const primary = part !== 'secondary'
  const secondary = part !== 'primary'
  return (
    <>
      <div className="ix-actions">
        {primary && (running ? (
          <Button variant="secondary" size="sm" loading={c.busy === 'stop'} onClick={() => void c.stop()}>Stop indexing</Button>
        ) : canBuild && (
          <Button variant="secondary" size="sm" loading={c.busy === 'build'} onClick={() => void c.build()}>
            {indexed ? 'Update index' : 'Build index'}
          </Button>
        ))}
        {secondary && canDelete && indexed && !running && (
          <Button variant="danger-outline" size="sm" onClick={() => c.setConfirming(true)}>Delete index</Button>
        )}
        {secondary && <IndexLogButton />}
      </div>
      {c.error && <Callout tone="error">{c.error}</Callout>}
      {secondary && (
        <ConfirmModal open={c.confirming} onClose={c.closeConfirm} destructive closeOnConfirm={false}
          title="Delete the index?" confirmLabel="Delete index" cancelLabel="Keep it"
          description={<>
            Search, Query and Stats find nothing in {folder ? <code>{folder}</code> : 'the main folder'} until the index is built
            again. The files in the folder are not touched.
          </>}
          pending={c.busy === 'delete'} error={c.deleteError || undefined}
          onConfirm={() => c.remove()}
        />
      )}
    </>
  )
}

/** A finished build's line: when, how long, and each phase's time. */
export function LastBuild({ status }: { status: IndexStatus | null }) {
  const b = status?.lastBuild
  if (!b || b.kind !== 'build') return null
  return (
    <p className={b.state === 'done' ? 'ix-note' : 'ix-note ix-warn'}>
      {jobOutcome(b)}{b.finishedAt ? ` ${new Date(b.finishedAt).toLocaleString()}.` : ''}
    </p>
  )
}
