import { execFile } from 'node:child_process'
import fs from 'node:fs'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { INDEX_BASE, KB_ROOT, PW_CLI } from './config.js'
import { personas } from './extensions.js'

/**
 * Missions: many headless agents on one objective, visible from one place.
 *
 * A mission is an objective, a set of agents drawn from the persona
 * library, a board recording what happened, and a results directory in
 * the corpus. Agents are `pw code` one-shot runs: headless, bounded, and
 * daemon-independent, with the orchestrator here recording lifecycle to
 * the board and collecting results. An agent can request subtasks in its
 * result, which spawns children, so a fleet can grow itself; the ceiling
 * and depth limit are enforced here, server-side, because asking a model
 * to be restrained is not a control.
 *
 * Results are corpus files under missions/<id>/ and messages only point
 * at them, so outcomes index, search, and survive like any other
 * material, and the supervisor's context never holds the fleet's output.
 *
 * The board is append-only JSONL under the index directory, one file per
 * mission, with an in-memory tail for the viewer. Workers do not write
 * the board directly in this slice: that needs mission-scoped tokens on
 * /api/mcp, which is the next step, and lifecycle recording from the
 * orchestrator covers coordination up to the tens of agents this slice
 * targets.
 */

export interface BoardMsg { seq: number; at: string; from: string; topic: string; body: string }
export interface Participant {
  name: string; persona: string; objective: string; parent: string | null; depth: number
  status: 'running' | 'done' | 'failed' | 'stopped'
  startedAt: string; updatedAt: string; note: string; resultPath: string | null
}
export interface Mission {
  id: string; objective: string; model: string; createdAt: string
  status: 'running' | 'done' | 'stopped'
  maxAgents: number; maxDepth: number
  participants: Map<string, Participant>
  board: BoardMsg[]
  seq: number
  procs: Map<string, ReturnType<typeof execFile>>
}

const missions = new Map<string, Mission>()
const MISSIONS_DIR = path.join(INDEX_BASE, 'missions')

/** Hard ceilings the per-mission settings are clamped to. */
const MAX_AGENTS_CAP = 24
const MAX_DEPTH_CAP = 3
const AGENT_TIMEOUT_MS = 15 * 60_000

function slug(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40) || 'mission'
}

function post(m: Mission, from: string, topic: string, body: string): void {
  const msg: BoardMsg = { seq: ++m.seq, at: new Date().toISOString(), from, topic, body: body.slice(0, 4000) }
  m.board.push(msg)
  if (m.board.length > 500) m.board.splice(0, m.board.length - 500)
  try {
    fs.mkdirSync(path.join(MISSIONS_DIR, m.id), { recursive: true })
    fs.appendFileSync(path.join(MISSIONS_DIR, m.id, 'board.jsonl'), JSON.stringify(msg) + '\n')
  } catch { /* the in-memory board still serves the viewer */ }
}

function agentCount(m: Mission): number { return m.participants.size }

function maybeFinish(m: Mission): void {
  if (m.status !== 'running') return
  const alive = [...m.participants.values()].some(p => p.status === 'running')
  if (!alive) {
    m.status = 'done'
    post(m, 'orchestrator', 'mission', 'Every agent has finished.')
  }
}

/**
 * The result contract. The agent's final output is asked for as JSON with
 * a markdown result and an optional subtask list; a reply that is not
 * JSON is kept whole as the result, so a model that ignores the contract
 * still produces a usable outcome instead of an error.
 */
function agentPromptFor(m: Mission, personaName: string, objective: string): string {
  const p = personas().find(x => x.name === personaName)
  const personaText = p ? (() => {
    try {
      const dir = path.join(KB_ROOT, '.agents')
      return fs.readFileSync(path.join(dir, `${p.file}`), 'utf8').replace(/^---\n[\s\S]*?\n---\n?/, '').trim()
    } catch { return '' }
  })() : ''
  return [
    personaText && `Adopt this persona for the task:\n${personaText}`,
    `You are one agent in a coordinated mission. Mission objective: ${m.objective}`,
    `Your task: ${objective}`,
    'Work headlessly and do not ask questions. When finished, output ONLY a JSON object:',
    '{"result": "<your findings or work product, as markdown>", "subtasks": [{"persona": "<optional persona name>", "objective": "<subtask>"}]}',
    'Include subtasks only when part of your task genuinely needs a separate agent; most tasks need none.',
  ].filter(Boolean).join('\n\n')
}

function spawnAgent(m: Mission, opts: { persona: string; objective: string; parent: string | null; depth: number }): string {
  if (m.status !== 'running') throw new Error('mission is not running')
  if (agentCount(m) >= m.maxAgents) throw new Error(`agent ceiling reached (${m.maxAgents})`)
  if (opts.depth > m.maxDepth) throw new Error(`depth limit reached (${m.maxDepth})`)
  const name = `${slug(opts.persona || 'agent')}-${agentCount(m) + 1}`
  const p: Participant = {
    name, persona: opts.persona, objective: opts.objective, parent: opts.parent, depth: opts.depth,
    status: 'running', startedAt: new Date().toISOString(), updatedAt: new Date().toISOString(),
    note: 'starting', resultPath: null,
  }
  m.participants.set(name, p)
  post(m, 'orchestrator', 'spawn', `${name} (persona ${opts.persona || 'none'}, depth ${opts.depth}${opts.parent ? `, child of ${opts.parent}` : ''}): ${opts.objective}`)

  const workdir = path.join(MISSIONS_DIR, m.id, 'work', name)
  fs.mkdirSync(workdir, { recursive: true })
  const args = ['code', '-p', agentPromptFor(m, opts.persona, opts.objective), '-o', 'json',
    '--permission-mode', 'read-only', '-w', workdir, '-m', m.model]
  const child = execFile(PW_CLI, args, { timeout: AGENT_TIMEOUT_MS, maxBuffer: 16 * 1024 * 1024 }, (err, stdout) => {
    m.procs.delete(name)
    p.updatedAt = new Date().toISOString()
    if (p.status === 'stopped') { maybeFinish(m); return }
    if (err && !stdout) {
      p.status = 'failed'
      p.note = String(err.message).slice(0, 200)
      post(m, name, 'failed', p.note)
      maybeFinish(m)
      return
    }
    // Three shapes are tolerated: the agent's contract JSON at top level,
    // the CLI's envelope with the contract (or plain text) inside, and
    // plain text. Unwrapping must not discard the contract's subtasks,
    // which is exactly what a naive result-first unwrap does.
    const text = String(stdout ?? '').trim()
    let result = text
    let subtasks: { persona?: string; objective?: string }[] = []
    const tryContract = (o: unknown): boolean => {
      if (o && typeof o === 'object' && ('result' in (o as object) || 'subtasks' in (o as object))) {
        const c = o as { result?: unknown; subtasks?: unknown }
        result = String(c.result ?? '')
        subtasks = Array.isArray(c.subtasks) ? c.subtasks as typeof subtasks : []
        return true
      }
      return false
    }
    try {
      const env = JSON.parse(text)
      if (!tryContract(env)) {
        const unwrapped = String((env as { content?: unknown; output?: unknown; message?: unknown }).content
          ?? (env as { output?: unknown }).output ?? (env as { message?: unknown }).message ?? '')
        if (unwrapped) {
          result = unwrapped
          try { tryContract(JSON.parse(unwrapped)) } catch { /* plain text inside the envelope */ }
        }
      }
    } catch { /* plain text output */ }

    // The result is corpus material under the mission.
    const relDir = path.join('missions', m.id)
    const rel = path.join(relDir, `${name}.md`)
    try {
      fs.mkdirSync(path.join(KB_ROOT, relDir), { recursive: true })
      fs.writeFileSync(path.join(KB_ROOT, rel),
        `---\nmission: ${m.id}\nagent: ${name}\npersona: ${opts.persona}\n---\n\n# ${opts.objective}\n\n${result}\n`)
      p.resultPath = rel
    } catch { /* the board still carries the text */ }
    void import('./indexing.js').then(x => x.incrementalIndexDir(relDir)).catch(() => {})

    p.status = 'done'
    p.note = 'finished'
    post(m, name, 'complete', p.resultPath ? `Result at ${p.resultPath}` : result.slice(0, 1500))

    // Sub-spawning, inside the same server-enforced limits.
    for (const st of subtasks.slice(0, 5)) {
      const objective = String(st?.objective ?? '').slice(0, 2000)
      if (!objective) continue
      try {
        spawnAgent(m, { persona: String(st?.persona ?? opts.persona), objective, parent: name, depth: opts.depth + 1 })
      } catch (e) {
        post(m, 'orchestrator', 'refused', `Subtask from ${name} refused: ${String((e as Error).message)}`)
      }
    }
    maybeFinish(m)
  })
  m.procs.set(name, child)
  return name
}

export function createMission(opts: {
  objective: string; model: string
  agents: { persona?: string; objective: string }[]
  maxAgents?: number; maxDepth?: number
}): Mission {
  const id = `${new Date().toISOString().slice(0, 10)}-${slug(opts.objective)}-${Math.random().toString(36).slice(2, 6)}`
  const m: Mission = {
    id, objective: opts.objective.slice(0, 2000), model: opts.model, createdAt: new Date().toISOString(),
    status: 'running',
    maxAgents: Math.min(Math.max(Number(opts.maxAgents) || 10, 1), MAX_AGENTS_CAP),
    maxDepth: Math.min(Math.max(Number(opts.maxDepth) || 2, 0), MAX_DEPTH_CAP),
    participants: new Map(), board: [], seq: 0, procs: new Map(),
  }
  missions.set(id, m)
  post(m, 'orchestrator', 'mission', `Objective: ${m.objective} (up to ${m.maxAgents} agents, depth ${m.maxDepth})`)
  for (const a of opts.agents.slice(0, m.maxAgents)) {
    spawnAgent(m, { persona: String(a.persona ?? ''), objective: String(a.objective ?? '').slice(0, 2000), parent: null, depth: 0 })
  }
  return m
}

export function stopMission(id: string): boolean {
  const m = missions.get(id)
  if (!m) return false
  m.status = 'stopped'
  for (const [name, proc] of m.procs) {
    const p = m.participants.get(name)
    if (p) { p.status = 'stopped'; p.note = 'stopped by user' }
    try { proc.kill('SIGTERM') } catch { /* already gone */ }
  }
  m.procs.clear()
  post(m, 'orchestrator', 'mission', 'Stopped by user.')
  return true
}

export function missionSummary(m: Mission) {
  return {
    id: m.id, objective: m.objective, model: m.model, status: m.status,
    createdAt: m.createdAt, maxAgents: m.maxAgents, maxDepth: m.maxDepth,
    agents: [...m.participants.values()],
  }
}

export function listMissions() {
  return [...missions.values()].map(m => ({
    id: m.id, objective: m.objective, status: m.status, createdAt: m.createdAt,
    agents: m.participants.size,
    running: [...m.participants.values()].filter(p => p.status === 'running').length,
  }))
}

export function getMission(id: string): Mission | undefined { return missions.get(id) }

export async function missionRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/missions', async () => ({ missions: listMissions() }))

  app.get('/api/missions/:id', async (req, reply) => {
    const m = missions.get((req.params as { id: string }).id)
    if (!m) return reply.status(404).send({ error: 'no such mission' })
    const since = Number((req.query as { since?: string }).since ?? 0)
    return { ...missionSummary(m), board: m.board.filter(b => b.seq > since) }
  })

  app.post('/api/missions', async (req, reply) => {
    const b = (req.body ?? {}) as { objective?: string; model?: string; agents?: { persona?: string; objective: string }[]; maxAgents?: number; maxDepth?: number }
    if (!b.objective || !b.model || !Array.isArray(b.agents) || b.agents.length === 0) {
      return reply.status(400).send({ error: 'objective, model, and at least one agent are required' })
    }
    try {
      const m = createMission({ objective: b.objective, model: b.model, agents: b.agents, maxAgents: b.maxAgents, maxDepth: b.maxDepth })
      return missionSummary(m)
    } catch (e) {
      return reply.status(400).send({ error: String((e as Error).message) })
    }
  })

  app.post('/api/missions/:id/stop', async (req, reply) => {
    const ok = stopMission((req.params as { id: string }).id)
    return ok ? { ok } : reply.status(404).send({ error: 'no such mission' })
  })
}
