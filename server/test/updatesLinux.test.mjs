// Updates of the Linux payload: which release is offered (the Linux assets,
// not the macOS ones), the per-user install that replaces itself (signed
// manifest, checks on every download, the swap with the previous payload
// kept, the rollback, the restart through the run loop's exit 76), and the
// installs a package manager owns (.deb, .rpm, a read-only /opt), which are
// told the version and the package command and never write anything. The
// macOS app's own tests are in updates.test.mjs and do not change.
import test from 'node:test'
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import fs from 'node:fs'
import fsp from 'node:fs/promises'
import http from 'node:http'
import os from 'node:os'
import path from 'node:path'
import { spawn, spawnSync } from 'node:child_process'

const REPO = path.resolve(import.meta.dirname, '..', '..')
const LINUX = path.join(REPO, 'deploy', 'linux')
const tmp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'updates-linux-')))
const LIB = path.join(tmp, 'lib')
const ROOT = path.join(LIB, 'activate-studio')
const PREVIOUS = path.join(LIB, '.activate-studio.previous')
const DATA = path.join(tmp, 'data')
const UPD = path.join(DATA, 'updates')
const BIN = path.join(tmp, 'bin')
const CALLS = path.join(tmp, 'calls')
const isRoot = process.getuid() === 0

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
const versionIn = root => JSON.parse(fs.readFileSync(path.join(root, 'app', 'build-info.json'), 'utf8')).version
const calls = () => { try { return fs.readFileSync(CALLS, 'utf8') } catch { return '' } }
const listing = dir => { try { return fs.readdirSync(dir).sort() } catch { return null } }
/** Every path in a tree with its mode and size, to show that nothing in it changed. */
const snapshot = dir => {
  const out = []
  const walk = d => {
    for (const e of fs.readdirSync(d, { withFileTypes: true })) {
      const p = path.join(d, e.name)
      const st = fs.lstatSync(p)
      out.push(`${path.relative(dir, p)} ${st.mode.toString(8)} ${st.size} ${st.mtimeMs}`)
      if (e.isDirectory()) walk(p)
    }
  }
  walk(dir)
  return out.sort()
}

const { publicKey, privateKey } = crypto.generateKeyPairSync('ed25519')
const PUB = publicKey.export({ type: 'spki', format: 'pem' })
const PRIV = privateKey.export({ type: 'pkcs8', format: 'pem' })
const stranger = crypto.generateKeyPairSync('ed25519').privateKey

// The server imports under test: set the environment first.
write(path.join(BIN, 'launchctl'), `#!/bin/sh\necho "launchctl $*" >>"${CALLS}"\n`, 0o755)
write(path.join(BIN, 'lsregister'), `#!/bin/sh\necho "lsregister $*" >>"${CALLS}"\n`, 0o755)
process.env.STUDIO_LSREGISTER = path.join(BIN, 'lsregister')
process.env.KB_ROOT = fs.mkdtempSync(path.join(tmp, 'kb-'))
process.env.INDEX_BASE = path.join(DATA, 'index')
process.env.STUDIO_DATA_DIR = DATA
process.env.PATH = `${BIN}:${process.env.PATH}`
for (const k of ['AUTH_JWKS_URL', 'PW_PLATFORM_HOST', 'STUDIO_CONFIG_FILE', 'STUDIO_APP_BUNDLE', 'STUDIO_SUPERVISOR']) delete process.env[k]
process.env.STUDIO_RESTARTABLE = '1'

const { default: Fastify } = await import('fastify')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const { settingsRoutes } = await import('../dist/settings.js')
const U = await import('../dist/updates.js')
const L = await import('../dist/lifecycle.js')
const ASSET = U.linuxAsset()
const MANIFEST = ASSET.replace(/\.tar\.gz$/, '.manifest.json')
const SIGNATURE = `${MANIFEST}.sig`
const MAC_ASSET = 'activate-studio-macos-arm64.tar.gz'
const MAC_MANIFEST = 'activate-studio-macos-arm64.manifest.json'
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
  // The stop the server asks for comes a moment after it says it is restarting.
  if (s.state === 'restarting') assert.ok(await until(() => stops.length > 0), 'the server asks to stop')
  return s
}

/** The exit statuses the server asked its run loop for. */
const stops = []
L.setStopper(code => { stops.push(code) })

// ---- the payload and its archive ----

/** A payload tree: what the installer checks, with the real run loop. */
function payloadTree(dir, version, { key = PUB, kind, shell, manifest = {}, noRun = false, node = '#!/bin/sh\n', locked = false } = {}) {
  if (!noRun) write(path.join(dir, 'studio-run.sh'), fs.readFileSync(path.join(LINUX, 'studio-run.sh')), 0o755)
  write(path.join(dir, 'node', 'bin', 'node'), node, 0o755)
  write(path.join(dir, 'app', 'server', 'dist', 'main.js'), '// server\n')
  write(path.join(dir, 'app', 'build-info.json'), `{\n "version": "${version}",\n "builtAt": "2026-10-01T00:00:00Z"\n}\n`)
  write(path.join(dir, 'manifest.json'), JSON.stringify({ name: 'ACTIVATE Studio', platform: 'linux-x86_64', version, minGlibc: '2.34', ...(shell ? { shell: 'bin/window' } : {}), ...manifest }, null, 1) + '\n')
  if (shell) write(path.join(dir, 'bin', 'window'), shell, 0o755)
  if (key) write(path.join(dir, 'update-signing.pub'), key)
  if (kind) write(path.join(dir, 'install-kind'), `${kind}\n`)
  if (locked) {
    // Directories without a write bit, as an archive of Python's library leaves them.
    for (const d of ['python/lib/python3.12/email', 'python/lib/python3.12']) {
      write(path.join(dir, d, 'x.py'), '# x\n')
      fs.chmodSync(path.join(dir, d), 0o555)
    }
  }
}

function archiveOf(version, { outside = false, ...opts } = {}) {
  const dir = fs.mkdtempSync(path.join(tmp, 'build-'))
  payloadTree(path.join(dir, 'activate-studio'), version, opts)
  if (outside) write(path.join(dir, 'evil.txt'), 'x\n')
  const file = path.join(dir, ASSET)
  const r = spawnSync('tar', ['-czf', file, '-C', dir, 'activate-studio', ...(outside ? ['evil.txt'] : [])])
  assert.equal(r.status, 0, String(r.stderr))
  return file
}

// ---- a stand-in for GitHub ----

const files = new Map()
const cut = new Set()
let releases = []
let etag = '"r0"'
const server = http.createServer((req, res) => {
  if (req.url.startsWith('/repos/parallelworks/activate-studio/releases')) {
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
process.env.STUDIO_UPDATE_URL = BASE
let seq = 0

/** A release as GitHub lists it. `assets` picks which builds it carries:
 *  linux (the default), mac, or both; the rest is as in updates.test.mjs. */
function release(tag, { assets = 'linux', manifest = {}, signWith = privateKey, archiveVersion = tag, prerelease = false, app: tree = {}, cutArchive = false, noSig = false } = {}) {
  const id = ++seq
  const list = []
  const serve = (name, body) => {
    files.set(`/dl/${id}/${name}`, body)
    list.push({ name, size: body.length, browser_download_url: `${BASE}/dl/${id}/${name}` })
  }
  if (assets === 'linux' || assets === 'both') {
    const archive = fs.readFileSync(archiveOf(archiveVersion, tree))
    const man = Buffer.from(JSON.stringify({ version: tag, asset: ASSET, sha256: sha(archive), size: archive.length, minGlibc: '2.34', builtAt: '2026-10-20T00:00:00Z', ...manifest }, null, 1) + '\n')
    serve(ASSET, archive)
    serve(MANIFEST, man)
    if (!noSig) serve(SIGNATURE, Buffer.from(crypto.sign(null, man, signWith).toString('base64') + '\n'))
    if (cutArchive) cut.add(`/dl/${id}/${ASSET}`)
  }
  if (assets === 'mac' || assets === 'both') {
    serve(MAC_ASSET, Buffer.from('not a real app'))
    serve(MAC_MANIFEST, Buffer.from('{}'))
    serve(`${MAC_MANIFEST}.sig`, Buffer.from('x'))
  }
  return { tag_name: tag, draft: false, prerelease, published_at: '2026-10-20T12:00:00Z', body: `Notes for ${tag}`, html_url: `${BASE}/releases/${tag}`, assets: list }
}
const setReleases = list => { releases = list; etag = `"r${++seq}"` }

/** The payload at `version` in a per-user place, nothing beside it, no updates state. */
async function fresh(version = 'v1.139', { kind = 'user', tree = {}, timings } = {}) {
  for (const d of [LIB, tmp + '/ro']) {
    try { fs.chmodSync(d, 0o755) } catch { /* not there */ }
  }
  spawnSync('chmod', ['-R', 'u+w', LIB])
  fs.rmSync(LIB, { recursive: true, force: true })
  fs.rmSync(UPD, { recursive: true, force: true })
  fs.rmSync(CALLS, { force: true })
  stops.length = 0
  payloadTree(ROOT, version, { kind, ...tree })
  process.env.STUDIO_PAYLOAD_ROOT = ROOT
  U.resetUpdatesForTests({ version, ...timings })
  const r = await j('PUT', '/api/settings', { updates: { check: true, install: 'manual', channel: 'stable' } })
  assert.equal(r.status, 200)
}

// ---- which release ----

test('the Linux asset names carry the architecture of this computer', () => {
  assert.match(ASSET, /^activate-studio-linux-(x86_64|aarch64)\.tar\.gz$/)
})

test('a per-user install is offered the newest release that has the Linux build, and the macOS build does not count', async () => {
  await fresh()
  setReleases([
    release('v1.138', { assets: 'mac' }),
    release('v1.140'),
    release('v1.141', { assets: 'mac' }),
    release('v1.142', { noSig: true }),
    release('v1.143', { prerelease: true }),
    release('v1.144', { assets: 'both' }),
  ])
  let r = await j('POST', '/api/updates/check')
  assert.equal(r.body.mode, 'app')
  assert.equal(r.body.package, null)
  assert.equal(r.body.command, null)
  assert.equal(r.body.latest.version, 'v1.144', 'a release with both builds counts')
  setReleases([release('v1.138', { assets: 'mac' }), release('v1.140'), release('v1.141', { assets: 'mac' }), release('v1.142', { noSig: true }), release('v1.143', { prerelease: true })])
  r = await j('POST', '/api/updates/check')
  assert.equal(r.body.latest.version, 'v1.140', 'not v1.141 (macOS only), v1.142 (no signature) or v1.143 (a prerelease)')
  assert.equal(r.body.available, true)
  assert.equal(r.body.canInstall, true)
  assert.equal(r.body.reason, null)
  assert.deepEqual(r.body.releases.map(x => x.version), ['v1.140'])
  assert.equal((await j('PUT', '/api/settings', { updates: { check: true, install: 'manual', channel: 'prerelease' } })).status, 200)
  assert.equal((await j('GET', '/api/updates')).body.latest.version, 'v1.143')

  // Only macOS builds: nothing to install, and not "available".
  await fresh()
  setReleases([release('v1.141', { assets: 'mac' })])
  r = await j('POST', '/api/updates/check')
  assert.equal(r.body.latest, null)
  assert.equal(r.body.available, false)
  assert.equal(r.body.canInstall, false)
})

test('the install-kind file and the path decide the mode', async () => {
  setReleases([release('v1.140')])
  // user, in a place this user can write
  await fresh('v1.139', { kind: 'user' })
  let s = (await j('POST', '/api/updates/check')).body
  assert.equal(s.mode, 'app')
  // no file, in a place this user can write: also theirs
  await fresh('v1.139', { kind: null })
  s = (await j('POST', '/api/updates/check')).body
  assert.equal(s.mode, 'app')
  // a junk file counts as none
  await fresh('v1.139', { kind: 'snap' })
  assert.equal(U.updateMode(), 'app')
  // deb and rpm never replace themselves, even where they could write
  for (const kind of ['deb', 'rpm']) {
    await fresh('v1.139', { kind })
    s = (await j('POST', '/api/updates/check')).body
    assert.equal(s.mode, 'package', kind)
    assert.equal(s.canInstall, false)
    assert.equal(s.package.kind, kind)
  }
  // a package name after the kind
  await fresh('v1.139', { kind: 'deb activate-studio-beta' })
  s = (await j('POST', '/api/updates/check')).body
  assert.match(s.command, /apt install --only-upgrade activate-studio-beta$/)
  // a name that is not a package name is the default
  await fresh('v1.139', { kind: 'deb x;rm' })
  assert.match((await j('POST', '/api/updates/check')).body.command, /--only-upgrade activate-studio$/)
  // not a payload: the server runs from a clone
  delete process.env.STUDIO_PAYLOAD_ROOT
  assert.equal(U.updateMode(), 'clone')
  assert.match((await j('GET', '/api/updates')).body.reason, /git pull && pnpm install && pnpm build/)
})

// ---- the per-user install ----

test('a newer signed release installs: unpacked beside, swapped in, the previous kept, the mark written, the run loop asked for exit 76', async () => {
  await fresh('v1.139', { kind: null })
  setReleases([release('v1.140')])
  let r = await j('POST', '/api/updates/check')
  assert.equal(r.body.canInstall, true)
  r = await j('POST', '/api/updates/install')
  assert.equal(r.status, 200)
  const s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(s.error, null)
  assert.equal(versionIn(ROOT), 'v1.140')
  assert.equal(versionIn(PREVIOUS), 'v1.139')
  assert.deepEqual(s.previous, { version: 'v1.139' })
  assert.equal(s.reopen, false, 'no window of its own in this payload')
  assert.equal(fs.readFileSync(path.join(ROOT, 'install-kind'), 'utf8'), 'user\n', 'the new payload says it is a per-user install, though the old one had no file')
  const pending = JSON.parse(fs.readFileSync(path.join(UPD, 'pending.json'), 'utf8'))
  assert.equal(pending.from, 'v1.139')
  assert.equal(pending.to, 'v1.140')
  assert.ok(await until(() => stops.length > 0), 'the server stops')
  assert.deepEqual(stops, [L.RELAUNCH_EXIT_CODE])
  assert.equal(L.RELAUNCH_EXIT_CODE, 76)
  await sleep(150)
  assert.equal(calls(), '', 'no LaunchAgent and no LaunchServices on Linux')
  assert.deepEqual(listing(LIB), ['.activate-studio.previous', 'activate-studio'])
  assert.deepEqual(listing(UPD), ['pending.json', 'state.json'])
  assert.ok(fs.statSync(path.join(ROOT, 'studio-run.sh')).mode & 0o100, 'the run loop is executable')
  assert.equal((await j('POST', '/api/updates/install')).body.state, 'restarting', 'a second request changes nothing')

  // An older .previous is replaced, and a package name in the marker is kept.
  await fresh('v1.140', { kind: 'user foo-studio' })
  payloadTree(PREVIOUS, 'v1.120')
  setReleases([release('v1.141')])
  await j('POST', '/api/updates/install')
  const t = await settled()
  assert.equal(t.state, 'restarting', t.error)
  assert.equal(versionIn(ROOT), 'v1.141')
  assert.equal(versionIn(PREVIOUS), 'v1.140')
  assert.equal(fs.readFileSync(path.join(ROOT, 'install-kind'), 'utf8'), 'user foo-studio\n')
})

test('an update that changes the window program asks for the window to be reopened; one that does not, does not', async () => {
  await fresh('v1.139', { tree: { shell: '#!/bin/sh\n# window 1\n' } })
  setReleases([release('v1.140', { app: { shell: '#!/bin/sh\n# window 2\n' } })])
  await j('POST', '/api/updates/install')
  let s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(s.reopen, true)
  assert.ok(fs.existsSync(path.join(UPD, 'reopen')))

  await fresh('v1.139', { tree: { shell: '#!/bin/sh\n# window 1\n' } })
  setReleases([release('v1.140', { app: { shell: '#!/bin/sh\n# window 1\n' } })])
  await j('POST', '/api/updates/install')
  s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(s.reopen, false)
})

test('a release that fails any check is refused, cleaned up, and the payload left alone', async () => {
  const glibc = process.report.getReport().header.glibcVersionRuntime
  const cases = [
    ['a bad signature', { signWith: stranger }, /signature does not verify/],
    ['a wrong SHA-256', { manifest: { sha256: '0'.repeat(64) } }, /SHA-256 does not match/],
    ['a shorter download than the manifest says', { manifest: { size: 10 ** 7 } }, /the manifest says 10000000/],
    ['a longer download than the manifest says', { manifest: { size: 100 } }, /larger than the 100 bytes/],
    ['a download cut off', { cutArchive: true }, /The update failed/],
    ['the macOS asset named in the manifest', { manifest: { asset: MAC_ASSET } }, /names the asset activate-studio-macos-arm64/],
    ['an older manifest under a newer release', { manifest: { version: 'v1.138' } }, /v1\.138 is not newer than the running v1\.139/],
    ['a manifest for another release', { manifest: { version: 'v1.141' } }, /for v1\.141, but the release is v1\.140/],
    ['a payload of another version', { archiveVersion: 'v1.142' }, /The new app is v1\.142, not v1\.140/],
    ['an entry outside the payload', { app: { outside: true } }, /holds evil\.txt, outside activate-studio/],
    ['a payload with no run loop', { app: { noRun: true } }, /has no studio-run\.sh/],
    ['a payload that needs a newer glibc', { manifest: { minGlibc: '99.1' } }, glibc ? /needs glibc 99\.1; this computer has/ : null],
  ]
  for (const [what, opts, why] of cases) {
    if (!why) continue
    await fresh()
    const before = snapshot(ROOT)
    setReleases([release('v1.140', opts)])
    await j('POST', '/api/updates/check')
    const r = await j('POST', '/api/updates/install')
    assert.equal(r.status, 200, what)
    const s = await settled()
    assert.equal(s.state, 'error', `${what}: ${s.error}`)
    assert.match(s.error, why, what)
    assert.equal(versionIn(ROOT), 'v1.139', what)
    assert.deepEqual(snapshot(ROOT), before, `${what}: the payload is untouched`)
    assert.deepEqual(listing(LIB), ['activate-studio'], `${what}: nothing is left beside the payload`)
    assert.deepEqual(listing(UPD)?.filter(f => f !== 'state.json'), [], `${what}: nothing is left in the updates folder`)
    assert.equal(s.previous, null, what)
    assert.deepEqual(stops, [], `${what}: no restart`)
  }
  // The payload's key not matching what the release signs with is the first of these:
  // a payload with no key cannot verify anything.
  await fresh('v1.139', { tree: { key: null } })
  setReleases([release('v1.140')])
  let r = await j('POST', '/api/updates/check')
  assert.equal(r.body.canInstall, false)
  assert.match(r.body.reason, /no update signing key/)
  r = await j('POST', '/api/updates/install')
  assert.equal(r.status, 409)
})

test('a swap that fails half way puts the running payload back', async () => {
  await fresh()
  setReleases([release('v1.140')])
  await j('POST', '/api/updates/check')
  const before = snapshot(ROOT)
  const real = fsp.rename
  // The move of the new payload into place fails; the move of the old one out has happened.
  fsp.rename = async (from, to) => {
    if (to === ROOT && from.endsWith('.activate-studio.new')) throw Object.assign(new Error('EXDEV: stand-in failure'), { code: 'EXDEV' })
    return real(from, to)
  }
  try {
    await j('POST', '/api/updates/install')
    const s = await settled()
    assert.equal(s.state, 'error')
    assert.match(s.error, /stand-in failure/)
    assert.equal(versionIn(ROOT), 'v1.139')
    assert.deepEqual(snapshot(ROOT), before)
    assert.deepEqual(listing(LIB), ['activate-studio'])
    assert.ok(!fs.existsSync(path.join(UPD, 'pending.json')), 'no mark for an update that did not happen')
    assert.deepEqual(stops, [])
  } finally {
    fsp.rename = real
  }
})

test('a rollback puts the previous payload back and asks for exit 76; automatic install then skips that version', async () => {
  await fresh()
  setReleases([release('v1.140')])
  await j('POST', '/api/updates/install')
  assert.equal((await settled()).state, 'restarting')
  // The run loop started the new payload: a server of v1.140.
  U.resetUpdatesForTests({ version: 'v1.140' })
  stops.length = 0
  let r = await j('GET', '/api/updates')
  assert.deepEqual(r.body.previous, { version: 'v1.139' })
  r = await j('POST', '/api/updates/rollback')
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(r.body.state, 'restarting')
  assert.equal(versionIn(ROOT), 'v1.139')
  assert.ok(!fs.existsSync(PREVIOUS))
  assert.equal(r.body.previous, null)
  assert.deepEqual(listing(LIB), ['activate-studio'])
  const back = JSON.parse(fs.readFileSync(path.join(UPD, 'rolled-back.json'), 'utf8'))
  assert.equal(back.from, 'v1.140')
  assert.equal(back.to, 'v1.139')
  assert.ok(!fs.existsSync(path.join(UPD, 'pending.json')))
  assert.ok(await until(() => stops.length > 0))
  assert.deepEqual(stops, [76])
  assert.equal((await j('POST', '/api/updates/rollback')).status, 409, 'nothing more to go back to')

  // Started again as v1.139, with install set to auto: v1.140 stays put.
  U.resetUpdatesForTests({ version: 'v1.139', idleMs: 50, idlePollMs: 20 })
  stops.length = 0
  await j('PUT', '/api/settings', { updates: { check: true, install: 'auto', channel: 'stable' } })
  r = await j('POST', '/api/updates/check')
  assert.equal(r.body.available, true)
  await sleep(400)
  assert.equal(versionIn(ROOT), 'v1.139')
  assert.deepEqual(listing(LIB), ['activate-studio'])
  assert.deepEqual(stops, [])
})

test('automatic install verifies first, then waits until the Studio has been idle, and swaps then', async () => {
  await fresh('v1.139', { timings: { idleMs: 300, idlePollMs: 20 } })
  await j('PUT', '/api/settings', { updates: { check: true, install: 'auto', channel: 'stable' } })
  setReleases([release('v1.140')])
  await j('POST', '/api/updates/check')
  assert.ok(await until(() => fs.existsSync(path.join(LIB, '.activate-studio.new'))), 'verified and unpacked beside the payload')
  assert.equal(versionIn(ROOT), 'v1.139', 'not swapped yet')
  assert.ok(await until(() => stops.length > 0, 5000), 'swapped once idle')
  assert.equal(versionIn(ROOT), 'v1.140')
  assert.equal(versionIn(PREVIOUS), 'v1.139')
  assert.deepEqual(stops, [76])
})

test('a package install is never installed automatically', async () => {
  await fresh('v1.139', { kind: 'deb', timings: { idleMs: 50, idlePollMs: 20 } })
  await j('PUT', '/api/settings', { updates: { check: true, install: 'auto', channel: 'stable' } })
  setReleases([release('v1.140')])
  const before = snapshot(ROOT)
  await j('POST', '/api/updates/check')
  await sleep(300)
  assert.deepEqual(snapshot(ROOT), before)
  assert.deepEqual(listing(LIB), ['activate-studio'])
  assert.deepEqual(stops, [])
})

test('a payload with read-only directories (as Python\'s library is) is replaced, its previous copy deleted, and rolled back', { skip: isRoot && 'root ignores modes' }, async () => {
  await fresh('v1.139', { tree: { locked: true } })
  setReleases([release('v1.140', { app: { locked: true } })])
  await j('POST', '/api/updates/check')
  await j('POST', '/api/updates/install')
  let s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(versionIn(ROOT), 'v1.140')
  assert.equal(versionIn(PREVIOUS), 'v1.139')
  // The next update deletes that previous payload, read-only directories and all.
  U.resetUpdatesForTests({ version: 'v1.140' })
  stops.length = 0
  setReleases([release('v1.141', { app: { locked: true } })])
  await j('POST', '/api/updates/check')
  await j('POST', '/api/updates/install')
  s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(versionIn(ROOT), 'v1.141')
  assert.equal(versionIn(PREVIOUS), 'v1.140')
  // And a rollback deletes the payload it leaves.
  U.resetUpdatesForTests({ version: 'v1.141' })
  stops.length = 0
  s = (await j('POST', '/api/updates/rollback')).body
  assert.equal(s.state, 'restarting')
  assert.equal(versionIn(ROOT), 'v1.140')
  assert.deepEqual(listing(LIB), ['activate-studio'])
})

test('a second update replaces the payload again, and keeps the one it replaced as the previous', async () => {
  await fresh()
  setReleases([release('v1.140')])
  await j('POST', '/api/updates/install')
  assert.equal((await settled()).state, 'restarting')
  // The same Studio, restarted on v1.140.
  U.resetUpdatesForTests({ version: 'v1.140' })
  setReleases([release('v1.141')])
  const s0 = (await j('POST', '/api/updates/check')).body
  assert.equal(s0.latest.version, 'v1.141')
  await j('POST', '/api/updates/install')
  const s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(versionIn(ROOT), 'v1.141')
  assert.equal(versionIn(PREVIOUS), 'v1.140')
})

// ---- the run loop ----

/** A run loop over a payload whose server fails at once, with a previous
 *  payload beside it whose server stays up. */
function crashingPayload(name) {
  const root = path.join(tmp, name)
  const out = path.join(root, 'out')
  const payload = path.join(root, 'lib', 'activate-studio')
  const data = path.join(root, 'data')
  fs.mkdirSync(out, { recursive: true })
  payloadTree(payload, 'v1.140', { node: '#!/bin/bash\necho new >>"$OUT/starts"\nexit 1\n' })
  payloadTree(path.join(root, 'lib', '.activate-studio.previous'), 'v1.139', {
    node: '#!/bin/bash\necho previous >>"$OUT/starts"\ntrap \'exit 0\' TERM\nwhile :; do sleep 0.1; done\n',
  })
  const loop = spawn('bash', [path.join(payload, 'studio-run.sh'), data], {
    env: { HOME: root, PATH: '/usr/bin:/bin', OUT: out }, stdio: 'ignore',
  })
  const starts = () => { try { return fs.readFileSync(path.join(out, 'starts'), 'utf8').trim().split('\n') } catch { return [] } }
  return { root, out, payload, data, loop, starts }
}

test('the run loop puts the previous payload back after three quick failures of a new version', async () => {
  const root = path.join(tmp, 'rollback')
  write(path.join(root, 'data', 'updates', 'pending.json'), '{\n "from": "v1.139",\n "to": "v1.140",\n "at": "2026-10-21T00:00:00Z"\n}\n')
  const c = crashingPayload('rollback')
  try {
    assert.ok(await until(() => c.starts().includes('previous'), 30_000), `starts: ${c.starts()}`)
    assert.deepEqual(c.starts(), ['new', 'new', 'new', 'previous'])
    assert.equal(versionIn(c.payload), 'v1.139')
    assert.deepEqual(listing(path.dirname(c.payload)), ['activate-studio'])
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

test('without the mark, the run loop keeps restarting the same payload', async () => {
  const c = crashingPayload('no-rollback')
  try {
    assert.ok(await until(() => c.starts().length >= 3, 30_000))
    await sleep(1000)
    assert.ok(!c.starts().includes('previous'))
    assert.equal(versionIn(c.payload), 'v1.140')
    assert.ok(fs.existsSync(path.join(path.dirname(c.payload), '.activate-studio.previous')))
  } finally {
    c.loop.kill('SIGKILL')
  }
})

test('exit 76 hands the run loop over to the payload now at its path', async () => {
  const root = path.join(tmp, 'handover')
  const out = path.join(root, 'out')
  const payload = path.join(root, 'lib', 'activate-studio')
  fs.mkdirSync(out, { recursive: true })
  // The old server exits 76 at once; the new payload's server stays up.
  payloadTree(payload, 'v1.139', { node: '#!/bin/bash\necho old >>"$OUT/starts"\nsleep 1\nexit 76\n' })
  const loop = spawn('bash', [path.join(payload, 'studio-run.sh'), path.join(root, 'data')], { env: { HOME: root, PATH: '/usr/bin:/bin', OUT: out }, stdio: 'ignore' })
  const starts = () => { try { return fs.readFileSync(path.join(out, 'starts'), 'utf8').trim().split('\n') } catch { return [] } }
  try {
    assert.ok(await until(() => starts().includes('old')))
    // What the update does to the directory while the old server runs.
    const next = path.join(root, 'lib', '.activate-studio.new')
    payloadTree(next, 'v1.140', { node: '#!/bin/bash\necho new >>"$OUT/starts"\ntrap \'exit 0\' TERM\nwhile :; do sleep 0.1; done\n' })
    fs.renameSync(payload, path.join(root, 'lib', '.activate-studio.previous'))
    fs.renameSync(next, payload)
    assert.ok(await until(() => starts().includes('new'), 20_000), `starts: ${starts()}`)
    assert.deepEqual(starts(), ['old', 'new'])
    assert.equal(loop.exitCode, null, 'the same process, still the supervisor')
    loop.kill('SIGTERM')
    assert.ok(await until(() => loop.exitCode !== null))
  } finally {
    loop.kill('SIGKILL')
  }
})

// ---- the signing script ----

const opensslVersion = spawnSync('openssl', ['version'], { encoding: 'utf8' }).stdout ?? ''
test('deploy/linux/sign_release.sh signs a manifest the server accepts', { skip: /^OpenSSL [3-9]/.test(opensslVersion) ? false : 'needs OpenSSL 3' }, async () => {
  const dir = fs.mkdtempSync(path.join(tmp, 'sign-'))
  const key = path.join(dir, 'key.pem')
  const pub = path.join(dir, 'pub.pem')
  assert.equal(spawnSync('openssl', ['genpkey', '-algorithm', 'ed25519', '-out', key]).status, 0)
  assert.equal(spawnSync('openssl', ['pkey', '-in', key, '-pubout', '-out', pub]).status, 0)
  const pubText = fs.readFileSync(pub, 'utf8')
  // Built one commit past the tag; the manifest names the release.
  const archive = archiveOf('v1.140-1-gabc1234', { key: pubText, manifest: { minGlibc: '2.34' } })
  const sign = (env, ...args) => spawnSync('bash', [path.join(LINUX, 'sign_release.sh'), archive, ...args], {
    env: { PATH: process.env.PATH, HOME: tmp, ...env }, encoding: 'utf8',
  })
  let r = sign({ STUDIO_UPDATE_SIGNING_KEY_FILE: key })
  assert.notEqual(r.status, 0, 'a key that does not match the committed public key is refused')
  assert.match(r.stderr, /does not match the public key/)
  r = sign({ STUDIO_UPDATE_SIGNING_KEY_FILE: key, STUDIO_UPDATE_PUBLIC_KEY: pub }, 'v1.141')
  assert.notEqual(r.status, 0)
  assert.match(r.stderr, /the payload in the archive is v1\.140-1-gabc1234, not v1\.141/)
  // A payload that carries another key than the one the release is signed with could not verify the next update.
  const other = path.join(dir, 'other.pub')
  fs.writeFileSync(other, PUB)
  r = sign({ STUDIO_UPDATE_SIGNING_KEY_FILE: key, STUDIO_UPDATE_PUBLIC_KEY: other })
  assert.notEqual(r.status, 0)
  r = sign({ STUDIO_UPDATE_SIGNING_KEY_FILE: key, STUDIO_UPDATE_PUBLIC_KEY: pub })
  assert.equal(r.status, 0, r.stderr)
  const manifestFile = archive.replace(/\.tar\.gz$/, '.manifest.json')
  const man = JSON.parse(fs.readFileSync(manifestFile, 'utf8'))
  assert.deepEqual({ ...man, builtAt: undefined }, {
    version: 'v1.140', asset: ASSET, sha256: sha(fs.readFileSync(archive)), size: fs.statSync(archive).size, minGlibc: '2.34', builtAt: undefined,
  })

  // The server accepts what the script wrote.
  await fresh('v1.139', { tree: { key: pubText } })
  const id = ++seq
  const served = []
  for (const [name, file] of [[ASSET, archive], [MANIFEST, manifestFile], [SIGNATURE, `${manifestFile}.sig`]]) {
    const body = fs.readFileSync(file)
    files.set(`/dl/${id}/${name}`, body)
    served.push({ name, size: body.length, browser_download_url: `${BASE}/dl/${id}/${name}` })
  }
  setReleases([{ tag_name: 'v1.140', draft: false, prerelease: false, published_at: '2026-10-20T12:00:00Z', body: '', html_url: `${BASE}/releases/v1.140`, assets: served }])
  await j('POST', '/api/updates/install')
  const s = await settled()
  assert.equal(s.state, 'restarting', s.error)
  assert.equal(versionIn(ROOT), 'v1.140-1-gabc1234')
})

// ---- installs a package manager owns ----

test('a .deb install is told the version and the apt command, installs nothing, and refuses the install and rollback requests', async () => {
  await fresh('v1.139', { kind: 'deb' })
  setReleases([release('v1.138', { assets: 'mac' }), release('v1.140'), release('v1.141', { assets: 'mac' })])
  const before = snapshot(ROOT)
  const r = await j('POST', '/api/updates/check')
  const s = r.body
  assert.equal(s.mode, 'package')
  assert.equal(s.latest.version, 'v1.140', 'a version with no Linux build is not announced')
  assert.equal(s.available, true)
  assert.equal(s.canInstall, false)
  assert.deepEqual(s.package, { kind: 'deb', manager: 'apt', name: 'activate-studio' })
  assert.equal(s.command, 'sudo apt update\nsudo apt install --only-upgrade activate-studio')
  assert.match(s.commandNote, /installed from a \.deb package, so it does not update itself/)
  assert.match(s.reason, /\.deb package.*Update it with apt in a terminal, then quit and reopen/)
  assert.doesNotMatch(JSON.stringify(s), /git pull|pnpm|dnf|rpm/)
  assert.deepEqual(s.releases.map(x => x.version), ['v1.140'])
  assert.equal(s.previous, null)
  for (const [url, body] of [['/api/updates/install'], ['/api/updates/rollback']]) {
    const x = await j('POST', url, body)
    assert.equal(x.status, 409, url)
    assert.match(x.body.error, /\.deb package/)
  }
  assert.deepEqual(snapshot(ROOT), before, 'not one file in the payload changed')
  assert.deepEqual(listing(LIB), ['activate-studio'])
  assert.deepEqual(listing(UPD), ['state.json'], 'nothing downloaded')
  assert.deepEqual(stops, [])

  // Up to date: no command, and the reason says where updates come from.
  U.resetUpdatesForTests({ version: 'v1.140' })
  const t = (await j('POST', '/api/updates/check')).body
  assert.equal(t.available, false)
  assert.equal(t.command, null)
  assert.equal(t.commandNote, null)
  assert.match(t.reason, /updates come through apt/)
})

test('an .rpm install is told the dnf command', async () => {
  await fresh('v1.139', { kind: 'rpm' })
  setReleases([release('v1.140')])
  const s = (await j('POST', '/api/updates/check')).body
  assert.equal(s.mode, 'package')
  assert.deepEqual(s.package, { kind: 'rpm', manager: 'dnf', name: 'activate-studio' })
  assert.equal(s.command, 'sudo dnf upgrade --refresh activate-studio')
  assert.match(s.commandNote, /installed from an \.rpm package.*run this in a terminal/)
  assert.match(s.reason, /Update it with dnf/)
  assert.doesNotMatch(JSON.stringify(s), /apt|\.deb/)
  const x = await j('POST', '/api/updates/install')
  assert.equal(x.status, 409)
  assert.match(x.body.error, /\.rpm package/)
})

test('a package install runs as root, and still does not replace itself', { skip: !isRoot && 'needs root' }, async () => {
  await fresh('v1.139', { kind: 'deb' })
  setReleases([release('v1.140')])
  const s = (await j('POST', '/api/updates/check')).body
  assert.equal(s.mode, 'package')
  assert.equal((await j('POST', '/api/updates/install')).status, 409)
})

test('a root-owned read-only /opt install (a package) is told the command and writes nothing, as a plain user', { skip: isRoot && 'root can write anywhere' }, async () => {
  for (const kind of ['deb', 'rpm', null, 'user']) {
    await fresh('v1.139', { kind })
    setReleases([release('v1.140')])
    const opt = path.join(tmp, 'ro')
    fs.rmSync(opt, { recursive: true, force: true })
    fs.renameSync(LIB, opt)
    const root = path.join(opt, 'activate-studio')
    process.env.STUDIO_PAYLOAD_ROOT = root
    // Read-only the way a package leaves it: no write bit in the tree, nor in the directory above.
    spawnSync('chmod', ['-R', 'a-w', opt])
    try {
      const before = snapshot(root)
      assert.equal(U.updateMode(), 'package', String(kind))
      const s = (await j('POST', '/api/updates/check')).body
      assert.equal(s.mode, 'package', String(kind))
      assert.equal(s.canInstall, false)
      assert.equal(s.available, true)
      assert.equal(s.package.kind, kind === 'deb' || kind === 'rpm' ? kind : 'other', String(kind))
      if (kind === 'deb') assert.match(s.command, /^sudo apt update\n/)
      else if (kind === 'rpm') assert.match(s.command, /^sudo dnf upgrade/)
      else {
        assert.equal(s.command, null)
        assert.match(s.reason, new RegExp(`installed in ${root.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}, which you cannot write to`))
        assert.match(s.reason, /releases page/)
        assert.doesNotMatch(s.reason, /git pull|pnpm/, 'never the clone advice')
      }
      assert.equal((await j('POST', '/api/updates/install')).status, 409)
      assert.deepEqual(snapshot(root), before)
      assert.deepEqual(stops, [])
    } finally {
      spawnSync('chmod', ['-R', 'u+w', opt])
      fs.renameSync(opt, LIB)
    }
  }
})

test('a tree with no run loop is not a payload', async () => {
  await fresh()
  fs.rmSync(path.join(ROOT, 'studio-run.sh'))
  assert.equal(U.updateMode(), 'clone')
})

test.after(() => {
  server.close()
  spawnSync('chmod', ['-R', 'u+w', tmp])
  fs.rmSync(tmp, { recursive: true, force: true })
})
