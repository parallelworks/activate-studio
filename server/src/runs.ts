import fs from 'node:fs'
import path from 'node:path'
import { execFile } from 'node:child_process'
import { INDEX_BASE, PW_CLI } from './config.js'
import { appendConversationNote } from './conversations.js'

/**
 * Every workflow run the assistant launches, kept on disk and followed to
 * its end, across restarts. A run lives on the platform and outlasts any
 * Studio process; what did not survive a rollout was the knowledge that a
 * conversation was waiting on it. The registry is an append-only file
 * under the index base (which a redeploy preserves), and at startup every
 * run that has not ended is watched again. When a run ends, the
 * conversation that launched it gets a note, so a user who comes back
 * after a deploy finds the outcome where they asked for it.
 */
export interface RunRecord {
  slug: string
  workflow: string
  resource: string | null
  conversationId: string | null
  owner: string | null
  launchedAt: string
  state: string
  endedAt: string | null
}

const FILE = path.join(INDEX_BASE, 'runs.jsonl')
const TERMINAL = new Set(['completed', 'error', 'failed', 'canceled', 'cancelled'])
export const isTerminalRunState = (s: string): boolean => TERMINAL.has(String(s).toLowerCase())

let cache: Map<string, RunRecord> | null = null
const timers = new Map<string, NodeJS.Timeout>()
let log: (msg: string) => void = () => {}

type Cli = (args: string[]) => Promise<string>
let cli: Cli = args => new Promise((resolve, reject) => {
  execFile(PW_CLI, args, { timeout: 60_000, maxBuffer: 4 * 1024 * 1024, env: process.env }, (err, stdout, stderr) =>
    err ? reject(new Error(stderr || err.message)) : resolve(stdout))
})
/** Test seam: the pw CLI the watcher calls. */
export function setRunsCli(fn: Cli): void { cli = fn }
export function setRunsLog(fn: (msg: string) => void): void { log = fn }

function load(): Map<string, RunRecord> {
  if (cache) return cache
  cache = new Map()
  try {
    for (const line of fs.readFileSync(FILE, 'utf8').split('\n')) {
      if (!line.trim()) continue
      try {
        const ev = JSON.parse(line)
        if (ev.t === 'launch') cache.set(ev.slug, { slug: ev.slug, workflow: ev.workflow, resource: ev.resource ?? null, conversationId: ev.conversationId ?? null, owner: ev.owner ?? null, launchedAt: ev.launchedAt, state: 'running', endedAt: null })
        else if (ev.t === 'end') { const r = cache.get(ev.slug); if (r) { r.state = ev.state; r.endedAt = ev.endedAt } }
      } catch { /* a torn line is skipped */ }
    }
  } catch { /* no registry yet */ }
  return cache
}

function append(ev: Record<string, unknown>): void {
  try { fs.mkdirSync(path.dirname(FILE), { recursive: true }); fs.appendFileSync(FILE, JSON.stringify(ev) + '\n') } catch { /* the in-memory record still drives this process */ }
}

export function recordRun(rec: { slug: string; workflow: string; resource?: string | null; conversationId?: string | null; owner?: string | null }): RunRecord {
  const r: RunRecord = { slug: rec.slug, workflow: rec.workflow, resource: rec.resource ?? null, conversationId: rec.conversationId ?? null, owner: rec.owner ?? null, launchedAt: new Date().toISOString(), state: 'running', endedAt: null }
  load().set(r.slug, r)
  append({ t: 'launch', ...r })
  watchRun(r.slug)
  return r
}

export function listRuns(limit = 50): RunRecord[] {
  return [...load().values()].sort((a, b) => (a.launchedAt < b.launchedAt ? 1 : -1)).slice(0, limit)
}

export function markRunEnded(slug: string, state: string): void {
  const r = load().get(slug)
  if (!r || r.endedAt) return
  r.state = state
  r.endedAt = new Date().toISOString()
  append({ t: 'end', slug, state, endedAt: r.endedAt })
  const t = timers.get(slug)
  if (t) { clearInterval(t); timers.delete(slug) }
  if (r.conversationId) {
    const where = r.resource ? ` on ${r.resource}` : ''
    appendConversationNote(r.conversationId,
      `Run ${slug} of ${r.workflow}${where} finished: ${state}. Ask for its detail (workflow_run_detail ${slug}) to see the output${state === 'completed' ? '' : ' and why it failed'}.`)
  }
  log(`run ${slug} ended: ${state}`)
}

/** How often the watcher asks the platform; tests shorten it. */
const pollMs = () => Number(process.env.STUDIO_RUN_POLL_MS) || 60_000

export function watchRun(slug: string): void {
  if (timers.has(slug)) return
  let misses = 0
  const check = async () => {
    try {
      const out = await cli(['workflows', 'runs', 'view', slug, '-o', 'json'])
      const start = out.search(/[[{]/)
      const doc = JSON.parse(out.slice(start < 0 ? 0 : start).replace(/\n[^{}\[\]]*$/, '')) as { status?: string }
      misses = 0
      if (doc.status && isTerminalRunState(doc.status)) markRunEnded(slug, String(doc.status).toLowerCase())
    } catch {
      // A run the platform no longer knows, or a CLI without credentials,
      // is not worth polling forever.
      if (++misses >= 30) { const t = timers.get(slug); if (t) { clearInterval(t); timers.delete(slug) }; log(`run ${slug}: stopped watching after ${misses} failed checks`) }
    }
  }
  const t = setInterval(() => { void check() }, pollMs())
  t.unref?.()
  timers.set(slug, t)
  void check()
}

/** Re-attach to every run that had not ended when this process last stopped. */
export function watchRegisteredRuns(): number {
  let n = 0
  for (const r of load().values()) if (!r.endedAt && !isTerminalRunState(r.state)) { watchRun(r.slug); n++ }
  return n
}

/** Tests only: forget everything so a fresh registry can be loaded. */
export function resetRunsForTests(): void {
  for (const t of timers.values()) clearInterval(t)
  timers.clear(); cache = null
}
