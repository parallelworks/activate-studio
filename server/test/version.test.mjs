// Which build is running answers "did the deploy land". A deployment has
// no git checkout, so the stamp written into the bundle has to win, and a
// checkout with no stamp has to fall back to the tag rather than inventing
// a version that would make a stale deploy look current.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))

// PROJECT_ROOT is two levels above the compiled module, which is the repo
// here and the bundle's app/ directory in a deployment.
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..')
const stamp = path.join(root, 'build-info.json')
const load = async () => (await import(`../dist/version.js?t=${Date.now()}${Math.random()}`)).buildInfo()

test('the bundle stamp wins, because a deployment has no checkout to ask', async t => {
  assert.ok(!fs.existsSync(stamp), 'the repo must not carry a stamp of its own')
  fs.writeFileSync(stamp, JSON.stringify({ version: 'v9.9', commit: 'deadbee', builtAt: '2026-01-02T03:04:05Z' }))
  t.after(() => fs.rmSync(stamp, { force: true }))
  const info = await load()
  assert.equal(info.version, 'v9.9')
  assert.equal(info.commit, 'deadbee')
  assert.equal(info.builtAt, '2026-01-02T03:04:05Z')
  assert.equal(info.source, 'bundle')
})

test('a stamp without a version is ignored rather than trusted', async t => {
  fs.writeFileSync(stamp, JSON.stringify({ commit: 'deadbee' }))
  t.after(() => fs.rmSync(stamp, { force: true }))
  const info = await load()
  assert.notEqual(info.source, 'bundle', 'an incomplete stamp must not be believed')
})

test('unreadable json falls through instead of throwing', async t => {
  fs.writeFileSync(stamp, 'not json at all')
  t.after(() => fs.rmSync(stamp, { force: true }))
  const info = await load()
  assert.ok(info.version, 'a broken stamp still yields an answer')
  assert.notEqual(info.source, 'bundle')
})

test('with no stamp the checkout answers, and says so', async () => {
  const info = await load()
  assert.equal(info.source, 'git', 'this test runs inside the repository')
  assert.match(info.commit ?? '', /^[0-9a-f]{7,}$/, 'a real short commit')
  // Untagged reads as dev rather than borrowing the package version, so a
  // stale deploy cannot look like a release.
  assert.ok(info.version === 'dev' || info.version.startsWith('v'), `unexpected version ${info.version}`)
})

test('the process start time is always reported', async () => {
  const info = await load()
  assert.ok(Date.parse(info.startedAt) > 0)
})
