import { useEffect, useRef } from 'react'

/**
 * What the Studio's pages keep on the screen, read in one place.
 *
 * A part of the page that shows something which changes watches its path,
 * with how often it is worth reading. Watched once however many show it,
 * a path is read at the fastest pace asked, and each watcher is given every
 * reading, as if it read for itself.
 *
 * The readings come over one WebSocket (GET /api/live,
 * server-go/internal/live): the server reads each path at its pace, as the
 * person, and sends what changed, so an open page makes no requests while
 * nothing happens. Until the socket is open, and where none can open, the
 * page reads the paths itself at the same pace; after the socket has failed
 * a few times, those due together go in one request (/api/live/read).
 *
 * Nothing is read while the tab is hidden. When it shows again everything
 * watched is read at once.
 */

/** A reading that failed; status 0 when nothing answered. */
export interface LiveError extends Error { status: number; body?: unknown }
interface Sub { every: number; on: (value: unknown) => void; error?: (e: LiveError) => void }
interface Reading { status: number; text: string }
interface Entry {
  path: string
  subs: Set<Sub>
  /** When its last reading began, 0 for never. */
  at: number
  last: Reading | null
  /** The last one that came over this socket. */
  sent: Reading | null
  /** Numbers its readings as they begin; one that ends after a later one's
   *  answer was taken is older news. */
  begun: number
  taken: number
  timer: ReturnType<typeof setTimeout> | null
  /** Requests for it under way. */
  reading: number
  /** Reads asked for over the socket, by number. */
  asked: Map<number, { done: (r: Reading) => void; fail: (e: Error) => void }>
}

/** The tab left hidden this long closes the socket; a glance at another tab
 *  does not. */
const HIDDEN_FOR_MS = 10_000
/** A socket that says nothing this long (its heartbeat is 15 s) is gone. */
const SILENT_MS = 40_000
/** A read asked for over the socket that takes longer goes as a request. */
const ASK_MS = 8_000
/** Paths due within this of one another go in one request. */
const TOGETHER_MS = 2_000
/** A path read less often than this goes by itself: some of those ask the
 *  platform, and the others do not wait for it. */
const SLOW_MS = 60_000
const RETRY_MS = 1_000
const RETRY_MAX_MS = 60_000
/** One that was open this long was not refused: the next is tried at once. */
const LASTED_MS = 20_000
/** Where none has ever opened. */
const RETRY_NEVER_MS = 300_000

const entries = new Map<string, Entry>()
let opener: ((path: string) => WebSocket) | null = null
let socket: WebSocket | null = null
/** The socket is open: the paths are read over it. */
let open = false
let everOpen = false
let fails = 0
let retry: ReturnType<typeof setTimeout> | null = null
let silent: ReturnType<typeof setInterval> | null = null
let heardAt = 0
let openedAt = 0
let leaving: ReturnType<typeof setTimeout> | null = null
let telling = false
let wanted = ''
let asks = 0
/** The server answers the combined read. */
let together = true

const hidden = () => document.visibilityState === 'hidden'
const pace = (e: Entry) => Math.min(...[...e.subs].map(s => s.every))

const failure = (path: string, status: number, text: string): LiveError => {
  let body: unknown
  try { body = JSON.parse(text) } catch { /* not JSON */ }
  return Object.assign(new Error((body as { error?: string } | undefined)?.error || `${path}: ${status}`), { status, body })
}

/** Gives a reading to everyone who shows the path. */
function take(e: Entry, n: number, r: Reading): void {
  if (n < e.taken) return
  e.taken = n
  e.last = r
  for (const s of [...e.subs]) give(e, s, r)
}
function give(e: Entry, s: Sub, r: Reading): void {
  if (!e.subs.has(s)) return
  if (r.status < 200 || r.status > 299) { s.error?.(failure(e.path, r.status, r.text)); return }
  let value: unknown
  try { value = JSON.parse(r.text) } catch { return }
  s.on(value)
}

function entry(path: string): Entry {
  let e = entries.get(path)
  if (!e) {
    e = { path, subs: new Set(), at: 0, last: null, sent: null, begun: 0, taken: 0, timer: null, reading: 0, asked: new Map() }
    entries.set(path, e)
  }
  return e
}

// ---- by request ----

function request(e: Entry): Promise<Reading> {
  e.at = Date.now()
  e.reading++
  const n = ++e.begun
  const run = (async () => {
    const res = await fetch(e.path)
    const r = { status: res.status, text: await res.text() }
    take(e, n, r)
    return r
  })().finally(() => { e.reading--; schedule(e) })
  run.catch(err => {
    const said = Object.assign(err instanceof Error ? err : new Error(String(err)), { status: 0 }) as LiveError
    for (const s of [...e.subs]) s.error?.(said)
  })
  return run
}

/** Those due together, in one request. */
async function requestTogether(list: Entry[]): Promise<void> {
  const ns = new Map<Entry, number>()
  for (const e of list) { e.at = Date.now(); e.reading++; ns.set(e, ++e.begun) }
  let reads: { path: string; status: number; body: unknown }[] | null = null
  try {
    const res = await fetch(`/api/live/read?${list.map(e => `p=${encodeURIComponent(e.path)}`).join('&')}`)
    if (res.ok) reads = (await res.json() as { reads?: { path: string; status: number; body: unknown }[] }).reads ?? null
    // A server without it: each by itself from now on.
    if (!reads && (res.ok || res.status === 404 || res.status === 400)) together = false
  } catch { /* the next one */ }
  for (const e of list) {
    const r = reads?.find(x => x.path === e.path)
    if (r) take(e, ns.get(e)!, { status: r.status, text: JSON.stringify(r.body) })
    e.reading--
    if (together) schedule(e)
    else if (e.subs.size) void request(e).catch(() => {})
  }
}

/** Reads a path when it is next due, while no socket does. */
function schedule(e: Entry): void {
  if (e.timer) { clearTimeout(e.timer); e.timer = null }
  if (!e.subs.size || e.reading || open || hidden()) return
  e.timer = setTimeout(() => { e.timer = null; due(e) }, Math.max(0, e.at + pace(e) - Date.now()))
}

function due(e: Entry): void {
  if (!e.subs.size || open || hidden()) return
  if (!together || !opener || everOpen || fails < 3 || pace(e) >= SLOW_MS) { void request(e).catch(() => {}); return }
  const now = Date.now()
  const list = [...entries.values()].filter(x => x === e || (x.subs.size && !x.reading && pace(x) < SLOW_MS && x.at + pace(x) - now <= TOGETHER_MS))
  if (list.length < 2) { void request(e).catch(() => {}); return }
  for (const x of list) if (x.timer) { clearTimeout(x.timer); x.timer = null }
  void requestTogether(list)
}

// ---- by socket ----

/** Tells the server what is shown now, once for several changes. */
function tell(): void {
  if (telling || !open) return
  telling = true
  queueMicrotask(() => {
    telling = false
    if (!open) return
    const shown = [...entries.values()].filter(e => e.subs.size)
    const key = shown.map(e => `${e.path} ${pace(e)}`).join()
    if (key === wanted) return
    wanted = key
    // Each with how long until it is due, from what the page has read.
    const want = Object.fromEntries(shown.map(e => [e.path, [pace(e), e.at ? Math.max(0, e.at + pace(e) - Date.now()) : 0]]))
    socket?.send(JSON.stringify({ want }))
  })
}

/** Looked at by the clock: a computer that slept finds it out on waking. */
function heard(): void {
  heardAt = Date.now()
  silent ??= setInterval(() => {
    if (Date.now() - heardAt <= SILENT_MS) return
    socket?.close()
    lost()
  }, 5_000)
}

function connect(): void {
  if (retry) { clearTimeout(retry); retry = null }
  if (!opener || socket || hidden() || ![...entries.values()].some(e => e.subs.size)) return
  let ws: WebSocket
  try { ws = opener('/api/live') } catch { return }
  socket = ws
  ws.onopen = () => {
    if (socket !== ws) return
    open = everOpen = true
    wanted = ''
    openedAt = Date.now()
    for (const e of entries.values()) {
      e.sent = null
      if (e.timer) { clearTimeout(e.timer); e.timer = null }
    }
    heard()
    tell()
  }
  ws.onmessage = m => {
    if (socket !== ws) return
    heard()
    let f: { path?: string; status?: number; body?: unknown; same?: string[]; n?: number[] }
    try { f = JSON.parse(String(m.data)) as typeof f } catch { return }
    for (const p of f.same ?? []) got(entries.get(p), null, [])
    if (f.path) got(entries.get(f.path), { status: f.status ?? 0, text: JSON.stringify(f.body ?? null) }, f.n ?? [])
  }
  ws.onclose = () => { if (socket === ws) lost() }
  ws.onerror = () => { /* it closes */ }
}

/** A reading that came over the socket; null says what the last one said. */
function got(e: Entry | undefined, r: Reading | null, asked: number[]): void {
  if (!e) return
  r ??= e.sent
  if (!r) return
  e.at = Date.now()
  e.sent = r
  // A read made by request before this one came is older news.
  take(e, ++e.begun, r)
  for (const n of asked) { e.asked.get(n)?.done(r); e.asked.delete(n) }
}

function lost(): void {
  const was = socket
  if (open && Date.now() - openedAt >= LASTED_MS) fails = 0
  socket = null
  open = false
  if (was) { was.onclose = null; was.onmessage = null }
  if (silent) { clearInterval(silent); silent = null }
  for (const e of entries.values()) {
    for (const a of e.asked.values()) a.fail(new Error('closed'))
    e.asked.clear()
    schedule(e)
  }
  if (!opener || hidden()) return
  fails++
  retry = setTimeout(connect, !everOpen && fails > 3 ? RETRY_NEVER_MS : Math.min(RETRY_MS * 2 ** (fails - 1), RETRY_MAX_MS))
}

function leave(): void {
  if (retry) { clearTimeout(retry); retry = null }
  if (silent) { clearInterval(silent); silent = null }
  const was = socket
  socket = null
  open = false
  if (was) { was.onclose = null; was.onmessage = null; was.close() }
  for (const e of entries.values()) {
    if (e.timer) { clearTimeout(e.timer); e.timer = null }
    for (const a of e.asked.values()) a.fail(new Error('closed'))
    e.asked.clear()
  }
}

/** Shown again: everything is read now, over the socket when it is still
 *  there, by request while another opens. */
function onVisibility(): void {
  if (hidden()) {
    leaving ??= setTimeout(() => { leaving = null; if (hidden()) leave() }, opener ? HIDDEN_FOR_MS : 0)
    return
  }
  if (leaving) { clearTimeout(leaving); leaving = null }
  for (const e of entries.values()) {
    if (!e.subs.size) continue
    if (open && Date.now() - heardAt <= SILENT_MS) socket?.send(JSON.stringify({ read: e.path }))
    else void request(e).catch(() => {})
  }
  connect()
}

let listening = false
function listen(): void {
  if (listening) return
  listening = true
  document.addEventListener('visibilitychange', onVisibility)
}

// ---- what the pages use ----

export interface Watch {
  /** How often from now on. */
  every: (ms: number) => void
  stop: () => void
}

/**
 * Follows a path: `on` gets each reading, the first at once. What another
 * part of the page follows already is given as last read; with `keep`, so
 * is what was read less than `every` ago, and it is read when next due.
 */
export function watch<T>(path: string, every: number, on: (value: T) => void, error?: (e: LiveError) => void, keep = false): Watch {
  listen()
  const e = entry(path)
  const s: Sub = { every, on: on as (v: unknown) => void, error }
  const shared = e.subs.size > 0 || (keep && Date.now() - e.at < every)
  e.subs.add(s)
  if (shared && e.last) {
    const r = e.last
    queueMicrotask(() => give(e, s, r))
  } else if (!e.reading) e.at = 0
  if (open) tell()
  else if (!hidden()) {
    if (!e.at) void request(e).catch(() => {})
    else schedule(e)
    connect()
  }
  return {
    every: ms => {
      if (s.every === ms || !e.subs.has(s)) return
      s.every = ms
      if (open) tell()
      else schedule(e)
    },
    stop: () => {
      if (!e.subs.delete(s)) return
      if (open) tell()
      else schedule(e)
    },
  }
}

/** Reads a path now, for those who watch it as well; its answer, or its
 *  refusal as an error with the status. */
export async function readNow<T>(path: string): Promise<T> {
  const e = entry(path)
  let r: Reading | null = null
  if (open && e.subs.size) {
    tell()
    const n = ++asks
    const asked = new Promise<Reading>((done, fail) => { e.asked.set(n, { done, fail }) })
    queueMicrotask(() => { if (open) socket?.send(JSON.stringify({ read: path, n })) })
    let timer: ReturnType<typeof setTimeout> | undefined
    const late = new Promise<null>(f => { timer = setTimeout(() => f(null), ASK_MS) })
    r = await Promise.race([asked, late]).catch(() => null)
    clearTimeout(timer)
    e.asked.delete(n)
  }
  r ??= await request(e)
  if (r.status < 200 || r.status > 299) throw failure(path, r.status, r.text)
  return JSON.parse(r.text) as T
}

/** Watches a path while `on`; the callback may change from one render to
 *  the next. */
export function useWatch<T>(path: string, every: number, on: boolean, fn: (value: T) => void, error?: (e: LiveError) => void, keep = false): void {
  const cb = useRef({ fn, error })
  cb.current = { fn, error }
  useEffect(() => {
    if (!on) return
    const w = watch<T>(path, every, v => cb.current.fn(v), e => cb.current.error?.(e), keep)
    return w.stop
  }, [path, every, on, keep])
}

/** Runs `fn` after `ms`, or when the tab next shows if it is hidden then. */
export function shownTimeout(fn: () => void, ms: number): () => void {
  const show = () => { if (!hidden()) { stop(); fn() } }
  const t = setTimeout(() => {
    if (hidden()) document.addEventListener('visibilitychange', show)
    else fn()
  }, ms)
  const stop = () => { clearTimeout(t); document.removeEventListener('visibilitychange', show) }
  return stop
}

/** Runs `fn` every `ms` while the tab shows, and at once when it shows again
 *  after longer. */
export function shownInterval(fn: () => void, ms: number): () => void {
  let stop = () => {}
  const next = () => { stop = shownTimeout(() => { next(); fn() }, ms) }
  next()
  return () => stop()
}

/** The socket's address on this Studio. */
export const liveURL = (path: string, origin = location.origin) => origin.replace(/^http/, 'ws') + path

/** Lets what is watched come over the socket, for as long as the app is
 *  open; the app calls it once. */
export function startLive(open: (path: string) => WebSocket = path => new WebSocket(liveURL(path))): () => void {
  listen()
  opener = open
  connect()
  return () => { opener = null; leave(); for (const e of entries.values()) schedule(e) }
}

/** Tests start with nothing watched and no socket. */
export function resetLiveForTests(): void {
  opener = null
  leave()
  if (leaving) { clearTimeout(leaving); leaving = null }
  entries.clear()
  everOpen = false
  fails = 0
  together = true
  wanted = ''
}
