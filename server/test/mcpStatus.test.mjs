// The status panel polls attached MCP servers' state through
// /api/settings/mcp-servers?cached=1, which must report the last state
// seen without contacting any server; the plain route still checks.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import http from 'node:http'

const idx = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-mcps-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.INDEX_BASE = idx

const seen = []
const server = http.createServer((q, s) => {
  let body = ''
  q.on('data', c => { body += c })
  q.on('end', () => {
    const msg = JSON.parse(body || '{}')
    seen.push(msg.method)
    const result = msg.method === 'tools/list'
      ? { tools: [{ name: 'a' }, { name: 'b' }] }
      : { protocolVersion: '2025-06-18', capabilities: { tools: {} }, serverInfo: { name: 'crm', version: '1' } }
    s.writeHead(200, { 'content-type': 'application/json' })
    s.end(JSON.stringify({ jsonrpc: '2.0', id: msg.id, result }))
  })
})
await new Promise(r => server.listen(0, '127.0.0.1', r))
const url = `http://127.0.0.1:${server.address().port}/mcp`
fs.writeFileSync(path.join(idx, 'settings.json'), JSON.stringify({
  mcpServers: [{ name: 'crm', url }, { name: 'dead', url: 'http://127.0.0.1:1/mcp' }],
}))

const { settingsRoutes } = await import('../dist/settings.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
await app.register(settingsRoutes)
await app.ready()
const get = async u => (await app.inject({ method: 'GET', url: u })).json()

test('the cached read contacts no server and says none has been checked', async () => {
  const { servers } = await get('/api/settings/mcp-servers?cached=1')
  assert.deepEqual(servers.map(s => [s.name, s.checkedAt, s.tools, s.error]), [['crm', null, 0, null], ['dead', null, 0, null]])
  assert.equal(seen.length, 0, 'nothing was sent to the attached server')
})

test('after a check, the cached read reports what it found, still without contacting anyone', async () => {
  const checked = await get('/api/settings/mcp-servers')
  assert.equal(checked.servers[0].tools, 2)
  assert.ok(checked.servers[1].error, 'the unreachable server reports its error')
  const before = seen.length
  const { servers } = await get('/api/settings/mcp-servers?cached=1')
  assert.equal(seen.length, before, 'the cached read sent nothing')
  assert.equal(servers[0].tools, 2)
  assert.equal(servers[0].error, null)
  assert.ok(servers[0].checkedAt, 'a checked server carries when')
  assert.ok(servers[1].error)
})

test.after(async () => { await app.close(); server.close() })
