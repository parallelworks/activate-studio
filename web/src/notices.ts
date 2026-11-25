import { useEffect, useSyncExternalStore } from 'react'
import type { PwCodeMachine, PwCodeSession } from '@parallelworks/code'
import type { TransferSummary } from './transferApi'
import { waitsOnYou } from './code/waiting'
import { watch, type Watch } from './live'

/**
 * What needs the person across the Studio, in one place: the menu's marks,
 * the chat list's marks and the Notices page all read this, and it carries
 * the Code page's listing, so they cannot disagree.
 *
 * It is followed at 30 seconds while the tab shows (live.ts), and read at
 * once when something in the page says it changed: a reply ended, pw code
 * asked or was answered, the Code page's own listing shows another session
 * waiting, or another tab of this browser said so. While a chat is open in
 * front of the person, a reply in it is said to be seen as it shows up.
 */

export interface Notice {
  id: string
  kind: 'chat' | 'code' | 'fleet' | 'delegated' | 'transfer' | 'library'
  what: 'waiting' | 'reply' | 'note' | 'finished' | 'failed'
  title: string
  text: string
  at: string
  href: string
  conversation?: string
  session?: string
  /** The menu entries that count it. */
  nav: string[]
}

/** The state of one of the person's chats, for its row in the list. */
export interface ChatNotice {
  session?: string
  /** That session's machine, as the chat knows it. */
  machine?: string
  machineName?: string
  /** The session is on the Studio's own computer. */
  local?: boolean
  /** Its session where the listing does not have it. */
  row?: PwCodeSession
  working?: boolean
  waiting?: boolean
  unread?: 'reply' | 'note'
}

export interface Notices {
  items: Notice[]
  chats: Record<string, ChatNotice>
  /** The Code page's listing could be read. */
  listed: boolean
  machines: PwCodeMachine[]
  sessions: PwCodeSession[]
  /** The Transfers entry's summary, as of the last read. */
  transfers?: TransferSummary
  /** Read at least once. */
  loaded: boolean
}

const NONE: Notices = { items: [], chats: {}, listed: false, machines: [], sessions: [], loaded: false }
/** How often the notices are followed while the tab shows. */
export const NOTICES_EVERY_MS = 30_000
/** How long after something happens its read goes, so several in a row are
 *  one request, and the server has stored what happened. */
export const NOTICES_SOON_MS = 400
/** A reading that comes this soon after the page's own may be from before it. */
const SETTLE_MS = 3_000
const CHANNEL = 'studio-notices'

let state = NONE
let shown = NONE
const listeners = new Set<() => void>()
let enabled = false
/** The chat open on the page, and whether the Transfers page is. */
let front: string | null = null
let onTransfers = false
/** Replies being written in this page, which it knows before a read. */
const writing = new Set<string>()
let following: Watch | null = null
let soon: ReturnType<typeof setTimeout> | null = null
let soonAt = 0
let soonTell = false
let reads = 0
/** Requests of the page's own under way, and when the last one ended. */
let sending = 0
let sentAt = 0
let channel: BroadcastChannel | null = null
/** Chats the person left since the last read, to say seen with the next. */
let left: string[] = []

const visible = () => document.visibilityState === 'visible'

/** The chat in front of the person: open, on the page, in a tab that shows. */
const inFront = () => (visible() ? front : null)

/** The notices as the page shows them: without those of the chat in front
 *  of the person, which they are dealing with, nor a failed transfer's
 *  while the Transfers page shows it. */
function compute(): Notices {
  const here = inFront()
  const items = state.items.filter(i => !(here && i.conversation === here) && !(onTransfers && i.kind === 'transfer'))
  let chats = state.chats
  if (writing.size || (here && chats[here]?.unread)) {
    chats = { ...chats }
    for (const id of writing) chats[id] = { ...chats[id], working: true, unread: undefined }
    if (here && chats[here]?.unread) chats[here] = { ...chats[here], unread: undefined }
  }
  return { ...state, items, chats }
}

function changed() {
  shown = compute()
  for (const f of listeners) f()
}

function take(d: Partial<Notices> | null) {
  if (!d || !Array.isArray(d.items)) return
  state = { items: d.items, chats: d.chats ?? {}, listed: !!d.listed, machines: d.machines ?? [], sessions: d.sessions ?? [], transfers: d.transfers, loaded: true }
  changed()
}

async function send(path: string, body?: unknown, keepalive = false): Promise<void> {
  const n = ++reads
  sending++
  try {
    const res = await fetch(path, body === undefined ? undefined : {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body), keepalive,
    })
    const d = res.ok ? await res.json() as Partial<Notices> : null
    // A slower answer to an earlier request is older news.
    if (n === reads) {
      if (d) take(d)
      else if (state.loaded && state.listed) { state = { ...state, listed: false }; changed() }
    }
  } catch { /* the next read tries again */ } finally { sending--; sentAt = Date.now() }
}

/** What is followed, as it is read. */
function follow(d: Partial<Notices>): void {
  // The page's own requests mark things seen: what was read before that is
  // older news, and it is read again.
  if (sending || Date.now() - sentAt < SETTLE_MS) { if (!soon) refreshNotices({ tell: false, after: SETTLE_MS }); return }
  take(d)
  const here = inFront()
  if (here && d.chats?.[here]?.unread) void read()
}

/** Reads the notices now. The chat in front, and those just left, are seen. */
function read(): Promise<void> {
  if (soon) { clearTimeout(soon); soon = null }
  const tell = soonTell
  soonTell = false
  const seen = [...new Set([...left, ...(inFront() ? [front!] : [])])]
  left = []
  const done = seen.length ? send('/api/notices/seen', { conversations: seen }) : send('/api/notices')
  if (tell) void done.then(tellTabs)
  return done
}

/** Tells this browser's other tabs that something changed. */
function tellTabs() {
  try { channel?.postMessage('changed') } catch { /* closed */ }
}

/** Reads shortly, once for several calls, and tells the other tabs. */
export function refreshNotices({ tell = true, after = NOTICES_SOON_MS }: { tell?: boolean; after?: number } = {}): void {
  if (!enabled) return
  soonTell ||= tell
  // Several in a row are one read, after the last of them.
  const at = Date.now() + after
  if (soon && at <= soonAt) return
  if (soon) clearTimeout(soon)
  soonAt = at
  soon = setTimeout(() => {
    soon = null
    if (visible() || left.length) void read()
    // A hidden tab reads when it shows again; the others may read now.
    else if (soonTell) { soonTell = false; tellTabs() }
  }, after)
}

/** The chat the Chat page shows, or null for none or another page. */
export function setFrontChat(id: string | null): void {
  if (front === id) return
  if (front && enabled && visible()) left.push(front)
  front = id
  changed()
  if (enabled && (left.length || id)) refreshNotices({ after: 50 })
}

/** Whether the Transfers page shows: its failures are seen there. */
export function setTransfersShown(on: boolean): void {
  if (onTransfers === on) return
  onTransfers = on
  changed()
}

/** A reply in a chat is being written, or ended. */
export function setReplying(conversationId: string, on: boolean): void {
  if (writing.has(conversationId) === on) return
  if (on) writing.add(conversationId)
  else writing.delete(conversationId)
  changed()
}

const waitKey = (sessions: readonly PwCodeSession[]) => sessions
  .filter(waitsOnYou)
  .map(s => `${s.id}:${((s as { approvals?: { id: string }[] | null }).approvals ?? []).map(a => a.id).sort().join()}`).sort().join(' ')

/** The Code page's own listing, which it reads more often: what it shows
 *  waiting is what the menu shows. */
export function publishListing(d: { machines?: PwCodeMachine[] | null; sessions?: PwCodeSession[] | null } | undefined): void {
  if (!enabled || !d?.sessions) return
  const other = state.loaded && waitKey(d.sessions) !== waitKey(state.sessions)
  state = { ...state, machines: d.machines ?? [], sessions: d.sessions, listed: true }
  changed()
  if (other) refreshNotices({ tell: false })
}

/** A pw code session was opened: the end of delegated work there is seen. */
export function sessionSeen(session: string): void {
  if (state.items.some(i => i.session === session && i.what !== 'waiting' && i.kind === 'delegated')) void send('/api/notices/seen', { conversations: [], session }).then(tellTabs)
}

/** Takes notices off the list until something else happens there. */
export function dismissNotices(ids: string[]): Promise<void> {
  state = { ...state, items: state.items.filter(i => !ids.includes(i.id)) }
  changed()
  return send('/api/notices/dismiss', { ids }).then(tellTabs)
}

function onVisibility() {
  if (!visible()) {
    // What the open chat showed until now was seen; a reply from here on is not.
    if (front) { left.push(front); void read() }
    changed()
    return
  }
  changed()
}
const onChange = () => refreshNotices()
const EVENTS: [string, (e: Event) => void][] = [
  ['ade:chat-answered', onChange],
  ['ade-pwcode-approval', onChange],
  ['ade-pwcode-approval-resolved', onChange],
  ['ade:fleet-changed', onChange],
]

function start() {
  enabled = true
  following = watch<Partial<Notices>>('/api/notices', NOTICES_EVERY_MS, follow, () => {
    if (state.loaded && state.listed) { state = { ...state, listed: false }; changed() }
  })
  document.addEventListener('visibilitychange', onVisibility)
  for (const [e, f] of EVENTS) window.addEventListener(e, f)
  try {
    channel = typeof BroadcastChannel === 'undefined' ? null : new BroadcastChannel(CHANNEL)
    if (channel) channel.onmessage = () => { if (visible()) refreshNotices({ tell: false }) }
  } catch { channel = null }
}

function stop() {
  enabled = false
  following?.stop()
  following = null
  if (soon) clearTimeout(soon)
  soon = null
  sentAt = 0
  soonTell = false
  left = []
  document.removeEventListener('visibilitychange', onVisibility)
  for (const [e, f] of EVENTS) window.removeEventListener(e, f)
  channel?.close()
  channel = null
  writing.clear()
  reads++
  state = NONE
  changed()
}

/** Reads the notices while the app is open. */
export function useNoticesRead(on: boolean): void {
  useEffect(() => {
    if (!on) return
    start()
    return stop
  }, [on])
}

const subscribe = (f: () => void) => { listeners.add(f); return () => { listeners.delete(f) } }

export function useNotices(): Notices {
  return useSyncExternalStore(subscribe, () => shown)
}

/** The machines of the Code page's listing, for what names one (machines.ts);
 *  null while the listing cannot be read. */
export function useNoticeMachines(): { machines: PwCodeMachine[] | null; loaded: boolean } {
  const machines = useSyncExternalStore(subscribe, () => (shown.listed ? shown.machines : null))
  const loaded = useSyncExternalStore(subscribe, () => shown.loaded)
  return { machines, loaded }
}

/** How many notices a menu entry counts. */
export function countFor(n: Notices, entry: string): number {
  return n.items.filter(i => i.nav.includes(entry)).length
}
