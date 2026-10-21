// A launcher's settings file: written for a POSIX shell to source, read
// back the same, and a restart only where a launcher offers one.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync } from 'node:child_process'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'life-'))
process.env.STUDIO_CONFIG_FILE = path.join(dir, 'studio.env')
const L = await import('../dist/lifecycle.js')

test('settings round-trip, and a shell sources them to the same values', () => {
  L.saveLauncherSetting('KB_ROOT', "/Users/alex/My Files/it's here")
  L.saveLauncherSetting('PORT', '4180')
  L.saveLauncherSetting('KB_ROOT', '/Users/alex/Research')
  assert.deepEqual(L.readLauncherSettings(), { KB_ROOT: '/Users/alex/Research', PORT: '4180' })
  L.saveLauncherSetting('LABEL', "Alex's notes")
  const out = execFileSync('sh', ['-c', `. "${process.env.STUDIO_CONFIG_FILE}"; printf '%s|%s' "$KB_ROOT" "$LABEL"`], { encoding: 'utf8' })
  assert.equal(out, "/Users/alex/Research|Alex's notes")
  assert.throws(() => L.saveLauncherSetting('bad name', 'x'))
  assert.throws(() => L.saveLauncherSetting('X', 'two\nlines'))
})

test('a restart is offered only by a launcher', () => {
  delete process.env.STUDIO_RESTARTABLE
  assert.equal(L.canRestart(), false)
  assert.equal(L.requestRestart(), false)
  process.env.STUDIO_RESTARTABLE = '1'
  let code = null
  L.setStopper(c => { code = c })
  assert.equal(L.requestRestart(), true)
  return new Promise(r => setTimeout(() => { assert.equal(code, L.RESTART_EXIT_CODE); r() }, 400))
})
