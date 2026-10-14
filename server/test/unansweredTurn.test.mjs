// A user message whose reply was lost (the Studio restarted mid-turn, or the
// turn failed or was stopped before any text) comes back with an error, which
// the chat shows with a Retry button; while a reply is streaming it does not.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'unans-ix-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'unans-kb-'))
process.env.INDEX_BASE = IX
const msg = (id, role, parentId) => ({ id, role, content: id, parentId, timestamp: '2026-10-14T00:00:00Z' })
fs.writeFileSync(path.join(IX, 'conversations.json'), JSON.stringify([{
  id: 'c1', title: 'c1', owner: null, createdAt: '2026-10-14T00:00:00Z', updatedAt: '2026-10-14T00:00:00Z', activeBranchId: 'u2',
  messages: [msg('u1', 'user', null), msg('a1', 'assistant', 'u1'), msg('u2', 'user', 'a1')],
}]))

const { default: Fastify } = await import('fastify')
const { conversationRoutes, beginAnswer, NO_REPLY_NOTE } = await import('../dist/conversations.js')
const app = Fastify()
await app.register(conversationRoutes)
await app.ready()
const get = async () => (await app.inject({ method: 'GET', url: '/api/chat/conversations/c1' })).json().messages

test('the last user message, with no reply under it, carries the no-reply error', async () => {
  const m = await get()
  assert.equal(m.find(x => x.id === 'u2').error, NO_REPLY_NOTE)
  assert.equal(m.find(x => x.id === 'u1').error, undefined, 'an answered message has none')
  assert.equal(m.find(x => x.id === 'a1').error, undefined)
})

test('while a reply streams, the message is not reported as unanswered, and is again once it ends', async () => {
  const end = beginAnswer('c1')
  assert.equal((await get()).find(x => x.id === 'u2').error, undefined)
  end()
  end()
  assert.equal((await get()).find(x => x.id === 'u2').error, NO_REPLY_NOTE)
})

test('the error is never written to the stored conversation', () => {
  const stored = JSON.parse(fs.readFileSync(path.join(IX, 'conversations.json'), 'utf8'))
  assert.ok(stored[0].messages.every(m => m.error === undefined))
})
