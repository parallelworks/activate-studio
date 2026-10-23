import { type CSSProperties, useEffect, useRef, useState } from 'react'
import { Button, Callout, type CalloutTone, EmptyState, Input, Loader, ProgressBar } from '@parallelworks/ui'
import type { IndexJob as ApiIndexJob, PublicLibrary } from '../api'
import { chooseFolderNatively, hasNativeFolderChooser } from '../nativeShell'
import { kickIndexStatus } from '../indexActivity'
import { IndexJobView, jobOutcome } from './IndexActivity'

/**
 * The primary folder on a single-user install, shared by the setup wizard
 * and Settings, Libraries: choosing another folder, which restarts the
 * Studio on it where the app's launcher runs it, and building the folder's
 * whole index with its progress (/api/setup/*).
 */

/** An index job as /api/index/job/:id and /api/setup/index return it:
 *  its phases with their counts, and how it ended. */
export type IndexJob = ApiIndexJob

export interface SetupStatus {
  firstRun: boolean
  standalone: boolean
  canRestart: boolean
  launcher?: boolean
  /** The macOS app's bundle, when the Studio runs as that app. */
  macApp?: string | null
  /** The port the Studio listens on, and one saved for its next start. */
  port?: number
  portNext?: number | null
  primary?: { path: string; exists: boolean; writable: boolean; indexed: boolean; files?: number; earlierIndex?: { builtAt: string | null } | null }
  gufi?: { found: boolean; bin: string; semantic: boolean }
  embedModel?: { path: string; found: boolean }
  /** The Studio's own icons, as the browser loads them (BrandIcons). */
  branding?: { light: string | null; dark: string | null; iconUrl?: string; iconUrlDark?: string }
  libraries?: PublicLibrary[]
  job?: IndexJob | null
}

interface Listing { path: string; parent: string | null; writable: boolean; dirs: { name: string; path: string }[] }

const sleep = (ms: number) => new Promise(r => setTimeout(r, ms))
export const errText = (e: unknown) => String((e as Error)?.message ?? e)

export async function json<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, init)
  const body = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((body as { error?: string }).error ?? `${res.status}`)
  return body as T
}
export const post = <T,>(url: string, body?: unknown) => json<T>(url, {
  method: 'POST',
  headers: { 'Content-Type': 'application/json' },
  body: JSON.stringify(body ?? {}),
})

/** Wait for the server to answer again as a new process. */
export async function waitForRestart(before: string, pollMs: number, alive: () => boolean): Promise<boolean> {
  const until = Date.now() + Math.max(120 * pollMs, 90_000)
  while (alive() && Date.now() < until) {
    await sleep(pollMs)
    try {
      const v = await json<{ startedAt?: string }>('/api/version')
      if (v.startedAt && v.startedAt !== before) return true
    } catch { /* still restarting */ }
  }
  return false
}

/** False once the component has gone, so a loop that outlives it stops. */
function useAlive() {
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])
  return alive
}

const row: CSSProperties = { display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }
const FOLDER_ICON = <svg viewBox="0 0 16 16" width="14" height="14" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M1.5 3.5A1 1 0 0 1 2.5 2.5h3l1.5 2h6a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1v-9z"/></svg>

/** The folders on this computer, one folder at a time (/api/setup/browse). */
function useFolderListing(start: string) {
  const [listing, setListing] = useState<Listing | null>(null)
  const [pathInput, setPathInput] = useState('')
  const [error, setError] = useState('')
  const [browsing, setBrowsing] = useState(false)
  const alive = useAlive()
  const browse = async (p: string) => {
    setBrowsing(true)
    setError('')
    try {
      const l = await json<Listing>(`/api/setup/browse?path=${encodeURIComponent(p)}`)
      if (!alive.current) return
      setListing(l)
      setPathInput(l.path)
    } catch (e) {
      if (alive.current) setError(errText(e))
    } finally {
      if (alive.current) setBrowsing(false)
    }
  }
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => { void browse(start) }, [])
  return { listing, pathInput, setPathInput, error, browsing, browse }
}

/** The path box, Open and Up, and the folders inside the one open. */
function FolderList({ f }: { f: ReturnType<typeof useFolderListing> }) {
  return (
    <>
      <form style={row} onSubmit={e => { e.preventDefault(); void f.browse(f.pathInput) }}>
        <div style={{ flex: '1 1 260px' }}>
          <Input aria-label="Folder" value={f.pathInput} onChange={e => f.setPathInput(e.target.value)} placeholder="~/Documents" />
        </div>
        <Button type="submit" variant="secondary" disabled={f.browsing}>Open</Button>
        <Button variant="ghost" disabled={!f.listing?.parent || f.browsing} onClick={() => f.listing?.parent && void f.browse(f.listing.parent)}>Up</Button>
      </form>
      {f.error && <Callout tone="error">{f.error}</Callout>}
      {f.listing && (
        <div style={{ border: '1px solid var(--pw-border)', borderRadius: 8, maxHeight: 220, overflowY: 'auto' }}>
          {f.listing.dirs.length ? f.listing.dirs.map(d => (
            <Button key={d.path} variant="ghost" size="sm" style={{ width: '100%', justifyContent: 'flex-start' }}
              icon={FOLDER_ICON} disabled={f.browsing} onClick={() => void f.browse(d.path)}>
              {d.name}
            </Button>
          )) : <EmptyState variant="bare" title="No folders inside" />}
        </div>
      )}
    </>
  )
}

/** Browse the folders on this computer and pick one, as for an index
 *  directory. Single-user installs only, like the browse route. */
export function FolderBrowser({ start, pickLabel = 'Use this folder', onPick }: {
  start: string
  pickLabel?: string
  onPick: (path: string) => void
}) {
  const f = useFolderListing(start)
  return (
    <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
      <FolderList f={f} />
      <div style={row}>
        <Button variant="secondary" disabled={!f.listing || f.browsing} onClick={() => f.listing && onPick(f.listing.path)}>{pickLabel}</Button>
      </div>
    </div>
  )
}

/**
 * Pick a folder on this computer and make it the primary: with the system
 * folder chooser in the macOS app's window, and by browsing the folders in
 * the page everywhere else (or if the chooser cannot open). Where the
 * launcher can restart the Studio it does, and this waits for it to come
 * back; elsewhere the server says what to set instead.
 */
export function FolderPicker({ current, start, disabled = false, pollMs = 1000, onRestarting, onChanged }: {
  /** The folder in use, which cannot be chosen again. */
  current: string | null | undefined
  /** Where browsing starts. */
  start: string
  /** Holds "Use this folder" off, as while the index builds. */
  disabled?: boolean
  pollMs?: number
  /** Told when a restart starts, and when it is over either way. */
  onRestarting?: (restarting: boolean) => void
  /** Once the Studio is back on the new folder; may give the folder now in use. */
  onChanged?: () => Promise<string | null | undefined> | void
}) {
  const f = useFolderListing(start)
  const { listing } = f
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState<{ tone: CalloutTone; text: string } | null>(null)
  const [restart, setRestart] = useState<'idle' | 'waiting' | 'back' | 'timeout'>('idle')
  const [changedTo, setChangedTo] = useState('')
  const [native, setNative] = useState(hasNativeFolderChooser)
  const alive = useAlive()

  const chooseNatively = async () => {
    setNote(null)
    let picked: string | null
    try {
      // Opened beside the folder in use, so its neighbors show.
      const from = current ? current.replace(/\/+[^/]+\/*$/, '') || '/' : start
      picked = await chooseFolderNatively({ start: from, message: 'Choose the folder for the Studio to index.', prompt: 'Use This Folder' })
    } catch {
      // The window could not open it; browse in the page instead.
      setNative(false)
      return
    }
    if (!alive.current || !picked) return
    if (picked === current) { setNote({ tone: 'info', text: 'This folder is already in use.' }); return }
    await choose(picked)
  }

  const choose = async (chosen: string) => {
    setBusy(true)
    setNote(null)
    try {
      const before = await json<{ startedAt?: string }>('/api/version').then(v => v.startedAt ?? '').catch(() => '')
      const r = await post<{ saved: boolean; restarting: boolean; note?: string }>('/api/setup/primary-folder', { path: chosen })
      if (r.restarting) {
        setRestart('waiting')
        onRestarting?.(true)
        const back = await waitForRestart(before, pollMs, () => alive.current)
        if (!alive.current) return
        onRestarting?.(false)
        if (!back) { setRestart('timeout'); return }
        setChangedTo(chosen)
        setRestart('back')
        const now = (await onChanged?.()) || chosen
        if (!alive.current) return
        setChangedTo(now)
        void f.browse(now)
      } else if (r.note) {
        setNote({ tone: r.saved ? 'info' : 'warning', text: r.note })
      }
    } catch (e) {
      if (alive.current) setNote({ tone: 'error', text: errText(e) })
    } finally {
      if (alive.current) setBusy(false)
    }
  }

  return (
    <>
      {restart === 'back' && <Callout tone="success">The Studio restarted and now uses {changedTo}.</Callout>}
      {restart === 'timeout' && <Callout tone="error">The Studio did not come back after the restart. Open it again from the app.</Callout>}
      {restart === 'waiting' ? (
        <Loader inline size={16} label="Restarting the Studio with the new folder…" />
      ) : native ? (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <div style={row}>
            <Button variant="secondary" icon={FOLDER_ICON} loading={busy} disabled={disabled} onClick={() => void chooseNatively()}>
              Choose another folder…
            </Button>
          </div>
          {note && <Callout tone={note.tone} live>{note.text}</Callout>}
        </div>
      ) : (
        <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
          <FolderList f={f} />
          <div style={row}>
            <Button variant="primary" loading={busy}
              disabled={!listing || !listing.writable || listing.path === current || disabled}
              onClick={() => listing && void choose(listing.path)}>
              Use this folder
            </Button>
            {listing && !listing.writable && <span className="muted">This folder is not writable.</span>}
          </div>
          {note && <Callout tone={note.tone} live>{note.text}</Callout>}
        </div>
      )}
    </>
  )
}

/** Builds the primary folder's whole index and follows the job. */
export function useIndexBuild({ pollMs = 1000, onDone }: { pollMs?: number; onDone?: () => unknown } = {}) {
  const [job, setJob] = useState<IndexJob | null>(null)
  const [error, setError] = useState('')
  const alive = useAlive()
  const done = useRef(onDone)
  useEffect(() => { done.current = onDone })

  const follow = async (first: IndexJob) => {
    let j = first
    setJob(j)
    while (alive.current && (j.state === 'queued' || j.state === 'running')) {
      await sleep(pollMs)
      const res = await fetch(`/api/index/job/${j.id}`).catch(() => null)
      if (res?.status === 404) {
        setJob(null)
        setError('The index build was interrupted. Start it again.')
        return
      }
      if (res?.ok) {
        j = await res.json() as IndexJob
        if (alive.current) setJob(j)
      }
    }
    if (j.state === 'done' && alive.current) await done.current?.()
  }

  const start = async () => {
    setError('')
    try {
      const job = await post<IndexJob>('/api/setup/index')
      // The footer and the other views follow the same job.
      kickIndexStatus()
      void follow(job)
    } catch (e) {
      setError(errText(e))
    }
  }

  const reset = () => { setJob(null); setError('') }
  const building = !!job && (job.state === 'queued' || job.state === 'running')
  return { job, error, building, start, follow, reset }
}

/** A build's progress while it runs, phase by phase, then how it ended. A
 *  job without phases (from an older server) shows its one count. */
export function IndexBuildProgress({ job, error }: { job: IndexJob | null; error: string }) {
  const building = !!job && (job.state === 'queued' || job.state === 'running')
  const phased = !!job?.phases?.length
  const progress = job?.progress
  const failed = progress?.failed ?? []
  const detail = !progress ? 'Starting'
    : progress.phase === 'files' ? 'Files at the top of the folder'
    : progress.phase === 'directories' ? `${progress.current ?? ''} (${progress.done} of ${progress.total})`
    : 'Finishing'
  return (
    <>
      {building && (phased
        ? <IndexJobView job={job} />
        : <ProgressBar label="Building the index" value={progress ? progress.done : null} max={progress?.total || 1} detail={detail} />)}
      {job?.state === 'done' && (
        <Callout tone="success">{phased ? jobOutcome(job) : `Index built${job.ms !== null ? ` in ${Math.max(1, Math.round(job.ms / 1000))} s` : ''}.`}</Callout>
      )}
      {job?.state === 'done' && (failed.length > 0 || (job.warningCount ?? 0) > 0) && (
        <Callout tone="warning" title={failed.length ? 'Some folders could not be indexed' : 'Some files could not be read'}>
          {failed.map(f => <div key={f}>{f}</div>)}
          {(job.warningCount ?? 0) > 0 && <div>{job.warningCount!.toLocaleString()} files could not be read; the index log names them.</div>}
        </Callout>
      )}
      {job?.state === 'cancelled' && <Callout tone="info">{jobOutcome(job)}</Callout>}
      {job?.state === 'error' && <Callout tone="error" title="The index build failed">{job.error}</Callout>}
      {error && <Callout tone="error">{error}</Callout>}
    </>
  )
}
