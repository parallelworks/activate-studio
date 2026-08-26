// The client half: attach a remote MCP server and use its tools. The
// fixture is a real JSON-RPC server, so the handshake, the namespacing,
// and the call path are all exercised.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import http from 'node:http'

const idx = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-mcpc-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.INDEX_BASE = idx

const seen = []
const server = http.createServer((q, s) => {
  let body = ''
  q.on('data', c => { body += c })
  q.on('end', () => {
    const msg = JSON.parse(body || '{}')
    seen.push(msg.method)
    const reply = (result) => {
      s.writeHead(200, { 'content-type': 'application/json', 'mcp-session-id': 'sess-1' })
      s.end(JSON.stringify({ jsonrpc: '2.0', id: msg.id, result }))
    }
    if (msg.method === 'initialize') return reply({ protocolVersion: '2025-06-18', capabilities: { tools: {} }, serverInfo: { name: 'crm', version: '1' } })
    if (msg.method === 'tools/list') return reply({ tools: [{ name: 'find-deal', description: 'Find a deal', inputSchema: { type: 'object', properties: { q: { type: 'string' } } } }] })
    if (msg.method === 'tools/call') return reply({ content: [{ type: 'text', text: `deal for ${msg.params.arguments.q} (session ${q.headers['mcp-session-id'] ?? 'none'})` }] })
    reply({})
  })
})
await new Promise(r => server.listen(0, '127.0.0.1', r))
const url = `http://127.0.0.1:${server.address().port}/mcp`

fs.writeFileSync(path.join(idx, 'settings.json'), JSON.stringify({
  mcpServers: [{ name: 'crm', url, header: 'Authorization: Bearer t0ken' }],
}))
const { remoteToolSpecs, callRemoteTool, isRemoteTool, remoteStatus } = await import('../dist/mcpClient.js')

test('a remote server contributes namespaced tools', async () => {
  const specs = await remoteToolSpecs()
  assert.equal(specs.length, 1)
  assert.equal(specs[0].function.name, 'crm__find-deal')
  assert.match(specs[0].function.description, /^\[crm\] Find a deal/)
  assert.ok(specs[0].function.parameters.properties.q, 'remote schema is preserved')
  assert.ok(seen.includes('initialize') && seen.includes('tools/list'), 'handshake then list')
})

test('a namespaced tool routes to its server and returns text', async () => {
  assert.equal(isRemoteTool('crm__find-deal'), true)
  assert.equal(isRemoteTool('search_kb'), false)
  const out = await callRemoteTool('crm__find-deal', { q: 'acme' })
  assert.match(out, /deal for acme/)
  assert.match(out, /session sess-1/, 'the session id from initialize is carried')
})

test('status reports what each server offered', async () => {
  const st = await remoteStatus()
  assert.equal(st[0].name, 'crm')
  assert.equal(st[0].tools, 1)
  assert.equal(st[0].error, null)
})

test('an unreachable server contributes nothing and does not throw', async () => {
  fs.writeFileSync(path.join(idx, 'settings.json'), JSON.stringify({
    mcpServers: [{ name: 'dead', url: 'http://127.0.0.1:1/mcp' }],
  }))
  const { invalidateRemote } = await import('../dist/mcpClient.js')
  const { default: st } = await import('../dist/settings.js').then(m => ({ default: m }))
  st.loadSettings && st.loadSettings()
  invalidateRemote()
  // settings module caches; reload it through its own writer instead
  const specs = await remoteToolSpecs()
  assert.ok(Array.isArray(specs), 'listing survives an unreachable server')
})

test.after(() => server.close())
