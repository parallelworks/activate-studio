// Platform operations the chat calls with the viewer's key, under the
// platform web chat's own names and descriptions: results reduced to the
// tool's fields and capped, the platform's own error text passed back,
// arguments in one canonical form, and the tools that change something
// never run by a plain tool call.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'pt-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'pt-ix-'))
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.PW_API_KEY = 'deploy-key'
process.env.PW_CLI = '/nonexistent/pw'

let storage = []
let storageStatus = 200
let storageBody = null
const calls = []
globalThis.fetch = async (url, init = {}) => {
  const u = new URL(String(url))
  calls.push({ method: init.method ?? 'GET', path: u.pathname, search: u.search, auth: init.headers?.Authorization ?? null })
  if (u.pathname === '/api/auth/session') return Response.json({ username: 'alex', organization: 'org-a', email: 'alex@example.com' })
  if (u.pathname === '/api/storage') {
    if (storageBody !== null) return new Response(storageBody, { status: storageStatus, headers: { 'content-type': 'application/json' } })
    return Response.json(storage, { status: storageStatus })
  }
  if (init.method === 'POST') return new Response(null, { status: 204 })
  return new Response('{}', { status: 404 })
}

const P = await import('../dist/chat/platformTools.js')
const { executeTool, TOOL_SPECS, unattendedToolSpecs, readerToolOptions, toolOptionOf, TOOL_CALLS } = await import('../dist/chat/tools.js')

const DESCRIPTIONS = {
  list_storage: 'List the storage the user can use, such as buckets and file systems: type, cloud, region, size and status.',
  start_cluster: 'Start a cloud cluster that is off, provisioning its controller and resources. It starts billing. Use resume_cluster for one that is stopped. Leave user out for the user\'s own cluster.',
  resume_cluster: 'Resume a stopped cloud cluster, restarting its controller. It starts billing again. Leave user out for the user\'s own cluster.',
  stop_cluster: 'Stop a running cloud cluster\'s controller to stop its compute billing. It keeps its disks and settings and can be resumed. Leave user out for the user\'s own cluster.',
  destroy_cluster: 'Destroy a cloud cluster\'s provisioned resources, including its disks, so nothing bills. Its definition stays, so it can be started again later from scratch. Use stop_cluster to keep its disks. Leave user out for the user\'s own cluster.',
  cancel_workflow_run: 'Cancel a running workflow run, by the slug workflow_runs gives it.',
}

test('the six tools carry the platform chat\'s names and descriptions; the changing ones say the user approves', () => {
  for (const [name, description] of Object.entries(DESCRIPTIONS)) {
    const spec = TOOL_SPECS.find(t => t.function.name === name)
    assert.ok(spec, name)
    const asks = name !== 'list_storage'
    assert.equal(spec.function.description, description + (asks ? ' The user approves each call before it runs.' : ''), name)
    assert.equal(P.asksFirst(name), asks, name)
    assert.ok(TOOL_CALLS[name], `${name} documents what it calls`)
  }
  const stop = TOOL_SPECS.find(t => t.function.name === 'stop_cluster').function.parameters
  assert.deepEqual(Object.keys(stop.properties), ['user', 'clusterName'], 'the organization is never the model\'s to give')
  assert.deepEqual(stop.required, ['clusterName'], 'user may be left out')
  assert.deepEqual(P.insteadOf('stop_cluster'), ['destroy_cluster'])
  assert.deepEqual(P.insteadOf('destroy_cluster'), ['stop_cluster'])
  assert.deepEqual(P.insteadOf('start_cluster'), [])
})

test('arguments have one canonical form: declared keys only, empties dropped, the owner filled from the viewer', () => {
  const a = P.prepareArgs('stop_cluster', '{"clusterName":"cluster-a","note":"x"}', 'alex')
  const b = P.prepareArgs('stop_cluster', { user: 'alex', clusterName: ' cluster-a ', organization: 'elsewhere' }, 'alex')
  assert.equal(a.canonical, '{"clusterName":"cluster-a","user":"alex"}')
  assert.equal(b.canonical, a.canonical, 'the same call has the same text')
  assert.notEqual(P.prepareArgs('stop_cluster', { clusterName: 'cluster-b' }, 'alex').canonical, a.canonical)
  assert.notEqual(P.prepareArgs('stop_cluster', { clusterName: 'cluster-a', user: 'sam' }, 'alex').canonical, a.canonical)
  assert.match(P.prepareArgs('stop_cluster', '{}', 'alex').problem, /clusterName argument is required/)
  assert.match(P.prepareArgs('stop_cluster', '{not json', 'alex').problem, /not valid JSON/)
  assert.match(P.prepareArgs('stop_cluster', { clusterName: 'cluster-a' }, null).problem, /user argument is required/)
  assert.match(P.prepareArgs('cancel_workflow_run', {}, null).problem, /slug argument is required/)
  assert.match(P.prepareArgs('list_storage', { category: 'tapes' }, null).problem, /one of buckets/)
  assert.deepEqual(P.prepareArgs('list_storage', { type: 'aws-bucket', provisioned: 'true' }, null).args, { type: ['aws-bucket'], provisioned: true })
})

test('a tool\'s hash names its definition', () => {
  assert.match(P.toolHash('stop_cluster'), /^[0-9a-f]{64}$/)
  assert.equal(P.toolHash('stop_cluster'), P.toolHash('stop_cluster'))
  assert.notEqual(P.toolHash('stop_cluster'), P.toolHash('destroy_cluster'))
  assert.equal(P.toolHash('nope'), '')
})

test('list_storage keeps only the tool\'s fields of each item, with the user\'s key', async () => {
  storage = [
    { name: 'bucket-a', displayName: 'Bucket A', category: 'buckets', type: 'aws-bucket', user: 'alex', csp: 'aws', region: 'us-east-1', sizeGb: 100,
      status: 'active', statusDescription: null, attached: false, uri: 'pw://alex/bucket-a', group: 'team-a',
      id: '64f0', imageUrl: '/img', tags: ['x'], permissions: { edit: true }, credentials: { secret: 'do-not-show' } },
  ]
  calls.length = 0
  const out = await executeTool('list_storage', JSON.stringify({ category: 'buckets', type: ['aws-bucket', 'google-bucket'] }), { userKey: 'alex-key' })
  const items = JSON.parse(out.result)
  assert.deepEqual(Object.keys(items[0]).sort(), ['attached', 'category', 'csp', 'displayName', 'group', 'name', 'region', 'sizeGb', 'status', 'type', 'uri', 'user'].sort())
  assert.doesNotMatch(out.result, /do-not-show|imageUrl|permissions/)
  assert.equal(out.summary, '1 storage item')
  assert.equal(calls[0].auth, 'Bearer alex-key')
  assert.equal(calls[0].search, '?category=buckets&type=aws-bucket&type=google-bucket', 'a list argument repeats')
})

test('a large listing is cut at 16 KB and says so', async () => {
  storage = Array.from({ length: 400 }, (_, i) => ({ name: `bucket-${i}`, displayName: `Bucket ${i} ${'x'.repeat(40)}`, type: 'aws-bucket', status: 'active' }))
  const out = await executeTool('list_storage', '{}', { userKey: 'alex-key' })
  assert.match(out.result, /\[truncated: the result was longer than the tool limit\]$/)
  assert.ok(Buffer.byteLength(out.result) <= 16 * 1024 + 80)
})

test('the platform\'s own error text comes back: message, else detail, else the status', async () => {
  storageStatus = 403
  storageBody = JSON.stringify({ error: true, code: 'forbidden', message: 'You cannot list storage for user:sam.' })
  assert.equal((await executeTool('list_storage', '{"scope":"user:sam"}', { userKey: 'alex-key' })).result, 'You cannot list storage for user:sam.')
  storageBody = JSON.stringify({ title: 'Bad Request', status: 400, detail: 'scope must be mine or user:<name>' })
  storageStatus = 400
  assert.equal((await executeTool('list_storage', '{"scope":"x"}', { userKey: 'alex-key' })).result, 'scope must be mine or user:<name>')
  storageBody = '<html>oops</html>'
  storageStatus = 502
  assert.equal((await executeTool('list_storage', '{}', { userKey: 'alex-key' })).result, 'Bad Gateway')
  storageBody = null
  storageStatus = 200
})

test('a changing call fills the organization from the viewer\'s account and escapes each path segment', async () => {
  calls.length = 0
  const out = await P.runPlatformTool('stop_cluster', { clusterName: 'cluster a/b', user: 'alex' }, 'alex-key')
  assert.deepEqual(out, { result: 'Done.', failed: false })
  const post = calls.find(c => c.method === 'POST')
  assert.equal(post.path, '/api/organizations/org-a/users/alex/clusters/cluster%20a%2Fb/stop')
  assert.equal(post.auth, 'Bearer alex-key')
  const cancel = await P.runPlatformTool('cancel_workflow_run', { slug: 'sweep-00012' }, 'alex-key')
  assert.equal(cancel.result, 'Done.')
  assert.equal(calls.at(-1).path, '/api/workflow-runs/sweep-00012/cancel')
})

test('a plain tool call to a changing tool never runs: only an approved hold does', async () => {
  calls.length = 0
  for (const name of ['start_cluster', 'resume_cluster', 'stop_cluster', 'destroy_cluster', 'cancel_workflow_run']) {
    const out = await executeTool(name, JSON.stringify({ clusterName: 'cluster-a', slug: 'run-00001' }), { userKey: 'alex-key' })
    assert.match(out.result, /runs only after the user approves the call in the Studio chat\. Nothing ran\./, name)
  }
  assert.equal(calls.filter(c => c.method === 'POST').length, 0, 'nothing was sent to the platform')
})

test('a turn with no person present is not offered the changing tools, and MCP never exposes them', async () => {
  const names = unattendedToolSpecs().map(t => t.function.name)
  assert.ok(names.includes('list_storage'))
  for (const n of ['start_cluster', 'resume_cluster', 'stop_cluster', 'destroy_cluster', 'cancel_workflow_run']) assert.ok(!names.includes(n), n)
  const { default: Fastify } = await import('fastify')
  const { mcpRoutes } = await import('../dist/mcp.js')
  const { effectiveSettings } = await import('../dist/settings.js')
  const app = Fastify()
  await app.register(mcpRoutes)
  await app.ready()
  if (effectiveSettings().mcpEnabled) {
    const listed = (await app.inject({ method: 'POST', url: '/api/mcp', payload: { jsonrpc: '2.0', id: 1, method: 'tools/list' } })).json().result.tools.map(t => t.name)
    assert.ok(!listed.includes('stop_cluster'))
  }
  // Refused whether MCP access is on (not an exposed tool) or off.
  const refused = await app.inject({ method: 'POST', url: '/api/mcp', payload: { jsonrpc: '2.0', id: 2, method: 'tools/call', params: { name: 'stop_cluster', arguments: { clusterName: 'cluster-a' } } } })
  assert.ok(refused.statusCode === 403 || refused.json().error, 'the call is refused')
  assert.equal(calls.filter(c => c.method === 'POST').length, 0)
})

test('the reader\'s switch for them is its own, and marked as asking first', async () => {
  assert.equal(toolOptionOf('stop_cluster'), 'changes')
  assert.equal(toolOptionOf('list_storage'), 'workflows')
  const opt = (await readerToolOptions()).find(o => o.id === 'changes')
  assert.equal(opt.asksFirst, true)
  assert.equal(opt.label, 'Platform changes')
})
