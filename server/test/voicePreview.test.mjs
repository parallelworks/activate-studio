import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
delete process.env.VOICE_ENABLED; delete process.env.VOICE_URL
const { effectiveSettings } = await import('../dist/settings.js')
const { resolveModel, VOICE_STYLE } = await import('../dist/ragProxy.js')

test('the voice preview is off until a deployment turns it on, and the env flag can', () => {
  assert.equal(effectiveSettings().voiceEnabled, false)
  assert.equal(effectiveSettings().voiceUrl, '')
  process.env.VOICE_ENABLED = '1'; process.env.VOICE_URL = 'https://unmute.example'
  assert.equal(effectiveSettings().voiceEnabled, true)
  assert.equal(effectiveSettings().voiceUrl, 'https://unmute.example')
  delete process.env.VOICE_ENABLED; delete process.env.VOICE_URL
})
test('studio-voice is the agent with a spoken-answer style, and keeps an explicit underlying model', () => {
  const v = resolveModel('studio-voice')
  assert.equal(v.mode, 'agent'); assert.equal(v.voice, true)
  const w = resolveModel('studio-voice/me:provider/model-x')
  assert.equal(w.underlying, 'me:provider/model-x'); assert.equal(w.voice, true)
  assert.equal(resolveModel('studio-agent').voice, undefined)
  assert.match(VOICE_STYLE, /one or two short sentences/)
  assert.match(VOICE_STYLE, /No markdown/)
})
