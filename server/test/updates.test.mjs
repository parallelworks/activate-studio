// Updates: the check against a stand-in for GitHub's releases API, the
// signed manifest and the checks on every download, the swap of the macOS
// app with the previous copy kept, the rollback (by hand, and by the run
// loop when a new version keeps stopping), the automatic install's wait
// for an idle Studio, and sign_release.sh agreeing with the server.
import test from 'node:test'
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import fs from 'node:fs'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'
import { spawn, spawnSync } from 'node:child_process'

const REPO = path.resolve(import.meta.dirname, '..', '..')
const MAC = path.join(REPO, 'deploy', 'mac')
const ASSET = 'activate-studio-macos-arm64.tar.gz'
const MANIFEST = 'activate-studio-macos-arm64.manifest.json'
const LABEL = 'com.parallelworks.activate-studio'
const tmp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'updates-')))
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

test('versions: v1.140 > v1.139 > v1.99, and a git describe suffix is ignored', () => {
  assert.ok(U.isNewer('v1.140', 'v1.139'))
  assert.ok(U.isNewer('v1.139', 'v1.99'))
  assert.ok(!U.isNewer('v1.99', 'v1.139'))
  assert.ok(!U.isNewer('v1.139', 'v1.139'))
  assert.ok(U.isNewer('v1.139.1', 'v1.139'))
  assert.ok(!U.isNewer('v1.137', 'v1.137-6-g3140181'), 'v1.137-6-g3140181 counts as v1.137')
  assert.ok(U.isNewer('v1.138', 'v1.137-6-g3140181'))
  assert.ok(U.isNewer('v1.1', 'dev'), 'a build of no known version is older than any release')
  assert.ok(!U.isNewer('nightly', 'v1.1'))
  assert.deepEqual(U.parseVersion('v1.137-6-g3140181'), [1, 137, 0])
})

test('a clone reports the newest release and how to update, and refuses to install; a hosted Studio refuses everything', async () => {
  await fresh()
  delete process.env.STUDIO_APP_BUNDLE
  setReleases([release('v1.140'), release('v1.141', { prerelease: true })])
  let r = await j('GET', '/api/updates')
  assert.equal(r.status, 200)
  assert.equal(r.body.mode, 'clone')
  assert.equal(r.body.latest, null)
  assert.equal(r.body.canInstall, false)
  assert.match(r.body.reason, /git pull && pnpm install && pnpm build/)
  assert.deepEqual(r.body.policy, { check: true, install: 'manual', channel: 'stable' })

  r = await j('POST', '/api/updates/check')
  assert.equal(r.body.latest.version, 'v1.140', 'a prerelease only on the prerelease channel')
  assert.equal(r.body.latest.notes, 'Notes for v1.140')
  assert.equal(r.body.available, true)
  assert.equal(r.body.canInstall, false)
  assert.equal(r.body.state, 'idle')
  assert.ok(r.body.checkedAt)
  assert.equal(r.body.previous, null)
  assert.match(apiRequests.at(-1)['user-agent'], /^activate-studio\/v1\.139/)
  assert.equal(apiRequests.at(-1)['if-none-match'], undefined)

  // The stored ETag goes with the next check, and a 304 keeps the list.
  r = await j('POST', '/api/updates/check')
  assert.equal(apiRequests.at(-1)['if-none-match'], etag)
  assert.equal(r.body.latest.version, 'v1.140')
  assert.equal((await j('PUT', '/api/settings', { updates: { check: true, install: 'manual', channel: 'prerelease' } })).status, 200)
  assert.equal((await j('GET', '/api/updates')).body.latest.version, 'v1.141')

  // A failed check says so and keeps the last result.
  apiStatus = 500
  r = await j('POST', '/api/updates/check')
  assert.equal(r.body.state, 'error')
  assert.match(r.body.error, /HTTP 500/)
  assert.equal(r.body.latest.version, 'v1.141')
  apiStatus = 200

  r = await j('POST', '/api/updates/install')
  assert.equal(r.status, 409)
  assert.match(r.body.error, /clone/)
  assert.equal((await j('POST', '/api/updates/rollback')).status, 409)

  // An app in a folder this user cannot write to cannot replace itself.
  if (process.getuid() !== 0) {
    const locked = path.join(tmp, 'locked')
    appTree(path.join(locked, 'ACTIVATE Studio.app'), 'v1.139')
    fs.chmodSync(locked, 0o555)
    process.env.STUDIO_APP_BUNDLE = path.join(locked, 'ACTIVATE Studio.app')
    r = await j('GET', '/api/updates')
    assert.equal(r.body.mode, 'clone')
    assert.match(r.body.reason, /not writable/)
    fs.chmodSync(locked, 0o755)
    delete process.env.STUDIO_APP_BUNDLE
  }

  process.env.PW_PLATFORM_HOST = 'platform.test'
  for (const [method, url] of [['GET', '/api/updates'], ['POST', '/api/updates/check'], ['POST', '/api/updates/install'], ['POST', '/api/updates/rollback']]) {
    assert.equal((await j(method, url)).status, 403, `${method} ${url}`)
  }
  delete process.env.PW_PLATFORM_HOST
})

test('a newer signed release installs: swapped in, the previous copy kept, the mark written, the agent restarted', async () => {
  await fresh()
  setReleases([release('v1.138'), release('v1.140', { app: { shell: '#!/bin/sh\n# a new window\n' } }), release('v1.141', { prerelease: true })])
  let r = await j('POST', '/api/updates/check')
  assert.equal(r.body.mode, 'app')
  assert.equal(r.body.latest.version, 'v1.140')
  assert.equal(r.body.canInstall, true)
  assert.equal(r.body.reason, null)

  r = await j('POST', '/api/updates/install')
  assert.equal(r.status, 200)
  assert.ok(['downloading', 'verifying', 'installing', 'restarting'].includes(r.body.state), r.body.state)
  const s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(s.error, null)
  assert.equal(versionIn(APP), 'v1.140')
  assert.equal(versionIn(PREVIOUS), 'v1.139')
  assert.deepEqual(s.previous, { version: 'v1.139' })
  assert.equal(s.reopen, true, 'the window changed, so it has to be reopened')
  const pending = JSON.parse(fs.readFileSync(path.join(UPD, 'pending.json'), 'utf8'))
  assert.equal(pending.from, 'v1.139')
  assert.equal(pending.to, 'v1.140')
  assert.ok(await until(() => calls().includes(kickstart)), calls())
  assert.ok(await until(() => calls().includes(`lsregister -f ${APP}`)), 'LaunchServices looks at the new copy, for its icon')
  assert.deepEqual(fs.readdirSync(APPS).sort(), ['.ACTIVATE Studio.app.previous', 'ACTIVATE Studio.app'])
  assert.deepEqual(fs.readdirSync(UPD).sort(), ['pending.json', 'reopen', 'state.json'])
  // A second request while restarting changes nothing.
  assert.equal((await j('POST', '/api/updates/install')).body.state, 'restarting')

  // The same window: nothing to reopen. An older .previous is replaced.
  await fresh('v1.140')
  appTree(PREVIOUS, 'v1.120')
  setReleases([release('v1.141')])
  await j('POST', '/api/updates/install')
  const t = await settled()
  assert.equal(t.state, 'restarting', t.error)
  assert.equal(t.reopen, false)
  assert.equal(versionIn(APP), 'v1.141')
  assert.equal(versionIn(PREVIOUS), 'v1.140')
})

test('a release that fails any check is refused, cleaned up, and the app left alone', async () => {
  const cases = [
    ['a bad signature', { signWith: stranger }, /signature does not verify/],
    ['a wrong SHA-256', { manifest: { sha256: '0'.repeat(64) } }, /SHA-256 does not match/],
    ['a shorter download than the manifest says', { manifest: { size: 10 ** 7 } }, /the manifest says 10000000/],
    ['a longer download than the manifest says', { manifest: { size: 100 } }, /larger than the 100 bytes/],
    ['a download cut off', { cutArchive: true }, /The update failed/],
    ['another asset', { manifest: { asset: 'activate-studio-macos-x64.tar.gz' } }, /names the asset activate-studio-macos-x64/],
    ['an older manifest under a newer release', { manifest: { version: 'v1.138' } }, /v1\.138 is not newer than the running v1\.139/],
    ['an equal manifest under a newer release', { manifest: { version: 'v1.139' } }, /v1\.139 is not newer/],
    ['a manifest for another release', { manifest: { version: 'v1.141' } }, /for v1\.141, but the release is v1\.140/],
    ['an archive of another version', { archiveVersion: 'v1.142' }, /The new app is v1\.142, not v1\.140/],
    ['another app', { app: { id: 'com.example.other' } }, /bundle identifier is com\.example\.other/],
  ]
  for (const [what, opts, why] of cases) {
    await fresh()
    setReleases([release('v1.140', opts)])
    const r = await j('POST', '/api/updates/install')
    assert.equal(r.status, 200, `${what}: ${JSON.stringify(r.body)}`)
    const s = await settled()
    assert.equal(s.state, 'error', what)
    assert.match(s.error, why, what)
    assert.equal(s.progress, null)
    assert.equal(versionIn(APP), 'v1.139', what)
    assert.deepEqual(fs.readdirSync(APPS), ['ACTIVATE Studio.app'], `${what}: nothing left beside the app`)
    assert.deepEqual(fs.readdirSync(UPD), ['state.json'], `${what}: no download left`)
    assert.equal(calls(), '', `${what}: no restart`)
  }

  // Nothing newer: refused before anything is fetched.
  for (const tag of ['v1.139', 'v1.138']) {
    await fresh()
    setReleases([release(tag)])
    const r = await j('POST', '/api/updates/install')
    assert.equal(r.status, 409, tag)
    assert.match(r.body.error, /no version newer than v1\.139/)
  }

  // No key in the app: check and tell, never install.
  await fresh('v1.139', { app: { key: null } })
  setReleases([release('v1.140')])
  const s = (await j('POST', '/api/updates/check')).body
  assert.equal(s.available, true)
  assert.equal(s.canInstall, false)
  assert.match(s.reason, /no update signing key/)
  assert.equal((await j('POST', '/api/updates/install')).status, 409)
})

test('a rollback puts the previous copy back and restarts; automatic install then skips that version', async () => {
  await fresh('v1.140')
  appTree(PREVIOUS, 'v1.139')
  write(path.join(UPD, 'pending.json'), '{"from": "v1.139", "to": "v1.140"}\n')
  let r = await j('GET', '/api/updates')
  assert.deepEqual(r.body.previous, { version: 'v1.139' })
  r = await j('POST', '/api/updates/rollback')
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(r.body.state, 'restarting')
  assert.equal(versionIn(APP), 'v1.139')
  assert.ok(!fs.existsSync(PREVIOUS))
  assert.equal(r.body.previous, null)
  assert.ok(!fs.existsSync(path.join(UPD, 'pending.json')))
  const back = JSON.parse(fs.readFileSync(path.join(UPD, 'rolled-back.json'), 'utf8'))
  assert.equal(back.from, 'v1.140')
  assert.equal(back.to, 'v1.139')
  assert.ok(await until(() => calls().includes(kickstart)))
  assert.equal((await j('POST', '/api/updates/rollback')).status, 409, 'nothing more to go back to')

  // Started again as v1.139, with install set to auto: v1.140 stays put.
  U.resetUpdatesForTests({ version: 'v1.139', idleMs: 50, idlePollMs: 20 })
  fs.rmSync(CALLS, { force: true })
  await j('PUT', '/api/settings', { updates: { check: true, install: 'auto', channel: 'stable' } })
  setReleases([release('v1.140')])
  r = await j('POST', '/api/updates/check')
  assert.equal(r.body.available, true)
  await sleep(400)
  assert.equal(versionIn(APP), 'v1.139')
  assert.ok(!fs.existsSync(path.join(APPS, '.ACTIVATE Studio.app.new')))
  assert.equal(calls(), '')
})

test('automatic install verifies first, then waits until the Studio has been idle', async () => {
  await fresh('v1.139', { timings: { idleMs: 400, idlePollMs: 25 } })
  let r = await j('PUT', '/api/settings', { updates: { install: 'sometimes' } })
  assert.equal(r.status, 400)
  r = await j('PUT', '/api/settings', { updates: { check: true, install: 'auto', channel: 'stable' } })
  assert.deepEqual(r.body.effective.updates, { check: true, install: 'auto', channel: 'stable' })
  let busy = true
  U.setUpdateBusyCheck(() => busy)
  setReleases([release('v1.140')])
  await j('POST', '/api/updates/check')
  const NEW = path.join(APPS, '.ACTIVATE Studio.app.new')
  assert.ok(await until(async () => fs.existsSync(NEW) && (await j('GET', '/api/updates')).body.state === 'idle'), 'downloaded and verified')
  await sleep(800)
  assert.equal(versionIn(APP), 'v1.139', 'not while busy')
  assert.equal(calls(), '')
  const quiet = Date.now()
  busy = false
  assert.ok(await until(() => calls().includes(kickstart)), 'installed once idle')
  // Idle counts from the server's last poll that saw it busy, which can be
  // up to one poll (25 ms) before quiet.
  assert.ok(Date.now() - quiet >= 400 - 25, 'after the idle period')
  assert.equal(versionIn(APP), 'v1.140')
  assert.equal(versionIn(PREVIOUS), 'v1.139')
  assert.ok(!fs.existsSync(NEW))
})

test('after an update the server clears the mark once up, and reports a rollback the run loop made', async () => {
  await fresh('v1.140', { timings: { healthyMs: 100, firstCheckMs: 3_600_000 } })
  write(path.join(UPD, 'pending.json'), '{"from": "v1.139", "to": "v1.140"}\n')
  write(path.join(UPD, 'rolled-back.json'), '{"from": "v1.141", "to": "v1.140", "reason": "it stopped 3 times within two minutes of starting"}\n')
  const log = []
  U.startUpdates(m => log.push(m))
  assert.ok(await until(() => !fs.existsSync(path.join(UPD, 'pending.json'))), 'the mark is cleared')
  const s = (await j('GET', '/api/updates')).body
  assert.match(s.error, /v1\.141 did not start properly \(it stopped 3 times/)
  assert.equal(JSON.parse(fs.readFileSync(path.join(UPD, 'rolled-back.json'), 'utf8')).noted, true, 'reported once')
  U.resetUpdatesForTests()
})

test('the app takes the newest release that carries the signed app, and checks again soon while a newer one waits for it', async () => {
  await fresh('v1.139', { timings: { firstCheckMs: 20, soonMs: 150, intervalMs: 3_600_000, pollMs: 20 } })
  // v1.141 was just published; its macOS build has not attached the app yet.
  setReleases([{ ...release('v1.141'), assets: [], published_at: new Date().toISOString() }, release('v1.140')])
  const before = apiRequests.length
  U.startUpdates(() => {})
  assert.ok(await until(() => apiRequests.length > before), 'the first check')
  const s = (await j('GET', '/api/updates')).body
  assert.equal(s.latest.version, 'v1.140')
  assert.equal(s.available, true)
  assert.equal(s.canInstall, true, s.reason)
  // The build attaches it, and the sooner check finds it.
  setReleases([release('v1.141'), release('v1.140')])
  assert.ok(await until(async () => (await j('GET', '/api/updates')).body.latest?.version === 'v1.141', 3000), 'checked again within the shorter wait')
  U.resetUpdatesForTests()
})

test('a release the macOS build was not asked for, published hours ago, does not bring the next check forward', async () => {
  await fresh('v1.139', { timings: { firstCheckMs: 20, soonMs: 50, intervalMs: 3_600_000, pollMs: 20 } })
  setReleases([{ ...release('v1.141'), assets: [], published_at: new Date(Date.now() - 3 * 3600_000).toISOString() }, release('v1.140')])
  const before = apiRequests.length
  U.startUpdates(() => {})
  assert.ok(await until(() => apiRequests.length > before), 'the first check')
  assert.equal((await j('GET', '/api/updates')).body.latest.version, 'v1.140')
  await sleep(300)
  assert.equal(apiRequests.length, before + 1, 'no sooner check')
  U.resetUpdatesForTests()
})

test('a check is due by the clock: time the Mac slept counts, though its timers stopped', async () => {
  await fresh('v1.139', { timings: { firstCheckMs: 20, intervalMs: 3_600_000, pollMs: 20 } })
  setReleases([release('v1.140')])
  const before = apiRequests.length
  U.startUpdates(() => {})
  assert.ok(await until(() => apiRequests.length > before), 'the first check')
  await sleep(150)
  assert.equal(apiRequests.length, before + 1, 'not due again within the hour')
  // Two hours pass on the clock while no timer runs, as across a sleep.
  const realNow = Date.now
  Date.now = () => realNow() + 2 * 3_600_000
  try {
    assert.ok(await until(() => apiRequests.length > before + 1), 'the next poll finds the check due')
  } finally {
    Date.now = realNow
    U.resetUpdatesForTests()
  }
})

test('the window asking for the status starts a check that is due, once', async () => {
  await fresh('v1.139', { timings: { firstCheckMs: 3_600_000, pollMs: 3_600_000 } })
  setReleases([release('v1.140')])
  const before = apiRequests.length
  U.startUpdates(() => {})
  const s = (await j('GET', '/api/updates')).body
  assert.equal(s.state, 'checking', 'never checked, so due as the window opens')
  assert.ok(await until(async () => (await j('GET', '/api/updates')).body.latest?.version === 'v1.140'), 'the check finds the release')
  await j('GET', '/api/updates')
  assert.equal(apiRequests.length, before + 1, 'not checked again while it is not due')
  U.resetUpdatesForTests()
})

test('a failed check waits out the interval instead of retrying at every poll', async () => {
  await fresh('v1.139', { timings: { firstCheckMs: 20, intervalMs: 3_600_000, pollMs: 20 } })
  apiStatus = 500
  const before = apiRequests.length
  U.startUpdates(() => {})
  assert.ok(await until(() => apiRequests.length > before), 'the first check')
  await sleep(200)
  assert.equal(apiRequests.length, before + 1, 'one attempt')
  assert.equal((await j('GET', '/api/updates')).body.state, 'error')
  apiStatus = 200
  U.resetUpdatesForTests()
})

test('a clone is told of the newest release, signed app or not', async () => {
  await fresh('v1.139')
  delete process.env.STUDIO_APP_BUNDLE
  setReleases([{ ...release('v1.141'), assets: [] }, release('v1.140')])
  const s = (await j('POST', '/api/updates/check')).body
  assert.equal(s.mode, 'clone')
  assert.equal(s.latest.version, 'v1.141')
  process.env.STUDIO_APP_BUNDLE = APP
})

/** Notes as scripts/release-notes.mjs writes them: the change log's own
 *  regeneration first, then one entry per pull request. */
const notes = (prev, ...titles) => [
  `### Change log through ${prev} (#${++seq})`, '', 'Regenerated after the release.', '',
  ...titles.flatMap(t => [`### ${t} (#${++seq})`, '', `What "${t}" changed.`, '']),
].join('\n')
const withNotes = (rel, body) => ({ ...rel, body })
const titles = r => r.entries.map(e => e.title)

test('the status lists every release an update brings, a failed macOS build included, housekeeping left out', async () => {
  await fresh('v1.139')
  setReleases([
    withNotes(release('v1.138'), notes('v1.137', 'Older change')),
    withNotes(release('v1.140'), notes('v1.139', 'Search: faster', 'Settings: a sidebar switch')),
    // Its macOS build failed: no app attached, but its changes are in v1.142.
    withNotes({ ...release('v1.141'), assets: [] }, notes('v1.140', 'Chat: retry a reply')),
    withNotes(release('v1.142'), notes('v1.141', 'Icons: a drawn icon')),
    withNotes(release('v1.143', { prerelease: true }), notes('v1.142', 'A test version')),
    // Published, its build not finished: beyond what the app would install.
    withNotes({ ...release('v1.144'), assets: [] }, notes('v1.143', 'Not built yet')),
  ])
  let s = (await j('POST', '/api/updates/check')).body
  assert.equal(s.latest.version, 'v1.142')
  assert.equal(s.command, null)
  assert.deepEqual(s.releases.map(r => r.version), ['v1.142', 'v1.141', 'v1.140'])
  assert.deepEqual(s.releases.map(titles), [['Icons: a drawn icon'], ['Chat: retry a reply'], ['Search: faster', 'Settings: a sidebar switch']])
  assert.deepEqual(Object.keys(s.releases[0]).sort(), ['entries', 'publishedAt', 'url', 'version'])
  assert.equal(s.releases[0].publishedAt, '2026-10-20T12:00:00Z')
  assert.match(s.releases[0].url, /\/releases\/v1\.142$/)
  assert.deepEqual(Object.keys(s.releases[2].entries[0]).sort(), ['body', 'pr', 'title'])
  assert.equal(s.releases[2].entries[0].body, 'What "Search: faster" changed.')
  assert.ok(Number.isInteger(s.releases[2].entries[0].pr))

  // Pre-releases count on that channel only.
  await j('PUT', '/api/settings', { updates: { check: true, install: 'manual', channel: 'prerelease' } })
  s = (await j('GET', '/api/updates')).body
  assert.equal(s.latest.version, 'v1.143')
  assert.deepEqual(s.releases.map(r => r.version), ['v1.143', 'v1.142', 'v1.141', 'v1.140'])
  await j('PUT', '/api/settings', { updates: { check: true, install: 'manual', channel: 'stable' } })

  // A clone takes the newest release, built or not, and is given the commands.
  delete process.env.STUDIO_APP_BUNDLE
  s = (await j('GET', '/api/updates')).body
  assert.equal(s.mode, 'clone')
  assert.equal(s.command, 'git pull\npnpm install\npnpm build')
  assert.deepEqual(s.releases.map(r => r.version), ['v1.144', 'v1.142', 'v1.141', 'v1.140'])
  process.env.STUDIO_APP_BUNDLE = APP

  // Nothing newer: no releases.
  U.resetUpdatesForTests({ version: 'v1.142' })
  s = (await j('GET', '/api/updates')).body
  assert.equal(s.available, false)
  assert.deepEqual(s.releases, [])

  // An app its user cannot write to is not a clone of the repository.
  if (process.getuid() !== 0) {
    const locked = path.join(tmp, 'locked-command')
    appTree(path.join(locked, 'ACTIVATE Studio.app'), 'v1.139')
    fs.chmodSync(locked, 0o555)
    process.env.STUDIO_APP_BUNDLE = path.join(locked, 'ACTIVATE Studio.app')
    s = (await j('GET', '/api/updates')).body
    assert.equal(s.mode, 'clone')
    assert.equal(s.command, null)
    fs.chmodSync(locked, 0o755)
    process.env.STUDIO_APP_BUNDLE = APP
  }
})

test('the change log: the bundled file, with the notes of releases up to the running version it lacks', async () => {
  const file = path.join(tmp, 'CHANGELOG.md')
  write(file, `# Change log\n\nEvery version, newest first.\n\n## v1.139 (2026-10-18)\n\n${notes('v1.138', 'Chat: retry a reply')}\n## v1.138 (2026-10-17)\n\n${notes('v1.137', 'Search: faster')}\n## v1.137 (2026-10-16)\n\n_No pull requests; tag only._\n`)
  process.env.STUDIO_CHANGELOG_FILE = file
  try {
    // v1.140's build carries the change log through v1.139; the check that
    // found v1.140 saved its notes.
    await fresh('v1.140')
    setReleases([
      withNotes(release('v1.141'), notes('v1.140', 'Not installed yet')),
      withNotes(release('v1.140'), notes('v1.139', 'Icons: a drawn icon')),
      withNotes(release('v1.139'), notes('v1.138', 'Chat: retry a reply, as released')),
    ])
    await j('POST', '/api/updates/check')
    let r = await j('GET', '/api/changelog')
    assert.equal(r.status, 200)
    assert.equal(r.body.included, true)
    assert.equal(r.body.current, 'v1.140')
    assert.deepEqual(r.body.versions.map(v => [v.version, v.date, titles(v)]), [
      ['v1.140', '2026-10-20', ['Icons: a drawn icon']],
      ['v1.139', '2026-10-18', ['Chat: retry a reply']],
      ['v1.138', '2026-10-17', ['Search: faster']],
    ], 'newest first; the bundled log wins where both have a version; a tag with no entries is left out')
    r = await j('GET', '/api/changelog?after=v1.138')
    assert.deepEqual(r.body.versions.map(v => v.version), ['v1.140', 'v1.139'])
    r = await j('GET', '/api/changelog?after=v1.140')
    assert.deepEqual(r.body.versions, [])

    // A build without the file says so, and still has the saved notes.
    process.env.STUDIO_CHANGELOG_FILE = path.join(tmp, 'no-such-CHANGELOG.md')
    r = await j('GET', '/api/changelog')
    assert.equal(r.status, 200)
    assert.equal(r.body.included, false)
    assert.deepEqual(r.body.versions.map(v => v.version), ['v1.140', 'v1.139'])

    // A hosted Studio has the bundled file only: it never checks.
    process.env.STUDIO_CHANGELOG_FILE = file
    process.env.PW_PLATFORM_HOST = 'platform.test'
    r = await j('GET', '/api/changelog')
    assert.equal(r.status, 200)
    assert.deepEqual(r.body.versions.map(v => v.version), ['v1.139', 'v1.138'])
    delete process.env.PW_PLATFORM_HOST
  } finally {
    delete process.env.STUDIO_CHANGELOG_FILE
    delete process.env.PW_PLATFORM_HOST
  }
})

const opensslVersion = spawnSync('openssl', ['version'], { encoding: 'utf8' }).stdout ?? ''
test('sign_release.sh signs a manifest the server accepts, from a key file or the key in a variable', { skip: /^OpenSSL [3-9]/.test(opensslVersion) ? false : 'needs OpenSSL 3' }, async () => {
  const dir = fs.mkdtempSync(path.join(tmp, 'sign-'))
  const key = path.join(dir, 'key.pem')
  const pub = path.join(dir, 'pub.pem')
  assert.equal(spawnSync('openssl', ['genpkey', '-algorithm', 'ed25519', '-out', key]).status, 0)
  assert.equal(spawnSync('openssl', ['pkey', '-in', key, '-pubout', '-out', pub]).status, 0)
  // Built one commit past the tag; the manifest names the release.
  const archive = archiveOf('v1.140-1-gabc1234', { key: fs.readFileSync(pub, 'utf8') })
  const sign = (env, ...args) => spawnSync('bash', [path.join(MAC, 'sign_release.sh'), archive, ...args], {
    env: { PATH: process.env.PATH, HOME: tmp, ...env }, encoding: 'utf8',
  })
  let r = sign({ STUDIO_UPDATE_SIGNING_KEY_FILE: key })
  assert.notEqual(r.status, 0, 'a key that does not match the committed public key is refused')
  assert.match(r.stderr, /does not match the public key/)
  r = sign({ STUDIO_UPDATE_SIGNING_KEY_FILE: key, STUDIO_UPDATE_PUBLIC_KEY: pub }, 'v1.141')
  assert.notEqual(r.status, 0)
  assert.match(r.stderr, /the app in the archive is v1\.140-1-gabc1234, not v1\.141/)
  r = sign({ STUDIO_UPDATE_SIGNING_KEY_FILE: key, STUDIO_UPDATE_PUBLIC_KEY: pub })
  assert.equal(r.status, 0, r.stderr)
  const manifestFile = archive.replace(/\.tar\.gz$/, '.manifest.json')
  const man = JSON.parse(fs.readFileSync(manifestFile, 'utf8'))
  assert.deepEqual({ ...man, builtAt: undefined }, {
    version: 'v1.140', asset: ASSET, sha256: sha(fs.readFileSync(archive)), size: fs.statSync(archive).size, minMacos: '14.0', builtAt: undefined,
  })

  await fresh('v1.139', { app: { key: fs.readFileSync(pub, 'utf8') } })
  setReleases([release('v1.140', { files: { archive, manifest: manifestFile, sig: `${manifestFile}.sig` } })])
  await j('POST', '/api/updates/install')
  const s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(versionIn(APP), 'v1.140-1-gabc1234')

  // The key as PEM text in a variable, as a CI secret gives it; it is never printed.
  const nodeArchive = archiveOf('v1.140')
  const nodePub = path.join(dir, 'node.pub')
  fs.writeFileSync(nodePub, PUB)
  r = spawnSync('bash', [path.join(MAC, 'sign_release.sh'), nodeArchive], {
    env: { PATH: process.env.PATH, HOME: tmp, STUDIO_UPDATE_SIGNING_KEY: PRIV, STUDIO_UPDATE_PUBLIC_KEY: nodePub }, encoding: 'utf8',
  })
  assert.equal(r.status, 0, r.stderr)
  assert.doesNotMatch(r.stdout + r.stderr, /PRIVATE KEY|BEGIN/)
  const bytes = fs.readFileSync(nodeArchive.replace(/\.tar\.gz$/, '.manifest.json'))
  const sig = Buffer.from(fs.readFileSync(nodeArchive.replace(/\.tar\.gz$/, '.manifest.json.sig'), 'utf8').trim(), 'base64')
  assert.ok(crypto.verify(null, bytes, publicKey, sig))
})

/** A run loop over an app whose server fails at once, with a previous
 *  copy beside it whose server stays up. */
function crashingApp(name) {
  const root = path.join(tmp, name)
  const out = path.join(root, 'out')
  const app = path.join(root, 'Applications', 'ACTIVATE Studio.app')
  const data = path.join(root, 'data')
  fs.mkdirSync(out, { recursive: true })
  appTree(app, 'v1.140', { node: '#!/bin/bash\necho new >>"$OUT/starts"\nenv >"$OUT/env"\nexit 1\n' })
  appTree(path.join(root, 'Applications', '.ACTIVATE Studio.app.previous'), 'v1.139', {
    node: '#!/bin/bash\necho previous >>"$OUT/starts"\ntrap \'exit 0\' TERM\nwhile :; do sleep 0.1; done\n',
  })
  const loop = spawn('bash', [path.join(app, 'Contents', 'Resources', 'studio-run.sh'), data], {
    env: { HOME: root, PATH: '/usr/bin:/bin', OUT: out }, stdio: 'ignore',
  })
  const starts = () => { try { return fs.readFileSync(path.join(out, 'starts'), 'utf8').trim().split('\n') } catch { return [] } }
  return { root, out, app, data, loop, starts }
}

test('the run loop puts the previous copy back after three quick failures of a new version', async () => {
  const root = path.join(tmp, 'rollback')
  write(path.join(root, 'data', 'updates', 'pending.json'), '{\n "from": "v1.139",\n "to": "v1.140",\n "at": "2026-10-21T00:00:00Z"\n}\n')
  const c = crashingApp('rollback')
  try {
    assert.ok(await until(() => c.starts().includes('previous'), 30_000), `starts: ${c.starts()}`)
    assert.deepEqual(c.starts(), ['new', 'new', 'new', 'previous'])
    const env = Object.fromEntries(fs.readFileSync(path.join(c.out, 'env'), 'utf8').split('\n').filter(Boolean).map(l => [l.slice(0, l.indexOf('=')), l.slice(l.indexOf('=') + 1)]))
    assert.equal(env.STUDIO_APP_BUNDLE, c.app)
    assert.equal(env.STUDIO_DATA_DIR, c.data)
    assert.equal(versionIn(c.app), 'v1.139')
    assert.deepEqual(fs.readdirSync(path.dirname(c.app)), ['ACTIVATE Studio.app'])
    const back = JSON.parse(fs.readFileSync(path.join(c.data, 'updates', 'rolled-back.json'), 'utf8'))
    assert.equal(back.from, 'v1.140')
    assert.equal(back.to, 'v1.139')
    assert.match(back.reason, /3 times within two minutes/)
    assert.ok(!fs.existsSync(path.join(c.data, 'updates', 'pending.json')))
    assert.match(fs.readFileSync(path.join(c.data, 'logs', 'studio.log'), 'utf8'), /v1\.140 stopped 3 times within two minutes; v1\.139 is back/)
    c.loop.kill('SIGTERM')
    assert.ok(await until(() => c.loop.exitCode !== null), 'the restored loop stops on TERM')
  } finally {
    c.loop.kill('SIGKILL')
  }
})

test('without the mark, the run loop keeps restarting the same app', async () => {
  const c = crashingApp('no-rollback')
  try {
    assert.ok(await until(() => c.starts().length >= 3, 30_000))
    await sleep(1000)
    assert.ok(!c.starts().includes('previous'))
    assert.equal(versionIn(c.app), 'v1.140')
    assert.ok(fs.existsSync(path.join(path.dirname(c.app), '.ACTIVATE Studio.app.previous')))
    assert.ok(!fs.existsSync(path.join(c.data, 'updates', 'rolled-back.json')))
  } finally {
    c.loop.kill('SIGKILL')
  }
})

test('the launcher names the app in the agent, clears a reopen request, and removes kept copies on uninstall', () => {
  const root = path.join(tmp, 'launcher')
  const app = path.join(root, 'Applications', 'ACTIVATE Studio.app')
  appTree(app, 'v1.140')
  write(path.join(app, 'Contents', 'MacOS', 'ACTIVATE Studio'), fs.readFileSync(path.join(MAC, 'launcher.sh')), 0o755)
  appTree(path.join(root, 'Applications', '.ACTIVATE Studio.app.previous'), 'v1.139')
  const home = path.join(root, 'home')
  const data = path.join(home, 'Library', 'Application Support', 'ACTIVATE Studio')
  write(path.join(data, 'updates', 'reopen'), 'v1.140\n')
  const bin = path.join(root, 'bin')
  const state = path.join(root, 'state')
  fs.mkdirSync(state, { recursive: true })
  write(path.join(bin, 'launchctl'), `#!/bin/bash\ncase "$1" in print) [ -f "${state}/loaded" ] ;; bootstrap) touch "${state}/loaded" ;; bootout) rm -f "${state}/loaded" ;; esac\n`, 0o755)
  write(path.join(bin, 'curl'), `#!/bin/bash\n[ -f "${state}/loaded" ] || exit 7\necho '{"version":"v1.140","builtAt":"2026-10-01T00:00:00Z"}'\n`, 0o755)
  for (const b of ['open', 'osascript']) write(path.join(bin, b), '#!/bin/sh\n', 0o755)
  const run = (...args) => spawnSync(path.join(app, 'Contents', 'MacOS', 'ACTIVATE Studio'), args, {
    env: { HOME: home, PATH: `${bin}:/usr/bin:/bin`, STUDIO_NO_BROWSER: '1' }, encoding: 'utf8',
  })
  let r = run()
  assert.equal(r.status, 0, r.stderr)
  const plist = fs.readFileSync(path.join(home, 'Library', 'LaunchAgents', `${LABEL}.plist`), 'utf8')
  assert.match(plist, new RegExp(`<key>EnvironmentVariables</key>\\s*<dict>\\s*<key>STUDIO_APP_BUNDLE</key>\\s*<string>${app.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}</string>`))
  assert.ok(!fs.existsSync(path.join(data, 'updates', 'reopen')))
  r = run('--uninstall')
  assert.equal(r.status, 0, r.stderr)
  assert.deepEqual(fs.readdirSync(path.join(root, 'Applications')), [])
})

test.after(() => {
  server.close()
  fs.rmSync(tmp, { recursive: true, force: true })
})
