import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
delete process.env.DELEGATION_ENABLED
const { effectiveSettings } = await import('../dist/settings.js')

test('delegation is on by default and DELEGATION_ENABLED=0 is the kill switch', () => {
  assert.equal(effectiveSettings().delegationEnabled, true)
  process.env.DELEGATION_ENABLED = '0'
  assert.equal(effectiveSettings().delegationEnabled, false)
  delete process.env.DELEGATION_ENABLED
})
