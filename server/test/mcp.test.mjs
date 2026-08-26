// The MCP surface: protocol handshake, the read-only tool list, and a
// real tool call against a real corpus directory.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const kb = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
fs.writeFileSync(path.join(kb, 'note.md'), 'granny smith apples')
process.env.KB_ROOT = kb
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))
const { default: Fastify } = await import('fastify')
const { mcpRoutes } = await import('../dist/mcp.js')

const { settingsRoutes } = await import('../dist/settings.js')
const app = Fastify()
await app.register(mcpRoutes)
await app.register(settingsRoutes)
const rpc = (method, params, id = 1) =>
  app.inject({ method: 'POST', url: '/api/mcp', payload: { jsonrpc: '2.0', id, method, params } })
    .then(r => JSON.parse(r.body))

test('initialize answers the handshake', async () => {
  const r = await rpc('initialize', { protocolVersion: '2025-06-18' })
  assert.equal(r.result.serverInfo.name, 'activate-studio')
  assert.ok(r.result.capabilities.tools)
})

test('tools/list exposes the read-only corpus set and nothing else', async () => {
  const r = await rpc('tools/list', {})
  const names = r.result.tools.map(t => t.name)
  assert.ok(names.includes('search_kb'))
  assert.ok(names.includes('read_kb_file'))
  assert.ok(!names.includes('write_kb_file'), 'writes stay with the chat assistant')
  assert.ok(!names.includes('run_workflow'), 'platform actions stay with the chat assistant')
  assert.ok(r.result.tools.every(t => t.inputSchema && t.description))
})

test('tools/call runs a real tool over the corpus', async () => {
  const r = await rpc('tools/call', { name: 'list_kb_dir', arguments: { path: '' } })
  assert.equal(r.result.isError, false)
  assert.match(r.result.content[0].text, /note\.md/)
})

test('an unexposed tool is refused, an unknown method is an error', async () => {
  const bad = await rpc('tools/call', { name: 'write_kb_file', arguments: {} })
  assert.ok(bad.error)
  const unknown = await rpc('no/such', {})
  assert.equal(unknown.error.code, -32601)
})

test('notifications get no body', async () => {
  const r = await app.inject({ method: 'POST', url: '/api/mcp', payload: { jsonrpc: '2.0', method: 'notifications/initialized' } })
  assert.equal(r.statusCode, 202)
})

test('the settings switch turns the endpoint off', async () => {
  // Through the settings route, the way the UI does it, so the module's
  // cache is updated rather than bypassed.
  const put = await app.inject({ method: 'PUT', url: '/api/settings', payload: { mcpEnabled: false } })
  assert.equal(put.statusCode, 200)
  const r = await app.inject({ method: 'POST', url: '/api/mcp', payload: { jsonrpc: '2.0', id: 9, method: 'tools/list' } })
  assert.equal(r.statusCode, 403)
  await app.inject({ method: 'PUT', url: '/api/settings', payload: { mcpEnabled: true } })
})
