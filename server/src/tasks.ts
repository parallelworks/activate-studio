import { execFile } from 'node:child_process'
import { withWorkspace } from './workspace.js'
import fs from 'node:fs'
import { randomBytes } from 'node:crypto'
import path from 'node:path'
import type { FastifyInstance } from 'fastify'
import { INDEX_BASE, KB_ROOT, PORT, PW_CLI } from './config.js'
import { personas } from './extensions.js'
import * as pwcode from './pwcode.js'
import { effectiveSettings } from './settings.js'

/**
 * Delegated tasks: a request that decomposes into subtasks worked by
 * headless agents, visible as one tree.
 *
 * A task is what the user asked for. It may decompose into subtasks, each
 * carried out by an agent, and an agent may decompose its own subtask
 * further, which is the tree the operator watches. The vocabulary is
 * A2A's rather than ours: task, subtask, and the states below. An agent
 * runs one of two ways (Settings, External access, "Agent execution"): as a
 * one-shot `pw code -p` run (the runner), or as a session in the local pw
 * code daemon, which the Studio follows turn by turn and whose approval
 * requests it shows as input-required. Either way the orchestrator here
 * records lifecycle to the board and collects results. An agent can request subtasks in its
 * result, which spawns children, so a fleet can grow itself; the ceiling
 * and depth limit are enforced here, server-side, because asking a model
 * to be restrained is not a control.
 *
 * Results are corpus files under tasks/<id>/ and messages only point
 * at them, so outcomes index, search, and survive like any other
 * material, and the supervisor's context never holds the fleet's output.
 *
 * The board is append-only JSONL under the index directory, one file per
 * task, with an in-memory tail for the viewer. Workers do not write
 * the board directly in this slice: that needs task-scoped tokens on
 * /api/mcp, which is the next step, and lifecycle recording from the
 * orchestrator covers coordination up to the tens of agents this slice
 * targets.
 */

export interface BoardMsg { seq: number; at: string; from: string; topic: string; body: string }
/** A2A's task lifecycle, adopted verbatim so our states mean what the
 *  rest of the field means by them. input-required is the one we could
 *  not previously express: an agent blocked on a human decision. */
export type TaskState = 'submitted' | 'working' | 'input-required' | 'completed' | 'failed' | 'canceled'

export interface AgentTask {
  name: string; persona: string; objective: string; parent: string | null; depth: number
  state: TaskState
  startedAt: string; updatedAt: string; note: string; resultPath: string | null
  /** Campaign agents only: the platform run carrying this agent. */
  runSlug?: string | null
  /** Tokens the agent's model calls consumed, when pw code reported them. */
  usage?: AgentUsage | null
  /** Session agents only: the pw code session carrying this agent. */
  sessionId?: string | null
  /** Session agents only: what the agent is waiting for a person to approve. */
  approvals?: PendingApproval[] | null
}
export interface PendingApproval { id: string; kind: string; text: string }
/** runner: one-shot pw code runs. session: sessions in the pw code daemon. */
export type AgentRuntime = 'runner' | 'session'
export interface AgentUsage { input: number; output: number; total: number; cost?: number | null }

/**
 * Token usage from whatever envelope pw code emitted. Field names differ
 * between CLI versions and providers (usage.prompt_tokens, input_tokens,
 * total_cost_usd, ...), so every known spelling is tried and the result is
 * null rather than zeros when none is present.
 */
/** The task's agents' usage summed, or null when none reported any. */
export function sumUsage(m: { nodes: Map<string, AgentTask> }): AgentUsage | null {
  let out: AgentUsage | null = null
  for (const a of m.nodes.values()) {
    if (!a.usage) continue
    out = out ?? { input: 0, output: 0, total: 0, cost: null }
    out.input += a.usage.input; out.output += a.usage.output; out.total += a.usage.total
    if (a.usage.cost != null) out.cost = (out.cost ?? 0) + a.usage.cost
  }
  return out
}

export function usageOf(env: unknown): AgentUsage | null {
  if (!env || typeof env !== 'object') return null
  const o = env as Record<string, unknown>
  const u = (o.usage && typeof o.usage === 'object' ? o.usage : o) as Record<string, unknown>
  const num = (...keys: string[]): number | null => {
    for (const k of keys) { const v = u[k] ?? o[k]; if (typeof v === 'number' && Number.isFinite(v)) return v }
    return null
  }
  const input = num('prompt_tokens', 'input_tokens', 'inputTokens')
  const output = num('completion_tokens', 'output_tokens', 'outputTokens')
  const total = num('total_tokens', 'totalTokens') ?? (input != null || output != null ? (input ?? 0) + (output ?? 0) : null)
  const cost = num('total_cost_usd', 'cost_usd', 'cost', 'totalCostUsd')
  if (total == null && cost == null) return null
  return { input: input ?? 0, output: output ?? 0, total: total ?? 0, cost: cost ?? null }
}
export interface SharedTask { id: string; objective: string; persona: string; claimedBy: string | null; done: boolean; resultPath: string | null }
export interface Task {
  id: string; token: string; shared: SharedTask[]
  objective: string; model: string; createdAt: string
  state: TaskState
  maxAgents: number; maxDepth: number
  /** local: workers are child processes beside the server. campaign:
   *  workers are platform workflow runs on a named system, which is what
   *  outlives this process and scales past its host. */
  execution: 'local' | 'campaign'
  resource: string | null
  runtime: AgentRuntime
  nodes: Map<string, AgentTask>
  board: BoardMsg[]
  seq: number
  procs: Map<string, ReturnType<typeof execFile>>
  pollers: Map<string, ReturnType<typeof setInterval>>
}

const tasks = new Map<string, Task>()
const MISSIONS_DIR = path.join(INDEX_BASE, 'tasks')

/** Hard ceilings the per-task settings are clamped to. */
const MAX_AGENTS_CAP = 24
const MAX_DEPTH_CAP = 3
const AGENT_TIMEOUT_MS = 15 * 60_000
/** Where a worker reaches this Studio. Loopback by default, since workers
 *  run beside the server in this slice. */
const SELF_URL = (process.env.STUDIO_SELF_URL || `http://127.0.0.1:${process.env.PORT || 4080}`).replace(/\/$/, '')
/** Where an agent on this host reaches the board: the loopback address,
 *  never the public one, which sits behind the platform's session proxy
 *  and turns away a task token. */
const LOCAL_URL = `http://127.0.0.1:${PORT}`
/** The agent definition a session agent runs as; it carries the board. */
const SESSION_AGENT = 'studio_task'
const SESSION_POLL_MS = Number(process.env.STUDIO_SESSION_POLL_MS || 2_000)

const CAMPAIGN_POLL_MS = Number(process.env.STUDIO_CAMPAIGN_POLL_MS || 20_000)
const RUNNER_WORKFLOW = 'studio-agent-runner'

function pwRun(args: string[], timeoutMs = 60_000): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(PW_CLI, args, { timeout: timeoutMs, maxBuffer: 16 * 1024 * 1024 },
      (err, so, se) => (err ? reject(new Error(String(se || err.message))) : resolve(String(so))))
  })
}

/** The pw CLI appends upgrade notices to stdout, so take the first
 *  balanced JSON value rather than parsing the whole thing. */
function jsonHead<T = unknown>(text: string): T {
  const start = text.search(/[[{]/)
  if (start < 0) throw new Error(`no JSON in CLI output: ${text.slice(0, 120)}`)
  const open = text[start]; const close = open === '[' ? ']' : '}'
  let depth = 0; let inStr = false; let esc = false
  for (let i = start; i < text.length; i++) {
    const c = text[i]
    if (inStr) { if (esc) esc = false; else if (c === '\\') esc = true; else if (c === '"') inStr = false; continue }
    if (c === '"') inStr = true
    else if (c === open) depth++
    else if (c === close && --depth === 0) return JSON.parse(text.slice(start, i + 1)) as T
  }
  return JSON.parse(text.slice(start)) as T
}

/**
 * The runner: one job on the target system's login node that writes the
 * agent's prompt and board registration, runs pw code, and brackets the
 * final output in markers the poller extracts. Login-node execution is
 * deliberate: the agent itself is API calls and corpus work, and the
 * heavy compute is whatever workflows the agent launches, which go
 * through the scheduler like anything else. Registered once per account,
 * created automatically when absent, and validated against the platform
 * schema by the test suite.
 */
const RUNNER_YAML = `permissions:
  - '*'
'on':
  execute:
    inputs:
      resource:
        label: Agent host
        type: compute-clusters
        autoselect: true
        optional: false
      prompt:
        label: Agent prompt
        type: editor
      model:
        label: Model
        type: string
      agent_name:
        label: Agent name
        type: string
      board_url:
        label: Studio board URL
        type: string
        optional: true
      board_token:
        label: Board token
        type: string
        optional: true
      timeout_seconds:
        label: Timeout seconds
        type: number
        default: 7200
jobs:
  agent:
    ssh:
      remoteHost: \${{ inputs.resource.ip }}
    steps:
      - name: Run agent
        run: |
          set -e
          WORK="$PWD/agent-work"
          mkdir -p "$WORK/.agents"
          cat > "$WORK/prompt.txt" << 'STUDIO_PROMPT_EOF'
          \${{ inputs.prompt }}
          STUDIO_PROMPT_EOF
          if [ -n "\${{ inputs.board_url }}" ]; then
            cat > "$WORK/.agents/settings.local.json" << 'STUDIO_MCP_EOF'
          {"mcpServers":{"task":{"type":"http","url":"\${{ inputs.board_url }}/api/mcp","headers":{"Authorization":"Bearer \${{ inputs.board_token }}","X-Task-Agent":"\${{ inputs.agent_name }}"}}}}
          STUDIO_MCP_EOF
          fi
          set +e
          timeout "\${{ inputs.timeout_seconds }}" pw code -p "$(cat "$WORK/prompt.txt")" -o json --permission-mode read-only -w "$WORK" -m "\${{ inputs.model }}" > "$WORK/out.json" 2> "$WORK/err.log"
          RC=$?
          set -e
          echo "STUDIO_RESULT_BEGIN"
          cat "$WORK/out.json" 2>/dev/null || true
          echo ""
          echo "STUDIO_RESULT_END"
          tail -c 4000 "$WORK/err.log" 2>/dev/null || true
          exit $RC
`

export function runnerWorkflowYaml(): string { return RUNNER_YAML }

let runnerEnsured = false
async function ensureRunnerWorkflow(): Promise<void> {
  if (runnerEnsured) return
  try {
    await pwRun(['workflows', 'get', RUNNER_WORKFLOW, '-o', 'json'])
    runnerEnsured = true
    return
  } catch { /* absent: create it */ }
  const tmp = path.join(MISSIONS_DIR, 'agent-runner.yaml')
  fs.mkdirSync(MISSIONS_DIR, { recursive: true })
  fs.writeFileSync(tmp, RUNNER_YAML)
  await pwRun(['workflows', 'create', '--yaml', tmp, RUNNER_WORKFLOW], 60_000)
  runnerEnsured = true
}

function slug(s: string): string {
  return s.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 40) || 'task'
}

function post(m: Task, from: string, topic: string, body: string): void {
  const msg: BoardMsg = { seq: ++m.seq, at: new Date().toISOString(), from, topic, body: body.slice(0, 4000) }
  m.board.push(msg)
  if (m.board.length > 500) m.board.splice(0, m.board.length - 500)
  try {
    fs.mkdirSync(path.join(MISSIONS_DIR, m.id), { recursive: true })
    fs.appendFileSync(path.join(MISSIONS_DIR, m.id, 'board.jsonl'), JSON.stringify(msg) + '\n')
    // The snapshot is what makes a task survive a restart: every state
    // change routes through a post, so writing it here keeps disk and
    // memory in step without a second bookkeeping path. The token is
    // deliberately not in it; a rehydrated task is history, not a live
    // board, and history needs no bearer.
    fs.writeFileSync(path.join(MISSIONS_DIR, m.id, 'state.json'), JSON.stringify(taskSummary(m)))
  } catch { /* the in-memory board still serves the viewer */ }
}

/**
 * Tasks from before this process started, loaded back so a restart does
 * not amnesia a campaign the operator was watching. Anything recorded as
 * still working was killed with the old process, and saying so plainly
 * beats a spinner that never resolves.
 */
function rehydrate(): void {
  let dirs: string[] = []
  try { dirs = fs.readdirSync(MISSIONS_DIR) } catch { return }
  for (const id of dirs.slice(-100)) {
    if (tasks.has(id)) continue
    try {
      const snap = JSON.parse(fs.readFileSync(path.join(MISSIONS_DIR, id, 'state.json'), 'utf8')) as ReturnType<typeof taskSummary>
      const m: Task = {
        id: snap.id, objective: snap.objective, model: snap.model, createdAt: snap.createdAt,
        state: snap.state, maxAgents: snap.maxAgents, maxDepth: snap.maxDepth,
        execution: (snap as { execution?: 'local' | 'campaign' }).execution ?? 'local',
        resource: (snap as { resource?: string | null }).resource ?? null,
        runtime: (snap as { runtime?: AgentRuntime }).runtime ?? 'runner',
        nodes: new Map(), board: [], seq: 0, procs: new Map(), pollers: new Map(), token: '', shared: [],
      }
      let reattached = 0
      for (const a of snap.agents ?? []) {
        if (a.state === 'working' || a.state === 'input-required') {
          if (a.sessionId) {
            // A session lives in the pw code daemon, not in this process,
            // so it kept working through the restart; follow it again.
            a.note = 'reattached after a server restart'
            reattached++
          } else if (m.execution === 'campaign' && a.runSlug) {
            // The worker is a platform run that never depended on this
            // process; pick up watching it where the old process left off.
            a.note = 'reattached after a server restart'
            reattached++
          } else {
            a.state = 'canceled'; a.note = 'interrupted by a server restart'
          }
        }
        m.nodes.set(a.name, a)
      }
      if ((m.state === 'working' || m.state === 'submitted') && reattached === 0) m.state = 'canceled'
      if (reattached > 0) {
        for (const a of m.nodes.values()) {
          if ((a.state === 'working' || a.state === 'input-required') && a.sessionId) watchSessionAgent(m, a)
          else if (a.state === 'working' && a.runSlug) watchCampaignAgent(m, a)
        }
      }
      try {
        const lines = fs.readFileSync(path.join(MISSIONS_DIR, id, 'board.jsonl'), 'utf8').trim().split('\n').slice(-500)
        m.board = lines.map(l => JSON.parse(l) as BoardMsg)
        m.seq = m.board.length ? m.board[m.board.length - 1].seq : 0
      } catch { /* a snapshot with no board still lists */ }
      tasks.set(id, m)
    } catch { /* a directory without a snapshot predates snapshots */ }
  }
}
rehydrate()

function agentCount(m: Task): number { return m.nodes.size }

/** Listeners told when a task reaches a terminal state; the fleet ticks on it. */
const finishListeners: ((m: Task) => void)[] = []
export function onTaskFinished(fn: (m: Task) => void): void { finishListeners.push(fn) }
function notifyFinished(m: Task): void { for (const fn of finishListeners) { try { fn(m) } catch { /* a listener must not break the engine */ } } }

function maybeFinish(m: Task): void {
  if (m.state !== 'working') return
  const alive = [...m.nodes.values()].some(p => p.state === 'working' || p.state === 'input-required')
  if (!alive) {
    m.state = 'completed'
    post(m, 'orchestrator', 'task', 'Every agent has finished.')
    notifyFinished(m)
  }
}

/**
 * The result contract. The agent's final output is asked for as JSON with
 * a markdown result and an optional subtask list; a reply that is not
 * JSON is kept whole as the result, so a model that ignores the contract
 * still produces a usable outcome instead of an error.
 */
function agentPromptFor(m: Task, personaName: string, objective: string, board: boolean): string {
  const p = personas().find(x => x.name === personaName)
  const personaText = p ? (() => {
    try {
      const dir = path.join(KB_ROOT, '.agents')
      return fs.readFileSync(path.join(dir, `${p.file}`), 'utf8').replace(/^---\n[\s\S]*?\n---\n?/, '').trim()
    } catch { return '' }
  })() : ''
  return [
    personaText && `Adopt this persona for the task:\n${personaText}`,
    `You are one agent in a coordinated task. Task objective: ${m.objective}`,
    `Your task: ${objective}`,
    // Only an agent that has the board is told to use it; one without it
    // would spend turns calling tools that are not there.
    board && 'As you work, call the task MCP tool board_status before each major step with a one-line note of what you are doing; that note is what the operator watching the task sees. Post findings other agents need with board_post. The same server also carries the knowledge base search and read tools.',
    'Work headlessly and do not ask questions. When finished, output ONLY a JSON object:',
    '{"result": "<your findings or work product, as markdown>", "subtasks": [{"persona": "<optional persona name>", "objective": "<subtask>"}]}',
    'Include subtasks only when part of your task genuinely needs a separate agent; most tasks need none.',
  ].filter(Boolean).join('\n\n')
}

/**
 * One completion path for both worker kinds. Three output shapes are
 * tolerated: the agent's contract JSON at top level, the CLI's envelope
 * with the contract (or plain text) inside, and plain text. Unwrapping
 * must not discard the contract's subtasks, which is exactly what a naive
 * result-first unwrap does.
 */
function completeAgent(m: Task, p: AgentTask, rawText: string): void {
  const text = rawText.trim()
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
    const used = usageOf(env)
    if (used) p.usage = used
    if (!tryContract(env)) {
      const unwrapped = String((env as { content?: unknown; output?: unknown; message?: unknown }).content
        ?? (env as { output?: unknown }).output ?? (env as { message?: unknown }).message ?? '')
      if (unwrapped) {
        result = unwrapped
        try { tryContract(JSON.parse(unwrapped)) } catch { /* plain text inside the envelope */ }
      }
    }
  } catch { /* plain text output */ }

  // The result is corpus material under the task.
  const relDir = path.join('tasks', m.id)
  const rel = path.join(relDir, `${p.name}.md`)
  try {
    fs.mkdirSync(path.join(KB_ROOT, relDir), { recursive: true })
    fs.writeFileSync(path.join(KB_ROOT, rel),
      `---\nmission: ${m.id}\nagent: ${p.name}\npersona: ${p.persona}\n---\n\n# ${p.objective}\n\n${result}\n`)
    p.resultPath = rel
  } catch { /* the board still carries the text */ }
  void import('./indexing.js').then(x => x.incrementalIndexDir(relDir)).catch(() => {})

  p.state = 'completed'
  p.note = 'finished'
  p.updatedAt = new Date().toISOString()
  post(m, p.name, 'complete', p.resultPath ? `Result at ${p.resultPath}` : result.slice(0, 1500))

  // Sub-spawning, inside the same server-enforced limits.
  for (const st of subtasks.slice(0, 5)) {
    const objective = String(st?.objective ?? '').slice(0, 2000)
    if (!objective) continue
    try {
      spawnAgent(m, { persona: String(st?.persona ?? p.persona), objective, parent: p.name, depth: p.depth + 1 })
    } catch (e) {
      post(m, 'orchestrator', 'refused', `Subtask from ${p.name} refused: ${String((e as Error).message)}`)
    }
  }
  maybeFinish(m)
}

/** What the poller extracts from a campaign run's log: everything the
 *  runner bracketed between its markers. */
export function extractRunnerResult(log: string): string | null {
  const m = /STUDIO_RESULT_BEGIN\n([\s\S]*?)\nSTUDIO_RESULT_END/.exec(log)
  return m ? m[1].trim() : null
}

const RUN_TERMINAL = new Set(['completed', 'error', 'failed', 'canceled', 'cancelled'])

/**
 * Watch one campaign agent's platform run. The poll writes the run log
 * into the same live.log the local path streams to, so the viewer's
 * drill-down is identical for both worker kinds, and on a terminal state
 * the bracketed output goes through the shared completion path.
 */
function watchCampaignAgent(m: Task, p: AgentTask): void {
  const workdir = path.join(MISSIONS_DIR, m.id, 'work', p.name)
  fs.mkdirSync(workdir, { recursive: true })
  const live = path.join(workdir, 'live.log')
  const tick = async () => {
    if (p.state !== 'working') { clearPoller(m, p.name); return }
    let doc: { status?: string; executedJobs?: Record<string, { status?: string; steps?: { name?: string; status?: string }[] }> }
    try {
      doc = jsonHead(await pwRun(['workflows', 'runs', 'view', String(p.runSlug), '-o', 'json']))
    } catch { return /* transient; next tick */ }
    const job = doc.executedJobs?.agent
    const step = (job?.steps ?? []).find(st => st?.status && st.status !== 'completed')
    p.note = `run ${p.runSlug}: ${doc.status ?? 'unknown'}${job?.status ? `, job ${job.status}` : ''}${step?.name ? ` (${step.name})` : ''}`
    p.updatedAt = new Date().toISOString()
    let log = ''
    try {
      log = await pwRun(['workflows', 'runs', 'logs', String(p.runSlug)], 60_000)
      fs.writeFileSync(live, log)
    } catch { /* the note still updates */ }
    if (!RUN_TERMINAL.has(String(doc.status))) return
    clearPoller(m, p.name)
    if (p.state !== 'working') return
    const bracketed = extractRunnerResult(log)
    if (doc.status === 'completed' && bracketed !== null) {
      completeAgent(m, p, bracketed)
    } else {
      p.state = 'failed'
      p.note = `run ${p.runSlug} ${doc.status}${bracketed === null ? ', no result markers in the log' : ''}`
      p.updatedAt = new Date().toISOString()
      post(m, p.name, 'failed', p.note)
      maybeFinish(m)
    }
  }
  const h = setInterval(() => { void tick() }, CAMPAIGN_POLL_MS)
  // Watching must never be the reason the process cannot exit: an
  // un-unref'd interval holds the event loop open, which surfaced as the
  // test runner hanging after every test had finished.
  h.unref?.()
  m.pollers.set(p.name, h)
  void tick()
}

function clearPoller(m: Task, name: string): void {
  const h = m.pollers.get(name)
  if (h) { clearInterval(h); m.pollers.delete(name) }
}

function spawnAgent(m: Task, opts: { persona: string; objective: string; parent: string | null; depth: number }): string {
  if (m.state !== 'working') throw new Error('task is not running')
  if (agentCount(m) >= m.maxAgents) throw new Error(`agent ceiling reached (${m.maxAgents})`)
  if (opts.depth > m.maxDepth) throw new Error(`depth limit reached (${m.maxDepth})`)
  const name = `${slug(opts.persona || 'agent')}-${agentCount(m) + 1}`
  const p: AgentTask = {
    name, persona: opts.persona, objective: opts.objective, parent: opts.parent, depth: opts.depth,
    state: 'working', startedAt: new Date().toISOString(), updatedAt: new Date().toISOString(),
    note: 'starting', resultPath: null, runSlug: null,
  }
  m.nodes.set(name, p)
  post(m, 'orchestrator', 'spawn', `${name} (persona ${opts.persona || 'none'}, depth ${opts.depth}${opts.parent ? `, child of ${opts.parent}` : ''}${m.execution === 'campaign' ? `, campaign on ${m.resource}` : ''}): ${opts.objective}`)

  if (m.execution === 'campaign') {
    // The worker is a platform workflow run on the task's system, not a
    // child of this process. Submission is async; the agent shows as
    // working from the start and the watcher takes over from submission.
    void (async () => {
      try {
        await ensureRunnerWorkflow()
        const inputs = {
          resource: m.resource,
          prompt: agentPromptFor(m, opts.persona, opts.objective, false),
          model: m.model,
          agent_name: name,
          // No board for a remote runner: the Studio's public address sits
          // behind the platform's session proxy, which turns away a task
          // token, and pw code ignores the workspace settings file the
          // runner would register it in. Sending the token would only put
          // it in the run's inputs.
          board_url: '',
          board_token: '',
          timeout_seconds: 7200,
        }
        // A campaign submits many agents in a row; the first one to find
        // the user workspace scaled down starts it and waits, the rest
        // then go straight through.
        const { value: out, started } = await withWorkspace({ cli: pwRun }, () => pwRun(['workflows', 'run', '-i', JSON.stringify(inputs), RUNNER_WORKFLOW, '-o', 'json'], 180_000))
        if (started) post(m, 'orchestrator', 'status', 'the platform user workspace was not running; it was started and the submission retried')
        const run = (jsonHead<{ run?: { slug?: string; id?: string } }>(out).run ?? jsonHead<{ slug?: string; id?: string }>(out)) as { slug?: string; id?: string }
        p.runSlug = String(run.slug ?? run.id ?? '')
        if (!p.runSlug) throw new Error('submission returned no run identifier')
        post(m, 'orchestrator', 'spawn', `${name} submitted as run ${p.runSlug}`)
        watchCampaignAgent(m, p)
      } catch (e) {
        p.state = 'failed'
        p.note = `submission failed: ${String((e as Error).message).slice(0, 200)}`
        p.updatedAt = new Date().toISOString()
        post(m, name, 'failed', p.note)
        maybeFinish(m)
      }
    })()
    return name
  }

  if (m.runtime === 'session') {
    spawnSessionAgent(m, p, agentPromptFor(m, opts.persona, opts.objective, true))
    return name
  }

  // The runner: a one-shot pw code run beside the server. It has no board:
  // pw code loads a workspace settings file only after someone approves it
  // interactively, so registering the board there never worked, and the
  // task token would have to go in the command line, where any user on
  // the host can read it. Session agents carry the board instead.
  const workdir = path.join(MISSIONS_DIR, m.id, 'work', name)
  fs.mkdirSync(workdir, { recursive: true })
  const args = ['code', '-p', agentPromptFor(m, opts.persona, opts.objective, false), '-o', 'json',
    '--permission-mode', 'read-only', '-w', workdir, '-m', m.model]
  const live = path.join(workdir, 'live.log')
  const child = execFile(PW_CLI, args, { timeout: AGENT_TIMEOUT_MS, maxBuffer: 16 * 1024 * 1024 }, (err, stdout) => {
    m.procs.delete(name)
    p.updatedAt = new Date().toISOString()
    if (p.state === 'canceled') { maybeFinish(m); return }
    if (err && !stdout) {
      p.state = 'failed'
      p.note = String(err.message).slice(0, 200)
      post(m, name, 'failed', p.note)
      maybeFinish(m)
      return
    }
    completeAgent(m, p, String(stdout ?? '').trim())
  })
  // Everything the process says, as it says it. The CLI's structured
  // result arrives on stdout only at the end, but stderr narrates along
  // the way, and either way the log is the drill-down behind the agent's
  // one-line note in the viewer.
  const append = (buf: Buffer) => { try { fs.appendFileSync(live, buf) } catch { /* viewer just shows less */ } }
  child.stdout?.on('data', append)
  child.stderr?.on('data', append)
  m.procs.set(name, child)
  return name
}

function failAgent(m: Task, p: AgentTask, note: string): void {
  clearPoller(m, p.name)
  if (p.state !== 'working' && p.state !== 'input-required') return
  p.state = 'failed'
  p.note = note.slice(0, 300)
  p.approvals = null
  p.updatedAt = new Date().toISOString()
  post(m, p.name, 'failed', p.note)
  maybeFinish(m)
}

/** One history item as a line of the agent's live log. */
function historyLine(h: pwcode.HistoryItem): string | null {
  const text = (h.text ?? '').trim()
  switch (h.kind) {
    case 'user': return `> ${text}`
    case 'assistant': return text || null
    case 'tool_call': return `[${h.toolName ?? 'tool'}] ${(h.toolArgs ?? '').slice(0, 400)}`
    case 'tool_result': return text ? `  result: ${text.slice(0, 600)}` : null
    case 'notice': return text ? `! ${text}` : null
    default: return null
  }
}

/** What an approval request asks, in one line for the board and the viewer. */
function describeApproval(a: pwcode.ApprovalRequest): string {
  return [a.header || a.kind, a.command || a.question || a.reason].filter(Boolean).join(': ').slice(0, 400)
}

/**
 * A session agent: a pw code session in the local daemon, created with the
 * board registered inline and the board and corpus tools pre-approved, so
 * the read-only agent can search and report without asking. The request
 * goes over the daemon's owner-only socket, so the task token appears in
 * no process's arguments and no file. Remote control is on, so the
 * session can also be watched or driven from ACTIVATE's agents page.
 */
function spawnSessionAgent(m: Task, p: AgentTask, prompt: string): void {
  const workdir = path.join(MISSIONS_DIR, m.id, 'work', p.name)
  fs.mkdirSync(workdir, { recursive: true })
  void (async () => {
    try {
      await pwcode.ensureDaemon()
      const { taskToolNames } = await import('./mcp.js')
      const agentsJson = JSON.stringify({
        [SESSION_AGENT]: {
          description: 'An agent working one subtask of an ACTIVATE Studio task',
          mcpServers: [{ task: { type: 'http', url: `${LOCAL_URL}/api/mcp`, headers: { Authorization: `Bearer ${m.token}`, 'X-Task-Agent': p.name } } }],
        },
      })
      const session = await pwcode.createSession({
        workspace: workdir, model: m.model, permissionMode: 'read-only',
        agent: SESSION_AGENT, agentsJson,
        allowedTools: taskToolNames().map(t => `mcp__task__${t}`),
        remoteControl: true,
      })
      p.sessionId = session.id
      p.note = 'session started'
      post(m, 'orchestrator', 'spawn', `${p.name} runs as pw code session ${session.id}`)
      if (p.state !== 'working') { void pwcode.interruptSession(session.id).catch(() => {}); return }
      const servers = await pwcode.waitForMcp(session.id)
      const board = servers.find(x => x.name === 'task')
      if (board && !board.connected) {
        post(m, 'orchestrator', 'status', `${p.name}: the board did not connect (${board.err || 'no reason given'}); the agent works without it`)
      }
      await pwcode.sendMessage(session.id, prompt)
      watchSessionAgent(m, p)
    } catch (e) {
      failAgent(m, p, `the pw code session could not start: ${String((e as Error).message ?? e)}`)
    }
  })()
}

/**
 * Follow one session agent. Each poll appends the session's new history to
 * the agent's live log, turns approval requests into input-required with
 * the request on the board, and on the end of the turn completes the agent
 * from its last reply through the shared completion path.
 */
function watchSessionAgent(m: Task, p: AgentTask): void {
  const workdir = path.join(MISSIONS_DIR, m.id, 'work', p.name)
  fs.mkdirSync(workdir, { recursive: true })
  const live = path.join(workdir, 'live.log')
  const started = Date.now()
  let shown = 0
  let active = false
  let misses = 0
  let busy = false
  const tick = async () => {
    if (busy) return
    if (p.state !== 'working' && p.state !== 'input-required') { clearPoller(m, p.name); return }
    busy = true
    try {
      let d: pwcode.SessionDetail
      try {
        d = await pwcode.getSession(String(p.sessionId))
        misses = 0
      } catch (e) {
        if (++misses >= 15) failAgent(m, p, `lost the pw code session: ${String((e as Error).message ?? e)}`)
        return
      }
      const hist = d.history ?? []
      if (hist.length < shown) { shown = 0; try { fs.writeFileSync(live, '') } catch { /* next tick */ } }
      if (hist.length > shown) {
        const lines = hist.slice(shown).map(historyLine).filter((l): l is string => l !== null)
        if (lines.length) { try { fs.appendFileSync(live, lines.join('\n') + '\n') } catch { /* the viewer shows less */ } }
        shown = hist.length
      }
      if (d.status !== 'idle' || hist.length > 0) active = true
      p.updatedAt = new Date().toISOString()

      const pending = d.approvals ?? []
      if (pending.length) {
        const known = new Set((p.approvals ?? []).map(a => a.id))
        p.approvals = pending.map(a => ({ id: a.id, kind: a.kind, text: describeApproval(a) }))
        p.state = 'input-required'
        p.note = 'waiting for approval'
        for (const a of pending) if (!known.has(a.id)) post(m, p.name, 'input-required', `needs approval: ${describeApproval(a)}`)
        return
      }
      if (p.state === 'input-required') { p.state = 'working'; p.approvals = null; p.note = 'working' }
      const lastTool = [...hist].reverse().find(h => h.kind === 'tool_call')
      if (d.status === 'running' && lastTool && (p.note === 'session started' || p.note.startsWith('calling '))) {
        p.note = `calling ${lastTool.toolName}`
      }

      if (d.status === 'idle') {
        const lastUser = hist.map(h => h.kind).lastIndexOf('user')
        const reply = hist.slice(lastUser + 1).reverse().find(h => h.kind === 'assistant' && h.fromMain !== false && (h.text ?? '').trim())
        if (reply) {
          clearPoller(m, p.name)
          try {
            const st = await pwcode.sessionState(String(p.sessionId))
            const used = usageOf({ input_tokens: st?.inputTokens, output_tokens: st?.outputTokens })
            if (used) p.usage = used
          } catch { /* usage is optional */ }
          completeAgent(m, p, String(reply.text))
          return
        }
        // A turn that fails at once (an expired login, an unknown model)
        // ends before a poll ever sees it running and leaves no reply.
        if (active || Date.now() - started > 10_000) {
          failAgent(m, p, 'the session ended its turn without a reply; the model or the platform login on this host may need attention (see the session in pw code for its error)')
          return
        }
      }
      if (Date.now() - started > AGENT_TIMEOUT_MS) {
        void pwcode.interruptSession(String(p.sessionId)).catch(() => {})
        failAgent(m, p, `timed out after ${Math.round(AGENT_TIMEOUT_MS / 60_000)} minutes`)
      }
    } finally { busy = false }
  }
  const h = setInterval(() => { void tick() }, SESSION_POLL_MS)
  h.unref?.()
  m.pollers.set(p.name, h)
  void tick()
}

/** A person's answer to an agent's approval request, passed to its session. */
export async function answerAgentApproval(id: string, agent: string, approvalId: string, allowed: boolean, denyMessage?: string): Promise<void> {
  const m = tasks.get(id)
  const p = m?.nodes.get(agent)
  if (!m || !p) throw new Error('no such task or agent')
  if (!p.sessionId || p.state !== 'input-required') throw new Error('this agent is not waiting for an approval')
  if (!(p.approvals ?? []).some(a => a.id === approvalId)) throw new Error('no such approval request')
  await pwcode.answerApproval(p.sessionId, approvalId, { allowed, ...(allowed ? {} : { denyMessage: denyMessage || 'Denied by the operator in the Studio.' }) })
  p.approvals = (p.approvals ?? []).filter(a => a.id !== approvalId)
  if (!p.approvals.length) { p.approvals = null; p.state = 'working'; p.note = allowed ? 'approved; working' : 'denied; working' }
  post(m, 'orchestrator', 'status', `${agent}: request ${allowed ? 'approved' : 'denied'} by the operator`)
}

/** A person's direction to a working session agent, delivered mid-turn. */
export async function steerAgent(id: string, agent: string, text: string): Promise<boolean> {
  const m = tasks.get(id)
  const p = m?.nodes.get(agent)
  if (!m || !p) throw new Error('no such task or agent')
  if (!p.sessionId || (p.state !== 'working' && p.state !== 'input-required')) throw new Error('this agent is not a working session')
  const clean = text.trim().slice(0, 4000)
  if (!clean) throw new Error('nothing to send')
  const r = await pwcode.steerSession(p.sessionId, clean)
  post(m, 'operator', 'direction', `to ${agent}: ${clean}`)
  return r?.delivered !== false
}

/** Which way a new task's agents run, from the deployment's setting and,
 *  when it allows both, the request. Remote sessions arrive with the
 *  platform's agents routes, so a campaign always uses the runner. */
export function resolveRuntime(requested: unknown, execution: 'local' | 'campaign'): AgentRuntime {
  if (execution === 'campaign') return 'runner'
  const mode = effectiveSettings().agentExecution
  if (mode === 'runner') return 'runner'
  if (mode === 'sessions') return 'session'
  return requested === 'runner' ? 'runner' : 'session'
}

/** The tail of one agent's live output, for the viewer's drill-down. */
export function agentOutput(id: string, agent: string): string | null {
  const m = tasks.get(id)
  if (!m || !m.nodes.has(agent)) return null
  try {
    const f = path.join(MISSIONS_DIR, id, 'work', agent, 'live.log')
    const size = fs.statSync(f).size
    const fd = fs.openSync(f, 'r')
    const want = Math.min(size, 16 * 1024)
    const buf = Buffer.alloc(want)
    fs.readSync(fd, buf, 0, want, size - want)
    fs.closeSync(fd)
    return buf.toString('utf8')
  } catch { return '' }
}

export function createTask(opts: {
  objective: string; model: string
  agents: { persona?: string; objective: string }[]
  maxAgents?: number; maxDepth?: number
  execution?: 'local' | 'campaign'; resource?: string | null
  runtime?: AgentRuntime
}): Task {
  if (opts.execution === 'campaign' && !opts.resource) {
    throw new Error('campaign execution needs a resource (a connected system to run the agents on)')
  }
  const id = `${new Date().toISOString().slice(0, 10)}-${slug(opts.objective)}-${Math.random().toString(36).slice(2, 6)}`
  const m: Task = {
    id, objective: opts.objective.slice(0, 2000), model: opts.model, createdAt: new Date().toISOString(),
    state: 'working',
    maxAgents: Math.min(Math.max(Number(opts.maxAgents) || 10, 1), MAX_AGENTS_CAP),
    // 0 is a real setting (agents may not start subtasks), so only a
    // missing or non-numeric depth takes the default.
    maxDepth: Math.min(Math.max(Number.isFinite(Number(opts.maxDepth ?? NaN)) ? Number(opts.maxDepth) : 2, 0), MAX_DEPTH_CAP),
    execution: opts.execution === 'campaign' ? 'campaign' : 'local',
    resource: opts.resource ?? null,
    runtime: resolveRuntime(opts.runtime, opts.execution === 'campaign' ? 'campaign' : 'local'),
    nodes: new Map(), board: [], seq: 0, procs: new Map(), pollers: new Map(),
    // A task-scoped bearer, not a platform credential: it authorizes
    // exactly the board of one task, for as long as that task
    // exists, so a worker never holds anything wider than its own job.
    token: randomBytes(24).toString('hex'),
    shared: [],
  }
  tasks.set(id, m)
  post(m, 'orchestrator', 'task', `Objective: ${m.objective} (up to ${m.maxAgents} agents, depth ${m.maxDepth}; ${m.runtime === 'session' ? 'agents run as pw code sessions' : 'agents run as one-shot pw code runs'})`)
  for (const a of opts.agents.slice(0, m.maxAgents)) {
    spawnAgent(m, { persona: String(a.persona ?? ''), objective: String(a.objective ?? '').slice(0, 2000), parent: null, depth: 0 })
  }
  return m
}

export function stopTask(id: string): boolean {
  const m = tasks.get(id)
  if (!m) return false
  m.state = 'canceled'
  queueMicrotask(() => notifyFinished(m))
  for (const [name, proc] of m.procs) {
    const p = m.nodes.get(name)
    if (p) { p.state = 'canceled'; p.note = 'stopped by user' }
    try { proc.kill('SIGTERM') } catch { /* already gone */ }
  }
  m.procs.clear()
  for (const [name, h] of m.pollers) {
    clearInterval(h)
    const p = m.nodes.get(name)
    if (p && (p.state === 'working' || p.state === 'input-required')) {
      p.state = 'canceled'; p.note = 'stopped by user'; p.approvals = null
      if (p.sessionId) void pwcode.interruptSession(p.sessionId).catch(() => {})
      // The run outlives this process by design, so stopping the task
      // must reach out and cancel it, or the fleet keeps billing.
      if (p.runSlug) void pwRun(['workflows', 'runs', 'cancel', p.runSlug], 60_000).catch(() => {})
    }
  }
  m.pollers.clear()
  post(m, 'orchestrator', 'task', 'Stopped by user.')
  return true
}

export function taskSummary(m: Task) {
  return {
    id: m.id, objective: m.objective, model: m.model, state: m.state,
    createdAt: m.createdAt, maxAgents: m.maxAgents, maxDepth: m.maxDepth,
    execution: m.execution, resource: m.resource, runtime: m.runtime,
    usage: sumUsage(m),
    agents: [...m.nodes.values()],
  }
}

export function listTasks() {
  return [...tasks.values()].map(m => ({
    id: m.id, objective: m.objective, state: m.state, createdAt: m.createdAt,
    usage: sumUsage(m),
    execution: m.execution, resource: m.resource, runtime: m.runtime,
    agents: m.nodes.size,
    running: [...m.nodes.values()].filter(p => p.state === 'working' || p.state === 'input-required').length,
    waiting: [...m.nodes.values()].filter(p => p.state === 'input-required').length,
  }))
}

export function getTask(id: string): Task | undefined { return tasks.get(id) }

/** Resolve a task bearer to its task. */
export function taskByToken(token: string): Task | undefined {
  if (!token) return undefined
  for (const m of tasks.values()) if (m.token === token) return m
  return undefined
}

/** Board operations available to workers over MCP. */
export const board = {
  post(m: Task, from: string, topic: string, body: string) {
    post(m, from, topic || 'note', body)
    return { ok: true }
  },
  read(m: Task, since: number, limit = 50) {
    return m.board.filter(b => b.seq > since).slice(-limit)
  },
  status(m: Task, from: string, note: string) {
    const p = m.nodes.get(from)
    if (p) { p.note = note.slice(0, 200); p.updatedAt = new Date().toISOString() }
    post(m, from, 'status', note)
    return { ok: true }
  },
  addTask(m: Task, objective: string, persona = '') {
    const t: SharedTask = { id: `s${m.shared.length + 1}`, objective: objective.slice(0, 2000), persona, claimedBy: null, done: false, resultPath: null }
    m.shared.push(t)
    post(m, 'orchestrator', 'task', `${t.id} offered: ${t.objective}`)
    return t
  },
  /** Atomic by construction: this runs on the server's single thread, so
   *  the check and the write cannot interleave with another claim. That
   *  is what makes many workers on one queue safe. */
  claim(m: Task, from: string, taskId?: string) {
    const t = taskId ? m.shared.find(x => x.id === taskId) : m.shared.find(x => !x.claimedBy && !x.done)
    if (!t) return { claimed: null as SharedTask | null, reason: 'no unclaimed task' }
    if (t.claimedBy) return { claimed: null as SharedTask | null, reason: `already claimed by ${t.claimedBy}` }
    t.claimedBy = from
    post(m, from, 'claim', `${t.id}: ${t.objective}`)
    return { claimed: t, reason: '' }
  },
  complete(m: Task, from: string, taskId: string, resultPath: string) {
    const t = m.shared.find(x => x.id === taskId)
    if (!t) return { ok: false, reason: 'no such task' }
    t.done = true
    t.resultPath = resultPath || null
    post(m, from, 'complete', `${t.id} done${resultPath ? ` -> ${resultPath}` : ''}`)
    return { ok: true }
  },
}

export async function taskRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/tasks', async () => ({ tasks: listTasks() }))

  app.get('/api/tasks/:id', async (req, reply) => {
    const m = tasks.get((req.params as { id: string }).id)
    if (!m) return reply.status(404).send({ error: 'no such task' })
    const since = Number((req.query as { since?: string }).since ?? 0)
    return { ...taskSummary(m), board: m.board.filter(b => b.seq > since) }
  })

  app.post('/api/tasks', async (req, reply) => {
    const b = (req.body ?? {}) as { objective?: string; model?: string; agents?: { persona?: string; objective: string }[]; maxAgents?: number; maxDepth?: number; runtime?: AgentRuntime }
    if (!b.objective || !b.model || !Array.isArray(b.agents) || b.agents.length === 0) {
      return reply.status(400).send({ error: 'objective, model, and at least one agent are required' })
    }
    try {
      const m = createTask({ objective: b.objective, model: b.model, agents: b.agents, maxAgents: b.maxAgents, maxDepth: b.maxDepth, runtime: b.runtime })
      return taskSummary(m)
    } catch (e) {
      return reply.status(400).send({ error: String((e as Error).message) })
    }
  })

  app.get('/api/tasks/:id/agents/:name/output', async (req, reply) => {
    const { id, name } = req.params as { id: string; name: string }
    const tail = agentOutput(id, name)
    if (tail === null) return reply.status(404).send({ error: 'no such task or agent' })
    return { tail }
  })

  // What each way of running agents can reach right now, for Settings and
  // for choosing per task.
  app.get('/api/tasks/runtime', async () => {
    const s = await pwcode.daemonStatus()
    const reason = pwcode.unusableReason(s)
    return {
      mode: effectiveSettings().agentExecution,
      local: s
        ? { available: !reason, reason, version: s.version, protocol: s.protocolVersion, remoteStart: s.remoteStart, remoteMaxPermissionMode: s.remoteMaxPermissionMode }
        // Nothing answering is not a dead end: the first session agent starts the daemon.
        : { available: true, reason: 'no daemon is running yet; the first session agent starts one', version: null, protocol: null, remoteStart: null, remoteMaxPermissionMode: null },
    }
  })

  app.post('/api/tasks/:id/agents/:name/approvals/:approvalId', async (req, reply) => {
    const { id, name, approvalId } = req.params as { id: string; name: string; approvalId: string }
    const b = (req.body ?? {}) as { allowed?: boolean; denyMessage?: string }
    try {
      await answerAgentApproval(id, name, approvalId, b.allowed === true, b.denyMessage)
      return { ok: true }
    } catch (e) {
      return reply.status(409).send({ error: String((e as Error).message ?? e) })
    }
  })

  app.post('/api/tasks/:id/agents/:name/direction', async (req, reply) => {
    const { id, name } = req.params as { id: string; name: string }
    try {
      return { delivered: await steerAgent(id, name, String((req.body as { text?: string } | undefined)?.text ?? '')) }
    } catch (e) {
      return reply.status(409).send({ error: String((e as Error).message ?? e) })
    }
  })

  app.post('/api/tasks/:id/stop', async (req, reply) => {
    const ok = stopTask((req.params as { id: string }).id)
    return ok ? { ok } : reply.status(404).send({ error: 'no such task' })
  })
}
