import fs from 'node:fs'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { INDEX_BASE, KB_ROOT } from './config.js'
import { createTask, getTask, onTaskFinished, sumUsage, type Task } from './tasks.js'
import { effectiveSettings } from './settings.js'

/**
 * Standing agents: the long-lived residents of the Agents tab. A task
 * agent is born from one delegation and dies when its objective is met;
 * a standing agent has a goal, a persona, a budget, and triggers, and it
 * lives in ticks. A tick is one bounded delegated task whose objective
 * is the goal, the event that woke the agent, and the tail of the
 * agent's own journal, so the agent's memory is the journal on disk and
 * not a process a rollout would kill. The fleet directory sits under the
 * index base, which redeploys preserve, and the scheduler reloads every
 * agent at startup.
 */
export type FleetState = 'idle' | 'working' | 'paused' | 'input-required' | 'retired'
export interface FleetTriggers { every: string; onRunEnd: boolean; watchPath: string | null }
export interface FleetBudget { maxTicks: number | null; maxTokens: number | null }
export interface StandingAgent {
  id: string; name: string; goal: string; persona: string | null; model: string
  execution: 'local' | 'campaign'; resource: string | null
  triggers: FleetTriggers; budget: FleetBudget
  state: FleetState; note: string
  createdAt: string; updatedAt: string; lastTickAt: string | null; nextTickAt: string | null
  ticks: number; tokens: number
  currentTaskId: string | null
  /** Events that arrived while a tick was running; folded into the next one. */
  inbox: string[]
  lastSeenMtime: number
  owner: string | null; conversationId: string | null
}
export interface JournalEntry { at: string; kind: 'created' | 'tick' | 'result' | 'event' | 'message' | 'state' | 'error'; text: string; taskId?: string | null }

const DIR = path.join(INDEX_BASE, 'fleet')
const agents = new Map<string, StandingAgent>()
let loaded = false
let log: (msg: string) => void = () => {}
let timer: NodeJS.Timeout | null = null
const JOURNAL_TAIL = 12

const dirOf = (id: string) => path.join(DIR, id)
const CADENCE: Record<string, number> = { '15m': 15 * 60_000, '30m': 30 * 60_000, '1h': 3_600_000, '2h': 7_200_000, '6h': 21_600_000, '12h': 43_200_000, '1d': 86_400_000, manual: 0 }
export function cadenceMs(every: string): number { return CADENCE[every] ?? 0 }

function load(): void {
  if (loaded) return
  loaded = true
  try {
    for (const id of fs.readdirSync(DIR)) {
      try { const a = JSON.parse(fs.readFileSync(path.join(DIR, id, 'agent.json'), 'utf8')) as StandingAgent; agents.set(a.id, a) } catch { /* skip a torn record */ }
    }
  } catch { /* no fleet yet */ }
}
function persist(a: StandingAgent): void {
  a.updatedAt = new Date().toISOString()
  try { fs.mkdirSync(dirOf(a.id), { recursive: true }); fs.writeFileSync(path.join(dirOf(a.id), 'agent.json'), JSON.stringify(a, null, 1)) } catch { /* the in-memory record still drives this process */ }
}
export function journalAppend(id: string, e: JournalEntry): void {
  try { fs.mkdirSync(dirOf(id), { recursive: true }); fs.appendFileSync(path.join(dirOf(id), 'journal.jsonl'), JSON.stringify(e) + '\n') } catch { /* best effort */ }
}
export function journalTail(id: string, n = 40): JournalEntry[] {
  try {
    const lines = fs.readFileSync(path.join(dirOf(id), 'journal.jsonl'), 'utf8').trim().split('\n')
    return lines.slice(-n).map(l => { try { return JSON.parse(l) as JournalEntry } catch { return null } }).filter((e): e is JournalEntry => !!e)
  } catch { return [] }
}

const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 32) || 'agent'

export function listAgents(): StandingAgent[] {
  load()
  return [...agents.values()].sort((a, b) => (a.createdAt < b.createdAt ? 1 : -1))
}
export function getAgent(id: string): StandingAgent | undefined { load(); return agents.get(id) }

export function createAgent(input: {
  name: string; goal: string; persona?: string | null; model: string
  execution?: 'local' | 'campaign'; resource?: string | null
  every?: string; onRunEnd?: boolean; watchPath?: string | null
  maxTicks?: number | null; maxTokens?: number | null
  owner?: string | null; conversationId?: string | null
}): StandingAgent {
  load()
  const name = input.name.trim().slice(0, 60)
  if (!name || !input.goal.trim()) throw new Error('an agent needs a name and a goal')
  if (input.execution === 'campaign' && !input.resource) throw new Error('campaign execution needs a resource')
  const every = input.every && every_ok(input.every) ? input.every : '1h'
  const id = `${slug(name)}-${Math.random().toString(36).slice(2, 6)}`
  const now = new Date().toISOString()
  const a: StandingAgent = {
    id, name, goal: input.goal.trim().slice(0, 4000), persona: input.persona || null, model: input.model,
    execution: input.execution === 'campaign' ? 'campaign' : 'local', resource: input.resource ?? null,
    triggers: { every, onRunEnd: !!input.onRunEnd, watchPath: input.watchPath?.trim() || null },
    budget: { maxTicks: input.maxTicks ?? null, maxTokens: input.maxTokens ?? null },
    state: 'idle', note: 'created; first tick is due now',
    createdAt: now, updatedAt: now, lastTickAt: null, nextTickAt: now,
    ticks: 0, tokens: 0, currentTaskId: null, inbox: [], lastSeenMtime: newestMtime(input.watchPath?.trim() || null),
    owner: input.owner ?? null, conversationId: input.conversationId ?? null,
  }
  agents.set(id, a)
  persist(a)
  journalAppend(id, { at: now, kind: 'created', text: `Goal: ${a.goal}` })
  return a
}
const every_ok = (e: string) => e in CADENCE

function newestMtime(rel: string | null): number {
  if (!rel) return 0
  const root = path.join(KB_ROOT, rel)
  let newest = 0
  const walk = (d: string, depth: number) => {
    if (depth > 4) return
    for (const ent of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, ent.name)
      try {
        if (ent.isDirectory()) walk(p, depth + 1)
        else newest = Math.max(newest, fs.statSync(p).mtimeMs)
      } catch { /* vanished */ }
    }
  }
  try { walk(root, 0) } catch { /* not there yet */ }
  return newest
}

function setState(a: StandingAgent, state: FleetState, note: string): void {
  a.state = state; a.note = note
  journalAppend(a.id, { at: new Date().toISOString(), kind: 'state', text: `${state}: ${note}` })
  persist(a)
}

export function pauseAgent(id: string): boolean { const a = getAgent(id); if (!a || a.state === 'retired') return false; setState(a, 'paused', 'paused by user'); return true }
export function resumeAgent(id: string): boolean { const a = getAgent(id); if (!a || a.state === 'retired') return false; a.nextTickAt = new Date().toISOString(); setState(a, 'idle', 'resumed; next tick due now'); return true }
export function retireAgent(id: string): boolean { const a = getAgent(id); if (!a) return false; setState(a, 'retired', 'retired by user'); return true }

/** A human message is an event: journaled, then answered on the next tick, which is now. */
export function messageAgent(id: string, text: string): boolean {
  const a = getAgent(id)
  if (!a || a.state === 'retired') return false
  journalAppend(a.id, { at: new Date().toISOString(), kind: 'message', text })
  a.inbox.push(`Message from the user: ${text}`)
  if (a.state === 'input-required' || a.state === 'paused') a.state = 'idle'
  a.nextTickAt = new Date().toISOString()
  persist(a)
  return true
}

function budgetLeft(a: StandingAgent): string | null {
  if (a.budget.maxTicks != null && a.ticks >= a.budget.maxTicks) return `tick budget of ${a.budget.maxTicks} used`
  if (a.budget.maxTokens != null && a.tokens >= a.budget.maxTokens) return `token budget of ${a.budget.maxTokens} used`
  return null
}

function tickObjective(a: StandingAgent, events: string[]): string {
  const tail = journalTail(a.id, JOURNAL_TAIL).filter(e => e.kind !== 'tick').map(e => `- [${e.at.slice(0, 16)} ${e.kind}] ${e.text.slice(0, 400)}`).join('\n')
  return [
    `You are the standing agent "${a.name}". Your goal, which persists across ticks:`,
    a.goal,
    '',
    events.length ? `What woke you this tick:\n${events.map(e => `- ${e}`).join('\n')}` : 'What woke you this tick: the schedule.',
    '',
    tail ? `Your journal (most recent last):\n${tail}` : 'Your journal is empty; this is your first tick.',
    '',
    'Do one bounded round of work toward the goal now. Use the tools you have to look, act, and check. Then end with a status a person can read in a minute: what you did, what changed, what is next, and, if you need a decision from a person, one line starting with NEEDS DECISION: followed by the question. Do not repeat the journal back.',
  ].join('\n')
}

/** Run one tick now. Returns the task id, or null when the agent cannot tick. */
export function tickAgent(id: string, reason: string, opts: { force?: boolean } = {}): string | null {
  const a = getAgent(id)
  if (!a || a.state === 'retired' || a.state === 'working') return null
  if (!opts.force && a.state === 'paused') return null
  const exhausted = budgetLeft(a)
  if (exhausted && !opts.force) { setState(a, 'paused', exhausted); return null }
  const events = [...a.inbox, reason].filter(Boolean)
  a.inbox = []
  let m: Task
  try {
    m = createTask({
      objective: tickObjective(a, events), model: a.model,
      agents: [{ persona: a.persona ?? undefined, objective: `Tick for standing agent ${a.name}: ${a.goal.slice(0, 200)}` }],
      maxAgents: 1, maxDepth: 0, execution: a.execution, resource: a.resource,
    })
  } catch (e) {
    journalAppend(a.id, { at: new Date().toISOString(), kind: 'error', text: String((e as Error).message ?? e).slice(0, 400) })
    setState(a, 'idle', `tick failed to start: ${String((e as Error).message ?? e).slice(0, 120)}`)
    scheduleNext(a)
    return null
  }
  a.currentTaskId = m.id
  a.lastTickAt = new Date().toISOString()
  a.ticks++
  a.state = 'working'; a.note = `tick ${a.ticks} running`
  journalAppend(a.id, { at: a.lastTickAt, kind: 'tick', text: events.join(' | ').slice(0, 400), taskId: m.id })
  persist(a)
  return m.id
}

function scheduleNext(a: StandingAgent): void {
  const ms = cadenceMs(a.triggers.every)
  a.nextTickAt = ms > 0 ? new Date(Date.now() + ms).toISOString() : null
}

function onFinished(m: Task): void {
  load()
  for (const a of agents.values()) {
    if (a.currentTaskId !== m.id) continue
    a.currentTaskId = null
    const agent = [...m.nodes.values()][0]
    let result = agent?.note ?? ''
    if (agent?.resultPath) { try { result = fs.readFileSync(path.join(KB_ROOT, agent.resultPath), 'utf8') } catch { /* keep the note */ } }
    const used = sumUsage(m)
    if (used?.total) a.tokens += used.total
    journalAppend(a.id, { at: new Date().toISOString(), kind: 'result', text: result.slice(0, 4000), taskId: m.id })
    const decision = /NEEDS DECISION:\s*(.+)/i.exec(result)?.[1]?.trim()
    scheduleNext(a)
    if (m.state === 'canceled') setState(a, 'paused', 'last tick was stopped')
    else if (decision) setState(a, 'input-required', decision.slice(0, 300))
    else if (agent?.state === 'failed') setState(a, 'idle', `last tick failed: ${(agent.note || '').slice(0, 160)}`)
    else {
      const exhausted = budgetLeft(a)
      if (exhausted) setState(a, 'paused', exhausted)
      else setState(a, 'idle', a.nextTickAt ? `next tick ${a.nextTickAt.slice(0, 16)}` : 'waiting for an event or a message')
    }
  }
}

/** Runs ending on the platform wake the agents that asked to hear about them. */
export function notifyRunEnded(ev: { slug: string; workflow: string; resource: string | null; state: string }): void {
  load()
  const text = `Run ${ev.slug} of ${ev.workflow}${ev.resource ? ` on ${ev.resource}` : ''} ended: ${ev.state}`
  for (const a of agents.values()) {
    if (a.state === 'retired' || !a.triggers.onRunEnd) continue
    journalAppend(a.id, { at: new Date().toISOString(), kind: 'event', text })
    if (a.state === 'working') { a.inbox.push(text); persist(a); continue }
    if (a.state === 'paused') continue
    tickAgent(a.id, text)
  }
}

function pass(): void {
  load()
  const now = Date.now()
  for (const a of agents.values()) {
    if (a.state !== 'idle') continue
    if (a.triggers.watchPath) {
      const newest = newestMtime(a.triggers.watchPath)
      if (newest > a.lastSeenMtime) {
        a.lastSeenMtime = newest
        persist(a)
        journalAppend(a.id, { at: new Date().toISOString(), kind: 'event', text: `New or changed files under ${a.triggers.watchPath}` })
        tickAgent(a.id, `New or changed files under ${a.triggers.watchPath}`)
        continue
      }
    }
    if (a.nextTickAt && Date.parse(a.nextTickAt) <= now) tickAgent(a.id, 'the schedule')
  }
}

export function startFleet(logger?: (msg: string) => void): number {
  if (logger) log = logger
  load()
  onTaskFinished(onFinished)
  // A tick that was running when the last process stopped cannot be
  // resumed; the task engine marks its agent interrupted, so the standing
  // agent simply goes idle and picks up on its next trigger.
  for (const a of agents.values()) {
    if (a.state === 'working') { a.currentTaskId = null; a.state = 'idle'; a.note = 'a tick was interrupted by a restart; waiting for the next trigger'; if (!a.nextTickAt) scheduleNext(a); persist(a) }
  }
  const ms = Number(process.env.STUDIO_FLEET_TICK_MS) || 30_000
  if (timer) clearInterval(timer)
  timer = setInterval(pass, ms)
  timer.unref?.()
  const n = [...agents.values()].filter(a => a.state !== 'retired').length
  if (n) log(`fleet: ${n} standing agent(s) loaded`)
  return n
}
/** Tests only. */
export function resetFleetForTests(): void { if (timer) clearInterval(timer); timer = null; agents.clear(); loaded = false }

/**
 * Starter goals a user can create in one click. Each names a persona from
 * the default set and a cadence; the resource is left to the user, since
 * it is the one thing that differs between deployments. Written for a
 * demo on a small Slurm system: real work, small enough to finish.
 */
export interface FleetTemplate { key: string; name: string; persona: string; every: string; onRunEnd: boolean; watchPath: string | null; execution: 'local' | 'campaign'; goal: string }
export const FLEET_TEMPLATES: FleetTemplate[] = [
  {
    key: 'benchmark-campaign', name: 'Nightly benchmark campaign', persona: 'campaign_runner', every: '1d', onRunEnd: true, watchPath: null, execution: 'local',
    goal: 'Keep a small CPU benchmark campaign running on the target system named in your journal or, if none, ask for one (NEEDS DECISION). Each round: submit the activatebatch workflow through the scheduler on the debug partition with a 20 minute walltime, one node, and these commands: a Python script that times 40 rounds of a 2000x2000 numpy matrix multiply and prints one CSV line per round (round,seconds,gflops). Follow the run to completion with watch_run, read its output with workflow_run_detail, and append the rounds to a results table tasks/benchmarks/results.md in the knowledge base with the date, run id, node, and median GFLOPS. Report the trend across rounds in one paragraph. If a run fails for a mechanical reason (partition, walltime, GRES) correct it once and resubmit; otherwise record the failure and stop.',
  },
  {
    key: 'queue-watcher', name: 'Queue and run watcher', persona: 'watcher', every: '30m', onRunEnd: true, watchPath: null, execution: 'local',
    goal: 'Watch the connected systems and the runs on this account. Each tick: call hpc_status and workflow_runs; for any run that ended since your last journal entry read workflow_run_detail and classify the outcome in one sentence with the line that shows it; note systems that are degraded or have long queue waits. Keep a running status file tasks/watch/status.md: one table of systems (state, free nodes, queue wait) and one of recent runs (id, workflow, system, state, one-line cause for failures). End with what changed since the last tick. Do not resubmit anything; recommend, with the evidence, and mark it NEEDS DECISION when a person must choose.',
  },
  {
    key: 'results-reviewer', name: 'Results reviewer', persona: 'reviewer', every: 'manual', onRunEnd: false, watchPath: 'tasks', execution: 'local',
    goal: 'Whenever new or changed files appear under tasks/, read the new results and write or update a short summary beside them: what was run, the numbers that matter in a small table, figures embedded with the knowledge base embed links, and every anomaly named with the file that shows it. Check convergence, ranges, missing cases, and runs that finished with no output. Update the same summary file on later ticks rather than writing new ones, and say what changed.',
  },
  {
    key: 'daily-digest', name: 'Daily digest', persona: 'reporter', every: '1d', onRunEnd: false, watchPath: null, execution: 'local',
    goal: 'Once a day, write the digest of what the fleet did: read the task boards, the run registry (workflow_runs), and the results and status files under tasks/. Lead with outcomes, then what is still running and when it should finish, then failures and what was done about them, with numbers in one short table and a link for every claim. Write it to reports/digest-YYYY-MM-DD.md in the knowledge base and end your status with the first paragraph.',
  },
]

export async function fleetRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/fleet/templates', async () => ({ templates: FLEET_TEMPLATES }))
  const gate = () => { if (!effectiveSettings().delegationEnabled) throw Object.assign(new Error('delegation is disabled on this deployment'), { statusCode: 403 }) }
  app.get('/api/fleet', async () => { gate(); return { agents: listAgents() } })
  app.post('/api/fleet', async (req) => {
    gate()
    const b = req.body as Parameters<typeof createAgent>[0]
    const a = createAgent({ ...b, owner: (req as { user?: { id?: string } }).user?.id ?? null })
    return { agent: a }
  })
  app.get('/api/fleet/:id', async (req, reply) => {
    gate()
    const a = getAgent((req.params as { id: string }).id)
    if (!a) return reply.code(404).send({ error: 'no such agent' })
    const task = a.currentTaskId ? getTask(a.currentTaskId) : undefined
    return { agent: a, journal: journalTail(a.id, 60), currentTask: task ? { id: task.id, state: task.state } : null }
  })
  const act = (name: string, fn: (id: string, body: Record<string, unknown>) => boolean | string | null) =>
    app.post(`/api/fleet/:id/${name}`, async (req, reply) => {
      gate()
      const r = fn((req.params as { id: string }).id, (req.body ?? {}) as Record<string, unknown>)
      if (r === false || r === null) return reply.code(409).send({ error: `cannot ${name} this agent in its current state` })
      return { ok: true, result: r }
    })
  act('tick', id => tickAgent(id, 'the user asked for a tick now', { force: true }))
  act('pause', id => pauseAgent(id))
  act('resume', id => resumeAgent(id))
  act('retire', id => retireAgent(id))
  act('message', (id, b) => messageAgent(id, String(b.text ?? '').slice(0, 2000)))
}
