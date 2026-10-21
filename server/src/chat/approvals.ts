import fs from 'node:fs'
import fsp from 'node:fs/promises'
import path from 'node:path'
import { INDEX_BASE } from '../config.js'
import { authEnabled } from '../auth.js'
import { platformOrigin } from '../sessions.js'
import type { WireMessage } from './gateway.js'
import { insteadOf, prepareArgs, runPlatformTool, toolHash, type CallArgs } from './platformTools.js'

/**
 * Held calls: a call to a tool that changes something waits here until
 * the person who asked answers it in the chat. The record is the whole of
 * what was asked (the conversation, the tool call, the tool, its arguments
 * in canonical form, a hash of the tool's definition, and the viewer), and
 * an answer runs only the call it records: the client says which call it
 * answers and whether it allows it, never what to run. A record is taken
 * out before its call runs, so one approval runs one call once.
 *
 * Records are kept beside the index so an answer given after a restart of
 * the Studio still finds its call, and they expire: an answer after
 * APPROVAL_TTL_MS is refused.
 */

export const APPROVAL_TTL_MS = Number(process.env.STUDIO_APPROVAL_TTL_MS) || 30 * 60_000

export interface HeldCall {
  conversationId: string
  /** The stored assistant message that shows the call and its approval. */
  messageId: string
  toolCallId: string
  tool: string
  /** The arguments in canonical form (prepareArgs). */
  args: string
  /** toolHash of the tool when the call was held. */
  hash: string
  /** The tools the user may run in its place, with their hashes then. */
  instead: Record<string, string>
  /** Who may answer: the verified user's id, or "local" on a standalone deployment. */
  viewer: string
  viewerName: string
  heldAt: string
  expiresAt: string
  /** The reply's own messages up to the pause (the model's tool calls and
   *  the results it already had), replayed when it resumes. */
  turn: WireMessage[]
  /** The tool groups the reply had, so the resumed reply has them too. */
  groups: string[]
}

const FILE = path.join(INDEX_BASE, 'held-tool-calls.json')
let held: Map<string, HeldCall> | null = null
let writing: Promise<void> = Promise.resolve()
const keyOf = (conversationId: string, toolCallId: string) => `${conversationId}\0${toolCallId}`

function load(): Map<string, HeldCall> {
  if (held) return held
  held = new Map()
  try {
    for (const r of JSON.parse(fs.readFileSync(FILE, 'utf8')) as HeldCall[]) held.set(keyOf(r.conversationId, r.toolCallId), r)
  } catch { /* none held yet */ }
  return held
}

/** Written whole to a temporary file and renamed into place. Expired records
 *  are dropped on every write. */
function persist(): void {
  const now = Date.now()
  for (const [k, r] of load()) if (Date.parse(r.expiresAt) < now) load().delete(k)
  const snapshot = JSON.stringify([...load().values()])
  const tmp = `${FILE}.${process.pid}.tmp`
  writing = writing.then(async () => {
    await fsp.writeFile(tmp, snapshot, { mode: 0o600 })
    await fsp.rename(tmp, FILE)
  }).catch(() => {})
}

export function holdCall(rec: HeldCall): void {
  load().set(keyOf(rec.conversationId, rec.toolCallId), rec)
  persist()
}

export function heldCall(conversationId: string, toolCallId: string): HeldCall | undefined {
  return load().get(keyOf(conversationId, toolCallId))
}

/** The calls held in a conversation, for showing who may answer each. */
export function heldCallsIn(conversationId: string): HeldCall[] {
  return [...load().values()].filter(r => r.conversationId === conversationId)
}

function release(rec: HeldCall): boolean {
  const gone = load().delete(keyOf(rec.conversationId, rec.toolCallId))
  if (gone) persist()
  return gone
}

/** Tests start each case from an empty store. */
export function resetHeldForTests(): void { held = new Map(); persist() }

export interface Viewer { id: string; name: string }

/**
 * The person a chat request comes from, who is the only one who may answer
 * the calls held for it. With platform identity on, that is the verified
 * user and nobody else; a request without a verified identity has no one to
 * approve anything. A standalone deployment serves one person.
 */
export function viewerOf(req: { user?: { id?: string; username?: string } }): Viewer | null {
  if (!authEnabled()) return { id: 'local', name: process.env.APP_USERNAME || 'user' }
  if (!req.user?.id) return null
  return { id: req.user.id, name: req.user.username || req.user.id }
}

/**
 * Whether the tools that change something are offered to this request, and
 * if not, why. They need a platform, a person present who can approve each
 * call, that person's own platform key (a deployment or shared credential
 * belongs to someone else), and a conversation that is that person's own.
 */
export function changesAvailable(o: {
  viewer: Viewer | null
  /** The key the calls would run with: the viewer's own, or on a standalone
   *  deployment the deployment's, which is the one user's. */
  key: string | null
  /** The conversation's owner, when it has one. */
  owner?: string | null
  username?: string | null
}): { ok: true } | { ok: false; why: string } {
  if (!platformOrigin()) return { ok: false, why: 'no platform is configured' }
  if (!o.viewer) return { ok: false, why: 'nobody in this chat can approve a change' }
  if (!o.key) return { ok: false, why: authEnabled() ? 'changes run only with your own platform key, which you can add under Settings, Model access' : 'no platform credential is configured' }
  if (authEnabled() && o.owner && o.owner !== o.username) return { ok: false, why: 'this conversation belongs to someone else' }
  return { ok: true }
}

/** The held record for a new hold. */
export function newHold(o: Omit<HeldCall, 'hash' | 'instead' | 'heldAt' | 'expiresAt'>, now = Date.now()): HeldCall {
  return {
    ...o,
    hash: toolHash(o.tool),
    instead: Object.fromEntries(insteadOf(o.tool).map(n => [n, toolHash(n)])),
    heldAt: new Date(now).toISOString(),
    expiresAt: new Date(now + APPROVAL_TTL_MS).toISOString(),
  }
}

export interface Answer {
  conversationId: string
  messageId: string
  toolCallId: string
  allowed: boolean
  /** A tool to run in the call's place, from those the record allows. */
  tool?: string
  /** The call's arguments as the client sent them back; they must match. */
  args?: string
  feedback?: string
}

/**
 * Checks an answer against its held call and, when it may proceed, takes
 * the record out so nothing can answer it again. Returns the tool to run
 * (the held one, or the alternative the user chose), or why the answer is
 * refused. A refusal that could never succeed (expired, the tool changed)
 * releases the record; one from the wrong person, or for other arguments,
 * leaves it for the right answer.
 */
export function takeAnswer(a: Answer, viewer: Viewer | null, now = Date.now()): { ok: true; rec: HeldCall; tool: string } | { ok: false; why: string } {
  const rec = heldCall(a.conversationId, a.toolCallId)
  if (!rec) return { ok: false, why: 'This request is no longer waiting for an answer; nothing ran. Ask again to run it.' }
  if (!viewer || viewer.id !== rec.viewer) return { ok: false, why: `Only ${rec.viewerName}, who asked for this, can answer it; nothing ran.` }
  if (Date.parse(rec.expiresAt) <= now) { release(rec); return { ok: false, why: 'This request expired before it was answered; nothing ran. Ask again to run it.' } }
  if (a.messageId !== rec.messageId) return { ok: false, why: 'The answer does not belong to this request; nothing ran.' }
  if (toolHash(rec.tool) !== rec.hash) { release(rec); return { ok: false, why: `The ${rec.tool} tool changed after this was asked; nothing ran. Ask again to run it.` } }
  if (a.args !== undefined) {
    let held: Record<string, unknown> = {}
    try { held = JSON.parse(rec.args) } catch { /* compared as empty */ }
    let sent: unknown = null
    try { sent = typeof a.args === 'string' ? JSON.parse(a.args) : a.args } catch { /* refused below */ }
    // Defaults the client left out are the held call's, from the same viewer.
    const filled = sent && typeof sent === 'object' && !Array.isArray(sent) ? { ...held, ...(sent as Record<string, unknown>) } : sent
    const again = prepareArgs(rec.tool, filled, typeof held.user === 'string' ? held.user : null)
    if (!('canonical' in again) || again.canonical !== rec.args) return { ok: false, why: 'The approval was for other arguments than the call that was asked for; nothing ran.' }
  }
  let tool = rec.tool
  if (a.allowed && a.tool && a.tool !== rec.tool) {
    if (!(a.tool in rec.instead)) return { ok: false, why: `${a.tool} cannot be run in place of ${rec.tool}; nothing ran.` }
    if (toolHash(a.tool) !== rec.instead[a.tool]) { release(rec); return { ok: false, why: `The ${a.tool} tool changed after this was asked; nothing ran. Ask again to run it.` } }
    tool = a.tool
  }
  if (!release(rec)) return { ok: false, why: 'This request was already answered; nothing more ran.' }
  return { ok: true, rec, tool }
}

const DONE: Record<string, string> = {
  start_cluster: 'started', resume_cluster: 'resumed', stop_cluster: 'stopped', destroy_cluster: 'destroyed', cancel_workflow_run: 'cancelled',
}

/**
 * Settles an answer: refuses it (see takeAnswer), records a denial, or runs
 * the held call, or the alternative the user chose, with the arguments the
 * record holds and the viewer's key. Returns the text for the model and the
 * summary for the call's row in the chat.
 */
export async function answerHeld(a: Answer, viewer: Viewer | null, key: string | null, enabled: (tool: string) => boolean): Promise<
  { ok: false; why: string } | { ok: true; rec: HeldCall; tool: string; result: string; summary: string; failed: boolean }
> {
  // Checked before the record is taken, so the call stays held for an
  // answer that can run it.
  const pending = heldCall(a.conversationId, a.toolCallId)
  if (a.allowed && pending && viewer?.id === pending.viewer) {
    const tool = a.tool && a.tool !== pending.tool ? a.tool : pending.tool
    if (!key) return { ok: false, why: 'Running this needs your own platform key, which you can add under Settings, Model access; nothing ran.' }
    if (!enabled(tool)) return { ok: false, why: `${tool} is turned off for this deployment; nothing ran.` }
  }
  const taken = takeAnswer(a, viewer)
  if (!taken.ok) return taken
  const { rec, tool } = taken
  if (!a.allowed) {
    const said = a.feedback?.trim() ? ` They said: ${a.feedback.trim()}` : ''
    return { ok: true, rec, tool: rec.tool, result: `The user declined this call, so it did not run. Do not call it again unless the user asks.${said}`, summary: 'declined', failed: false }
  }
  let args: CallArgs = {}
  try { args = JSON.parse(rec.args) as CallArgs } catch { /* refused by the platform as incomplete */ }
  const out = await runPlatformTool(tool, args, key!)
  const target = String(args.clusterName ?? args.slug ?? '')
  const swapped = tool !== rec.tool
  const result = (swapped ? `The user chose ${tool} in place of ${rec.tool}, so ${tool} ran. ` : '') + out.result
  const summary = out.failed
    ? `failed: ${out.result.slice(0, 160)}`
    : `${DONE[tool] ?? 'ran'}${target ? ` ${target}` : ''}${swapped ? ` (in place of ${rec.tool})` : ''}`
  return { ok: true, rec, tool, result, summary, failed: out.failed }
}
