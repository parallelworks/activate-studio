// The coordination plane: mission-scoped tokens over MCP, and a claim
// that cannot be taken twice.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const kb = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.KB_ROOT = kb
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))
const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-bin-'))
// Workers that never finish, so the mission stays open for the test.
fs.writeFileSync(path.join(bin, 'pw'), '#!/bin/bash\nsleep 30\n', { mode: 0o755 })
process.env.PW_CLI = path.join(bin, 'pw')

const { default: Fastify } = await import('fastify')
const { mcpRoutes } = await import('../dist/mcp.js')
const { createMission, stopMission, getMission } = await import('../dist/missions.js')

const app = Fastify()
await app.register(mcpRoutes)

const m = createMission({ objective: 'board test', model: 'stub', agents: [{ objective: 'a' }, { objective: 'b' }], maxAgents: 4 })
const call = (token, agent, name, args = {}) => app.inject({
  method: 'POST', url: '/api/mcp',
  headers: { authorization: `Bearer ${token}`, 'x-mission-agent': agent },
  payload: { jsonrpc: '2.0', id: 1, method: 'tools/call', params: { name, arguments: args } },
}).then(r => JSON.parse(r.body))

const listTools = token => app.inject({
  method: 'POST', url: '/api/mcp',
  headers: token ? { authorization: `Bearer ${token}` } : {},
  payload: { jsonrpc: '2.0', id: 2, method: 'tools/list' },
}).then(r => JSON.parse(r.body).result.tools.map(t => t.name))

test('board tools appear only for a mission token', async () => {
  const withToken = await listTools(m.token)
  const without = await listTools(null)
  assert.ok(withToken.includes('board_claim'), 'a worker sees the board')
  assert.ok(withToken.includes('search_kb'), 'and still the corpus')
  assert.ok(!without.includes('board_claim'), 'an ordinary client does not')
})

test('a wrong token gets no board', async () => {
  const r = await call('not-a-real-token', 'x', 'board_post', { body: 'hi' })
  assert.ok(r.error, 'refused')
})

test('a task can be claimed once and only once', async () => {
  await call(m.token, 'agent-1', 'board_offer_task', { objective: 'shared work' })
  const first = JSON.parse((await call(m.token, 'agent-1', 'board_claim')).result.content[0].text)
  const second = JSON.parse((await call(m.token, 'agent-2', 'board_claim')).result.content[0].text)
  assert.equal(first.claimed.claimedBy, 'agent-1')
  assert.equal(second.claimed, null, 'the second agent is told it is taken')
  assert.match(second.reason, /no unclaimed|already claimed/)
})

test('status and posts reach the board and the participant table', async () => {
  await call(m.token, 'agent-1', 'board_status', { note: 'reading the corpus' })
  await call(m.token, 'agent-2', 'board_post', { topic: 'finding', body: 'two proposals overlap' })
  const read = JSON.parse((await call(m.token, 'agent-2', 'board_read', { since: 0 })).result.content[0].text)
  assert.ok(read.some(b => b.topic === 'status' && b.from === 'agent-1'))
  assert.ok(read.some(b => b.topic === 'finding' && /overlap/.test(b.body)))
  assert.equal(getMission(m.id).participants.get('agent-1').note, 'reading the corpus')
})

test('each worker is handed board access in its own workspace', () => {
  const cfg = path.join(process.env.INDEX_BASE, 'missions', m.id, 'work', 'agent-1', '.agents', 'settings.local.json')
  const parsed = JSON.parse(fs.readFileSync(cfg, 'utf8'))
  assert.match(parsed.mcpServers.mission.url, /\/api\/mcp$/)
  assert.equal(parsed.mcpServers.mission.headers.Authorization, `Bearer ${m.token}`)
})

test.after(() => stopMission(m.id))
