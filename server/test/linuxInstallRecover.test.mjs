// The command install.sh writes (~/.local/bin/activate-studio) is the one way
// into a per-user install that lives outside its directory, so it is where an
// update cut off between its two directory moves is put right before the
// launcher starts: the recovery script of the staged or the previous copy
// beside the install finishes the update or puts the previous payload back.
// The recovery itself is tested in updatesLinux.test.mjs.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'

const REPO = path.resolve(import.meta.dirname, '..', '..')
const tmp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'install-recover-')))
const HOME = path.join(tmp, 'home')
const LIB = path.join(HOME, '.local', 'lib')
const TREE = path.join(LIB, 'activate-studio')
const DATA = path.join(HOME, '.local', 'share', 'activate-studio-data')
const COMMAND = path.join(HOME, '.local', 'bin', 'activate-studio')
const env = { HOME, PATH: '/usr/bin:/bin', STUDIO_RECOVER_WAIT: '0' }

const write = (file, body, mode = 0o644) => {
  fs.mkdirSync(path.dirname(file), { recursive: true })
  fs.writeFileSync(file, body, { mode })
}
const versionIn = dir => JSON.parse(fs.readFileSync(path.join(dir, 'app', 'build-info.json'), 'utf8')).version

/** What the recovery script counts as a complete payload, with a launcher that says where it ran. */
function payload(dir, version, { complete = true, recover = true } = {}) {
  write(path.join(dir, 'studio-run.sh'), '#!/bin/bash\n', 0o755)
  if (recover) write(path.join(dir, 'studio-recover.sh'), fs.readFileSync(path.join(REPO, 'deploy', 'linux', 'studio-recover.sh')), 0o755)
  if (complete) write(path.join(dir, 'node', 'bin', 'node'), '#!/bin/sh\n', 0o755)
  write(path.join(dir, 'app', 'server', 'dist', 'main.js'), '// server\n')
  write(path.join(dir, 'app', 'build-info.json'), `{\n "version": "${version}"\n}\n`)
  write(path.join(dir, 'update-signing.pub'), 'key\n')
  write(path.join(dir, 'manifest.json'), `{\n "version": "${version}"\n}\n`)
  write(path.join(dir, 'bin', 'activate-studio'), '#!/bin/bash\necho "ran $(basename "$(dirname "$(dirname "$0")")")"\n', 0o755)
}

// install.sh from an unpacked tree, as a person would run it.
const src = path.join(tmp, 'src', 'activate-studio')
payload(src, 'v1')
write(path.join(src, 'install.sh'), fs.readFileSync(path.join(REPO, 'desktop', 'linux', 'install.sh')), 0o755)
write(path.join(src, 'share', 'applications', 'activate-studio.desktop'), 'Exec=@BIN@\n')
const inst = spawnSync('bash', [path.join(src, 'install.sh'), '--move'], { encoding: 'utf8', env })
assert.equal(inst.status, 0, String(inst.stderr) + String(inst.stdout))

const swap = (from, to) => write(path.join(DATA, 'updates', 'swap.json'), `{\n "from": "${from}",\n "to": "${to}",\n "at": "2026-11-04T00:00:00Z",\n "reopen": "no"\n}\n`)
const launch = () => spawnSync('bash', [COMMAND], { encoding: 'utf8', env })
function cutOff({ stagedComplete = true, recover = true } = {}) {
  fs.rmSync(LIB + '/.activate-studio.new', { recursive: true, force: true })
  fs.rmSync(LIB + '/.activate-studio.previous', { recursive: true, force: true })
  fs.rmSync(TREE, { recursive: true, force: true })
  payload(LIB + '/.activate-studio.previous', 'v1', { recover })
  payload(LIB + '/.activate-studio.new', 'v2', { complete: stagedComplete, recover })
}

test('the command starts an install that is there, and recovers nothing', () => {
  assert.ok(fs.existsSync(path.join(TREE, 'install-kind')))
  const r = launch()
  assert.equal(r.status, 0, String(r.stderr))
  assert.equal(r.stdout.trim(), 'ran activate-studio')
})

test('an update cut off after the first move, with the new payload complete, is finished before the launcher starts', () => {
  cutOff()
  swap('v1', 'v2')
  const r = launch()
  assert.equal(r.stdout.trim(), 'ran activate-studio', String(r.stderr))
  assert.equal(versionIn(TREE), 'v2')
  assert.equal(versionIn(LIB + '/.activate-studio.previous'), 'v1')
  assert.equal(JSON.parse(fs.readFileSync(path.join(DATA, 'updates', 'pending.json'), 'utf8')).to, 'v2')
  assert.ok(!fs.existsSync(path.join(DATA, 'updates', 'swap.json')))
})

test('with the new payload partial, or no swap file, the previous payload is put back', () => {
  cutOff({ stagedComplete: false })
  swap('v1', 'v2')
  assert.equal(launch().stdout.trim(), 'ran activate-studio')
  assert.equal(versionIn(TREE), 'v1')
  cutOff()
  fs.rmSync(path.join(DATA, 'updates', 'swap.json'), { force: true })
  assert.equal(launch().stdout.trim(), 'ran activate-studio')
  assert.equal(versionIn(TREE), 'v1')
})

test('a previous payload that has no recovery script is put back by the command itself', () => {
  cutOff({ recover: false })
  fs.rmSync(LIB + '/.activate-studio.new', { recursive: true })
  assert.equal(launch().stdout.trim(), 'ran activate-studio')
  assert.equal(versionIn(TREE), 'v1')
})

test.after(() => { fs.rmSync(tmp, { recursive: true, force: true }) })
