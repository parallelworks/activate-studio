// One Studio at a time indexes a folder. A second one started on the same
// index folder (the app opened twice, a second port) searches and queries,
// and refuses index jobs naming the first, instead of taking the first
// one's tree from under a build; a stale lock (a process that died, or a
// number another program now has) is taken over; and a Studio still
// stopping is waited for.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawn } from 'node:child_process'

const tmp = p => fs.mkdtempSync(path.join(os.tmpdir(), p))
const IX = tmp('ilock-ix-')
Object.assign(process.env, { HOME: tmp('ilock-home-'), KB_ROOT: tmp('ilock-kb-'), INDEX_BASE: IX, PORT: '4999' })
for (const k of ['AUTH_JWKS_URL', 'STUDIO_CONFIG_FILE', 'STUDIO_RESTARTABLE', 'PW_PLATFORM_HOST', 'STUDIO_INDEX_DIR', 'STUDIO_DATA_DIR', 'STUDIO_LIBRARIES']) delete process.env[k]

const K = await import('../dist/instanceLock.js')
const I = await import('../dist/indexing.js')
const LOCK = path.join(IX, '.studio-instance')
const lockOf = () => JSON.parse(fs.readFileSync(LOCK, 'utf8'))
const write = h => fs.writeFileSync(LOCK, JSON.stringify({ port: null, startedAt: new Date().toISOString(), ...h }))

/** A process that looks like a Studio to `ps` (node, main.js in its arguments) and runs until killed. */
const fakeStudio = () => spawn(process.execPath, ['-e', 'setInterval(() => {}, 1000)', 'server/dist/main.js'], { stdio: 'ignore' })
const waitGone = child => new Promise(resolve => { if (child.exitCode !== null || child.signalCode) resolve(); else child.once('exit', resolve) })

test('the first Studio takes the folder, and a test of a module that never asked is allowed', async () => {
  K.resetIndexLockForTests()
  assert.equal(K.indexWorkAllowed(), true)
  assert.equal(await K.acquireIndexLock(() => {}, 500), true)
  assert.equal(lockOf().pid, process.pid)
  assert.equal(lockOf().port, 4999)
  assert.equal(K.indexWorkAllowed(), true)
  assert.doesNotThrow(() => K.requireIndexWork())
  // Asking again is the same process: still its own.
  assert.equal(await K.acquireIndexLock(() => {}, 500), true)
})

test('a second Studio on a folder a running Studio holds does no index work, and says which one holds it', async () => {
  K.resetIndexLockForTests()
  const first = fakeStudio()
  try {
    write({ pid: first.pid, port: 4180 })
    const log = []
    assert.equal(await K.acquireIndexLock(m => log.push(m), 600), false)
    assert.equal(K.indexWorkAllowed(), false)
    assert.match(log.join('\n'), new RegExp(`another Studio \\(process ${first.pid}, port 4180\\) is using the index folder`))
    // The first one's lock is left as it was.
    assert.equal(lockOf().pid, first.pid)
    // Index jobs are refused, plainly, from every entry point.
    assert.throws(() => K.requireIndexWork(), e => e.status === 409 && new RegExp(`process ${first.pid}, port 4180`).test(e.message) && /Stop it, or use this one only to search and query/.test(e.message))
    assert.throws(() => I.startBuild(), e => e.status === 409)
    assert.throws(() => I.incrementalIndexDir('notes'), e => e.status === 409)
    assert.throws(() => I.startDelete(), e => e.status === 409)
  } finally { first.kill(); await waitGone(first); K.resetIndexLockForTests() }
})

test('a lock left by a process that died, or by one that is not a Studio, is taken over', async () => {
  K.resetIndexLockForTests()
  // A process that has exited.
  const dead = fakeStudio()
  const pid = dead.pid
  dead.kill(); await waitGone(dead)
  write({ pid })
  assert.equal(await K.acquireIndexLock(() => {}, 600), true)
  assert.equal(lockOf().pid, process.pid)
  // A live process that is not a Studio, as a number reused after a crash would be.
  K.resetIndexLockForTests()
  const other = spawn('sleep', ['30'], { stdio: 'ignore' })
  try {
    write({ pid: other.pid })
    assert.equal(await K.acquireIndexLock(() => {}, 600), true)
    assert.equal(lockOf().pid, process.pid)
  } finally { other.kill(); await waitGone(other); K.resetIndexLockForTests() }
  // A lock file that is not one.
  fs.writeFileSync(LOCK, 'not json')
  assert.equal(await K.acquireIndexLock(() => {}, 600), true)
  K.resetIndexLockForTests()
})

test('a Studio still stopping is waited for: the folder is taken once it has gone', async () => {
  K.resetIndexLockForTests()
  const first = fakeStudio()
  write({ pid: first.pid })
  setTimeout(() => first.kill(), 700)
  const t0 = Date.now()
  assert.equal(await K.acquireIndexLock(() => {}, 10_000), true)
  assert.ok(Date.now() - t0 >= 600 && Date.now() - t0 < 8000, `took ${Date.now() - t0} ms`)
  assert.equal(lockOf().pid, process.pid)
  await waitGone(first)
  K.resetIndexLockForTests()
})

test('the lock is released as the process exits, so the next start finds it free', async () => {
  K.resetIndexLockForTests()
  const dir = tmp('ilock-exit-')
  const child = spawn(process.execPath, ['--input-type=module', '-e', `
    process.env.INDEX_BASE = ${JSON.stringify(dir)}; process.env.KB_ROOT = ${JSON.stringify(dir)}
    const K = await import(${JSON.stringify(new URL('../dist/instanceLock.js', import.meta.url).href)})
    await K.acquireIndexLock(() => {}, 500)
    console.log('held')
  `], { stdio: ['ignore', 'pipe', 'inherit'] })
  let out = ''
  child.stdout.on('data', d => { out += d })
  await waitGone(child)
  assert.match(out, /held/)
  assert.ok(!fs.existsSync(path.join(dir, '.studio-instance')), 'removed as it exited')
})
