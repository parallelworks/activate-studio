/**
 * A chat's record as GET /api/chat/conversations/{id} gives it, read once
 * however many parts of the chat page ask at the same moment (the chat
 * list's thread, the pw code session view, the engine menu), and kept for
 * the chats opened lately, so going back to one starts from what is known
 * at once while a fresh read goes on.
 *
 * Kept in this page's memory only, and only for the person it was read
 * for: a record read for one person is never handed to another. The
 * records go when someone else signs in or the person signs out of
 * ACTIVATE (clearViewerCaches), and a read that was on its way when they
 * went is not kept.
 */

export interface ChatRecord {
  id?: string
  pwcode?: Record<string, unknown> & { session?: string }
  messages?: unknown[]
  [key: string]: unknown
}

/** How many chats are kept. */
const KEPT = 30

const records = new Map<string, ChatRecord>()
const reads = new Map<string, Promise<ChatRecord>>()
let viewer: string | null = null

/** Says whose records these are: a different person than before clears
 *  what was kept for the one before. */
export function setRecordViewer(username: string): void {
  if (viewer !== null && viewer !== username) clearChatRecords()
  viewer = username
}

/** Forgets every kept record, and any read still on its way. */
export function clearChatRecords(): void {
  records.clear()
  reads.clear()
  ahead.clear()
}

/** The chat's record as last read for this person, if it is kept. */
export function cachedChat(id: string | null | undefined): ChatRecord | undefined {
  if (!id) return undefined
  const r = records.get(id)
  if (r) {
    // Most recently used last, so the oldest goes first.
    records.delete(id)
    records.set(id, r)
  }
  return r
}

/** Forgets one chat's record (it was deleted, or changed on the server). */
export function forgetChat(id: string): void {
  records.delete(id)
  reads.delete(id)
  ahead.delete(id)
}

// The chat changed on the server: a read on its way may be from before, so
// what is read for the change asks again. This listens first (the module
// loads before any part of the page that reads a chat).
function outdated(e: Event): void {
  const id = ((e as CustomEvent).detail as { conversationId?: string } | null)?.conversationId
  if (!id) return
  reads.delete(id)
  ahead.delete(id)
}
if (typeof window !== 'undefined') for (const name of ['ade-pwcode-session', 'ade:chat-answered']) window.addEventListener(name, outdated)

/**
 * Reads the chat's record: one read for every caller that asks while it is
 * on its way, unless the chat has changed since it began. It rejects as the chat list's own reads do, with the status
 * and what the server said.
 */
export function readChat(id: string): Promise<ChatRecord> {
  const going = reads.get(id)
  if (going) return going
  const read: Promise<ChatRecord> = fetch(`/api/chat/conversations/${encodeURIComponent(id)}`)
    .then(async r => {
      if (!r.ok) throw new Error(`${r.status} ${await r.text()}`)
      return r.json() as Promise<ChatRecord>
    })
    .then(rec => {
      // Only the newest read of it is kept, and for this person.
      if (reads.get(id) === read) {
        records.delete(id)
        records.set(id, rec)
        while (records.size > KEPT) records.delete(records.keys().next().value as string)
      }
      return rec
    })
    .finally(() => { if (reads.get(id) === read) reads.delete(id) })
  reads.set(id, read)
  return read
}

/** How long a read ahead stands for the open that follows it. */
const AHEAD_MS = 5000

// Reads ahead, each taken once by the open that follows.
const ahead = new Map<string, { at: number; read: Promise<ChatRecord> }>()

/** Starts reading a chat the person is about to open (a pointer resting on
 *  it in the list), unless that is already on its way. */
export function prefetchChat(id: string): void {
  const had = ahead.get(id)
  if (had && Date.now() - had.at < AHEAD_MS) return
  const read = readChat(id)
  read.catch(() => { /* the open reads it again and says why */ })
  ahead.set(id, { at: Date.now(), read })
}

/**
 * The read ahead of a chat being opened, when one started in the last few
 * seconds: the open uses it rather than asking again. Taken once, so every
 * later read (a reply being watched, a reload) asks the server.
 */
export function takeReadAhead(id: string): Promise<ChatRecord> | undefined {
  const had = ahead.get(id)
  ahead.delete(id)
  return had && Date.now() - had.at < AHEAD_MS ? had.read : undefined
}
