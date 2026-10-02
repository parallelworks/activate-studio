// Bulk deletion of chat conversations: one request removes many, and only
// the caller's own (or unowned) conversations are removed.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'bulk-ix-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'bulk-kb-'))
process.env.INDEX_BASE = IX
// Auth on, so ownership applies; nothing is fetched from this URL.
process.env.AUTH_JWKS_URL = 'https://keys.example.invalid/jwks'
const conv = (id, owner) => ({ id, title: id, owner, createdAt: '2026-10-01T00:00:00Z', updatedAt: '2026-10-01T00:00:00Z', activeBranchId: null, messages: [] })
fs.writeFileSync(path.join(IX, 'conversations.json'), JSON.stringify([
  conv('a', 'me'), conv('b', 'me'), conv('c', 'me'), conv('theirs', 'someone-else'), conv('legacy', null),
]))

const { default: Fastify } = await import('fastify')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const { conversationRoutes } = await import('../dist/conversations.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
app.addHook('onRequest', async req => { req.user = { id: 'u-me', username: 'me' } })
await app.register(conversationRoutes)
await app.ready()

test('deletes the listed own and unowned conversations in one request, skipping the rest', async () => {
  const res = await app.inject({ method: 'POST', url: '/api/chat/conversations/delete', payload: { ids: ['a', 'b', 'theirs', 'legacy', 'nope'] } })
  assert.equal(res.statusCode, 200)
  const body = res.json()
  assert.deepEqual(body.deleted.sort(), ['a', 'b', 'legacy'])
  assert.deepEqual(body.skipped.sort(), ['nope', 'theirs'])
  // Shared history is on by default, so the other person's conversation was
  // visible to this caller, and still not deleted.
  const left = (await app.inject({ method: 'GET', url: '/api/chat/conversations' })).json().map(c => c.id).sort()
  assert.deepEqual(left, ['c', 'theirs'])
  // The save is asynchronous; the file always holds a whole snapshot, old or new.
  let stored = []
  for (let i = 0; i < 50; i++) {
    stored = JSON.parse(fs.readFileSync(path.join(IX, 'conversations.json'), 'utf8')).map(c => c.id).sort()
    if (stored.length === 2) break
    await new Promise(r => setTimeout(r, 20))
  }
  assert.deepEqual(stored, ['c', 'theirs'])
})

test('an empty or missing list is refused', async () => {
  assert.equal((await app.inject({ method: 'POST', url: '/api/chat/conversations/delete', payload: { ids: [] } })).statusCode, 400)
  assert.equal((await app.inject({ method: 'POST', url: '/api/chat/conversations/delete', payload: {} })).statusCode, 400)
})
