import { useSyncExternalStore } from 'react'
import { updatesApi, type UpdatePolicy, type UpdateState, type UpdatesStatus } from './api'

/**
 * The page's one view of updates: the status the server reports, a check,
 * install or rollback under way, and the wait for the new version after the
 * restart. The update notice, the footer and Settings, Updates all read it,
 * so an install started from any of them is followed once.
 *
 * An install ends with the server restarting on the new version. The page
 * then waits until /api/version reports that version and reloads, since its
 * own code is the old build. The status is read when the first reader
 * appears and every ten minutes after; with no readers left, everything is
 * forgotten.
 */

export interface Waiting {
  /** The version the page waits for; null takes any but `from`. */
  version: string | null
  from: string
  timedOut: boolean
}

export interface UpdateFlow {
  status: UpdatesStatus | null
  /** This Studio has no updates: a hosted deployment, or a server without them. */
  none: boolean
  /** The status could not be read, and why. */
  readError: string
  busy: 'check' | 'install' | 'rollback' | null
  /** The last check, install or rollback failed, and why. */
  error: string
  waiting: Waiting | null
}

/** The server's work on an install, which the page follows. */
export const UNDER_WAY = new Set<UpdateState>(['downloading', 'verifying', 'installing'])

/** An install or rollback is under way, or the page waits for its restart. */
export const underWay = (f: UpdateFlow): boolean =>
  !!f.waiting || (!!f.status && (UNDER_WAY.has(f.status.state) || f.status.state === 'restarting'))

// A release as both sides report it: no leading v, and no git describe
// suffix (v1.140-1-gabc1234 is the v1.140 release built a commit later).
export const bare = (v: string) => v.trim().replace(/^v/i, '').replace(/-\d+-g[0-9a-f]+$/i, '')

function parts(v: string): number[] | null {
  const m = /^v?(\d+)\.(\d+)(?:\.(\d+))?/i.exec(String(v ?? '').trim())
  return m ? [Number(m[1]), Number(m[2]), Number(m[3] ?? 0)] : null
}

/** Whether `a` is a release newer than `b`; a version of no known form
 *  (dev) is newer than nothing and older than every release. */
export function isNewer(a: string, b: string): boolean {
  const x = parts(a)
  if (!x) return false
  const y = parts(b) ?? [0, 0, 0]
  for (let i = 0; i < 3; i++) if (x[i] !== y[i]) return x[i] > y[i]
  return false
}

export const isVersion = (v: string) => parts(v) !== null

const DEFAULTS = { pollMs: 1500, giveUpMs: 180_000, refreshMs: 10 * 60_000, reload: () => location.reload() }
const options = { ...DEFAULTS }

/** Reloads the page, onto the version now running. */
export const reloadPage = () => options.reload()

/** Timings and the reload, for tests. Reset when the last reader goes. */
export function setUpdateFlowOptions(o: Partial<typeof DEFAULTS>): void {
  for (const [k, v] of Object.entries(o)) if (v !== undefined) Object.assign(options, { [k]: v })
}

const EMPTY: UpdateFlow = { status: null, none: false, readError: '', busy: null, error: '', waiting: null }
let flow: UpdateFlow = EMPTY
const listeners = new Set<() => void>()
/** Bumped on reset, so an answer to a forgotten request is dropped. */
let gen = 0
/** The version an install or rollback is going to. */
let target: string | null = null
/** The install or rollback was started on this page, not by the server. */
let asked = false
let lastState: UpdateState = 'idle'
let follow: { timer: number; from: string } | null = null
let refresh: number | null = null
let waitRun: object | null = null

const sleep = (ms: number) => new Promise(r => setTimeout(r, ms))
const errText = (e: unknown) => String((e as Error)?.message ?? e)

function set(patch: Partial<UpdateFlow>): void {
  flow = { ...flow, ...patch }
  for (const fn of [...listeners]) fn()
  settle()
}

/** Set before the reload after an install or rollback the person started
 *  on this page: the version it went to. The page that loads takes it to
 *  reopen the app's window, when the update changed it (UpdatedNotice). */
const REOPEN_KEY = 'ade-reopen-after-update'

function reloadOnNewVersion(): void {
  if (asked && target) { try { sessionStorage.setItem(REOPEN_KEY, target) } catch { /* reopened by hand */ } }
  options.reload()
}

/** Whether this page loaded after an install or rollback of `version` that
 *  the person started; true once only. */
export function takeReopenRequest(version: string): boolean {
  let v: string | null = null
  try {
    v = sessionStorage.getItem(REOPEN_KEY)
    sessionStorage.removeItem(REOPEN_KEY)
  } catch { return false }
  return !!v && bare(v) === bare(version)
}

function waitFor(version: string | null, from: string): void {
  if (!flow.waiting) set({ waiting: { version, from, timedOut: false } })
}

function apply(s: UpdatesStatus): void {
  lastState = s.state
  set({ status: s, readError: '' })
  // A restart quick enough to fall between two reads: the server already
  // runs the version being installed.
  if (target && s.state === 'idle' && bare(s.current.version) === bare(target)) { reloadOnNewVersion(); return }
  // An install the server started on its own is followed the same way.
  if (!target && s.latest && (UNDER_WAY.has(s.state) || s.state === 'restarting')) target = s.latest.version
  if (s.state === 'restarting') waitFor(target, s.current.version)
}

/** Starts or stops following an install, and waiting for the restart, as
 *  the state asks. Once the download is in hand, the server not answering
 *  means it is restarting; during the download it is taken for a passing
 *  failure and read again. */
function settle(): void {
  const following = !!flow.status && UNDER_WAY.has(flow.status.state) && !flow.waiting
  if (following && !follow) {
    const my = gen
    const from = flow.status!.current.version
    const timer = window.setInterval(() => {
      updatesApi.status()
        .then(s => { if (my === gen && follow?.timer === timer && s) apply(s) })
        .catch(() => { if (my === gen && follow?.timer === timer && lastState !== 'downloading') waitFor(target, from) })
    }, options.pollMs)
    follow = { timer, from }
  } else if (!following && follow) {
    window.clearInterval(follow.timer)
    follow = null
  }
  if (!flow.waiting || flow.waiting.timedOut) { waitRun = null; return }
  if (waitRun) return
  const run = {}
  waitRun = run
  const w = flow.waiting
  const until = Date.now() + options.giveUpMs
  const arrived = (v: string) => (w.version ? bare(v) === bare(w.version) : bare(v) !== bare(w.from))
  void (async () => {
    while (waitRun === run && Date.now() < until) {
      await sleep(options.pollMs)
      if (waitRun !== run) return
      try {
        const res = await fetch('/api/version')
        const b = res.ok ? await res.json() as { version?: string } : null
        if (waitRun === run && b?.version && arrived(b.version)) { reloadOnNewVersion(); return }
      } catch { /* still restarting */ }
    }
    if (waitRun === run) { waitRun = null; set({ waiting: { ...w, timedOut: true } }) }
  })()
}

function read(first: boolean): void {
  const my = gen
  updatesApi.status()
    .then(s => {
      if (my !== gen) return
      if (s && s.mode !== 'hosted') apply(s)
      else set({ none: true })
    })
    .catch(e => { if (my === gen && first) set({ readError: errText(e) }) })
}

function start(): void {
  read(true)
  refresh = window.setInterval(() => {
    if (!flow.none && !flow.busy && !follow && !flow.waiting) read(false)
  }, options.refreshMs)
}

function reset(): void {
  gen++
  if (follow) window.clearInterval(follow.timer)
  if (refresh !== null) window.clearInterval(refresh)
  follow = null
  refresh = null
  waitRun = null
  target = null
  asked = false
  lastState = 'idle'
  flow = EMPTY
  Object.assign(options, DEFAULTS)
}

function subscribe(fn: () => void): () => void {
  listeners.add(fn)
  if (listeners.size === 1) start()
  return () => {
    listeners.delete(fn)
    if (listeners.size === 0) reset()
  }
}

const snapshot = () => flow

export function useUpdateFlow(): UpdateFlow {
  return useSyncExternalStore(subscribe, snapshot, snapshot)
}

async function act(kind: 'check' | 'install' | 'rollback', call: () => Promise<UpdatesStatus>): Promise<boolean> {
  set({ busy: kind, error: '' })
  try {
    apply(await call())
    return true
  } catch (e) {
    if (kind !== 'check') { target = null; asked = false }
    set({ error: errText(e) })
    return false
  } finally {
    set({ busy: null })
  }
}

export const updateActions = {
  check: () => act('check', updatesApi.check),
  install: () => {
    target = flow.status?.latest?.version ?? null
    asked = true
    return act('install', updatesApi.install)
  },
  /** A rollback restarts at once, whatever state the answer reports. */
  rollback: async () => {
    const from = flow.status?.current.version ?? ''
    const to = flow.status?.previous?.version ?? null
    target = to
    asked = true
    if (await act('rollback', updatesApi.rollback)) waitFor(to, from)
  },
  /** Shows a policy at once, before or after it is saved. */
  showPolicy: (policy: UpdatePolicy) => {
    if (flow.status) set({ status: { ...flow.status, policy } })
  },
}
