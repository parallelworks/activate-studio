import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
for (const k of ['DELEGATION_ENABLED', 'AUTH_JWKS_URL', 'PW_PLATFORM_HOST', 'SUGGESTED_PROMPTS', 'KB_MISSION']) delete process.env[k]
const { effectiveSettings, PERSONAL_MISSION, PERSONAL_PROMPTS } = await import('../dist/settings.js')

test("a personal install suggests questions about one's own files and has a mission saying so", () => {
  const eff = effectiveSettings()
  assert.ok(eff.suggestedPrompts.length >= 4)
  assert.deepEqual(eff.suggestedPrompts, PERSONAL_PROMPTS)
  assert.equal(eff.kbMission, PERSONAL_MISSION)
  assert.match(eff.kbMission, /one person's own files/)
  // A Studio the platform deploys keeps the shared defaults.
  process.env.PW_PLATFORM_HOST = 'platform.example.com'
  try {
    assert.equal(effectiveSettings().kbMission, '')
    assert.ok(effectiveSettings().suggestedPrompts.includes('What is in this knowledge base?'))
  } finally { delete process.env.PW_PLATFORM_HOST }
  // The environment's own still win.
  process.env.KB_MISSION = 'A lab notebook.'
  process.env.SUGGESTED_PROMPTS = JSON.stringify(['One question'])
  try {
    assert.equal(effectiveSettings().kbMission, 'A lab notebook.')
    assert.deepEqual(effectiveSettings().suggestedPrompts, ['One question'])
  } finally { delete process.env.KB_MISSION; delete process.env.SUGGESTED_PROMPTS }
})

test('delegation is on by default and DELEGATION_ENABLED=0 is the kill switch', () => {
  assert.equal(effectiveSettings().delegationEnabled, true)
  process.env.DELEGATION_ENABLED = '0'
  assert.equal(effectiveSettings().delegationEnabled, false)
  delete process.env.DELEGATION_ENABLED
})

test('a reply may spend 50 tool rounds by default, and STUDIO_MAX_TOOL_ROUNDS changes it', () => {
  delete process.env.STUDIO_MAX_TOOL_ROUNDS
  assert.equal(effectiveSettings().maxToolRounds, 50)
  process.env.STUDIO_MAX_TOOL_ROUNDS = '80'
  assert.equal(effectiveSettings().maxToolRounds, 80)
  delete process.env.STUDIO_MAX_TOOL_ROUNDS
})
