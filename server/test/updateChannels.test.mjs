// Update channels (docs/UPDATES.md): Stable and Preview, releases without a
// channel field, the older prerelease setting, Preview kept from Stable, the
// explicit install of an older version (Move to Stable and the version
// picker) with its signature, size and SHA-256 checks, the state snapshot
// before every change of version and its restore, the state format marker,
// and a release that asks for the app to be reopened. The fixture is the
// one updates.test.mjs uses.
import test from 'node:test'
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import fs from 'node:fs'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'
import { spawn, spawnSync } from 'node:child_process'
import { haveOpenssl, httpsServer, makeCerts } from './tlsFixture.mjs'

const REPO = path.resolve(import.meta.dirname, '..', '..')
const MAC = path.join(REPO, 'deploy', 'mac')
const ASSET = 'activate-studio-macos-arm64.tar.gz'
const MANIFEST = 'activate-studio-macos-arm64.manifest.json'
const LABEL = 'com.parallelworks.activate-studio'
const tmp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'channels-')))
const APPS = path.join(tmp, 'Applications')
const APP = path.join(APPS, 'ACTIVATE Studio.app')
const PREVIOUS = path.join(APPS, '.ACTIVATE Studio.app.previous')
const DATA = path.join(tmp, 'data')
const UPD = path.join(DATA, 'updates')
const BIN = path.join(tmp, 'bin')
const CALLS = path.join(tmp, 'calls')

const write = (file, body, mode = 0o644) => {
  fs.mkdirSync(path.dirname(file), { recursive: true })
  fs.writeFileSync(file, body, { mode })
}
const sleep = ms => new Promise(r => setTimeout(r, ms))
const until = async (fn, ms = 15_000) => {
  const end = Date.now() + ms
  while (Date.now() < end) { if (await fn()) return true; await sleep(50) }
  return false
}
const sha = buf => crypto.createHash('sha256').update(buf).digest('hex')
const versionIn = app => JSON.parse(fs.readFileSync(path.join(app, 'Contents', 'Resources', 'app', 'build-info.json'), 'utf8')).version
const calls = () => { try { return fs.readFileSync(CALLS, 'utf8') } catch { return '' } }
const kickstart = `launchctl kickstart -k gui/${process.getuid()}/${LABEL}`

const { publicKey, privateKey } = crypto.generateKeyPairSync('ed25519')
const PUB = publicKey.export({ type: 'spki', format: 'pem' })
const PRIV = privateKey.export({ type: 'pkcs8', format: 'pem' })
const stranger = crypto.generateKeyPairSync('ed25519').privateKey

/** An app bundle: what the installer checks, and the real run loop. */
function appTree(dir, version, { key = PUB, id = LABEL, shell = '#!/bin/sh\n', node = '#!/bin/sh\n' } = {}) {
  write(path.join(dir, 'Contents', 'Info.plist'), `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0">
<dict>
\t<key>CFBundleExecutable</key>
\t<string>ACTIVATE Studio</string>
\t<key>CFBundleIdentifier</key>
\t<string>${id}</string>
\t<key>LSMinimumSystemVersion</key>
\t<string>14.0</string>
</dict>
</plist>
`)
  write(path.join(dir, 'Contents', 'MacOS', 'ACTIVATE Studio'), shell, 0o755)
  write(path.join(dir, 'Contents', 'Resources', 'studio-run.sh'), fs.readFileSync(path.join(MAC, 'studio-run.sh')), 0o755)
  write(path.join(dir, 'Contents', 'Resources', 'node', 'bin', 'node'), node, 0o755)
  write(path.join(dir, 'Contents', 'Resources', 'app', 'server', 'dist', 'main.js'), '// server\n')
  write(path.join(dir, 'Contents', 'Resources', 'app', 'build-info.json'), `{\n "version": "${version}",\n "builtAt": "2026-10-01T00:00:00Z"\n}\n`)
  if (key) write(path.join(dir, 'Contents', 'Resources', 'update-signing.pub'), key)
}

function archiveOf(version, opts) {
  const dir = fs.mkdtempSync(path.join(tmp, 'build-'))
  appTree(path.join(dir, 'ACTIVATE Studio.app'), version, opts)
  const file = path.join(dir, ASSET)
  const r = spawnSync('tar', ['-czf', file, '-C', dir, 'ACTIVATE Studio.app'])
  assert.equal(r.status, 0, String(r.stderr))
  return file
}

// ---- a stand-in for GitHub: the releases API and the asset downloads ----

const files = new Map()
const cut = new Set()
let releases = []
let etag = '"r0"'
let apiStatus = 200
const apiRequests = []
const server = http.createServer((req, res) => {
  if (req.url.startsWith('/repos/parallelworks/activate-studio/releases')) {
    apiRequests.push(req.headers)
    if (apiStatus !== 200) { res.writeHead(apiStatus); res.end('{}'); return }
    if (req.headers['if-none-match'] === etag) { res.writeHead(304); res.end(); return }
    res.writeHead(200, { 'content-type': 'application/json', etag })
    res.end(JSON.stringify(releases))
    return
  }
  const body = files.get(req.url)
  if (!body) { res.writeHead(404); res.end(); return }
  res.writeHead(200, { 'content-type': 'application/octet-stream', 'content-length': body.length })
  if (cut.has(req.url)) { res.write(body.subarray(0, Math.floor(body.length / 2))); setTimeout(() => res.destroy(), 20); return }
  res.end(body)
})
await new Promise(r => server.listen(0, '127.0.0.1', r))
const BASE = `http://127.0.0.1:${server.address().port}`
let seq = 0

/** A release as GitHub lists it, its assets served here: the archive, a
 *  manifest (fields overridable), and a signature by `signWith`. */
function release(tag, { manifest = {}, signWith = privateKey, archiveVersion = tag, prerelease = false, app = {}, files: given, cutArchive = false } = {}) {
  const id = ++seq
  let archive, man, sig
  if (given) {
    archive = fs.readFileSync(given.archive)
    man = fs.readFileSync(given.manifest)
    sig = fs.readFileSync(given.sig)
  } else {
    archive = fs.readFileSync(archiveOf(archiveVersion, app))
    man = Buffer.from(JSON.stringify({ version: tag, asset: ASSET, sha256: sha(archive), size: archive.length, minMacos: '14.0', builtAt: '2026-10-20T00:00:00Z', ...manifest }, null, 1) + '\n')
    sig = Buffer.from(crypto.sign(null, man, signWith).toString('base64') + '\n')
  }
  const assets = []
  for (const [name, body] of [[ASSET, archive], [MANIFEST, man], [`${MANIFEST}.sig`, sig]]) {
    files.set(`/dl/${id}/${name}`, body)
    assets.push({ name, size: body.length, browser_download_url: `${BASE}/dl/${id}/${name}` })
  }
  if (cutArchive) cut.add(`/dl/${id}/${ASSET}`)
  return { tag_name: tag, draft: false, prerelease, published_at: '2026-10-20T12:00:00Z', body: `Notes for ${tag}`, html_url: `${BASE}/releases/${tag}`, assets }
}
const setReleases = list => { releases = list; etag = `"r${++seq}"` }

write(path.join(BIN, 'launchctl'), `#!/bin/sh\necho "launchctl $*" >>"${CALLS}"\n`, 0o755)
write(path.join(BIN, 'lsregister'), `#!/bin/sh\necho "lsregister $*" >>"${CALLS}"\n`, 0o755)
process.env.STUDIO_LSREGISTER = path.join(BIN, 'lsregister')
process.env.KB_ROOT = fs.mkdtempSync(path.join(tmp, 'kb-'))
process.env.INDEX_BASE = path.join(DATA, 'index')
process.env.STUDIO_DATA_DIR = DATA
process.env.STUDIO_UPDATE_URL = BASE
process.env.PATH = `${BIN}:${process.env.PATH}`
for (const k of ['AUTH_JWKS_URL', 'PW_PLATFORM_HOST', 'STUDIO_CONFIG_FILE', 'STUDIO_RESTARTABLE', 'STUDIO_APP_BUNDLE']) delete process.env[k]

const { default: Fastify } = await import('fastify')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const { settingsRoutes } = await import('../dist/settings.js')
const U = await import('../dist/updates.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(settingsRoutes)
await app.register(U.updateRoutes)
await app.ready()
const j = async (method, url, body) => {
  const res = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: res.statusCode, body: res.json() }
}
const settled = async () => {
  let s
  assert.ok(await until(async () => { s = (await j('GET', '/api/updates')).body; return ['restarting', 'error', 'idle'].includes(s.state) }), 'the install settles')
  return s
}

/** The app at `version`, nothing beside it, no updates state. */
async function fresh(version = 'v1.139', opts = {}) {
  fs.rmSync(APPS, { recursive: true, force: true })
  fs.rmSync(UPD, { recursive: true, force: true })
  fs.rmSync(CALLS, { force: true })
  appTree(APP, version, opts.app)
  process.env.STUDIO_APP_BUNDLE = APP
  U.resetUpdatesForTests({ version, ...opts.timings })
  apiStatus = 200
  const r = await j('PUT', '/api/settings', { updates: { check: true, install: 'manual', channel: 'stable' } })
  assert.equal(r.status, 200)
}

const SF = await import('../dist/stateFormat.js')
const INDEX = process.env.INDEX_BASE
const SNAPS = path.join(UPD, 'state-snapshots')
const setChannel = channel => j('PUT', '/api/settings', { updates: { check: true, install: 'manual', channel } })
const readIndex = rel => fs.readFileSync(path.join(INDEX, rel), 'utf8')

/** Some of everything: state the snapshot keeps, and caches it leaves out. */
function seedState() {
  fs.rmSync(INDEX, { recursive: true, force: true })
  write(path.join(INDEX, 'conversations.json'), '{"conversations":["before"]}\n')
  write(path.join(INDEX, 'user-credentials.json'), '{"vault":"sealed"}\n', 0o600)
  write(path.join(INDEX, '.credentials-secret'), 'secret\n', 0o600)
  write(path.join(INDEX, 'libraries.json'), '[]\n')
  write(path.join(INDEX, 'users', 'abc', 'libraries.json'), '[]\n')
  write(path.join(INDEX, 'users', 'abc', 'maps', 'm1', 'map.bin'), 'cache\n')
  write(path.join(INDEX, 'libraries', 'docs', 'labels.json'), '{}\n')
  write(path.join(INDEX, 'libraries', 'docs', 'gufi', 'db.db'), 'index\n')
  write(path.join(INDEX, 'gufi', 'kb', 'db.db'), 'index\n')
  write(path.join(INDEX, 'extract', 'a.txt'), 'cache\n')
  write(path.join(INDEX, 'embed-cache.db'), 'cache\n')
  write(path.join(INDEX, '.studio-instance'), 'lock\n')
  write(path.join(DATA, 'studio.env'), "PORT='4180'\n")
}

test('channels: Stable and Preview, the older prerelease setting read as Preview, anything else refused', async () => {
  await fresh()
  setReleases([release('v1.140'), release('v1.141', { prerelease: true, manifest: { channel: 'preview' } })])
  let r = await j('POST', '/api/updates/check')
  assert.equal(r.body.policy.channel, 'stable')
  assert.equal(r.body.latest.version, 'v1.140', 'Stable does not see a Preview release')
  assert.deepEqual(r.body.versions.map(v => v.version), ['v1.140'])

  assert.equal((await setChannel('prerelease')).status, 200)
  r = await j('GET', '/api/updates')
  assert.equal(r.body.policy.channel, 'preview', 'the older setting is Preview')
  assert.equal(r.body.latest.version, 'v1.141')
  assert.deepEqual(r.body.versions.map(v => [v.version, v.channel, v.newer]), [['v1.141', 'preview', true], ['v1.140', 'stable', true]])
  // A settings file an older version wrote reads the same way.
  const settings = JSON.parse(readIndex('settings.json'))
  fs.writeFileSync(path.join(INDEX, 'settings.json'), JSON.stringify({ ...settings, updates: { check: true, install: 'manual', channel: 'prerelease' } }))
  assert.equal((await j('GET', '/api/updates')).body.policy.channel, 'preview')

  assert.equal((await setChannel('preview')).status, 200)
  assert.equal((await j('GET', '/api/updates')).body.policy.channel, 'preview')
  r = await setChannel('beta')
  assert.equal(r.status, 400)
  assert.match(r.body.error, /stable or preview/)
  assert.equal(U.releaseChannel({ prerelease: true }), 'preview')
  assert.equal(U.releaseChannel({ prerelease: false }), 'stable')
})

test('a release without the channel field is Stable, or Preview when it is a GitHub prerelease; both install', async () => {
  await fresh()
  setReleases([release('v1.140')])
  await j('POST', '/api/updates/install')
  let s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(versionIn(APP), 'v1.140')

  await fresh('v1.140')
  await setChannel('preview')
  setReleases([release('v1.141', { prerelease: true })])
  await j('POST', '/api/updates/install')
  s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(versionIn(APP), 'v1.141')
})

test('Preview is kept from Stable: not listed, not pickable, and a manifest signed as Preview on a stable release is refused', async () => {
  await fresh('v1.140')
  setReleases([release('v1.140'), release('v1.141', { prerelease: true, manifest: { channel: 'preview' } })])
  let r = await j('POST', '/api/updates/check')
  assert.equal(r.body.available, false)
  r = await j('POST', '/api/updates/install', { version: 'v1.141' })
  assert.equal(r.status, 409)
  assert.match(r.body.error, /not a release this Studio can install on the Stable channel/)
  assert.equal(versionIn(APP), 'v1.140')

  // Published without the prerelease flag, a Studio from before channels
  // would take it: the signed field catches the mistake here.
  setReleases([release('v1.142', { manifest: { channel: 'preview' } })])
  await j('POST', '/api/updates/check')
  await j('POST', '/api/updates/install')
  const s = await settled()
  assert.equal(s.state, 'error')
  assert.match(s.error, /Preview release, but it was published as a stable release/)
  assert.equal(versionIn(APP), 'v1.140')

  // A bad channel value is refused too.
  await fresh('v1.140')
  setReleases([release('v1.143', { manifest: { channel: 'nightly' } })])
  await j('POST', '/api/updates/install')
  assert.match((await settled()).error, /channel nightly is not stable or preview/)
})

test('Preview to Stable: Move to Stable is offered, the older Stable installs only when asked for by version, never on its own', async () => {
  await fresh('v1.150')
  setReleases([release('v1.145'), release('v1.150', { prerelease: true, manifest: { channel: 'preview' } })])
  await setChannel('preview')
  let r = await j('POST', '/api/updates/check')
  assert.equal(r.body.moveToStable, null, 'on Preview nothing to move to')
  r = await j('PUT', '/api/settings', { updates: { check: true, install: 'auto', channel: 'stable' } })
  r = await j('GET', '/api/updates')
  assert.deepEqual(r.body.moveToStable, { version: 'v1.145' })
  assert.equal(r.body.available, false)
  assert.equal(r.body.canInstall, false)

  // Update now does not go to an older version, and neither does the automatic install.
  r = await j('POST', '/api/updates/install')
  assert.equal(r.status, 409)
  assert.match(r.body.error, /no version newer than v1\.150/)
  await j('POST', '/api/updates/check')
  await sleep(300)
  assert.equal(versionIn(APP), 'v1.150')
  assert.ok(!fs.existsSync(path.join(APPS, '.ACTIVATE Studio.app.new')))

  // What it would do, from the signed manifest.
  r = await j('POST', '/api/updates/inspect', { version: 'v1.145' })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(r.body.older, true)
  assert.equal(r.body.readable, true)
  r = await j('POST', '/api/updates/install', { version: 'v1.145' })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  const s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(versionIn(APP), 'v1.145')
  assert.equal(versionIn(PREVIOUS), 'v1.150', 'Roll back returns to the Preview')
  assert.ok(await until(() => calls().includes(kickstart)), calls())

  // The running version is no install.
  await fresh('v1.145')
  setReleases([release('v1.145')])
  await j('POST', '/api/updates/check')
  r = await j('POST', '/api/updates/install', { version: 'v1.145' })
  assert.equal(r.status, 409)
  assert.match(r.body.error, /not a release|is the version running/)
})

test('an older version is installed only with a good signature, size and SHA-256', async () => {
  for (const [opts, why] of [
    [{ signWith: stranger }, /signature does not verify/],
    [{ manifest: { sha256: '0'.repeat(64) } }, /SHA-256 does not match/],
    [{ manifest: { size: 1 } }, /larger than the 1 bytes/],
    [{ manifest: { version: 'v1.144' } }, /manifest is for v1\.144, but the release is v1\.145/],
  ]) {
    await fresh('v1.150')
    setReleases([release('v1.145', opts)])
    await j('POST', '/api/updates/check')
    const r = await j('POST', '/api/updates/install', { version: 'v1.145' })
    assert.equal(r.status, 200)
    const s = await settled()
    assert.equal(s.state, 'error')
    assert.match(s.error, why)
    assert.equal(versionIn(APP), 'v1.150', 'the app is left alone')
    assert.ok(!fs.existsSync(PREVIOUS))
  }
})

test('the state is saved before a change of version: settings, conversations, the vault and per-user data, not indexes or caches', async () => {
  await fresh()
  seedState()
  await setChannel('stable')
  setReleases([release('v1.140')])
  await j('POST', '/api/updates/install')
  assert.equal((await settled()).state, 'restarting')
  const snaps = SF.listSnapshots()
  assert.equal(snaps.length, 1)
  const snap = snaps[0]
  assert.equal(snap.from, 'v1.139')
  assert.equal(snap.to, 'v1.140')
  assert.equal(snap.format, 1)
  assert.deepEqual(snap.files, [
    '.credentials-secret', '@config/studio.env', 'conversations.json', 'libraries.json', 'libraries/docs/labels.json',
    'settings.json', 'user-credentials.json', 'users/abc/libraries.json',
  ])
  const files = path.join(SNAPS, snap.id, 'files')
  assert.equal(fs.readFileSync(path.join(files, 'conversations.json'), 'utf8'), '{"conversations":["before"]}\n')
  assert.equal(fs.statSync(path.join(files, 'user-credentials.json')).mode & 0o777, 0o600, 'the vault keeps its mode')
  assert.equal(fs.statSync(SNAPS).mode & 0o777, 0o700)
  const r = await j('GET', '/api/updates')
  assert.equal(r.body.stateFormat, 1)
  assert.deepEqual(r.body.snapshots.map(s => s.id), [snap.id])
})

test('an older version that cannot read the state: refused without a snapshot it reads, and given that snapshot when asked', async () => {
  await fresh('v1.150')
  seedState()
  await setChannel('stable')
  // The state as the Preview found it, saved before it migrated to format 2.
  const before = SF.takeSnapshot({ from: 'v1.145', to: 'v1.150', reason: 'before an update' })
  fs.writeFileSync(path.join(INDEX, 'conversations.json'), '{"conversations":["migrated"]}\n')
  fs.writeFileSync(path.join(INDEX, 'tags-overlay.json'), '{}\n')
  SF.writeMarker(2, 'v1.150')
  setReleases([release('v1.145', { manifest: { stateFormat: 1 } })])
  await j('POST', '/api/updates/check')

  let r = await j('POST', '/api/updates/inspect', { version: 'v1.145' })
  assert.equal(r.body.readable, false)
  assert.equal(r.body.currentFormat, 2)
  assert.equal(r.body.snapshot.id, before.id)

  // Not asked for: refused, nothing changed.
  await j('POST', '/api/updates/install', { version: 'v1.145' })
  let s = await settled()
  assert.equal(s.state, 'error')
  assert.match(s.error, /cannot read the state as it is now/)
  assert.equal(versionIn(APP), 'v1.150')

  // Asked for: installed, the snapshot put back, the update policy kept.
  U.resetUpdatesForTests({ version: 'v1.150' })
  await j('PUT', '/api/settings', { updates: { check: false, install: 'manual', channel: 'stable' } })
  await j('POST', '/api/updates/check')
  await j('POST', '/api/updates/install', { version: 'v1.145', restoreState: true })
  s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(versionIn(APP), 'v1.145')
  assert.equal(readIndex('conversations.json'), '{"conversations":["before"]}\n')
  assert.ok(!fs.existsSync(path.join(INDEX, 'tags-overlay.json')), 'a file the snapshot did not hold is gone')
  assert.equal(SF.stateFormat(), 1)
  assert.deepEqual(JSON.parse(readIndex('settings.json')).updates, { check: false, install: 'manual', channel: 'stable' })
  assert.equal(readIndex('gufi/kb/db.db'), 'index\n', 'indexes are not touched')
  // What was there is kept: the snapshot of format 2 before the change.
  const displaced = SF.listSnapshots().find(x => x.format === 2)
  assert.ok(displaced)
  assert.equal(fs.readFileSync(path.join(SNAPS, displaced.id, 'files', 'conversations.json'), 'utf8'), '{"conversations":["migrated"]}\n')

  // With no snapshot it reads, it cannot be installed at all.
  await fresh('v1.150')
  seedState()
  SF.writeMarker(2, 'v1.150')
  setReleases([release('v1.145', { manifest: { stateFormat: 1 } })])
  await j('POST', '/api/updates/check')
  r = await j('POST', '/api/updates/inspect', { version: 'v1.145' })
  assert.equal(r.body.readable, false)
  assert.equal(r.body.snapshot, null)
  await j('POST', '/api/updates/install', { version: 'v1.145', restoreState: true })
  s = await settled()
  assert.match(s.error, /no snapshot in a format it reads/)
  assert.equal(versionIn(APP), 'v1.150')
})

test('the state format marker: written when missing; a newer format found at start puts back the newest snapshot this version reads', () => {
  seedState()
  fs.rmSync(UPD, { recursive: true, force: true })
  assert.equal(SF.reconcileState('v1.145'), 'marked')
  assert.equal(JSON.parse(readIndex(SF.MARKER)).format, SF.STATE_FORMAT)
  assert.equal(SF.reconcileState('v1.145'), 'ok')

  const old = SF.takeSnapshot({ from: 'v1.145', to: 'v1.150', reason: 'before an update' })
  fs.writeFileSync(path.join(INDEX, 'conversations.json'), '{"conversations":["migrated"]}\n')
  SF.writeMarker(2, 'v1.150')
  SF.takeSnapshot({ from: 'v1.150', to: 'v1.151', reason: 'before an update' })
  const logs = []
  assert.equal(SF.reconcileState('v1.145', m => logs.push(m)), 'restored')
  assert.equal(readIndex('conversations.json'), '{"conversations":["before"]}\n')
  assert.equal(SF.stateFormat(), 1)
  assert.match(logs.join('\n'), new RegExp(`snapshot taken ${old.at}`))

  // Without one, it says so and leaves the state alone.
  fs.rmSync(UPD, { recursive: true, force: true })
  SF.writeMarker(3, 'v2.0')
  assert.equal(SF.reconcileState('v1.145'), 'unreadable')
  assert.equal(SF.stateFormat(), 3)

  // Pruning keeps the newest of each format, so the one before a move to a
  // newer format survives later updates.
  fs.rmSync(UPD, { recursive: true, force: true })
  SF.writeMarker(1, 'v1.145')
  const first = SF.takeSnapshot({ from: 'v1.145', to: 'v2.0', reason: 'before an update' })
  SF.writeMarker(2, 'v2.0')
  for (let i = 0; i < 5; i++) SF.takeSnapshot({ from: `v2.${i}`, to: `v2.${i + 1}`, reason: 'before an update' })
  const kept = SF.listSnapshots()
  assert.equal(kept.length, 4)
  assert.ok(kept.some(x => x.id === first.id))
})

test('a rollback saves the state first', async () => {
  await fresh('v1.140')
  seedState()
  appTree(PREVIOUS, 'v1.139')
  const r = await j('POST', '/api/updates/rollback')
  assert.equal(r.status, 200, JSON.stringify(r.body))
  const snaps = SF.listSnapshots()
  assert.equal(snaps.length, 1)
  assert.equal(snaps[0].reason, 'before a rollback')
  assert.equal(versionIn(APP), 'v1.139')
  assert.ok(await until(() => calls().includes(kickstart)), calls())
})

test('a release that asks for a relaunch is swapped in, never restarted into, waits for the app to reopen, and is never installed on its own', async () => {
  await fresh('v1.150')
  setReleases([release('v2.0', { manifest: { requiresRelaunch: true } })])
  await j('POST', '/api/updates/check')
  await j('POST', '/api/updates/install')
  let s
  assert.ok(await until(async () => { s = (await j('GET', '/api/updates')).body; return ['relaunch', 'error', 'restarting'].includes(s.state) }))
  assert.equal(s.state, 'relaunch', s.error)
  assert.equal(s.reopen, true)
  assert.equal(versionIn(APP), 'v2.0')
  assert.equal(versionIn(PREVIOUS), 'v1.150')
  assert.equal(JSON.parse(fs.readFileSync(path.join(UPD, 'relaunch.json'), 'utf8')).to, 'v2.0')
  await sleep(300)
  assert.ok(!calls().includes('kickstart'), 'the server is not restarted into the new copy')
  // Nothing more installs until the app reopens.
  assert.equal((await j('POST', '/api/updates/install')).body.state, 'relaunch')

  // The automatic install verifies it, then waits for a person.
  await fresh('v1.150', { timings: { idleMs: 0, idlePollMs: 10 } })
  await j('PUT', '/api/settings', { updates: { check: true, install: 'auto', channel: 'stable' } })
  setReleases([release('v2.0', { manifest: { requiresRelaunch: true } })])
  await j('POST', '/api/updates/check')
  assert.ok(await until(() => fs.existsSync(path.join(APPS, '.ACTIVATE Studio.app.new'))), 'verified and unpacked')
  await sleep(300)
  assert.equal(versionIn(APP), 'v1.150')
  assert.equal((await j('GET', '/api/updates')).body.state, 'idle')

  // A bad value is refused.
  await fresh('v1.150')
  setReleases([release('v2.0', { manifest: { requiresRelaunch: 'yes' } })])
  await j('POST', '/api/updates/install')
  assert.match((await settled()).error, /requiresRelaunch is not true or false/)
})

/** A release of an app with no Node and no run loop (the shape of the Go
 *  version), signed, with the manifest fields given. */
function nodelessRelease(tag, manifest) {
  const dir = fs.mkdtempSync(path.join(tmp, 'go-'))
  const app = path.join(dir, 'ACTIVATE Studio.app')
  appTree(app, tag)
  for (const f of ['node', 'studio-run.sh', 'app/server']) fs.rmSync(path.join(app, 'Contents', 'Resources', f), { recursive: true })
  const archive = path.join(dir, ASSET)
  assert.equal(spawnSync('tar', ['-czf', archive, '-C', dir, 'ACTIVATE Studio.app']).status, 0)
  const bytes = fs.readFileSync(archive)
  const man = path.join(dir, MANIFEST)
  fs.writeFileSync(man, JSON.stringify({ version: tag, asset: ASSET, sha256: sha(bytes), size: bytes.length, minMacos: '14.0', ...manifest }))
  fs.writeFileSync(`${man}.sig`, crypto.sign(null, fs.readFileSync(man), privateKey).toString('base64'))
  return release(tag, { prerelease: manifest.channel === 'preview', files: { archive, manifest: man, sig: `${man}.sig` } })
}

test('an app with no Node installs only as a relaunch release', async () => {
  await fresh('v1.150')
  setReleases([nodelessRelease('v2.0', {})])
  await j('POST', '/api/updates/install')
  let s = await settled()
  assert.equal(s.state, 'error')
  assert.match(s.error, /no Resources\/studio-run\.sh/)
  assert.equal(versionIn(APP), 'v1.150')

  await fresh('v1.150')
  await setChannel('preview')
  setReleases([nodelessRelease('v2.0', { channel: 'preview', requiresRelaunch: true, stateFormat: 2 })])
  await j('POST', '/api/updates/install')
  assert.ok(await until(async () => { s = (await j('GET', '/api/updates')).body; return ['relaunch', 'error'].includes(s.state) }))
  assert.equal(s.state, 'relaunch', s.error)
  assert.equal(versionIn(APP), 'v2.0')
})

test('the run loop of the old copy stops when the app was replaced by one without Node that asks for a relaunch', async () => {
  const root = path.join(tmp, 'relaunch-loop')
  const app = path.join(root, 'Applications', 'ACTIVATE Studio.app')
  const data = path.join(root, 'data')
  appTree(app, 'v2.0')
  fs.rmSync(path.join(app, 'Contents', 'Resources', 'node'), { recursive: true })
  write(path.join(data, 'updates', 'relaunch.json'), '{"from":"v1.150","to":"v2.0"}\n')
  const r = spawnSync('bash', [path.join(app, 'Contents', 'Resources', 'studio-run.sh'), data], {
    env: { HOME: root, PATH: '/usr/bin:/bin' }, encoding: 'utf8', timeout: 30_000,
  })
  assert.equal(r.status, 0, r.stderr)
  assert.match(fs.readFileSync(path.join(data, 'logs', 'studio.log'), 'utf8'), /runs once the app is reopened; this run loop stops/)
})

test('the launcher, opened after a relaunch update, starts the server again from this copy and clears the request', () => {
  const root = path.join(tmp, 'relaunch-launcher')
  const app = path.join(root, 'Applications', 'ACTIVATE Studio.app')
  appTree(app, 'v1.150')
  write(path.join(app, 'Contents', 'MacOS', 'ACTIVATE Studio'), fs.readFileSync(path.join(MAC, 'launcher.sh')), 0o755)
  const home = path.join(root, 'home')
  const data = path.join(home, 'Library', 'Application Support', 'ACTIVATE Studio')
  const bin = path.join(root, 'bin')
  const state = path.join(root, 'state')
  fs.mkdirSync(state, { recursive: true })
  write(path.join(bin, 'launchctl'), `#!/bin/bash\necho "$*" >>"${state}/calls"\ncase "$1" in print) [ -f "${state}/loaded" ] ;; bootstrap) touch "${state}/loaded" ;; bootout) rm -f "${state}/loaded" ;; esac\n`, 0o755)
  write(path.join(bin, 'curl'), `#!/bin/bash\necho '{"version":"v1.150","builtAt":"2026-10-01T00:00:00Z"}'\n`, 0o755)
  for (const b of ['open', 'osascript']) write(path.join(bin, b), '#!/bin/sh\n', 0o755)
  const open = () => spawnSync(path.join(app, 'Contents', 'MacOS', 'ACTIVATE Studio'), [], {
    env: { HOME: home, PATH: `${bin}:/usr/bin:/bin`, STUDIO_NO_BROWSER: '1' }, encoding: 'utf8',
  })
  // The first opening installs and loads the agent; the server then runs
  // from the old copy when the update asks for a relaunch.
  let r = open()
  assert.equal(r.status, 0, r.stderr)
  fs.rmSync(path.join(state, 'calls'), { force: true })
  write(path.join(data, 'updates', 'relaunch.json'), '{"from":"v1.145","to":"v1.150"}\n')
  r = open()
  assert.equal(r.status, 0, r.stderr)
  assert.ok(!fs.existsSync(path.join(data, 'updates', 'relaunch.json')))
  const kicks = fs.readFileSync(path.join(state, 'calls'), 'utf8').split('\n').filter(l => l.startsWith('kickstart'))
  assert.equal(kicks.length, 1, kicks.join('\n'))
  // Without the request, opening the app leaves the server running.
  fs.rmSync(path.join(state, 'calls'), { force: true })
  r = open()
  assert.equal(r.status, 0, r.stderr)
  assert.ok(!(fs.existsSync(path.join(state, 'calls')) && fs.readFileSync(path.join(state, 'calls'), 'utf8').includes('kickstart')))
})

const opensslVersion = spawnSync('openssl', ['version'], { encoding: 'utf8' }).stdout ?? ''
test('sign_release.sh writes the channel, state format and relaunch fields, and the server reads them', { skip: /^OpenSSL [3-9]/.test(opensslVersion) ? false : 'needs OpenSSL 3' }, async () => {
  const dir = fs.mkdtempSync(path.join(tmp, 'sign-'))
  const pub = path.join(dir, 'pub.pem')
  fs.writeFileSync(pub, PUB)
  const archive = archiveOf('v1.160')
  const sign = env => spawnSync('bash', [path.join(MAC, 'sign_release.sh'), archive], {
    env: { PATH: process.env.PATH, HOME: tmp, STUDIO_UPDATE_SIGNING_KEY: PRIV, STUDIO_UPDATE_PUBLIC_KEY: pub, ...env }, encoding: 'utf8',
  })
  assert.notEqual(sign({ STUDIO_RELEASE_CHANNEL: 'beta' }).status, 0)
  assert.notEqual(sign({ STUDIO_STATE_FORMAT: 'two' }).status, 0)
  let r = sign({ STUDIO_RELEASE_CHANNEL: 'preview', STUDIO_STATE_FORMAT: '2', STUDIO_REQUIRES_RELAUNCH: '1' })
  assert.equal(r.status, 0, r.stderr)
  const manifestFile = archive.replace(/\.tar\.gz$/, '.manifest.json')
  const man = JSON.parse(fs.readFileSync(manifestFile, 'utf8'))
  assert.equal(man.channel, 'preview')
  assert.equal(man.stateFormat, 2)
  assert.equal(man.requiresRelaunch, true)
  r = sign({})
  assert.equal(r.status, 0, r.stderr)
  assert.deepEqual(Object.keys(JSON.parse(fs.readFileSync(manifestFile, 'utf8'))), ['version', 'asset', 'sha256', 'size', 'minMacos', 'builtAt'], 'without them, the manifest is as before')

  r = sign({ STUDIO_RELEASE_CHANNEL: 'preview' })
  await fresh('v1.150')
  await setChannel('preview')
  setReleases([release('v1.160', { prerelease: true, files: { archive, manifest: manifestFile, sig: `${manifestFile}.sig` } })])
  await j('POST', '/api/updates/install')
  const s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(versionIn(APP), 'v1.160')
})

test.after(() => {
  server.close()
  fs.rmSync(tmp, { recursive: true, force: true })
})
