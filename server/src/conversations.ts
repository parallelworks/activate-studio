import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import crypto from 'node:crypto'
import type { FastifyInstance } from 'fastify'
import { INDEX_BASE, KB_ROOT } from './config.js'
import { KbError } from './kb.js'
import { applyTagsCore } from './tags.js'
import { reindexForDir } from './indexing.js'
import { effectiveSettings } from './settings.js'
import { authEnabled } from './auth.js'

/**
 * Server-side conversation store, one JSON file beside the index. Browser
 * localStorage proved unreliable for this (the platform embeds the app in an
 * iframe, where storage is partitioned per top-level site and may be evicted),
 * so history lives with the deployment and follows the user across browsers.
 */

const FILE = path.join(INDEX_BASE, 'conversations.json')
const MAX_CONVERSATIONS = 500

/** Conversations are also exported into the knowledge base as markdown, so
 *  past sessions are searchable and the assistant has memory of them. The
 *  directory carries the chat-session label; files inherit it. */
const EXPORT_DIR = 'chat-sessions'
let exportDirLabeled = false
const ownerLabeled = new Set<string>()
let indexTimer: NodeJS.Timeout | null = null

function exportPath(c: StoredConversation): string {
  return path.join(KB_ROOT, EXPORT_DIR, `${c.createdAt.slice(0, 10)}-${c.id.slice(0, 8)}.md`)
}

async function exportToKb(c: StoredConversation): Promise<void> {
  try {
    await fsp.mkdir(path.join(KB_ROOT, EXPORT_DIR), { recursive: true })
    if (!exportDirLabeled) {
      exportDirLabeled = true
      await applyTagsCore([EXPORT_DIR], ['chat-session'], []).catch(() => { exportDirLabeled = false })
    }
    const firstUser = c.messages.find(m => m.role === 'user')?.content ?? ''
    const lines = [
      `# ${c.title ?? (firstUser ? firstUser.slice(0, 70) : 'Untitled conversation')}`,
      '',
      `Chat session ${c.id}, started ${c.createdAt.slice(0, 16).replace('T', ' ')}${c.owner ? ` by ${c.owner}` : ''}.`,
      '',
    ]
    for (const m of c.messages) {
      if (m.role !== 'user' && m.role !== 'assistant') continue
      lines.push(`## ${m.role === 'user' ? 'User' : `Assistant${m.model ? ` (${m.model})` : ''}`}`)
      lines.push('')
      lines.push((m.content ?? '').trim() || '(empty)')
      lines.push('')
    }
    await fsp.writeFile(exportPath(c), lines.join('\n'))
    // Owner label on the transcript, so Search, Query, and chat Scope can
    // filter sessions by user like any other label. Once per file per boot.
    if (c.owner && !ownerLabeled.has(c.id)) {
      ownerLabeled.add(c.id)
      const rel = path.relative(KB_ROOT, exportPath(c))
      const tag = `user-${c.owner.toLowerCase().replace(/[^a-z0-9_-]+/g, '-')}`
      await applyTagsCore([rel], [tag], []).catch(() => { ownerLabeled.delete(c.id) })
    }
    // Debounced re-index: bursts of turns cost one pass. A failure here is
    // recoverable (the sweep retries the dir) but must not be invisible.
    if (indexTimer) clearTimeout(indexTimer)
    indexTimer = setTimeout(() => {
      void reindexForDir(EXPORT_DIR).catch(err => {
        console.error(`chat-session export reindex failed (sweep will retry): ${String(err?.message ?? err)}`)
      })
    }, 5_000)
  } catch { /* export is best-effort; the JSON store is the source of truth */ }
}

/** Tool call made during the turn, in ai-chat's ToolCallPart shape; the
 *  package renders these as tool blocks above the message content. */
export interface StoredToolCallPart {
  kind: 'tool_call'
  id: string
  name: string
  args: string
  status: 'ok' | 'error'
  result?: string
}

export interface StoredMessage {
  id: string
  role: string
  content: string | null
  parentId?: string | null
  timestamp: string
  model?: string | null
  reasoning?: string
  /** Milliseconds spent thinking before the first answer token; ai-chat
   *  renders it as "Thought for Ns" on the stored message. */
  reasoningDuration?: number
  parts?: StoredToolCallPart[]
}

export interface StoredConversation {
  id: string
  title: string | null
  /** Verified username of whoever started the conversation; null on
   *  standalone deployments and for chats predating ownership. */
  owner?: string | null
  createdAt: string
  updatedAt: string
  activeBranchId: string | null
  messages: StoredMessage[]
}

let cache: StoredConversation[] | null = null
let writing: Promise<void> = Promise.resolve()

function load(): StoredConversation[] {
  if (cache) return cache
  try { cache = JSON.parse(fs.readFileSync(FILE, 'utf8')) } catch { cache = [] }
  return cache!
}

function persist(): void {
  const snapshot = JSON.stringify(load(), null, 1)
  writing = writing.then(() => fsp.writeFile(FILE, snapshot)).catch(() => {})
}

const summary = (c: StoredConversation) => ({
  id: c.id,
  ...(c.title !== null ? { title: c.title } : {}),
  owner: c.owner ?? null,
  messageCount: c.messages.length,
  createdAt: c.createdAt,
  updatedAt: c.updatedAt,
  isOwner: true,
  canCollaborate: true,
})

function find(id: string): StoredConversation {
  const c = load().find(x => x.id === id)
  if (!c) throw new KbError(404, 'conversation not found')
  return c
}

/** With shared history off, an owned conversation exists only for its
 *  owner; unowned (legacy, standalone) conversations stay visible to all.
 *  UI-level only: the exported transcripts remain in the knowledge base. */
function visibleTo(c: StoredConversation, username: string | null | undefined): boolean {
  if (effectiveSettings().chatSharedHistory || !authEnabled()) return true
  if (!c.owner) return true
  return !!username && c.owner === username
}

function findVisible(id: string, username: string | null | undefined): StoredConversation {
  const c = find(id)
  if (!visibleTo(c, username)) throw new KbError(404, 'conversation not found')
  return c
}

/** Record the user turn at stream start; tolerates an unknown conversation
 *  (the client always creates one first, but a race should not lose chat). */
export function recordUserTurn(conversationId: string | undefined, userMessageId: string | undefined, content: string, parentId: string | null, owner?: string | null): void {
  if (!conversationId) return
  const all = load()
  let c = all.find(x => x.id === conversationId)
  const now = new Date().toISOString()
  if (!c) {
    c = { id: conversationId, title: null, owner: owner ?? null, createdAt: now, updatedAt: now, activeBranchId: null, messages: [] }
    all.push(c)
  }
  if (owner && !c.owner) { c.owner = owner }
  // Retries and regenerations resend the same user message; never duplicate.
  if (userMessageId && c.messages.some(m => m.id === userMessageId)) return
  c.messages.push({ id: userMessageId ?? crypto.randomUUID(), role: 'user', content, parentId, timestamp: now })
  c.activeBranchId = userMessageId ?? c.activeBranchId
  c.updatedAt = now
  persist()
}

export function recordAssistantTurn(conversationId: string | undefined, message: StoredMessage): void {
  if (!conversationId) return
  const c = load().find(x => x.id === conversationId)
  if (!c) return
  c.messages.push(message)
  c.activeBranchId = message.id
  c.updatedAt = message.timestamp
  persist()
  void exportToKb(c)
}

export async function conversationRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/chat/conversations', async req =>
    [...load()].filter(c => visibleTo(c, req.user?.username))
      .sort((a, b) => b.createdAt.localeCompare(a.createdAt)).map(summary))

  app.get('/api/chat/conversations/:id', async req => {
    const c = findVisible((req.params as { id: string }).id, req.user?.username)
    return { ...c, isOwner: true, canCollaborate: true }
  })

  app.post('/api/chat/conversations', async req => {
    const title = (req.body as { title?: string } | null)?.title
    const now = new Date().toISOString()
    const c: StoredConversation = {
      id: crypto.randomUUID(), title: title ?? null,
      owner: req.user?.username ?? null,
      createdAt: now, updatedAt: now, activeBranchId: null, messages: [],
    }
    const all = load()
    all.push(c)
    if (all.length > MAX_CONVERSATIONS) all.splice(0, all.length - MAX_CONVERSATIONS)
    persist()
    return summary(c)
  })

  app.patch('/api/chat/conversations/:id', async req => {
    const c = findVisible((req.params as { id: string }).id, req.user?.username)
    c.title = String((req.body as { title?: string } | null)?.title ?? '') || null
    c.updatedAt = new Date().toISOString()
    persist()
    return summary(c)
  })

  app.delete('/api/chat/conversations/:id', async req => {
    const id = (req.params as { id: string }).id
    const gone = load().find(c => c.id === id)
    if (gone && !visibleTo(gone, req.user?.username)) throw new KbError(404, 'conversation not found')
    cache = load().filter(c => c.id !== id)
    persist()
    if (gone) {
      await fsp.rm(exportPath(gone), { force: true }).catch(() => {})
      if (indexTimer) clearTimeout(indexTimer)
      indexTimer = setTimeout(() => { void reindexForDir(EXPORT_DIR).catch(() => {}) }, 2_000)
    }
    return { deleted: id }
  })
}
