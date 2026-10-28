// A Studio on a computer of its own, as the macOS app runs it: a model
// server on this computer named by OPENAI_BASE_URL and OPENAI_API_KEY, no
// pw CLI sign-in, no platform. Drives the real chat route with a scripted
// model: nothing that needs a platform is offered or run, citations the
// model gets wrong are repaired as they stream, and read_kb_file returns a
// document's text from a library the Studio built of another folder.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'standalone-'))
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.OPENAI_BASE_URL = 'http://127.0.0.1:9/v1'
process.env.OPENAI_API_KEY = 'local-server-key'
process.env.PW_CREDENTIALS_DIR = path.join(dir, 'no-pw-sign-in')
for (const k of ['PW_GATEWAY_URL', 'PW_API_KEY', 'AUTH_JWKS_URL', 'PW_CONTEXT', 'SIDECAR_MODEL_URL']) delete process.env[k]
// A pw CLI that notes every call, which nothing here may make.
const pwLog = path.join(dir, 'pw.log')
fs.writeFileSync(path.join(dir, 'pw'), `#!/bin/sh\necho "$@" >> ${pwLog}\necho '[]'\n`, { mode: 0o755 })
process.env.PW_CLI = path.join(dir, 'pw')

fs.mkdirSync(path.join(process.env.KB_ROOT, 'notes'), { recursive: true })
fs.writeFileSync(path.join(process.env.KB_ROOT, 'notes', 'kickoff agenda.md'), '# Kickoff\n')
// A folder on this computer the Studio built a library of: its documents'
// text is in the library's own extraction cache, not the main folder's.
const shelf = path.join(dir, 'shelf')
const shelfCache = path.join(dir, 'ix', 'libraries', 'shelf', 'extract')
fs.mkdirSync(path.join(shelf, 'reports'), { recursive: true })
fs.writeFileSync(path.join(shelf, 'reports', 'site survey.docx'), 'PK\x03\x04 not text')
fs.writeFileSync(path.join(shelf, 'reports', 'scan.pdf'), '%PDF-1.4 not text')
fs.mkdirSync(path.join(shelfCache, 'reports'), { recursive: true })
fs.writeFileSync(path.join(shelfCache, 'reports', 'site survey.docx.txt'), '<!-- extracted by downmark 0.10.0 -->\n\n# Site survey\n\nThe pump house floods in spring.\n')

const modelCalls = []
let script = () => ({ text: 'ok' })
const sseOf = chunks => new Response(chunks.map(c => `data: ${JSON.stringify(c)}\n\n`).join('') + 'data: [DONE]\n\n', { headers: { 'content-type': 'text/event-stream' } })
globalThis.fetch = async (url, init = {}) => {
  const u = new URL(String(url))
  if (u.pathname === '/v1/chat/completions') {
    const body = JSON.parse(init.body)
    modelCalls.push(body)
    const r = script(body)
    // Text arrives in the pieces given, as a server streams it.
    if (r.text !== undefined) {
      const pieces = Array.isArray(r.text) ? r.text : [r.text]
      return sseOf([...pieces.map(p => ({ model: body.model, choices: [{ index: 0, delta: { content: p } }] })), { model: body.model, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }])
    }
    return sseOf([
      { model: body.model, choices: [{ index: 0, delta: { tool_calls: r.calls.map(([name, args], index) => ({ index, id: `call_${modelCalls.length}_${index}`, type: 'function', function: { name, arguments: JSON.stringify(args) } })) } }] },
      { model: body.model, choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }] },
    ])
  }
  return new Response('{}', { status: 404 })
}

const L = await import('../dist/libraries.js')
L.addBuiltLibrary({ id: 'shelf', label: 'Shelf', indexRoot: path.join(dir, 'ix', 'libraries', 'shelf', 'index'), sourceRoot: shelf, build: { indexDir: path.join(dir, 'ix', 'libraries', 'shelf'), cacheRoot: shelfCache, managed: true, state: 'ready', error: null, builtAt: null, files: 2, dirs: 1 } })
const { platformAvailable } = await import('../dist/workflowsTab.js')
const { default: Fastify } = await import('fastify')
const { chatRoutes } = await import('../dist/chat/routes.js')
const { conversationRoutes } = await import('../dist/conversations.js')
const app = Fastify()
await app.register(chatRoutes)
await app.register(conversationRoutes)
await app.ready()
test.after(() => fs.rmSync(dir, { recursive: true, force: true }))

function parseSSE(text) {
  return text.split('\n\n').filter(b => /^data:/m.test(b)).map(b => {
    let event = 'message', data = ''
    for (const line of b.split('\n')) {
      if (line.startsWith('event:')) event = line.slice(6).trim()
      else if (line.startsWith('data:')) data += line.slice(5).trim()
    }
    return { event, data: JSON.parse(data) }
  })
}
let seq = 0
async function ask(text, extra = {}) {
  const conversationId = (await app.inject({ method: 'POST', url: '/api/chat/conversations', payload: { title: 't' } })).json().id
  modelCalls.length = 0
  const res = await app.inject({ method: 'POST', url: '/api/chat/stream', payload: { model: 'local-model', conversationId, userMessageId: `u${++seq}`, messages: [{ role: 'user', content: text }], ...extra } })
  const events = parseSSE(res.payload)
  const content = events.filter(e => e.event === 'content').map(e => e.data.text).join('')
  const stored = (await app.inject({ method: 'GET', url: `/api/chat/conversations/${conversationId}` })).json()
  return { events, content, stored: stored.messages.at(-1) }
}
const toolNames = body => (body.tools ?? []).map(t => t.function.name)
const toolResults = body => body.messages.filter(m => m.role === 'tool').map(m => m.content)
const filler = ' That is what the knowledge base says about it, and nothing in it says otherwise.'.repeat(3)

test('a model server beside the Studio is not a platform, even with a key', () => {
  assert.equal(platformAvailable(undefined), false)
})

test('a question that mentions storage is offered the knowledge base tools only, and no platform section', async () => {
  script = () => ({ text: `He is not named in the knowledge base.${filler}` })
  await ask('Who is the godfather of parallel file systems and storage?')
  const body = modelCalls[0]
  const sent = toolNames(body)
  for (const n of ['search_kb', 'read_kb_file', 'list_kb_dir']) assert.ok(sent.includes(n), n)
  for (const n of ['list_workflows', 'list_clusters', 'hpc_status', 'pw_help', 'workflow_runs', 'list_storage', 'list_sessions', 'start_agent', 'delegate', 'stop_cluster']) {
    assert.ok(!sent.includes(n), `${n} is not offered`)
  }
  const more = body.tools.find(t => t.function.name === 'more_tools')
  if (more) assert.deepEqual(more.function.parameters.properties.group.enum, ['editing'], 'more_tools names no platform group')
  const system = body.messages[0].content
  assert.doesNotMatch(system, /Platform workflows registered|Platform tools are not listed|list_sessions|workflow DAG|When delegating/)
  assert.match(system, /search_kb/)
  assert.equal(fs.existsSync(pwLog), false, 'the pw CLI was not asked for a catalog')
})

test('a tool that was not offered is not run: the model is told so in a line', async () => {
  script = body => body.messages.at(-1).role === 'tool'
    ? { text: `Answered without them.${filler}` }
    : { calls: [['list_clusters', {}], ['pw_help', { command: 'clusters' }], ['more_tools', { group: 'workflows' }]] }
  const r = await ask('Which clusters can I use?')
  const results = toolResults(modelCalls.at(-1))
  assert.deepEqual(results, [
    'list_clusters is not available in this Studio, so it did not run. Answer with the tools you have.',
    'pw_help is not available in this Studio, so it did not run. Answer with the tools you have.',
    'The workflows tools are not available in this Studio. Answer with the tools you have.',
  ])
  assert.equal(fs.existsSync(pwLog), false, 'the pw CLI never ran')
  assert.ok(!toolNames(modelCalls.at(-1)).includes('list_clusters'), 'and the group did not join')
  assert.ok(r.events.some(e => e.event === 'tool' && e.data.phase === 'result' && e.data.summary === 'not available'))
})

test('the reader\'s Tools menu has no switch for the platform', async () => {
  const ids = (await app.inject({ method: 'GET', url: '/api/chat/tool-options' })).json().tools.map(t => t.id)
  assert.ok(ids.includes('kb'))
  for (const id of ['workflows', 'changes', 'agents']) assert.ok(!ids.includes(id), id)
})

test('a citation with the prompt\'s placeholder, split across streamed pieces, arrives as a viewer link', async () => {
  script = () => ({ text: ['The agenda is in [relative/pa', 'th/notes/kickoff%20agenda.md] and ', `[the notes](relative/path/notes/kickoff%20agenda.md).${filler}`] })
  const r = await ask('Where is the kickoff agenda?')
  const want = 'The agenda is in [notes/kickoff agenda.md](#open=file:notes/kickoff%20agenda.md) and [the notes](#open=file:notes/kickoff%20agenda.md).'
  assert.ok(r.content.startsWith(want), r.content)
  assert.ok(r.stored.content.startsWith(want), 'stored as shown')
})

test('read_kb_file returns a built library\'s document text, and a bare [path] it returned becomes a link into that library', async () => {
  script = body => body.messages.at(-1).role === 'tool'
    ? { text: `It floods in spring [reports/site survey.docx]; see also [reports/scan.pdf](#open=file:reports/scan.pdf).${filler}` }
    : { calls: [['read_kb_file', { paths: ['reports/site survey.docx', 'reports/scan.pdf'] }]] }
  const r = await ask('When does the pump house flood?', { library: 'shelf' })
  const [result] = toolResults(modelCalls.at(-1))
  assert.match(result, /### reports\/site survey\.docx\n# Site survey\n\nThe pump house floods in spring\./)
  assert.doesNotMatch(result, /Binary file|downmark/)
  assert.match(result, /### reports\/scan\.pdf\nNo text has been read out of reports\/scan\.pdf \(a document, \d+ bytes\): Shelf has not extracted it, or could not\./)
  assert.ok(r.content.startsWith('It floods in spring [reports/site survey.docx](#open=file:reports/site%20survey.docx&lib=shelf); see also [reports/scan.pdf](#open=file:reports/scan.pdf&lib=shelf).'), r.content)
  const part = r.stored.parts.find(p => p.kind === 'tool_call' && p.name === 'read_kb_file')
  assert.deepEqual(part.files, [{ path: 'reports/site survey.docx', library: 'shelf' }, { path: 'reports/scan.pdf', library: 'shelf' }], 'kept with the call, for later replies')
})

test('one path read alone comes back as text with its source, and an unknown bracket is left as written', async () => {
  const { executeTool } = await import('../dist/chat/tools.js')
  const out = await executeTool('read_kb_file', JSON.stringify({ path: 'reports/site survey.docx', library: 'shelf' }), {})
  assert.equal(out.result, '# Site survey\n\nThe pump house floods in spring.\n')
  assert.match(out.summary, /chars \(extracted\)/)
  assert.deepEqual(out.files, [{ path: 'reports/site survey.docx', library: 'shelf' }])
  script = () => ({ text: `Items [1] and [x] stay, as does [notes/missing.md].${filler}` })
  const r = await ask('anything')
  assert.ok(r.content.startsWith('Items [1] and [x] stay, as does [notes/missing.md].'), r.content)
})
