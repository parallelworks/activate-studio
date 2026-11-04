// Copies on a Studio that signs people in: each call is held for its own
// verified viewer, this computer's folders are nobody's, so only what the
// deployment names is allowed, the account's ssh keys are not lent, and one
// person's jobs are not another's.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const dir = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'taa-')))
process.env.HOME = path.join(dir, 'home')
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.GUFI_BIN = path.join(dir, 'no-gufi')
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.PW_API_KEY = 'deploy-key'
process.env.PW_CLI = '/nonexistent/pw'
process.env.AUTH_JWKS_URL = 'https://keys.example.invalid/jwks'
process.env.STUDIO_LIBRARIES = JSON.stringify([{ id: 'far', label: 'Far', indexRoot: path.join(dir, 'idx'), readThrough: { destination: 'files-host', root: '/data' } }])
const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
write(path.join(process.env.KB_ROOT, 'notes', 'report.md'), '# Report\n')
fs.mkdirSync(path.join(dir, 'ix'), { recursive: true })
fs.mkdirSync(path.join(dir, 'idx'), { recursive: true })
const share = path.join(dir, 'share')
fs.mkdirSync(share)
fs.mkdirSync(path.join(dir, 'home', 'Downloads'), { recursive: true })

const modelCalls = []
let script = () => ({ calls: [['transfer_files', { paths: ['notes/report.md'] }]] })
let callSeq = 0
const filler = ' The assistant reports the result here in full so the reply reads as an answer. '.repeat(4)
const sseOf = chunks => new Response(chunks.map(c => `data: ${JSON.stringify(c)}\n\n`).join('') + 'data: [DONE]\n\n', { headers: { 'content-type': 'text/event-stream' } })
globalThis.fetch = async (url, init = {}) => {
  const u = new URL(String(url))
  if (u.pathname === '/api/openai/v1/chat/completions') {
    const body = JSON.parse(init.body)
    modelCalls.push(body)
    const last = body.messages.at(-1)
    if (last.role === 'tool') return sseOf([{ model: body.model, choices: [{ index: 0, delta: { content: `Result: ${last.content}.${filler}` } }] }, { model: body.model, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }])
    const r = script(body)
    return sseOf([
      { model: body.model, choices: [{ index: 0, delta: { tool_calls: r.calls.map(([name, args], index) => ({ index, id: `call_${++callSeq}`, type: 'function', function: { name, arguments: JSON.stringify(args) } })) } }] },
      { model: body.model, choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }] },
    ])
  }
  return new Response('{}', { status: 404 })
}

const credentials = await import('../dist/credentials.js')
credentials.setUserKey('u-alex', 'alex-key', false, null)
const approvals = await import('../dist/chat/approvals.js')
const { default: Fastify } = await import('fastify')
const { chatRoutes } = await import('../dist/chat/routes.js')
const { conversationRoutes } = await import('../dist/conversations.js')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
app.addHook('onRequest', async req => {
  const who = String(req.headers['x-test-user'] ?? '')
  if (who) req.user = { id: `u-${who}`, username: who }
})
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
const headers = who => (who ? { 'x-test-user': who } : {})
const newConversation = async (who = 'alex') => (await app.inject({ method: 'POST', url: '/api/chat/conversations', headers: headers(who), payload: { title: 't' } })).json().id
let seq = 0
async function ask(who, conversationId, text = 'download the report') {
  const res = await app.inject({ method: 'POST', url: '/api/chat/stream', headers: headers(who), payload: { model: 'test-model', conversationId, userMessageId: `u${++seq}`, messages: [{ role: 'user', content: text }] } })
  const events = parseSSE(res.payload)
  return { events, approval: events.find(e => e.event === 'approval')?.data.approval, done: events.find(e => e.event === 'done')?.data, text }
}
async function answer(who, conversationId, asked) {
  const res = await app.inject({ method: 'POST', url: '/api/chat/stream', headers: headers(who), payload: {
    model: 'test-model', conversationId, userMessageId: `r${++seq}`, parentMessageId: asked.done?.messageId,
    messages: [{ role: 'user', content: asked.text }, { role: 'assistant', content: null }],
    resume: { messageId: asked.done?.messageId, toolCallId: asked.approval?.id, allowed: true },
  } })
  return parseSSE(res.payload)
}
const options = async who => (await app.inject({ method: 'GET', url: '/api/chat/tool-options', headers: headers(who) })).json().tools.map(t => t.id)
const lastTool = () => modelCalls.at(-1).messages.at(-1).content

test('with no folder named by the deployment, copies are not offered and do not run', async () => {
  delete process.env.STUDIO_TRANSFER_FOLDERS
  assert.ok(!(await options('alex')).includes('transfers'))
  const id = await newConversation()
  const asked = await ask('alex', id)
  assert.equal(asked.approval, undefined)
  assert.match(lastTool(), /no folder for copies/)
})

test('Settings cannot widen the folders where people sign in', async () => {
  write(path.join(dir, 'ix', 'settings.json'), JSON.stringify({ transferFolders: [share] }))
  const D = await import('../dist/movers/destination.js')
  assert.deepEqual(D.allowedFolders(), [])
})

test('each call is held for its own verified viewer', async () => {
  process.env.STUDIO_TRANSFER_FOLDERS = share
  assert.ok((await options('alex')).includes('transfers'))
  const id = await newConversation()
  const asked = await ask('alex', id)
  assert.ok(asked.approval)
  assert.equal(approvals.heldCall(id, asked.approval.id).viewer, 'u-alex')
  assert.match(asked.approval.plan.find(r => r.label === 'To').value, /on the Studio's server/)

  // Nobody else, and nobody unverified, can answer it.
  for (const who of ['sam', '']) {
    const ev = await answer(who, id, asked)
    assert.match(ev.find(e => e.event === 'error').data.message, /Only alex/, who)
    assert.ok(!fs.existsSync(path.join(share, 'report.md')))
  }
  const ev = await answer('alex', id, asked)
  assert.equal(ev.filter(e => e.event === 'tool' && e.data.phase === 'result').at(-1).data.error, false)
  assert.equal(fs.readFileSync(path.join(share, 'report.md'), 'utf8'), '# Report\n')

  // One person's jobs are theirs.
  const list = async who => (await app.inject({ method: 'GET', url: '/api/transfers', headers: headers(who) })).json().transfers
  const mine = await list('alex')
  assert.equal(mine.length, 1)
  assert.deepEqual(await list('sam'), [])
  assert.deepEqual(await list(''), [])
  assert.equal((await app.inject({ method: 'POST', url: `/api/transfers/${mine[0].id}/cancel`, headers: headers('sam') })).statusCode, 404)
  fs.rmSync(path.join(share, 'report.md'))
})

test('a person with no platform key can copy, and still cannot change the platform', async () => {
  const id = await newConversation('sam')
  const asked = await ask('sam', id)
  assert.ok(asked.approval, 'copies need no platform key')
  await answer('sam', id, asked)
  assert.ok(fs.existsSync(path.join(share, 'report.md')))
  fs.rmSync(path.join(share, 'report.md'))
  script = () => ({ calls: [['stop_cluster', { clusterName: 'cluster-a' }]] })
  const held = await ask('sam', id, 'stop cluster-a')
  assert.equal(held.approval, undefined)
  assert.match(lastTool(), /only with your own platform key/)
  script = () => ({ calls: [['transfer_files', { paths: ['notes/report.md'] }]] })
})

test('a conversation that is someone else\'s holds nothing, and nobody present means no copy', async () => {
  const id = await newConversation('alex')
  await ask('alex', id, 'hello')
  const other = await ask('sam', id)
  assert.equal(other.approval, undefined)
  assert.match(lastTool(), /belongs to someone else/)
  assert.ok(!(await options('')).includes('transfers'))
})

test('this computer\'s Downloads, and the account\'s ssh keys, are not lent', async () => {
  const id = await newConversation()
  script = () => ({ calls: [['transfer_files', { paths: ['notes/report.md'], destination: '~/Downloads' }]] })
  assert.equal((await ask('alex', id)).approval, undefined)
  assert.match(lastTool(), /not an allowed folder/)
  script = () => ({ calls: [['transfer_files', { library: 'far', paths: ['x.txt'] }]] })
  assert.equal((await ask('alex', id)).approval, undefined)
  assert.match(lastTool(), /only a Studio with sign-in off/)
})
