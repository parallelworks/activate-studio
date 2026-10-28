// A call to a tool that changes something on the platform is held until the
// person who asked approves that exact call in the chat. Drives the real
// chat route with a scripted model and a stand-in platform: the hold, an
// approval, a denial, running the alternative, and every refusal (other
// arguments, an expired hold, a changed tool, someone else's answer, a
// conversation that is not the viewer's, nobody present, a switch turned off).
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'ta-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ta-ix-'))
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.PW_API_KEY = 'deploy-key'
process.env.PW_CLI = '/nonexistent/pw'
// Platform identity on, so a viewer is a verified user; nothing is fetched from this URL.
process.env.AUTH_JWKS_URL = 'https://keys.example.invalid/jwks'

const ACCOUNTS = { 'alex-key': 'alex', 'sam-key': 'sam', 'deploy-key': 'deployer' }
const posts = []
const modelCalls = []
const filler = ' '.repeat(1) + 'The platform answered and the assistant reports it here in full. '.repeat(4)
const defaultScript = body => {
  const last = body.messages.at(-1)
  if (!body.tools || last.role === 'tool') return { text: `Result: ${last.content}.${filler}` }
  const q = String(body.messages.filter(m => m.role === 'user').at(-1)?.content ?? '')
  if (/both/.test(q)) return { calls: [['stop_cluster', { clusterName: 'cluster-a' }], ['destroy_cluster', { clusterName: 'cluster-b' }]] }
  if (/start/.test(q)) return { calls: [['start_cluster', { clusterName: 'cluster-a' }]] }
  if (/cancel/.test(q)) return { calls: [['cancel_workflow_run', { slug: 'sweep-00012' }]] }
  return { calls: [['stop_cluster', { clusterName: 'cluster-a' }]] }
}
let script = defaultScript
let callSeq = 0
const sseOf = chunks => new Response(chunks.map(c => `data: ${JSON.stringify(c)}\n\n`).join('') + 'data: [DONE]\n\n', { headers: { 'content-type': 'text/event-stream' } })

globalThis.fetch = async (url, init = {}) => {
  const u = new URL(String(url))
  const auth = String(init.headers?.Authorization ?? '').replace(/^Bearer /, '')
  if (u.pathname === '/api/openai/v1/chat/completions') {
    const body = JSON.parse(init.body)
    modelCalls.push(body)
    const r = script(body)
    if (r.text) return sseOf([{ model: body.model, choices: [{ index: 0, delta: { content: r.text } }] }, { model: body.model, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] }])
    return sseOf([
      { model: body.model, choices: [{ index: 0, delta: { tool_calls: r.calls.map(([name, args], index) => ({ index, id: `call_${++callSeq}`, type: 'function', function: { name, arguments: JSON.stringify(args) } })) } }] },
      { model: body.model, choices: [{ index: 0, delta: {}, finish_reason: 'tool_calls' }] },
    ])
  }
  if (u.pathname === '/api/auth/session') {
    return ACCOUNTS[auth] ? Response.json({ username: ACCOUNTS[auth], organization: 'org-a' }) : Response.json({ message: 'unauthorized' }, { status: 401 })
  }
  if ((init.method ?? 'GET') === 'POST') {
    posts.push({ path: u.pathname, key: auth })
    if (u.pathname.includes('/clusters/cluster-missing/')) return Response.json({ error: true, message: 'Cluster cluster-missing not found.' }, { status: 404 })
    return new Response(null, { status: 204 })
  }
  return new Response('{}', { status: 404 })
}

const credentials = await import('../dist/credentials.js')
credentials.setUserKey('u-alex', 'alex-key', false, null)
credentials.setUserKey('u-sam', 'sam-key', false, null)
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
async function newConversation(who = 'alex') {
  return (await app.inject({ method: 'POST', url: '/api/chat/conversations', headers: headers(who), payload: { title: 't' } })).json().id
}
let seq = 0
async function ask(who, conversationId, text, extra = {}) {
  const res = await app.inject({ method: 'POST', url: '/api/chat/stream', headers: headers(who), payload: { model: 'test-model', conversationId, userMessageId: `u${++seq}`, messages: [{ role: 'user', content: text }], ...extra } })
  const events = parseSSE(res.payload)
  const approval = events.find(e => e.event === 'approval')?.data.approval
  const done = events.find(e => e.event === 'done')?.data
  return { events, approval, done, text }
}
async function answer(who, conversationId, asked, resume) {
  const res = await app.inject({ method: 'POST', url: '/api/chat/stream', headers: headers(who), payload: {
    model: 'test-model', conversationId, userMessageId: `r${++seq}`, parentMessageId: asked.done?.messageId,
    messages: [{ role: 'user', content: asked.text }, { role: 'assistant', content: null }],
    resume: { messageId: asked.done?.messageId, toolCallId: asked.approval?.id, allowed: true, ...resume },
  } })
  return parseSSE(res.payload)
}
const stored = async (id, who = 'alex') => (await app.inject({ method: 'GET', url: `/api/chat/conversations/${id}`, headers: headers(who) })).json()
const toolNames = body => (body.tools ?? []).map(t => t.function.name)

test('a change is held, not run: the reply pauses on an approval for that exact call', async () => {
  posts.length = 0
  const c = await newConversation()
  const asked = await ask('alex', c, 'stop cluster-a, it is idle')
  assert.ok(toolNames(modelCalls.at(-1)).includes('stop_cluster'), 'the tool was offered')
  assert.equal(posts.length, 0, 'nothing reached the platform')
  assert.equal(asked.done.finishReason, 'tool_approval')
  assert.ok(asked.events.some(e => e.event === 'tool' && e.data.phase === 'wait' && e.data.id === asked.approval.id))
  assert.equal(asked.approval.kind, 'approval')
  assert.equal(asked.approval.approvalKind, 'confirm')
  assert.equal(asked.approval.header, 'Stop cluster cluster-a?')
  assert.equal(asked.approval.command, 'stop_cluster(user: "alex", clusterName: "cluster-a")', 'the owner defaults to the viewer and is shown')
  assert.equal(asked.approval.canAllowDirectory, false)
  assert.deepEqual(asked.approval.instead.map(i => i.tool), ['destroy_cluster'])
  assert.ok(Date.parse(asked.approval.expiresAt) - Date.now() > 29 * 60_000, 'held for about 30 minutes')
  const rec = approvals.heldCall(c, asked.approval.id)
  assert.equal(rec.tool, 'stop_cluster')
  assert.equal(rec.args, '{"clusterName":"cluster-a","user":"alex"}')
  assert.equal(rec.viewer, 'u-alex')
  assert.match(rec.hash, /^[0-9a-f]{64}$/)
  assert.equal(rec.messageId, asked.done.messageId)
  const msg = (await stored(c)).messages.find(m => m.id === asked.done.messageId)
  assert.deepEqual(msg.parts.map(p => [p.kind, p.status ?? null]), [['tool_call', 'waiting'], ['approval', null]])
  assert.ok(!/returned no answer/.test(msg.content), 'a paused reply is not an empty one')
})

test('approving runs the held call once, with the viewer\'s key, and the reply carries on', async () => {
  posts.length = 0
  const c = await newConversation()
  const asked = await ask('alex', c, 'stop cluster-a')
  const events = await answer('alex', c, asked, {})
  assert.deepEqual(posts, [{ path: '/api/organizations/org-a/users/alex/clusters/cluster-a/stop', key: 'alex-key' }])
  const result = events.find(e => e.event === 'tool' && e.data.phase === 'result')
  assert.equal(result.data.id, asked.approval.id)
  assert.equal(result.data.summary, 'stopped cluster-a')
  assert.equal(result.data.error, false)
  const lastModel = modelCalls.at(-1)
  assert.equal(lastModel.messages.at(-1).role, 'tool')
  assert.equal(lastModel.messages.at(-1).content, 'Done.', 'the model gets the platform\'s answer')
  assert.equal(lastModel.messages.at(-1).tool_call_id, asked.approval.id)
  assert.ok(lastModel.messages.some(m => m.role === 'assistant' && m.tool_calls?.[0]?.id === asked.approval.id), 'the paused round is replayed')
  assert.match(events.filter(e => e.event === 'content').map(e => e.data.text).join(''), /Result: Done\./)
  const conv = await stored(c)
  const paused = conv.messages.find(m => m.id === asked.done.messageId)
  const call = paused.parts.find(p => p.kind === 'tool_call')
  assert.equal(call.status, 'ok')
  assert.equal(call.decision.status, 'approved')
  assert.equal(call.decision.name, 'alex')
  assert.equal(paused.parts.find(p => p.kind === 'approval').resolved, true)
  const next = conv.messages.find(m => m.id === events.find(e => e.event === 'done').data.messageId)
  assert.equal(next.parentId, asked.done.messageId, 'the continuation hangs under the reply it resumes')
  assert.equal(conv.messages.filter(m => m.role === 'user').length, 1, 'no user message is recorded for the answer')
  // The same approval again runs nothing.
  const again = await answer('alex', c, asked, {})
  assert.match(again.find(e => e.event === 'error').data.message, /no longer waiting/)
  assert.equal(posts.length, 1)
})

test('denying runs nothing and tells the model the user declined, with their note', async () => {
  posts.length = 0
  const c = await newConversation()
  const asked = await ask('alex', c, 'stop cluster-a')
  const events = await answer('alex', c, asked, { allowed: false, feedback: 'not until the job finishes' })
  assert.equal(posts.length, 0)
  const toModel = modelCalls.at(-1).messages.at(-1)
  assert.equal(toModel.role, 'tool')
  assert.match(toModel.content, /^The user declined this call, so it did not run\. Do not call it again unless the user asks\. They said: not until the job finishes$/)
  assert.equal(events.find(e => e.event === 'tool' && e.data.phase === 'result').data.summary, 'declined')
  const call = (await stored(c)).messages.find(m => m.id === asked.done.messageId).parts.find(p => p.kind === 'tool_call')
  assert.equal(call.decision.status, 'denied')
  assert.equal(call.decision.note, 'not until the job finishes')
  assert.equal(approvals.heldCall(c, asked.approval.id), undefined, 'a denial settles the hold')
})

test('the user can run the alternative the hold allows, with the same arguments, and nothing else', async () => {
  posts.length = 0
  const c = await newConversation()
  const asked = await ask('alex', c, 'stop cluster-a')
  const events = await answer('alex', c, asked, { tool: 'destroy_cluster' })
  assert.deepEqual(posts.map(p => p.path), ['/api/organizations/org-a/users/alex/clusters/cluster-a/destroy'])
  assert.equal(events.find(e => e.event === 'tool' && e.data.phase === 'result').data.summary, 'destroyed cluster-a (in place of stop_cluster)')
  assert.match(modelCalls.at(-1).messages.at(-1).content, /^The user chose destroy_cluster in place of stop_cluster, so destroy_cluster ran\. Done\.$/)
  assert.equal((await stored(c)).messages.find(m => m.id === asked.done.messageId).parts.find(p => p.kind === 'tool_call').name, 'destroy_cluster')

  posts.length = 0
  const start = await ask('alex', c, 'start cluster-a')
  assert.equal(start.approval.toolName, 'start_cluster')
  assert.equal(start.approval.instead, undefined)
  const refused = await answer('alex', c, start, { tool: 'destroy_cluster' })
  assert.match(refused.find(e => e.event === 'error').data.message, /destroy_cluster cannot be run in place of start_cluster/)
  assert.equal(posts.length, 0)
  assert.ok(approvals.heldCall(c, start.approval.id), 'still held for a proper answer')
})

test('an approval for other arguments is refused, and the call stays held for the right one', async () => {
  posts.length = 0
  const c = await newConversation()
  const asked = await ask('alex', c, 'stop cluster-a')
  for (const args of ['{"clusterName":"cluster-b"}', '{"clusterName":"cluster-a","user":"sam"}', 'not json']) {
    const events = await answer('alex', c, asked, { args })
    assert.match(events.find(e => e.event === 'error').data.message, /other arguments/, args)
  }
  assert.equal(posts.length, 0)
  // The same call written another way is the same call.
  await answer('alex', c, asked, { args: '{ "user": "alex", "clusterName": "cluster-a" }' })
  assert.deepEqual(posts.map(p => p.path), ['/api/organizations/org-a/users/alex/clusters/cluster-a/stop'])
})

test('a held call expires and is refused after, as is one whose tool definition changed', async () => {
  posts.length = 0
  const c = await newConversation()
  const asked = await ask('alex', c, 'stop cluster-a')
  approvals.heldCall(c, asked.approval.id).expiresAt = new Date(Date.now() - 1000).toISOString()
  const events = await answer('alex', c, asked, {})
  assert.match(events.find(e => e.event === 'error').data.message, /expired before it was answered; nothing ran/)
  assert.equal(approvals.heldCall(c, asked.approval.id), undefined, 'an expired hold is dropped')

  const second = await ask('alex', c, 'stop cluster-a')
  approvals.heldCall(c, second.approval.id).hash = 'a-different-definition'
  const changed = await answer('alex', c, second, {})
  assert.match(changed.find(e => e.event === 'error').data.message, /stop_cluster tool changed after this was asked/)
  assert.equal(posts.length, 0)
})

test('only the person who asked can answer; everyone else sees whose answer it waits for', async () => {
  posts.length = 0
  const c = await newConversation('alex')
  const asked = await ask('alex', c, 'stop cluster-a')
  const bySam = await answer('sam', c, asked, {})
  assert.match(bySam.find(e => e.event === 'error').data.message, /Only alex, who asked for this, can answer it; nothing ran/)
  const noOne = await answer(null, c, asked, {})
  assert.match(noOne.find(e => e.event === 'error').data.message, /Only alex/)
  assert.equal(posts.length, 0)
  const approvalFor = async who => (await stored(c, who)).messages.find(m => m.id === asked.done.messageId).parts.find(p => p.kind === 'approval')
  assert.equal((await approvalFor('sam')).waitingOn, 'alex')
  assert.equal((await approvalFor('alex')).waitingOn, undefined)
  await answer('alex', c, asked, {})
  assert.equal(posts.length, 1)
})

test('a viewer in someone else\'s conversation, with no key of their own, or with no identity is not offered changes', async () => {
  posts.length = 0
  const c = await newConversation('alex')
  // Sam continues Alex's conversation (shared history): no changes offered.
  await ask('sam', c, 'stop cluster-a')
  assert.ok(!toolNames(modelCalls.at(-1)).includes('stop_cluster'))
  assert.ok(toolNames(modelCalls.at(-1)).includes('list_clusters'), 'the reads still are')
  // A model that calls one anyway gets told why, and nothing is held.
  const forced = await ask('sam', c, 'stop cluster-a')
  const r = forced.events.find(e => e.event === 'tool' && e.data.phase === 'result')
  assert.equal(r.data.error, true)
  assert.equal(forced.approval, undefined)
  assert.match(modelCalls.at(-1).messages.at(-1).content, /not available in this chat: this conversation belongs to someone else\. Nothing ran\./)

  // Kim has no personal key: the deployment's credential is not theirs to change things with.
  const k = await newConversation('kim')
  await ask('kim', k, 'stop cluster-a')
  assert.match(modelCalls.at(-1).messages.at(-1).content, /changes run only with your own platform key/)
  // No verified person at all.
  const n = await newConversation(null)
  await ask(null, n, 'stop cluster-a')
  assert.match(modelCalls.at(-1).messages.at(-1).content, /nobody in this chat can approve a change/)
  assert.equal(posts.length, 0)

  const options = async who => (await app.inject({ method: 'GET', url: '/api/chat/tool-options', headers: headers(who) })).json().tools.map(o => o.id)
  assert.ok((await options('alex')).includes('changes'))
  assert.ok(!(await options('kim')).includes('changes'))
  assert.ok(!(await options(null)).includes('changes'))
})

test('the reader\'s switch turns them off, and one change waits at a time', async () => {
  posts.length = 0
  const c = await newConversation()
  await ask('alex', c, 'stop cluster-a', { tools: ['kb', 'workflows'] })
  assert.ok(!toolNames(modelCalls.at(-1)).includes('stop_cluster'))
  assert.match(modelCalls.at(-1).messages.at(-1).content, /switched off Platform changes/)

  const both = await ask('alex', c, 'stop both clusters')
  assert.equal(both.approval.toolName, 'stop_cluster')
  const second = both.events.find(e => e.event === 'tool' && e.data.phase === 'result')
  assert.equal(second.data.error, true)
  assert.equal(approvals.heldCallsIn(c).length, 1)
  await answer('alex', c, both, {})
  assert.deepEqual(posts.map(p => p.path), ['/api/organizations/org-a/users/alex/clusters/cluster-a/stop'])
  const replayed = modelCalls.at(-1).messages.filter(m => m.role === 'tool').map(m => m.content)
  assert.ok(replayed.some(t => /Only one change waits for the user's approval at a time/.test(t)), 'the refused call\'s result is part of the replay')
})

test('a cancel and a refused change pass the platform\'s own answer back', async () => {
  posts.length = 0
  const c = await newConversation()
  const cancel = await ask('alex', c, 'cancel the run sweep-00012')
  assert.equal(cancel.approval.command, 'cancel_workflow_run(slug: "sweep-00012")')
  await answer('alex', c, cancel, {})
  assert.deepEqual(posts.map(p => p.path), ['/api/workflow-runs/sweep-00012/cancel'])
  script = body => (body.tools && body.messages.at(-1).role === 'user' ? { calls: [['stop_cluster', { clusterName: 'cluster-missing' }]] } : defaultScript(body))
  try {
    const missing = await ask('alex', c, 'stop cluster-missing')
    const events = await answer('alex', c, missing, {})
    const r = events.find(e => e.event === 'tool' && e.data.phase === 'result')
    assert.equal(r.data.error, true)
    assert.equal(modelCalls.at(-1).messages.at(-1).content, 'Cluster cluster-missing not found.')
  } finally { script = defaultScript }
})

test('the Settings catalog marks the tools that ask first and their alternative', async () => {
  const tools = (await app.inject({ method: 'GET', url: '/api/chat/tools', headers: headers('alex') })).json().tools
  const stop = tools.find(t => t.name === 'stop_cluster')
  assert.equal(stop.asksFirst, true)
  assert.deepEqual(stop.instead, ['destroy_cluster'])
  assert.equal(tools.find(t => t.name === 'list_storage').asksFirst, undefined)
})

test('with a platform connected, a question about storage gets the platform tools and the platform section', async () => {
  script = () => ({ text: `Here is the storage you can use.${filler}` })
  try {
    const c = await newConversation()
    await ask('alex', c, 'Which storage and file systems can I use?')
    const body = modelCalls.at(-1)
    for (const n of ['list_storage', 'list_workflows', 'list_clusters', 'hpc_status']) assert.ok(toolNames(body).includes(n), n)
    assert.match(body.messages[0].content, /Platform workflows registered in this account/)
    assert.match(body.messages[0].content, /list_sessions or watch_run/)
    const ids = (await app.inject({ method: 'GET', url: '/api/chat/tool-options', headers: headers('alex') })).json().tools.map(t => t.id)
    for (const id of ['workflows', 'changes', 'agents']) assert.ok(ids.includes(id), id)
  } finally { script = defaultScript }
})
