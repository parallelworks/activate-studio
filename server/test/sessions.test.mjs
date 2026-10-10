// Platform sessions: the apps workflows open, read from the platform API
// with the viewer's key, tied to the run that opened them, and shown in
// the conversation that launched that run.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'sx-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'skb-'))
process.env.INDEX_BASE = IX
process.env.STUDIO_RUN_POLL_MS = '15'
process.env.PW_API_KEY = 'deploy-key'
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
fs.writeFileSync(path.join(IX, 'conversations.json'), JSON.stringify([{ id: 'c1', title: 'explore', owner: 'me', createdAt: 'x', updatedAt: 'x', activeBranchId: null,
  messages: [{ id: 'u1', role: 'user', content: 'open the explorer', timestamp: 'x' }, { id: 'a1', role: 'assistant', content: 'launched', parentId: 'u1', timestamp: 'x' }] }]))

const RAW = [
  { name: 'explorer-00012', user: 'me', type: 'endpoint', status: 'running', healthy: true, domainName: 'explorer-00012.sessions.test',
    externalHref: 'https://explorer-00012.sessions.test/', createdAt: '2026-10-10T02:00:00Z', targetName: 'cluster-a',
    workflowRun: { id: 'r1', name: 'design-explorer', displayName: 'Design Explorer', number: 12, slug: 'design-explorer-00012', status: 'running' } },
  { name: 'code', user: 'me', type: 'vscode', status: 'running', externalHref: '/me/session/me/code?folder=%2Fhome', createdAt: '2026-10-10T01:00:00Z' },
  { name: 'old', user: 'other', type: 'endpoint', status: 'off', domainName: 'old.sessions.test', externalHref: 'https://old.sessions.test/', createdAt: '2026-10-09T00:00:00Z' },
  { user: 'nameless' },
]
const seen = []
globalThis.fetch = async (url, init) => {
  seen.push({ url: String(url), auth: init?.headers?.Authorization ?? null })
  if (String(url) === 'https://platform.test/api/sessions') return Response.json(RAW)
  return new Response('no', { status: 404 })
}

const sessions = await import('../dist/sessions.js')
const runs = await import('../dist/runs.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
await app.register(sessions.sessionRoutes)
await app.ready()
const sleep = ms => new Promise(r => setTimeout(r, ms))

test('a session record becomes an absolute address, its host kind, and the run that opened it', () => {
  const P = 'https://platform.test'
  const ex = sessions.sessionRow(RAW[0], P)
  assert.equal(ex.url, 'https://explorer-00012.sessions.test/')
  assert.equal(ex.ownHost, true)
  assert.deepEqual(ex.run, { slug: 'design-explorer-00012', workflow: 'Design Explorer', number: 12, status: 'running' })
  assert.equal(ex.resource, 'cluster-a')
  const code = sessions.sessionRow(RAW[1], P)
  assert.equal(code.url, 'https://platform.test/me/session/me/code?folder=%2Fhome', 'a platform path is made absolute')
  assert.equal(code.ownHost, false, 'served under the platform address, so not framable')
  assert.equal(code.run, null)
  assert.equal(sessions.sessionRow(RAW[3], P), null, 'a record without a name is dropped')
  assert.equal(sessions.sessionRow({ name: 'x', externalHref: 'http://plain.example/' }, P).url, null, 'only https is offered')
})

test('the list is read with the key, newest first, and filters by run; one session by name', async () => {
  sessions.resetSessionsForTests(); seen.length = 0
  const all = (await app.inject({ url: '/api/sessions' })).json().sessions
  assert.deepEqual(all.map(s => s.name), ['explorer-00012', 'code', 'old'])
  assert.equal(seen[0].auth, 'Bearer deploy-key')
  const one = (await app.inject({ url: '/api/sessions?run=design-explorer-00012' })).json().sessions
  assert.deepEqual(one.map(s => s.name), ['explorer-00012'])
  assert.equal((await app.inject({ url: '/api/sessions/item?user=me&name=code' })).json().type, 'vscode')
  assert.equal((await app.inject({ url: '/api/sessions/item?user=me&name=nope' })).statusCode, 404)
})

test('a session on its own host is embedded; one under the platform address gets a link', () => {
  const P = 'https://platform.test'
  assert.equal(sessions.sessionEmbed(sessions.sessionRow(RAW[0], P)), '![explorer-00012](/?embed=session&user=me&name=explorer-00012)')
  assert.equal(sessions.sessionEmbed(sessions.sessionRow(RAW[1], P)), '[Open code in a new tab](https://platform.test/me/session/me/code?folder=%2Fhome)')
})

test('a run launched from chat shows its session in that conversation, once', async () => {
  sessions.resetSessionsForTests()
  runs.setRunsCli(async args => JSON.stringify({ slug: args[3], status: 'running' }))
  runs.recordRun({ slug: 'design-explorer-00012', workflow: 'design-explorer', conversationId: 'c1', owner: 'me' })
  const notes = () => JSON.parse(fs.readFileSync(path.join(IX, 'conversations.json'), 'utf8'))[0].messages
    .filter(m => /opened the session/.test(m.content))
  for (let i = 0; i < 100 && notes().length === 0; i++) await sleep(15)
  await sleep(15 * 8)
  const shown = notes()
  assert.equal(shown.length, 1, 'posted once, not on every poll')
  assert.match(shown[0].content, /Run design-explorer-00012 of design-explorer opened the session explorer-00012/)
  assert.match(shown[0].content, /!\[explorer-00012\]\(\/\?embed=session&user=me&name=explorer-00012\)/)
  const events = fs.readFileSync(path.join(IX, 'runs.jsonl'), 'utf8').trim().split('\n').map(l => JSON.parse(l))
  assert.ok(events.some(e => e.t === 'session' && e.slug === 'design-explorer-00012'), 'recorded, so a restart does not post it again')
  runs.resetRunsForTests()
})
