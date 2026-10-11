// pw code agents at sites. A cluster's daemon is reached the way `pw code
// attach` reaches it, over `pw ssh … pw code daemon proxy`; here the bridge
// spawns a fake daemon that speaks HTTP on its stdin and stdout, and a fake
// pw answers the cluster list and the workspace command.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'as-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'as-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'as-ix-'))
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.AGENT_LOCAL_SITE = '0'
process.env.PW_API_KEY = 'deployment-key'
const pwLog = path.join(dir, 'pw.log')
const fakePw = path.join(dir, 'pw')
fs.writeFileSync(fakePw, `#!/bin/sh
echo "$@" >> ${pwLog}
case "$1" in
  clusters) echo '[{"name":"cluster-a","type":"existing"},{"name":"bad name","type":"x"}]'; echo 'A newer CLI is available.' ;;
  ssh) echo "/home/u/studio-agents/ws" ;;
esac
`, { mode: 0o755 })
process.env.PW_CLI = fakePw

// The fake daemon: an HTTP server whose one connection is this process's stdio.
const daemonFile = path.join(dir, 'daemon.mjs')
const stateFile = path.join(dir, 'daemon-state.jsonl')
fs.writeFileSync(daemonFile, `
import http from 'node:http'
import fs from 'node:fs'
import { Duplex } from 'node:stream'
const log = x => fs.appendFileSync(${JSON.stringify(stateFile)}, JSON.stringify(x) + '\\n')
let goal = null
const history = [{ kind: 'user', text: 'objective' }, { kind: 'assistant', text: 'working on it' },
  { kind: 'goal_progress', text: JSON.stringify({ turns: 2, outcome: '', reason: 'tests still failing' }) }]
const server = http.createServer((req, res) => {
  let body = ''
  req.on('data', d => { body += d })
  req.on('end', () => {
    const b = body ? JSON.parse(body) : null
    log({ method: req.method, url: req.url, body: b })
    const send = (code, v) => { const t = JSON.stringify(v); res.writeHead(code, { 'content-type': 'application/json', 'content-length': Buffer.byteLength(t) }); res.end(t) }
    if (req.url === '/v1/status') return send(200, { protocolVersion: 2, version: 'v7.120.0', pid: 9, hostname: 'login1', sessions: 1, remoteStart: true, remoteMaxPermissionMode: 'accept-edits' })
    if (req.url === '/v1/sessions' && req.method === 'POST') return send(200, { id: 'sess-1', status: 'idle', messages: 0, history: [], approvals: null, workspace: b.workspace, model: b.model ?? 'default' })
    if (req.url === '/v1/sessions') return send(200, [{ id: 'sess-1', name: 'fix tests', status: 'running', workspace: '/home/u/ws', model: 'm' }])
    if (req.url === '/v1/sessions/sess-1/messages') return send(200, { queued: true, turnId: 't1' })
    if (req.url === '/v1/sessions/sess-1/goal') { if (b.args && !['pause','resume','clear'].includes(b.args)) goal = b.args; return send(200, { output: b.args === 'pause' ? 'Goal paused.' : 'Goal set.', status: { active: true, paused: b.args === 'pause' } }) }
    if (req.url === '/v1/sessions/sess-1/state') return send(200, { goal: { active: !!goal, paused: false } })
    if (req.url === '/v1/sessions/sess-1/direction') return send(200, { delivered: false })
    if (req.url === '/v1/sessions/sess-1') return send(200, { id: 'sess-1', status: 'running', messages: 3, history, approvals: [{ id: 'ap1', kind: 'shell', command: 'make test' }], workspace: '/home/u/ws', model: 'm' })
    send(404, { detail: 'no route' })
  })
})
const sock = new Duplex({ read() {}, write(c, e, cb) { process.stdout.write(c, cb) } })
process.stdin.on('data', d => sock.push(d))
process.stdin.on('end', () => sock.push(null))
server.emit('connection', sock)
`)

const { spawn } = await import('node:child_process')
const bridge = await import('../dist/pwcodeBridge.js')
let spawns = 0
bridge.setBridgeSpawn((resource, key) => { spawns++; assert.equal(resource, 'cluster-a'); assert.equal(key, 'user-key'); return spawn(process.execPath, [daemonFile], { stdio: ['pipe', 'pipe', 'pipe'] }) })
const A = await import('../dist/agentSites.js')
const calls = () => fs.readFileSync(stateFile, 'utf8').trim().split('\n').map(l => JSON.parse(l))
test.after(() => bridge.closeBridges())

test('sites are the viewer\'s clusters; the local site only when offered', async () => {
  const sites = await A.listSites('user-key')
  assert.deepEqual(sites, [{ name: 'cluster-a', kind: 'cluster', type: 'existing' }], 'a name that is not a site is dropped')
  assert.throws(() => A.checkSite('local'), /not offered/)
  assert.throws(() => A.checkSite('a b; rm -rf ~'), /not a site name/)
  assert.throws(() => A.clientFor('cluster-a', null), /your own platform key/)
})

test('agents act with the viewer\'s own key; the deployment key only stands in on a one-person Studio', () => {
  assert.equal(A.agentKey('user-key'), 'user-key')
  assert.equal(A.agentKey(null), 'deployment-key', 'sign-in is off here, so the Studio has one user')
})

test('an agent starts at the site with its objective and a bounded goal, over one bridge', async () => {
  const r = await A.startAgent({ site: 'cluster-a', key: 'user-key', objective: 'Fix the failing tests in ~/proj', goal: 'make test passes', maxTurns: 8, model: 'u:prov/model' })
  assert.equal(r.session, 'sess-1'); assert.equal(r.host, 'login1'); assert.equal(r.workspace, '/home/u/studio-agents/ws'); assert.equal(r.mode, 'accept-edits')
  const c = calls()
  const create = c.find(x => x.url === '/v1/sessions' && x.method === 'POST')
  assert.deepEqual(create.body, { workspace: '/home/u/studio-agents/ws', permissionMode: 'accept-edits', remoteControl: true, model: 'u:prov/model' })
  assert.equal(c.find(x => x.url.endsWith('/messages')).body.text, 'Fix the failing tests in ~/proj')
  const goal = c.find(x => x.url.endsWith('/goal')).body.args
  assert.match(goal, /^make test passes\n\nIf this is not met within 8 turns, stop and say what remains\.$/)
  assert.ok(c.indexOf(c.find(x => x.url.endsWith('/messages'))) < c.indexOf(c.find(x => x.url.endsWith('/goal'))), 'the objective goes first, then the goal')
  const ssh = fs.readFileSync(pwLog, 'utf8').split('\n').find(l => l.startsWith('ssh'))
  assert.match(ssh, /--platform-host platform\.test cluster-a mkdir -p -- ~\/studio-agents\/\S+ && cd -- ~\/studio-agents\/\S+ && pwd/)
  assert.equal(spawns, 1, 'every daemon call shared one ssh bridge')
})

test('status reads the goal, the last check, approvals, and the transcript end', async () => {
  const st = await A.agentStatus('cluster-a', 'user-key', 'sess-1')
  assert.equal(st.status, 'running')
  assert.deepEqual(st.goal, { active: true, paused: false })
  assert.equal(st.lastCheck.reason, 'tests still failing')
  assert.deepEqual(st.approvals, [{ id: 'ap1', kind: 'shell', text: 'shell: make test' }])
  assert.ok(st.recent.some(l => /\[goal\] turn 2: not met yet, tests still failing/.test(l)))
})

test('steering with no working turn becomes the next message; goals pause; bad input is refused', async () => {
  assert.deepEqual(await A.agentSteer('cluster-a', 'user-key', 'sess-1', 'use the debug partition'), { queued: true, asMessage: true })
  const p = await A.agentGoal('cluster-a', 'user-key', 'sess-1', 'pause')
  assert.equal(p.status.paused, true)
  await assert.rejects(A.agentGoal('cluster-a', 'user-key', 'sess-1', 'set', ''), /condition is required/)
  await assert.rejects(A.startAgent({ site: 'cluster-a', key: 'user-key', objective: 'x', workspace: '~/a; rm -rf /' }), /workspace/)
  await assert.rejects(A.startAgent({ site: 'cluster-a', key: 'user-key', objective: 'x', permissionMode: 'bypass-permissions' }), /permission mode/)
  assert.throws(() => A.checkSession('../../etc'), /not a session id/)
})

test('a goal bound is clamped and the condition kept short', () => {
  assert.match(A.boundedGoal('done', 0), /within 20 turns/)
  assert.match(A.boundedGoal('done', 999), /within 200 turns/)
  assert.ok(A.boundedGoal('x'.repeat(5000), 5).length < 4000)
})
