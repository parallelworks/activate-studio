// Session agents: delegated agents run as sessions in the pw code daemon.
// A fake daemon on a Unix socket stands in for pw code, so the request the
// Studio sends, the history it follows, approvals, steering, and stopping
// are all exercised without a model.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'

const kb = fs.mkdtempSync(path.join(os.tmpdir(), 'sess-kb-'))
const idx = fs.mkdtempSync(path.join(os.tmpdir(), 'sess-idx-'))
const sock = path.join(fs.mkdtempSync(path.join(os.tmpdir(), 'sess-sock-')), 'code.sock')
process.env.KB_ROOT = kb
process.env.INDEX_BASE = idx
process.env.PW_CODE_SOCKET = sock
process.env.STUDIO_SESSION_POLL_MS = '40'
process.env.AGENT_EXECUTION = 'sessions'
process.env.PORT = '4999'

// ---- the fake daemon ----
const sessions = new Map()
const seen = { creates: [], messages: [], answers: [], directions: [], interrupts: [] }
let n = 0
const reply = text => ({ kind: 'assistant', fromMain: true, text })
function behave(s, text) {
  s.history.push({ kind: 'user', fromMain: true, text })
  if (text.includes('needs-approval')) {
    s.status = 'awaiting_approval'
    s.history.push({ kind: 'tool_call', fromMain: true, toolName: 'Bash', toolArgs: '{"command":"ls /data"}' })
    s.approvals = [{ id: 'ap1', kind: 'confirm', header: 'Shell command', command: 'ls /data' }]
  } else if (text.includes('fail-fast')) {
    s.status = 'idle' // the turn ended before anything was recorded, like an expired login
  } else if (text.includes('slow')) {
    s.status = 'running'
    s.history.push({ kind: 'tool_call', fromMain: true, toolName: 'mcp__task__search_kb', toolArgs: '{"query":"x"}' })
  } else {
    s.status = 'running'
    s.history.push({ kind: 'tool_call', fromMain: true, toolName: 'mcp__task__search_kb', toolArgs: '{"query":"decks"}' })
    s.history.push({ kind: 'tool_result', fromMain: true, text: '3 hits' })
    setTimeout(() => { s.history.push(reply('{"result": "session work done"}')); s.status = 'idle' }, 120)
  }
}
const daemon = http.createServer((req, res) => {
  let body = ''
  req.on('data', c => { body += c })
  req.on('end', () => {
    const send = (code, obj) => { res.writeHead(code, { 'Content-Type': 'application/json' }); res.end(obj === undefined ? '' : JSON.stringify(obj)) }
    const b = body ? JSON.parse(body) : {}
    const u = req.url
    if (u === '/v1/status') return send(200, { protocolVersion: 2, version: 'v-test', pid: 1, hostname: 'node-7', sessions: sessions.size, remoteStart: true, remoteMaxPermissionMode: 'read-only' })
    if (req.method === 'POST' && u === '/v1/sessions') {
      const id = `sess-${++n}`
      seen.creates.push(b)
      sessions.set(id, { id, status: 'idle', history: [], approvals: null, workspace: b.workspace, model: b.model, messages: 0 })
      return send(200, sessions.get(id))
    }
    const m = /^\/v1\/sessions\/([^/]+)(?:\/(.*))?$/.exec(u)
    const s = m && sessions.get(decodeURIComponent(m[1]))
    if (!s) return send(404, { detail: 'no such session' })
    const rest = m[2] ?? ''
    if (req.method === 'GET' && rest === '') return send(200, s)
    // Like the real daemon: right after creation the server is neither
    // connecting nor connected, then connects.
    if (req.method === 'GET' && rest === 'mcp') {
      s.mcpPolls = (s.mcpPolls ?? 0) + 1
      return send(200, [{ name: 'task', connected: s.mcpPolls > 2, connecting: s.mcpPolls === 2 }])
    }
    if (req.method === 'GET' && rest === 'state') return send(200, { inputTokens: 1200, outputTokens: 300 })
    if (req.method === 'POST' && rest === 'messages') { seen.messages.push({ id: s.id, text: b.text, mcpConnected: (s.mcpPolls ?? 0) > 2 }); behave(s, b.text); return send(200, { queued: false, turnId: 't1' }) }
    if (req.method === 'POST' && rest.startsWith('approvals/')) {
      seen.answers.push({ id: s.id, approval: rest.split('/')[1], ...b })
      s.approvals = null; s.status = 'running'
      setTimeout(() => { s.history.push(reply('{"result": "approved work done"}')); s.status = 'idle' }, 80)
      return send(204)
    }
    if (req.method === 'POST' && rest === 'direction') { seen.directions.push({ id: s.id, text: b.text }); return send(200, { delivered: true }) }
    if (req.method === 'POST' && rest === 'interrupt') { seen.interrupts.push(s.id); s.status = 'idle'; return send(200, {}) }
    send(404, { detail: `unhandled ${req.method} ${u}` })
  })
})
await new Promise(r => daemon.listen(sock, r))
daemon.unref()

const { createTask, stopTask, answerAgentApproval, steerAgent, resolveRuntime } = await import('../dist/tasks.js')
const pwcode = await import('../dist/pwcode.js')

const until = async (cond, ms = 5000) => {
  const end = Date.now() + ms
  while (Date.now() < end) { if (cond()) return; await new Promise(r => setTimeout(r, 25)) }
  throw new Error('condition not reached in time')
}
const only = m => [...m.nodes.values()][0]

test('the setting decides the runtime, and a campaign stays on the runner', () => {
  assert.equal(resolveRuntime(undefined, 'local'), 'session')
  assert.equal(resolveRuntime('runner', 'local'), 'session', 'sessions mode does not let a task opt out')
  assert.equal(resolveRuntime(undefined, 'campaign'), 'runner')
})

test('the daemon client checks the protocol', async () => {
  const s = await pwcode.daemonStatus()
  assert.equal(s.protocolVersion, 2)
  assert.equal(pwcode.unusableReason(s), null)
  assert.match(pwcode.unusableReason({ ...s, protocolVersion: 3 }), /protocol 3/)
  assert.match(pwcode.unusableReason(null), /no pw code daemon/)
})

test('a session agent carries the board inline, works, and completes from its reply', async () => {
  const m = createTask({ objective: 'sessions', model: 'u:openai/m', agents: [{ objective: 'summarize the decks' }], maxAgents: 2, maxDepth: 0 })
  assert.equal(m.runtime, 'session')
  const a = only(m)
  await until(() => a.state === 'completed')
  const req = seen.creates.at(-1)
  assert.equal(req.permissionMode, 'read-only')
  assert.equal(req.agent, 'studio_task')
  assert.equal(req.remoteControl, true)
  assert.ok(req.allowedTools.includes('mcp__task__board_status') && req.allowedTools.includes('mcp__task__search_kb'))
  assert.ok(!req.allowedTools.some(t => !t.startsWith('mcp__task__')), 'only the task server is pre-approved')
  const def = JSON.parse(req.agentsJson).studio_task
  const board = def.mcpServers[0].task
  assert.equal(board.url, 'http://127.0.0.1:4999/api/mcp', 'the board is reached on loopback, not through the proxy')
  assert.equal(board.headers.Authorization, `Bearer ${m.token}`)
  assert.equal(board.headers['X-Task-Agent'], a.name)
  assert.match(seen.messages.at(-1).text, /board_status/, 'a session agent is told to use the board it has')
  assert.equal(seen.messages.at(-1).mcpConnected, true, 'the prompt waits for the board to connect')
  assert.ok(!m.board.some(x => /did not connect/.test(x.body)), 'no false alarm about the board')
  assert.equal(m.maxDepth, 0, 'a depth of 0 is kept, not replaced by the default')
  assert.match(a.sessionId, /^sess-/)
  assert.equal(a.host, 'node-7', 'the agent names the machine its daemon runs on')
  assert.ok(fs.readFileSync(path.join(kb, a.resultPath), 'utf8').includes('session work done'))
  assert.deepEqual(a.usage, { input: 1200, output: 300, total: 1500, cost: null })
  const live = fs.readFileSync(path.join(idx, 'tasks', m.id, 'work', a.name, 'live.log'), 'utf8')
  assert.match(live, /\[mcp__task__search_kb\]/)
  assert.ok(!fs.existsSync(path.join(idx, 'tasks', m.id, 'work', a.name, '.agents')), 'no workspace settings file')
  await until(() => m.state === 'completed')
})

test('an approval request makes the agent input-required until someone answers', async () => {
  const m = createTask({ objective: 'approvals', model: 'u:openai/m', agents: [{ objective: 'needs-approval to list data' }], maxAgents: 1, maxDepth: 0 })
  const a = only(m)
  await until(() => a.state === 'input-required')
  assert.deepEqual(a.approvals, [{ id: 'ap1', kind: 'confirm', text: 'Shell command: ls /data' }])
  assert.ok(m.board.some(b => b.topic === 'input-required' && b.body.includes('ls /data')))
  await assert.rejects(answerAgentApproval(m.id, a.name, 'nope', true), /no such approval/)
  await answerAgentApproval(m.id, a.name, 'ap1', true)
  assert.deepEqual(seen.answers.at(-1), { id: a.sessionId, approval: 'ap1', allowed: true })
  await until(() => a.state === 'completed')
  assert.ok(fs.readFileSync(path.join(kb, a.resultPath), 'utf8').includes('approved work done'))
})

test('a working session can be steered, and stopping the task interrupts it', async () => {
  const m = createTask({ objective: 'steer', model: 'u:openai/m', agents: [{ objective: 'slow survey' }], maxAgents: 1, maxDepth: 0 })
  const a = only(m)
  await until(() => a.sessionId && seen.messages.some(x => x.id === a.sessionId))
  assert.equal(await steerAgent(m.id, a.name, 'focus on the 2024 runs'), true)
  assert.deepEqual(seen.directions.at(-1), { id: a.sessionId, text: 'focus on the 2024 runs' })
  assert.ok(m.board.some(b => b.topic === 'direction' && b.body.includes('2024 runs')))
  stopTask(m.id)
  assert.equal(a.state, 'canceled')
  await until(() => seen.interrupts.includes(a.sessionId))
  await assert.rejects(steerAgent(m.id, a.name, 'too late'), /not a working session/)
})

test('a turn that ends without a reply fails the agent with a reason', async () => {
  const m = createTask({ objective: 'failure', model: 'u:openai/m', agents: [{ objective: 'fail-fast please' }], maxAgents: 1, maxDepth: 0 })
  const a = only(m)
  await until(() => a.state === 'failed', 15_000)
  assert.match(a.note, /without a reply/)
})

test('Settings accepts the three execution modes and nothing else', async () => {
  const { default: Fastify } = await import('fastify')
  const { settingsRoutes, effectiveSettings } = await import('../dist/settings.js')
  const s = Fastify(); await s.register(settingsRoutes); await s.ready()
  assert.equal((await s.inject({ method: 'PUT', url: '/api/settings', payload: { agentExecution: 'everything' } })).statusCode, 400)
  assert.equal((await s.inject({ method: 'PUT', url: '/api/settings', payload: { agentExecution: 'both' } })).statusCode, 200)
  assert.equal(effectiveSettings().agentExecution, 'both')
  assert.equal(resolveRuntime('runner', 'local'), 'runner', 'with both, a task may choose')
  assert.equal(resolveRuntime(undefined, 'local'), 'session')
})
