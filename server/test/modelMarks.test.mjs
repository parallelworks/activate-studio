import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
const { markImpaired, stripAvailabilityMark } = await import('../dist/chat/routes.js')

test('a locked provider marks id and name, leaves other providers alone, and lists what it marked', () => {
  const verdicts = new Map([['me:lockedprov', { ok: false, kind: 'locked', unlockUrl: 'https://u' }], ['me:other', { ok: true, kind: null, unlockUrl: null }]])
  const { models, impaired } = markImpaired([
    { id: 'me:lockedprov/gemini', name: 'Provider (gemini)' },
    { id: 'me:other/model', name: 'Other' },
    { id: 'local-model' },
  ], verdicts)
  assert.equal(models[0].id, 'me:lockedprov/gemini [locked]')
  assert.equal(models[0].name, 'Provider (gemini) [locked]')
  assert.equal(models[0].callable, false)
  assert.equal(models[1].id, 'me:other/model')
  assert.equal(models[2].id, 'local-model')
  assert.deepEqual(impaired, [{ id: 'me:lockedprov/gemini', locked: true, unlock_url: 'https://u' }])
})
test('an unresponsive provider is marked unavailable, and the mark strips off an inbound id', () => {
  const verdicts = new Map([['me:lockedprov', { ok: false, kind: 'unavailable', unlockUrl: null }]])
  const { models } = markImpaired([{ id: 'me:lockedprov/gemini' }], verdicts)
  assert.equal(models[0].id, 'me:lockedprov/gemini [unavailable]')
  assert.equal(stripAvailabilityMark(models[0].id), 'me:lockedprov/gemini')
  assert.equal(stripAvailabilityMark('me:lockedprov/gemini [locked]'), 'me:lockedprov/gemini')
  assert.equal(stripAvailabilityMark('plain'), 'plain')
})
