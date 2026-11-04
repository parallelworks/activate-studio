// A copy runs only from an approved held call. Drives the real chat route
// on a standalone Studio (a model server on this computer, no platform, no
// sign-in) with a scripted model: the plan, an approval that runs once, a
// denial, a changed argument, expiry, every other way a transfer might have
// started, and the platform tools' rules, which stay as they were.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const dir = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'tap-')))
process.env.HOME = path.join(dir, 'home')
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.GUFI_BIN = path.join(dir, 'no-gufi')
process.env.OPENAI_BASE_URL = 'http://127.0.0.1:9/v1'
process.env.OPENAI_API_KEY = 'local-server-key'
process.env.PW_CREDENTIALS_DIR = path.join(dir, 'no-pw-sign-in')
process.env.PW_CLI = '/nonexistent/pw'
process.env.STUDIO_TRANSFER_REPORT_MS = '400'
for (const k of ['PW_GATEWAY_URL', 'PW_API_KEY', 'AUTH_JWKS_URL', 'PW_CONTEXT', 'SIDECAR_MODEL_URL']) delete process.env[k]
const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
const kb = process.env.KB_ROOT
const downloads = path.join(dir, 'home', 'Downloads')
write(path.join(kb, 'notes', 'report.md'), '# Report\n')
write(path.join(kb, 'notes', 'other.md'), '# Other\n')
fs.mkdirSync(path.join(dir, 'ix'), { recursive: true })
fs.mkdirSync(downloads, { recursive: true })

const modelCalls = []
let script = () => ({ text: 'ok' })
let callSeq = 0
const filler = ' The assistant reports the result here in full so the reply reads as an answer. '.repeat(4)
const sseOf = chunks => new Response(chunks.map(c => `data: ${JSON.stringify(c)}\n\n`).join('') + 'data: [DONE]\n\n', { headers: { 'content-type': 'text/event-stream' } })
globalThis.fetch = async (url, init = {}) => {
  const u = new URL(String(url))
  if (u.pathname !== '/v1/chat/completions') return new Response('{}', { status: 404 })
  const body = JSON.parse(init.body)
  modelCalls.push(body)
  const last = body.messages.at(-1)
  const r = last.role === 'tool' ? { text: `Result: ${last.content}.${filler}` } : script(body)
  if (r.text !== undefined) return sseOf([{ model: body.model, choices: [{ index: 0, delta: { content: r.text } }] }, { model: body.model, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }])
  return sseOf([
    { model: body.model, choices: [{ index: 0, delta: { tool_calls: r.calls.map(([name, args], index) => ({ index, id: `call_${++callSeq}`, type: 'function', function: { name, arguments: JSON.stringify(args) } })) } }] },
    { model: body.model, choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }] },
  ])
}

const approvals = await import('../dist/chat/approvals.js')
const tools = await import('../dist/chat/tools.js')
const T = await import('../dist/movers/types.js')
const { copyMover } = await import('../dist/movers/copy.js')
const { default: Fastify } = await import('fastify')
const { chatRoutes } = await import('../dist/chat/routes.js')
const { conversationRoutes } = await import('../dist/conversations.js')
const { settingsRoutes } = await import('../dist/settings.js')
const app = Fastify()
app.setErrorHandler((err, _req, reply) => reply.status(err.status ?? 500).send({ error: err.message }))
await app.register(chatRoutes)
await app.register(conversationRoutes)
await app.register(settingsRoutes)
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
const newConversation = async () => (await app.inject({ method: 'POST', url: '/api/chat/conversations', payload: { title: 't' } })).json().id
let seq = 0
async function ask(conversationId, text, extra = {}) {
  const res = await app.inject({ method: 'POST', url: '/api/chat/stream', payload: { model: 'test-model', conversationId, userMessageId: `u${++seq}`, messages: [{ role: 'user', content: text }], ...extra } })
  const events = parseSSE(res.payload)
  return { events, approval: events.find(e => e.event === 'approval')?.data.approval, done: events.find(e => e.event === 'done')?.data, text }
}
async function answer(conversationId, asked, resume = {}) {
  const res = await app.inject({ method: 'POST', url: '/api/chat/stream', payload: {
    model: 'test-model', conversationId, userMessageId: `r${++seq}`, parentMessageId: asked.done?.messageId,
    messages: [{ role: 'user', content: asked.text }, { role: 'assistant', content: null }],
    resume: { messageId: asked.done?.messageId, toolCallId: asked.approval?.id, allowed: true, ...resume },
  } })
  return parseSSE(res.payload)
}
const toolResults = events => events.filter(e => e.event === 'tool' && e.data.phase === 'result')
const copyOf = (paths, more = {}) => () => ({ calls: [['transfer_files', { paths, ...more }]] })
const landed = name => fs.existsSync(path.join(downloads, name))
const jobs = async () => (await app.inject({ method: 'GET', url: '/api/transfers' })).json().transfers
const clear = () => { for (const n of fs.readdirSync(downloads)) fs.rmSync(path.join(downloads, n), { recursive: true }) }

test('standalone: the copy tool is offered with no platform, and the platform tools are not', async () => {
  const opts = (await app.inject({ method: 'GET', url: '/api/chat/tool-options' })).json().tools
  const copying = opts.find(o => o.id === 'transfers')
  assert.ok(copying?.asksFirst, 'its switch says it asks first')
  for (const id of ['changes', 'workflows', 'agents']) assert.ok(!opts.some(o => o.id === id), id)
  script = copyOf(['notes/report.md'])
  await ask(await newConversation(), 'download the report')
  const names = modelCalls.at(-1).tools.map(t => t.function.name)
  assert.ok(names.includes('transfer_files'))
  for (const n of ['stop_cluster', 'destroy_cluster', 'start_cluster', 'cancel_workflow_run']) assert.ok(!names.includes(n), n)
})

test('nothing runs until the person answers, and the approval shows the plan', async () => {
  clear()
  const id = await newConversation()
  script = copyOf(['notes/report.md'])
  const asked = await ask(id, 'download the report')
  assert.equal(asked.done.finishReason, 'tool_approval')
  const a = asked.approval
  assert.match(a.header, /Copy 1 file \(9 B\) to ~\/Downloads\?/)
  const rows = Object.fromEntries(a.plan.map(r => [r.label, r.value]))
  assert.equal(rows.Files, '1 file')
  assert.equal(rows.Size, '9 B')
  assert.match(rows.From, /on this computer/)
  assert.match(rows.To, /home\/Downloads on this computer/)
  assert.equal(rows.Mover, 'Studio copy')
  assert.equal(rows.Items, 'notes/report.md')
  assert.ok(!landed('report.md'))
  assert.deepEqual(await jobs(), [])
  // The record holds the plan the card shows.
  const rec = approvals.heldCall(id, a.id)
  assert.equal(rec.plan.dest, downloads)
  assert.equal(rec.viewer, 'local')
  assert.ok(rec.hash)

  const ev = await answer(id, asked)
  const r = toolResults(ev).at(-1).data
  assert.equal(r.error, false)
  assert.match(r.summary, /copied 1 file/)
  assert.equal(fs.readFileSync(path.join(downloads, 'report.md'), 'utf8'), '# Report\n')
  // The assistant is told the checked result.
  assert.match(modelCalls.at(-1).messages.at(-1).content, /Job \w+ done\. Copied 1 file .* checked the file count and sizes/)
  assert.equal((await jobs())[0].state, 'done')
  assert.ok(fs.existsSync(path.join(kb, 'notes', 'report.md')), 'the original stays')

  // An approval runs one call once.
  fs.rmSync(path.join(downloads, 'report.md'))
  const again = await answer(id, asked)
  assert.match(again.find(e => e.event === 'error').data.message, /no longer waiting/)
  assert.ok(!landed('report.md'))
  assert.equal((await jobs()).length, 1)
})

test('a denial runs nothing', async () => {
  clear()
  const id = await newConversation()
  script = copyOf(['notes/other.md'])
  const asked = await ask(id, 'download the other one')
  await answer(id, asked, { allowed: false, feedback: 'not now' })
  assert.ok(!landed('other.md'))
  assert.match(modelCalls.at(-1).messages.at(-1).content, /declined this call.*not now/)
  assert.ok(!(await jobs()).some(j => j.names.includes('other.md')))
})

test('an answer for other arguments is refused and the call stays held', async () => {
  clear()
  const id = await newConversation()
  script = copyOf(['notes/other.md'])
  const asked = await ask(id, 'download the other one')
  for (const args of [{ paths: ['notes/report.md'] }, { destination: '/tmp' }, { library: 'kb', paths: ['notes/other.md', 'notes/report.md'] }, { paths: ['../x'] }]) {
    const ev = await answer(id, asked, { args: JSON.stringify(args) })
    assert.match(ev.find(e => e.event === 'error').data.message, /other arguments/, JSON.stringify(args))
    assert.ok(!landed('report.md') && !landed('other.md'))
  }
  // The client may send the call back as the model made it.
  const ev = await answer(id, asked, { args: JSON.stringify({ paths: ['notes/other.md'] }) })
  assert.equal(toolResults(ev).at(-1).data.error, false)
  assert.ok(landed('other.md'))
})

test('an answer cannot run another tool in its place', async () => {
  clear()
  const id = await newConversation()
  script = copyOf(['notes/other.md'])
  const asked = await ask(id, 'download the other one')
  const ev = await answer(id, asked, { tool: 'stop_cluster' })
  assert.match(ev.find(e => e.event === 'error').data.message, /cannot be run in place/)
  assert.ok(!landed('other.md'))
})

test('an expired hold is refused', async () => {
  clear()
  const id = await newConversation()
  script = copyOf(['notes/other.md'])
  const asked = await ask(id, 'download the other one')
  const rec = approvals.heldCall(id, asked.approval.id)
  rec.expiresAt = new Date(Date.now() - 1000).toISOString()
  const ev = await answer(id, asked)
  assert.match(ev.find(e => e.event === 'error').data.message, /expired/)
  assert.ok(!landed('other.md'))
})

test('a change to the movers after the hold refuses the approval', async () => {
  clear()
  const id = await newConversation()
  script = copyOf(['notes/other.md'])
  const asked = await ask(id, 'download the other one')
  T.registerMover({ ...copyMover, id: 'newcomer', canReach: () => ({ ok: false, why: 'no' }) })
  try {
    const ev = await answer(id, asked)
    assert.match(ev.find(e => e.event === 'error').data.message, /tool changed/)
    assert.ok(!landed('other.md'))
  } finally { T.unregisterMover('newcomer') }
})

test('a call that fails its checks is refused with the reason and asks nothing', async () => {
  const id = await newConversation()
  for (const [args, why] of [
    [{ paths: ['../../etc/passwd'] }, /may not contain/],
    [{ paths: ['/etc/passwd'] }, /not absolute/],
    [{ paths: ['notes/report.md'], destination: '/etc' }, /not an allowed folder/],
    [{ paths: ['notes/missing.md'] }, /no such file/],
  ]) {
    script = () => ({ calls: [['transfer_files', args]] })
    const asked = await ask(id, 'download it')
    assert.equal(asked.approval, undefined, JSON.stringify(args))
    assert.match(modelCalls.at(-1).messages.at(-1).content, why)
    assert.equal(toolResults(asked.events)[0].data.error, true)
  }
  assert.deepEqual(approvals.heldCallsIn(id), [])
})

test('one call waits at a time', async () => {
  clear()
  const id = await newConversation()
  script = () => ({ calls: [['transfer_files', { paths: ['notes/report.md'] }], ['transfer_files', { paths: ['notes/other.md'] }]] })
  const asked = await ask(id, 'download both')
  assert.equal(approvals.heldCallsIn(id).length, 1)
  assert.match(toolResults(asked.events)[0].data.summary, /not run/)
  await answer(id, asked, { allowed: false })
})

test('no other path starts a transfer', async () => {
  clear()
  // The tool itself, as the endpoint for other tools and MCP clients would reach it.
  const out = await tools.executeTool('transfer_files', JSON.stringify({ paths: ['notes/report.md'] }), { userId: null })
  assert.match(out.result, /only after the user approves.*Nothing ran/)
  assert.ok(!tools.unattendedToolSpecs().some(t => t.function.name === 'transfer_files'))
  const { taskToolNames } = await import('../dist/mcp.js')
  assert.ok(!taskToolNames().includes('transfer_files'))
  // No route creates one.
  for (const [method, url] of [['POST', '/api/transfers'], ['PUT', '/api/transfers'], ['POST', '/api/transfers/x/start']]) {
    assert.equal((await app.inject({ method, url, payload: { paths: ['notes/report.md'] } })).statusCode, 404, url)
  }
  // A resume with no held call starts nothing.
  const ev = await answer(await newConversation(), { text: 'x', done: { messageId: 'm' }, approval: { id: 'call_made_up' } })
  assert.match(ev.find(e => e.event === 'error').data.message, /no longer waiting/)
  assert.ok(!landed('report.md'))
  // A switch turned off stops it before any hold.
  script = copyOf(['notes/report.md'])
  const id = await newConversation()
  const asked = await ask(id, 'download the report', { tools: ['kb'] })
  assert.equal(asked.approval, undefined)
  assert.deepEqual(approvals.heldCallsIn(id), [])
})

test('turned off for the deployment, it does not run even if already held', async () => {
  clear()
  const id = await newConversation()
  script = copyOf(['notes/other.md'])
  const asked = await ask(id, 'download the other one')
  const put = payload => app.inject({ method: 'PUT', url: '/api/settings', payload })
  assert.equal((await put({ disabledTools: ['transfer_files'] })).statusCode, 200)
  try {
    const ev = await answer(id, asked)
    assert.match(ev.find(e => e.event === 'error').data.message, /turned off/)
    assert.ok(!landed('other.md'))
  } finally { await put({ disabledTools: [] }) }
})

test('Settings names another folder, and holds what it takes to a full path', async () => {
  const put = payload => app.inject({ method: 'PUT', url: '/api/settings', payload })
  const extra = path.join(dir, 'home', 'copies')
  const id = await newConversation()
  script = copyOf(['notes/report.md'], { destination: '~/copies/run1' })
  assert.equal((await ask(id, 'copy the report to my copies folder')).approval, undefined)
  assert.equal((await put({ transferFolders: ['~/copies', ''] })).statusCode, 200)
  const asked = await ask(id, 'copy the report to my copies folder')
  assert.match(asked.approval.plan.find(r => r.label === 'To').value, /copies\/run1/)
  await answer(id, asked)
  assert.ok(fs.existsSync(path.join(extra, 'run1', 'report.md')))
  for (const bad of ['relative', '/a/../b', '/a\nb']) assert.equal((await put({ transferFolders: [bad] })).statusCode, 400, bad)
  await put({ transferFolders: [] })
})

test('platform tools are as they were: no platform, no hold, nothing run', async () => {
  const id = await newConversation()
  script = () => ({ calls: [['stop_cluster', { clusterName: 'cluster-a' }]] })
  const asked = await ask(id, 'stop cluster-a')
  assert.equal(asked.approval, undefined)
  assert.deepEqual(approvals.heldCallsIn(id), [])
  assert.match(modelCalls.at(-1).messages.at(-1).content, /not available in this chat: no platform is connected/)
  assert.deepEqual(approvals.changesAvailable({ viewer: { id: 'local', name: 'u' }, key: 'k' }), { ok: false, why: 'no platform is connected' })
})

test('a copy that outlasts the reply goes on, and its result reaches the conversation', async () => {
  clear()
  const slow = {
    ...copyMover, id: 'slow', pollMs: 20, start: async req => { slowReq = req; return 'x' },
    status: async () => (Date.now() - t0 < 900 ? { state: 'running', files: 0, bytes: 1 } : (write(path.join(slowReq.dest, 'report.md'), '# Report\n'), { state: 'done' })),
  }
  let slowReq = null
  const t0 = Date.now()
  T.unregisterMover('studio-copy')
  T.registerMover(slow)
  try {
    const id = await newConversation()
    script = copyOf(['notes/report.md'])
    const asked = await ask(id, 'download the report')
    const going = answer(id, asked)
    // A reload while the reply waits finds the approval answered.
    await new Promise(r => setTimeout(r, 150))
    const during = (await app.inject({ method: 'GET', url: `/api/chat/conversations/${id}` })).json()
    assert.equal(during.messages.flatMap(m => m.parts ?? []).find(p => p.kind === 'approval').resolved, true)
    const ev = await going
    assert.match(toolResults(ev).at(-1).data.summary, /copying, see the status panel/)
    assert.match(modelCalls.at(-1).messages.at(-1).content, /running as job/)
    const job = (await jobs())[0]
    assert.equal(job.state, 'running')
    assert.deepEqual(job.bytes.total, 9)
    for (let i = 0; i < 100 && (await jobs())[0].state === 'running'; i++) await new Promise(r => setTimeout(r, 30))
    assert.equal((await jobs())[0].state, 'done')
    assert.ok(landed('report.md'))
    await new Promise(r => setTimeout(r, 50))
    const stored = (await app.inject({ method: 'GET', url: `/api/chat/conversations/${id}` })).json()
    assert.match(stored.messages.at(-1).content, /Copy to ~\/Downloads: done\. Copied 1 file/)
    // And the next reply knows where it stands.
    script = () => ({ text: 'ok' })
    await ask(id, 'did it finish?')
    assert.match(modelCalls.at(-1).messages[0].content, /Copies started in this conversation.*: done, 1 of 1 files/)
  } finally { T.unregisterMover('slow'); T.registerMover(copyMover) }
})

test('cancel answers only for a job that exists', async () => {
  assert.equal((await app.inject({ method: 'POST', url: '/api/transfers/nope/cancel' })).statusCode, 404)
})
