// The macOS app's launcher and run loop (deploy/mac), driven on any host
// with stand-ins for node, launchctl, curl, open and osascript: what the
// LaunchAgent runs, how the run loop restarts the server and passes its
// settings, and what opening the app does.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawn, spawnSync } from 'node:child_process'

const REPO = path.resolve(import.meta.dirname, '..', '..')
const MAC = path.join(REPO, 'deploy', 'mac')
// Resolved, since the scripts name their own directory with pwd -P (on
// macOS the temporary directory is reached through a /var symlink).
const tmp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'mac-app-')))
const write = (file, body, mode = 0o644) => {
  fs.mkdirSync(path.dirname(file), { recursive: true })
  fs.writeFileSync(file, body, { mode })
}
const waitFor = async (fn, ms = 15_000) => {
  const end = Date.now() + ms
  while (Date.now() < end) { if (fn()) return true; await new Promise(r => setTimeout(r, 100)) }
  return false
}
const envOf = file => Object.fromEntries(fs.readFileSync(file, 'utf8').split('\n').filter(Boolean).map(l => [l.slice(0, l.indexOf('=')), l.slice(l.indexOf('=') + 1)]))

/** An app bundle with the real scripts and a stand-in for node. */
function bundle(root, node) {
  const res = path.join(root, 'ACTIVATE Studio.app', 'Contents', 'Resources')
  write(path.join(res, 'studio-run.sh'), fs.readFileSync(path.join(MAC, 'studio-run.sh')), 0o755)
  write(path.join(res, 'node', 'bin', 'node'), node, 0o755)
  write(path.join(res, 'models', 'minilm384.gguf'), 'model')
  write(path.join(res, 'models', 'minilm-l6', 'model.onnx'), 'model')
  write(path.join(res, 'app', 'build-info.json'), '{\n "version": "v1",\n "builtAt": "B1"\n}\n')
  const launcher = path.join(root, 'ACTIVATE Studio.app', 'Contents', 'MacOS', 'ACTIVATE Studio')
  write(launcher, fs.readFileSync(path.join(MAC, 'launcher.sh')), 0o755)
  return { res, launcher }
}

test('the run loop passes settings, restarts on 75 at once and after a pause otherwise, and stops on TERM', async () => {
  const root = path.join(tmp, 'loop')
  const out = path.join(root, 'out')
  fs.mkdirSync(out, { recursive: true })
  const chosen = path.join(root, 'My Files')
  // Run 1 saves a folder the way the setup wizard does and asks for a
  // restart; run 2 fails; run 3 serves until it is stopped.
  const { res } = bundle(root, `#!/bin/bash
n=$(( $(cat "$OUT/n" 2>/dev/null || echo 0) + 1 )); echo $n >"$OUT/n"
env | sort >"$OUT/env.$n"; printf '%s\\n' "$@" >"$OUT/args.$n"; date +%s >"$OUT/at.$n"
case $n in
  1) printf "KB_ROOT='%s'\\nPORT='4999'\\n" "$CHOSEN" >"$STUDIO_CONFIG_FILE"; exit 75 ;;
  2) exit 3 ;;
  *) trap 'touch "$OUT/term"; exit 0' TERM; while :; do sleep 0.1; done ;;
esac
`)
  const home = path.join(root, 'home')
  const data = path.join(home, 'Library', 'Application Support', 'ACTIVATE Studio')
  fs.mkdirSync(home, { recursive: true })
  const loop = spawn('bash', [path.join(res, 'studio-run.sh'), data], {
    env: { HOME: home, PATH: '/usr/bin:/bin', OUT: out, CHOSEN: chosen }, stdio: 'ignore',
  })
  try {
    // at.N is the stand-in's last write for a start, done once it has text.
    const written = f => fs.existsSync(f) && fs.readFileSync(f, 'utf8').trim() !== ''
    assert.ok(await waitFor(() => written(path.join(out, 'at.3'))), 'three starts')
    const first = envOf(path.join(out, 'env.1'))
    assert.equal(first.HOST, '127.0.0.1')
    assert.equal(first.PORT, '4180')
    assert.equal(first.STUDIO_RESTARTABLE, '1')
    assert.equal(first.STUDIO_CONFIG_FILE, path.join(data, 'studio.env'))
    assert.equal(first.INDEX_BASE, path.join(data, 'index'))
    assert.equal(first.PYTHON_BIN, path.join(res, 'python', 'bin', 'python3'))
    assert.equal(first.STUDIO_GUFI_PREFIX, path.join(res, 'gufi'))
    // No bytecode caches written into the signed app.
    assert.equal(first.PYTHONDONTWRITEBYTECODE, '1')
    assert.ok(first.PATH.startsWith(path.join(res, 'node', 'bin') + ':'))
    // The default folder is created for the server to seed.
    assert.equal(first.KB_ROOT, path.join(home, 'Documents', 'ACTIVATE Studio'))
    assert.ok(fs.statSync(first.KB_ROOT).isDirectory())
    assert.equal(first.KB_STARTER_DIRS, undefined)
    assert.equal(fs.readFileSync(path.join(out, 'args.1'), 'utf8').trim(), path.join(res, 'app', 'server', 'dist', 'main.js'))
    // The restart reads the saved settings; a chosen folder gets no starters.
    const second = envOf(path.join(out, 'env.2'))
    assert.equal(second.KB_ROOT, chosen)
    assert.equal(second.PORT, '4999')
    assert.equal(second.KB_STARTER_DIRS, '')
    assert.equal(second.HOST, '127.0.0.1')
    const at = n => Number(fs.readFileSync(path.join(out, `at.${n}`), 'utf8'))
    assert.ok(at(2) - at(1) <= 1, 'restart on 75 is immediate')
    assert.ok(at(3) - at(2) >= 1, 'another exit waits first')
    assert.equal(fs.readlinkSync(path.join(data, 'index', 'models', 'minilm384.gguf')), path.join(res, 'models', 'minilm384.gguf'))
    assert.equal(fs.readlinkSync(path.join(data, 'index', 'models', 'minilm-l6')), path.join(res, 'models', 'minilm-l6'), 'and the ONNX model folder')
    assert.match(fs.readFileSync(path.join(data, 'logs', 'studio.log'), 'utf8'), /stopped with status 3/)
    loop.kill('SIGTERM')
    assert.ok(await waitFor(() => loop.exitCode !== null), 'the loop exits on TERM')
    assert.equal(loop.exitCode, 0)
    assert.ok(fs.existsSync(path.join(out, 'term')), 'the server got the TERM')
    assert.ok(!fs.existsSync(path.join(out, 'args.4')), 'and was not started again')
  } finally {
    loop.kill('SIGKILL')
  }
})

test('opening the app installs and loads the agent, waits for the server, and opens an app window', () => {
  const root = path.join(tmp, 'open')
  const { res, launcher } = bundle(root, '#!/bin/sh\n')
  const home = path.join(root, 'home')
  const bin = path.join(root, 'bin')
  const state = path.join(root, 'state')
  fs.mkdirSync(state, { recursive: true })
  // Stand-ins that record their calls; the agent counts as running once
  // bootstrapped, and the server answers while it runs.
  const log = (name, extra = '') => write(path.join(bin, name), `#!/bin/bash\necho "${name} $*" >>"${state}/calls"\n${extra}`, 0o755)
  log('launchctl', `case "$1" in
  print) [ -f "${state}/loaded" ] ;;
  bootstrap) touch "${state}/loaded" ;;
  bootout) rm -f "${state}/loaded" ;;
esac`)
  log('curl', `[ -f "${state}/loaded" ] || exit 7; echo '{"version":"v1","builtAt":"'"$(cat "${state}/built" 2>/dev/null || echo B1)"'"}'`)
  log('open')
  log('osascript')
  write(path.join(home, 'Applications', 'Google Chrome.app', 'Contents', 'Info.plist'), '')
  const env = { HOME: home, PATH: `${bin}:/usr/bin:/bin` }
  const run = (...args) => spawnSync(launcher, args, { env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
  const calls = () => fs.readFileSync(path.join(state, 'calls'), 'utf8')
  const reset = () => fs.writeFileSync(path.join(state, 'calls'), '')
  const uid = String(process.getuid())
  const label = 'com.parallelworks.activate-studio'
  const agent = path.join(home, 'Library', 'LaunchAgents', `${label}.plist`)
  const data = path.join(home, 'Library', 'Application Support', 'ACTIVATE Studio')

  let r = run()
  assert.equal(r.status, 0, r.stderr)
  const plist = fs.readFileSync(agent, 'utf8')
  assert.match(plist, new RegExp(`<string>/bin/bash</string>\\s*<string>${res.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}/studio-run.sh</string>\\s*<string>${data}</string>`))
  assert.match(plist, /<key>KeepAlive<\/key>\s*<true\/>/)
  assert.match(plist, new RegExp(`<key>AssociatedBundleIdentifiers</key>\\s*<array>\\s*<string>${label}</string>`))
  assert.ok(fs.existsSync(path.join(data, 'logs')) && fs.existsSync(path.join(data, 'index')))
  assert.equal(fs.readFileSync(path.join(data, 'studio.env'), 'utf8'), "PORT='4180'\n")
  assert.match(calls(), new RegExp(`launchctl enable gui/${uid}/${label}\\nlaunchctl print gui/${uid}/${label}\\nlaunchctl bootstrap gui/${uid} ${agent}`))
  assert.match(calls(), /open -na .*\/Google Chrome\.app --args --app=http:\/\/127\.0\.0\.1:4180\/\n/)
  assert.doesNotMatch(calls(), /kickstart/)

  // Opened again: the agent is running and unchanged, so it is left alone.
  reset()
  r = run()
  assert.equal(r.status, 0, r.stderr)
  assert.doesNotMatch(calls(), /bootstrap|bootout/)
  assert.match(calls(), /--app=http:\/\/127\.0\.0\.1:4180\//)

  // A server from an earlier copy of the app is restarted once.
  reset()
  fs.writeFileSync(path.join(state, 'built'), 'B0')
  r = run()
  assert.equal(r.status, 0, r.stderr)
  assert.match(calls(), new RegExp(`launchctl kickstart -k gui/${uid}/${label}`))
  fs.rmSync(path.join(state, 'built'))

  // The port comes from the settings file.
  reset()
  fs.writeFileSync(path.join(data, 'studio.env'), "PORT='4321'\n")
  r = run()
  assert.equal(r.status, 0, r.stderr)
  assert.match(calls(), /--app=http:\/\/127\.0\.0\.1:4321\//)

  // --stop unloads the agent and keeps it from starting at login.
  reset()
  r = run('--stop')
  assert.equal(r.status, 0, r.stderr)
  assert.match(calls(), new RegExp(`launchctl bootout gui/${uid}/${label}\\nlaunchctl disable gui/${uid}/${label}`))

  // A moved app rewrites the agent to point at the new copy and loads it.
  reset()
  fs.writeFileSync(path.join(state, 'loaded'), '')
  const moved = path.join(tmp, 'moved')
  fs.cpSync(root, moved, { recursive: true, verbatimSymlinks: true })
  r = spawnSync(path.join(moved, 'ACTIVATE Studio.app', 'Contents', 'MacOS', 'ACTIVATE Studio'), [], { env, encoding: 'utf8' })
  assert.equal(r.status, 0, r.stderr)
  assert.match(calls(), /launchctl bootout[\s\S]*launchctl bootstrap/)
  assert.match(fs.readFileSync(agent, 'utf8'), new RegExp(`${moved.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}/ACTIVATE Studio\\.app/Contents/Resources/studio-run\\.sh`))

  // --uninstall removes the agent and the app, and keeps the data.
  reset()
  r = spawnSync(path.join(moved, 'ACTIVATE Studio.app', 'Contents', 'MacOS', 'ACTIVATE Studio'), ['--uninstall'], { env, encoding: 'utf8' })
  assert.equal(r.status, 0, r.stderr)
  assert.ok(!fs.existsSync(agent))
  assert.ok(fs.existsSync(path.join(data, 'studio.env')))
  // The moved copy has no Info.plist naming the bundle, so it is not deleted.
  assert.ok(fs.existsSync(path.join(moved, 'ACTIVATE Studio.app')))
})

test('opening the app restarts a running server that does not answer on the port studio.env names', () => {
  const root = path.join(tmp, 'port')
  const { launcher } = bundle(root, '#!/bin/sh\n')
  const home = path.join(root, 'home')
  const bin = path.join(root, 'bin')
  const state = path.join(root, 'state')
  const data = path.join(home, 'Library', 'Application Support', 'ACTIVATE Studio')
  fs.mkdirSync(state, { recursive: true })
  // The server serves the port studio.env named when it last started; a
  // kickstart starts it again on the one named now.
  const serve = `( . "${data}/studio.env"; echo "\${PORT:-4180}" >"${state}/serving" )`
  const log = (name, extra = '') => write(path.join(bin, name), `#!/bin/bash\necho "${name} $*" >>"${state}/calls"\n${extra}`, 0o755)
  log('launchctl', `case "$1" in
  print) [ -f "${state}/loaded" ] ;;
  bootstrap) touch "${state}/loaded"; ${serve} ;;
  kickstart) ${serve} ;;
esac`)
  log('curl', `port=$(printf '%s' "$*" | sed -n 's/.*127\\.0\\.0\\.1:\\([0-9]*\\).*/\\1/p')
[ -f "${state}/loaded" ] && [ "$port" = "$(cat "${state}/serving")" ] || exit 7
echo '{"version":"v1","builtAt":"B1"}'`)
  log('open')
  log('osascript')
  write(path.join(home, 'Applications', 'Google Chrome.app', 'Contents', 'Info.plist'), '')
  const env = { HOME: home, PATH: `${bin}:/usr/bin:/bin` }
  const run = () => spawnSync(launcher, [], { env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
  const calls = () => fs.readFileSync(path.join(state, 'calls'), 'utf8')

  let r = run()
  assert.equal(r.status, 0, r.stderr)
  assert.equal(fs.readFileSync(path.join(state, 'serving'), 'utf8').trim(), '4180')
  assert.doesNotMatch(calls(), /kickstart/)

  // studio.env names another port while the server runs on the old one.
  fs.writeFileSync(path.join(state, 'calls'), '')
  fs.writeFileSync(path.join(data, 'studio.env'), "PORT='4555'\n")
  r = run()
  assert.equal(r.status, 0, r.stderr)
  assert.match(calls(), /launchctl kickstart -k gui\/\d+\/com\.parallelworks\.activate-studio/)
  assert.match(calls(), /--app=http:\/\/127\.0\.0\.1:4555\//)
  assert.equal(fs.readFileSync(path.join(state, 'serving'), 'utf8').trim(), '4555')
})

test('install.sh verifies the archive and puts the app in place, replacing an older one', () => {
  const root = path.join(tmp, 'install')
  const src = path.join(root, 'src')
  write(path.join(src, 'ACTIVATE Studio.app', 'Contents', 'MacOS', 'ACTIVATE Studio'), '#!/bin/sh\necho opened\n', 0o755)
  write(path.join(src, 'ACTIVATE Studio.app', 'Contents', 'Resources', 'marker'), 'new')
  const archive = path.join(root, 'activate-studio-macos-arm64.tar.gz')
  assert.equal(spawnSync('tar', ['-czf', archive, '-C', src, 'ACTIVATE Studio.app']).status, 0)
  const sum = spawnSync('sh', ['-c', `sha256sum "${archive}" 2>/dev/null || shasum -a 256 "${archive}"`], { encoding: 'utf8' }).stdout.split(/\s+/)[0]
  const dest = path.join(root, 'Applications')
  write(path.join(dest, 'ACTIVATE Studio.app', 'Contents', 'Resources', 'marker'), 'old')
  // On a Mac the installer stops a running agent first and opens the app
  // with open; stand-ins take both calls, and open runs the app directly.
  write(path.join(root, 'bin', 'launchctl'), '#!/bin/sh\n', 0o755)
  write(path.join(root, 'bin', 'open'), '#!/bin/sh\nexec "$1/Contents/MacOS/ACTIVATE Studio"\n', 0o755)
  const env = { HOME: root, PATH: `${path.join(root, 'bin')}:${process.env.PATH}`, STUDIO_INSTALL_DIR: dest, STUDIO_INSTALL_ANY_HOST: '1' }
  const install = args => spawnSync('bash', [path.join(MAC, 'install.sh'), ...args], { env, encoding: 'utf8' })

  let r = install([archive])
  assert.notEqual(r.status, 0, 'no checksum beside the archive')
  assert.match(r.stderr, /no checksum/)
  fs.writeFileSync(`${archive}.sha256`, `${'0'.repeat(64)}  activate-studio-macos-arm64.tar.gz\n`)
  r = install([archive])
  assert.notEqual(r.status, 0)
  assert.match(r.stderr, /checksum mismatch/)
  assert.equal(fs.readFileSync(path.join(dest, 'ACTIVATE Studio.app', 'Contents', 'Resources', 'marker'), 'utf8'), 'old')

  fs.writeFileSync(`${archive}.sha256`, `${sum}  activate-studio-macos-arm64.tar.gz\n`)
  r = install([archive])
  assert.equal(r.status, 0, r.stderr)
  assert.equal(fs.readFileSync(path.join(dest, 'ACTIVATE Studio.app', 'Contents', 'Resources', 'marker'), 'utf8'), 'new')
  assert.match(r.stdout, /opened/, 'it opens the app once')
  assert.deepEqual(fs.readdirSync(dest), ['ACTIVATE Studio.app'])
})

test.after(() => fs.rmSync(tmp, { recursive: true, force: true }))

test('uninstall lists what it removes and keeps, then removes exactly that; the documents folder stays', () => {
  const root = path.join(tmp, 'uninstall')
  const { launcher } = bundle(root, '#!/bin/sh\n')
  const label = 'com.parallelworks.activate-studio'
  const app = path.join(root, 'ACTIVATE Studio.app')
  write(path.join(app, 'Contents', 'Info.plist'), `<plist><dict><key>CFBundleIdentifier</key><string>${label}</string></dict></plist>\n`)
  write(path.join(root, '.ACTIVATE Studio.app.previous', 'Contents', 'Info.plist'), '')
  const home = path.join(root, 'home')
  const bin = path.join(root, 'bin')
  write(path.join(bin, 'launchctl'), '#!/bin/sh\n', 0o755)
  const data = path.join(home, 'Library', 'Application Support', 'ACTIVATE Studio')
  const docs = path.join(home, 'Documents', 'Notes')
  write(path.join(docs, 'keep.md'), 'mine\n')
  // The index moved to a folder of its own, beside a file of the person's.
  const moved = path.join(home, 'External', 'Studio index')
  write(path.join(moved, 'gufi', 'Notes', 'db.db'), 'x')
  write(path.join(moved, 'extract', 'a.txt'), 'x')
  write(path.join(moved, 'embed-cache.db'), 'x')
  write(path.join(moved, 'inc-3-77', 'db.db'), 'x')
  write(path.join(moved, 'other.txt'), 'not the Studio\'s')
  write(path.join(data, 'studio.env'), `PORT='4180'\nKB_ROOT='${docs}'\nSTUDIO_INDEX_DIR='${moved}'\n`)
  // Built libraries: one with its index in a chosen folder (with the
  // manifest), one whose folder has no manifest, one in the data folder.
  const chosen = path.join(home, 'Indexes', 'papers')
  write(path.join(chosen, 'papers', 'db.db'), 'x')
  write(path.join(chosen, '.studio-index.json'), JSON.stringify({ version: 1, index: path.join(chosen, 'papers'), indexDir: chosen }))
  write(path.join(chosen, 'mine.txt'), 'the person\'s')
  const bare = path.join(home, 'Indexes', 'bare')
  write(path.join(bare, 'tree', 'db.db'), 'x')
  write(path.join(data, 'index', 'libraries.json'), JSON.stringify([
    { id: 'papers', indexRoot: path.join(chosen, 'papers'), build: { indexDir: chosen, managed: false } },
    { id: 'bare', indexRoot: path.join(bare, 'tree'), build: { indexDir: bare, managed: false } },
    { id: 'own', indexRoot: path.join(data, 'index', 'libraries', 'own', 'index', 'x'), build: { indexDir: path.join(data, 'index', 'libraries', 'own', 'index'), managed: true } },
    { id: 'site', indexRoot: '/data/site-index' },
  ]))
  write(path.join(data, 'index', 'libraries', 'own', 'index', '.studio-index.json'), '{}')
  const webkit = path.join(home, 'Library', 'WebKit', label)
  const prefs = path.join(home, 'Library', 'Preferences', `${label}.plist`)
  write(path.join(webkit, 'WebsiteData', 'x'), 'x')
  write(prefs, 'x')
  const agent = path.join(home, 'Library', 'LaunchAgents', `${label}.plist`)
  write(agent, 'x')

  const env = { HOME: home, PATH: `${bin}:/usr/bin:/bin` }
  const run = (...args) => spawnSync(launcher, ['--uninstall', ...args], { env, encoding: 'utf8' })
  const lines = out => out.trim().split('\n').map(l => l.split('\t'))

  // Without --purge: the server and the app, with the data and documents kept.
  let r = run('--dry-run')
  assert.equal(r.status, 0, r.stderr)
  assert.deepEqual(lines(r.stdout), [
    ['server', agent], ['app', app], ['update-copy', path.join(root, '.ACTIVATE Studio.app.previous')],
    ['keep-data', data], ['keep-documents', docs],
  ])
  // With --purge: the data, the moved index, the chosen library index (not
  // the one without a manifest, nor the one in the data folder), and the
  // window's data; never the documents.
  r = run('--dry-run', '--purge')
  assert.equal(r.status, 0, r.stderr)
  assert.deepEqual(lines(r.stdout), [
    ['server', agent], ['app', app], ['update-copy', path.join(root, '.ACTIVATE Studio.app.previous')],
    ['data', data], ['index', moved], ['library-index', chosen],
    ['window-data', webkit], ['window-data', prefs],
    ['keep-documents', docs],
  ])
  assert.ok(fs.existsSync(app) && fs.existsSync(data) && fs.existsSync(moved), 'a dry run removes nothing')

  r = run('--purge')
  assert.equal(r.status, 0, r.stderr)
  assert.match(r.stdout, /Your documents are untouched/)
  for (const gone of [agent, app, path.join(root, '.ACTIVATE Studio.app.previous'), data, webkit, prefs,
    path.join(moved, 'gufi'), path.join(moved, 'extract'), path.join(moved, 'embed-cache.db'), path.join(moved, 'inc-3-77'),
    path.join(chosen, 'papers'), path.join(chosen, '.studio-index.json')]) {
    assert.ok(!fs.existsSync(gone), `${gone} is removed`)
  }
  // What the Studio did not write stays, and so do the documents.
  assert.equal(fs.readFileSync(path.join(moved, 'other.txt'), 'utf8'), 'not the Studio\'s')
  assert.equal(fs.readFileSync(path.join(chosen, 'mine.txt'), 'utf8'), 'the person\'s')
  assert.ok(fs.existsSync(path.join(bare, 'tree', 'db.db')))
  assert.equal(fs.readFileSync(path.join(docs, 'keep.md'), 'utf8'), 'mine\n')
})

/** A Mac where no LaunchAgent can be used: the launcher with the real run
 *  loop, the app's Python (the host's, through a stand-in), and stand-ins for
 *  launchctl and curl. The server stand-in serves until TERM. */
function noAgentMac(name, { bootstrap = 'ok' } = {}) {
  const root = path.join(tmp, name)
  const state = path.join(root, 'state')
  fs.mkdirSync(state, { recursive: true })
  const { res, launcher } = bundle(root, `#!/bin/bash
echo "$STUDIO_SUPERVISOR" >"$STATE/supervisor"
n=$(( $(cat "$STATE/starts" 2>/dev/null || echo 0) + 1 )); echo $n >"$STATE/starts"
touch "$STATE/serving"
trap 'rm -f "$STATE/serving"; exit 0' TERM
while :; do sleep 0.1; done
`)
  write(path.join(res, 'python', 'bin', 'python3'), '#!/bin/sh\nexec python3 "$@"\n', 0o755)
  const home = path.join(root, 'home')
  const bin = path.join(root, 'bin')
  write(path.join(bin, 'launchctl'), `#!/bin/bash
echo "launchctl $*" >>"${state}/calls"
case "$1" in print) exit 1 ;; bootstrap) [ "${bootstrap}" = ok ] && touch "${state}/loaded"; [ "${bootstrap}" = ok ] ;; esac
`, 0o755)
  write(path.join(bin, 'curl'), `#!/bin/bash\n[ -f "${state}/serving" ] || exit 7\necho '{"version":"v1","builtAt":"B1"}'\n`, 0o755)
  write(path.join(bin, 'osascript'), '#!/bin/sh\n', 0o755)
  const data = path.join(home, 'Library', 'Application Support', 'ACTIVATE Studio')
  const env = { HOME: home, PATH: `${bin}:/usr/bin:/bin`, STATE: state, STUDIO_NO_BROWSER: '1' }
  // A timeout, so a server left holding the launcher's output (which the
  // app's window reads to its end) fails the test instead of hanging it.
  const run = (...args) => spawnSync(launcher, args, { env, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'], timeout: 60_000 })
  const pidOf = () => { try { return Number(fs.readFileSync(path.join(data, 'server.pid'), 'utf8').trim()) } catch { return null } }
  const alive = pid => { try { process.kill(pid, 0); return true } catch { return false } }
  const ps = (pid, field) => spawnSync('ps', ['-ww', '-o', `${field}=`, '-p', String(pid)], { encoding: 'utf8' }).stdout.trim()
  return { root, res, state, home, data, run, pidOf, alive, ps, calls: () => { try { return fs.readFileSync(path.join(state, 'calls'), 'utf8') } catch { return '' } } }
}

const asRoot = process.getuid?.() === 0

test('with ~/Library/LaunchAgents read-only, the app starts the server itself, keeps it, reuses it, stops and lists it', { skip: asRoot && 'root can write a read-only folder' }, async () => {
  const m = noAgentMac('no-agent')
  const agents = path.join(m.home, 'Library', 'LaunchAgents')
  fs.mkdirSync(agents, { recursive: true })
  fs.chmodSync(agents, 0o555)
  let pid = null
  try {
    let r = m.run()
    assert.equal(r.status, 0, r.stderr + r.stdout)
    assert.match(r.stdout, /ACTIVATE Studio is running at http:\/\/127\.0\.0\.1:4180\//)
    assert.ok(!fs.existsSync(path.join(agents, 'com.parallelworks.activate-studio.plist')))
    assert.doesNotMatch(m.calls(), /bootstrap/, 'no agent was written, so none is loaded')
    pid = m.pidOf()
    assert.ok(pid && m.alive(pid), 'the run loop outlives the launcher')
    assert.match(m.ps(pid, 'command'), new RegExp(`studio-run\\.sh ${m.data.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`))
    assert.equal(m.ps(pid, 'pgid'), String(pid), 'in a process group of its own')
    assert.equal(fs.readFileSync(path.join(m.state, 'supervisor'), 'utf8').trim(), 'detached')
    assert.match(fs.readFileSync(path.join(m.data, 'logs', 'agent.log'), 'utf8'), /no LaunchAgent \(.*LaunchAgents is not writable\)/)

    // Opened again: the same server is used.
    r = m.run()
    assert.equal(r.status, 0, r.stderr)
    assert.equal(m.pidOf(), pid)
    assert.equal(fs.readFileSync(path.join(m.state, 'starts'), 'utf8').trim(), '1')

    // The uninstall plan names it, and no agent.
    r = m.run('--uninstall', '--dry-run')
    assert.equal(r.status, 0, r.stderr)
    assert.match(r.stdout, new RegExp(`^server\\t${path.join(m.data, 'server.pid').replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`, 'm'))
    assert.doesNotMatch(r.stdout, /LaunchAgents/)

    // --stop stops the run loop and the server.
    r = m.run('--stop')
    assert.equal(r.status, 0, r.stderr)
    assert.ok(await waitFor(() => !m.alive(pid)), 'the run loop stops')
    assert.ok(!fs.existsSync(path.join(m.state, 'serving')), 'the server got the TERM')
    assert.ok(!fs.existsSync(path.join(m.data, 'server.pid')))

    // A stale PID file naming another process is not trusted.
    fs.writeFileSync(path.join(m.data, 'server.pid'), `${process.pid}\n`)
    r = m.run()
    assert.equal(r.status, 0, r.stderr)
    assert.ok(m.alive(process.pid), 'the process the stale file named is untouched')
    pid = m.pidOf()
    assert.notEqual(pid, process.pid)
    assert.equal(fs.readFileSync(path.join(m.state, 'starts'), 'utf8').trim(), '2')
  } finally {
    m.run('--stop')
    if (pid) try { process.kill(-pid, 'SIGKILL') } catch { /* gone */ }
    fs.chmodSync(agents, 0o755)
  }
})

test('when launchctl refuses the agent, the app starts the server itself', async () => {
  const m = noAgentMac('refused', { bootstrap: 'refused' })
  let pid = null
  try {
    const r = m.run()
    assert.equal(r.status, 0, r.stderr + r.stdout)
    assert.match(m.calls(), /launchctl bootstrap/)
    pid = m.pidOf()
    assert.ok(pid && m.alive(pid))
    assert.match(fs.readFileSync(path.join(m.data, 'logs', 'agent.log'), 'utf8'), /no LaunchAgent \(launchctl bootstrap did not load/)
    // Its uninstall plan lists both: the agent file written, and the server.
    const plan = m.run('--uninstall', '--dry-run').stdout
    assert.match(plan, /^server\t.*LaunchAgents\/com\.parallelworks\.activate-studio\.plist$/m)
    assert.match(plan, /^server\t.*server\.pid$/m)
  } finally {
    m.run('--stop')
    if (pid) try { process.kill(-pid, 'SIGKILL') } catch { /* gone */ }
  }
})

test('exit 76 hands the process over to the run loop of the copy an update put in place', async () => {
  const root = path.join(tmp, 'relaunch')
  const state = path.join(root, 'state')
  fs.mkdirSync(state, { recursive: true })
  const app = path.join(root, 'ACTIVATE Studio.app')
  // The new copy, staged beside: its run loop says it started, then runs
  // the real one; its server serves until TERM.
  const staged = path.join(root, 'staged')
  const next = bundle(staged, `#!/bin/bash
echo "new server $PPID" >>"$STATE/log"
trap 'exit 0' TERM
while :; do sleep 0.1; done
`)
  fs.writeFileSync(path.join(next.res, 'studio-run.sh'), fs.readFileSync(path.join(MAC, 'studio-run.sh'), 'utf8')
    .replace('#!/bin/bash\n', () => '#!/bin/bash\necho "new run loop $$" >>"$STATE/log"\n'))
  // The running copy's server swaps the new copy in, as the updater does,
  // and asks its run loop to hand over.
  const { res } = bundle(root, `#!/bin/bash
echo "old server $PPID" >>"$STATE/log"
mv "${app}" "${path.join(root, '.ACTIVATE Studio.app.previous')}"
mv "${path.join(staged, 'ACTIVATE Studio.app')}" "${app}"
exit 76
`)
  const home = path.join(root, 'home')
  const data = path.join(home, 'Library', 'Application Support', 'ACTIVATE Studio')
  const loop = spawn('bash', [path.join(res, 'studio-run.sh'), data], {
    env: { HOME: home, PATH: '/usr/bin:/bin', STATE: state, STUDIO_APP_BUNDLE: app, STUDIO_SUPERVISOR: 'detached' }, stdio: 'ignore',
  })
  try {
    const lines = () => { try { return fs.readFileSync(path.join(state, 'log'), 'utf8').trim().split('\n') } catch { return [] } }
    assert.ok(await waitFor(() => lines().length >= 3), lines().join('\n'))
    const p = String(loop.pid)
    assert.deepEqual(lines(), [`old server ${p}`, `new run loop ${p}`, `new server ${p}`], 'one process throughout')
    assert.match(fs.readFileSync(path.join(data, 'logs', 'studio.log'), 'utf8'), /the app was updated; its own run loop takes over/)
    loop.kill('SIGTERM')
    assert.ok(await waitFor(() => loop.exitCode !== null), 'the new loop stops on TERM')
  } finally {
    loop.kill('SIGKILL')
  }
})

/** A copy of the app not installed yet (as macOS runs one opened from its
 *  disk image or where it was downloaded: a path with /AppTranslocation/ in
 *  it), a home, and stand-ins that record their calls; osascript answers
 *  the install and replace dialogs with $INSTALL and $REPLACE. */
function notInstalled(name) {
  const root = path.join(tmp, name)
  const state = path.join(root, 'state')
  fs.mkdirSync(state, { recursive: true })
  const src = bundle(path.join(root, 'private', 'var', 'AppTranslocation', 'X1', 'd'), '#!/bin/sh\n')
  const home = path.join(root, 'home')
  const bin = path.join(root, 'bin')
  const log = (n, extra = '') => write(path.join(bin, n), `#!/bin/bash\necho "${n} $*" >>"${state}/calls"\n${extra}`, 0o755)
  log('launchctl', 'case "$1" in print) exit 1 ;; esac')
  log('curl', 'exit 7')
  log('open')
  log('osascript', `case "$*" in
  *'"Replace"'*) a="\${REPLACE:-Replace}" ;;
  *'"Install"'*) a="\${INSTALL:-Install}" ;;
  *) exit 0 ;;
esac
[ "$a" = Cancel ] && exit 1
echo "button returned:$a"`)
  const dest = path.join(home, 'Applications', 'ACTIVATE Studio.app')
  const env = { HOME: home, PATH: `${bin}:/usr/bin:/bin` }
  const run = (args = [], extra = {}) => spawnSync(src.launcher, args, { env: { ...env, ...extra }, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] })
  const calls = () => { try { return fs.readFileSync(path.join(state, 'calls'), 'utf8') } catch { return '' } }
  const reset = () => fs.rmSync(path.join(state, 'calls'), { force: true })
  return { root, src, home, dest, run, calls, reset }
}

const staged = dir => fs.readdirSync(dir).filter(f => f.startsWith('.ACTIVATE Studio.app.'))

test('--install puts the app in ~/Applications, replaces a copy there whole, and refuses a folder the person cannot write', () => {
  const m = notInstalled('install')
  let r = m.run(['--install'])
  assert.equal(r.status, 0, r.stderr)
  assert.match(r.stdout, /Installed ACTIVATE Studio in .*\/home\/Applications\/ACTIVATE Studio\.app\./)
  assert.equal(fs.readFileSync(path.join(m.dest, 'Contents', 'MacOS', 'ACTIVATE Studio'), 'utf8'), fs.readFileSync(path.join(MAC, 'launcher.sh'), 'utf8'))
  assert.ok(fs.statSync(path.join(m.dest, 'Contents', 'Resources', 'studio-run.sh')).mode & 0o111, 'modes are kept')

  // Again, over the copy there: replaced whole, with nothing staged left.
  write(path.join(m.dest, 'Contents', 'old-copy'), 'x')
  r = m.run(['--install'])
  assert.equal(r.status, 0, r.stderr)
  assert.ok(!fs.existsSync(path.join(m.dest, 'Contents', 'old-copy')), 'the old copy is gone')
  assert.ok(fs.existsSync(path.join(m.dest, 'Contents', 'Resources', 'studio-run.sh')))
  assert.deepEqual(staged(path.join(m.home, 'Applications')), [])

  // Into another folder, named; and the installed copy installing itself
  // where it is changes nothing.
  r = m.run(['--install', path.join(m.root, 'Elsewhere')])
  assert.equal(r.status, 0, r.stderr)
  assert.ok(fs.existsSync(path.join(m.root, 'Elsewhere', 'ACTIVATE Studio.app', 'Contents', 'MacOS', 'ACTIVATE Studio')))
  r = spawnSync(path.join(m.dest, 'Contents', 'MacOS', 'ACTIVATE Studio'), ['--install'], { env: { HOME: m.home, PATH: `${path.join(m.root, 'bin')}:/usr/bin:/bin` }, encoding: 'utf8' })
  assert.equal(r.status, 0, r.stderr)
  assert.match(r.stdout, /is the one in .* already/)
  assert.ok(fs.existsSync(m.dest))

  // A folder not writable for the person: refused, in plain words.
  if (!asRoot) {
    const locked = path.join(m.root, 'Locked')
    fs.mkdirSync(locked)
    fs.chmodSync(locked, 0o555)
    try {
      r = m.run(['--install', locked])
      assert.notEqual(r.status, 0)
      assert.match(r.stderr, /Locked is not writable for you\. The Applications folder in your home folder \(~\/Applications\) is\./)
      assert.deepEqual(fs.readdirSync(locked), [])
    } finally {
      fs.chmodSync(locked, 0o755)
    }
  }
  // A copy that fails part way (here a file it cannot read): said in plain
  // words, with nothing left in the folder.
  if (!asRoot) {
    const unreadable = path.join(m.src.res, 'secret')
    write(unreadable, 'x', 0o000)
    try {
      const into = path.join(m.root, 'Partial')
      r = m.run(['--install', into])
      assert.notEqual(r.status, 0)
      assert.match(r.stderr, /^Copying ACTIVATE Studio to .*\/Partial failed: .+\.$/m)
      assert.deepEqual(fs.readdirSync(into), [])
    } finally {
      fs.rmSync(unreadable, { force: true })
    }
  }
  // The uninstall plan of an installed copy lists an install's staging
  // copies left behind (by a stop part way).
  write(path.join(m.home, 'Applications', '.ACTIVATE Studio.app.install', 'x'), 'x')
  write(path.join(m.dest, 'Contents', 'Info.plist'), '<plist><dict><key>CFBundleIdentifier</key><string>com.parallelworks.activate-studio</string></dict></plist>\n')
  r = spawnSync(path.join(m.dest, 'Contents', 'MacOS', 'ACTIVATE Studio'), ['--uninstall', '--dry-run'], { env: { HOME: m.home, PATH: `${path.join(m.root, 'bin')}:/usr/bin:/bin` }, encoding: 'utf8' })
  assert.match(r.stdout, /^update-copy\t.*\/\.ACTIVATE Studio\.app\.install$/m)
})

test('opened before it is installed: a terminal is told how to install it, and the dialog installs it in ~/Applications and opens that copy', () => {
  const m = notInstalled('offer')
  // In a terminal (or the app's window, which asks itself): told, nothing started.
  let r = m.run([], { STUDIO_NO_BROWSER: '1' })
  assert.equal(r.status, 1)
  assert.match(r.stderr, /not installed yet/)
  assert.match(r.stderr, /"[^"]*\/AppTranslocation\/X1\/d\/ACTIVATE Studio\.app\/Contents\/MacOS\/ACTIVATE Studio" --install/)
  assert.doesNotMatch(m.calls(), /launchctl|osascript/)
  assert.ok(!fs.existsSync(m.dest))

  // Opened from Finder without the window: Cancel installs nothing.
  r = m.run([], { INSTALL: 'Cancel' })
  assert.equal(r.status, 0, r.stderr)
  assert.ok(!fs.existsSync(m.dest))
  assert.doesNotMatch(m.calls(), /launchctl (bootstrap|enable)/, 'the copy not installed starts no server')

  // Install: copied to ~/Applications, and that copy opened as a new instance.
  m.reset()
  r = m.run()
  assert.equal(r.status, 0, r.stderr)
  assert.ok(fs.existsSync(path.join(m.dest, 'Contents', 'MacOS', 'ACTIVATE Studio')))
  assert.match(m.calls(), new RegExp(`open -n ${m.dest.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}\n`))
  assert.doesNotMatch(m.calls(), /launchctl (bootstrap|enable)/)

  // A copy already there: asked first; Cancel keeps it, Replace replaces it.
  write(path.join(m.dest, 'Contents', 'old-copy'), 'x')
  m.reset()
  r = m.run([], { REPLACE: 'Cancel' })
  assert.equal(r.status, 0, r.stderr)
  assert.match(m.calls(), /"Replace"/)
  assert.ok(fs.existsSync(path.join(m.dest, 'Contents', 'old-copy')), 'kept')
  assert.doesNotMatch(m.calls(), /open -n/)
  m.reset()
  r = m.run()
  assert.equal(r.status, 0, r.stderr)
  assert.ok(!fs.existsSync(path.join(m.dest, 'Contents', 'old-copy')), 'replaced')
  assert.match(m.calls(), /open -n /)
})

test('installing over the copy the server runs from stops that server first', async () => {
  const m = noAgentMac('install-over-running', { bootstrap: 'refused' })
  let pid = null
  try {
    let r = m.run()
    assert.equal(r.status, 0, r.stderr + r.stdout)
    pid = m.pidOf()
    assert.ok(pid && m.alive(pid))
    // A newer copy, not installed yet, installed where the running one is.
    const src = bundle(path.join(m.root, 'AppTranslocation', 'X2', 'd'), '#!/bin/bash\necho new\n')
    r = spawnSync(src.launcher, ['--install', m.root], { env: { HOME: m.home, PATH: `${path.join(m.root, 'bin')}:/usr/bin:/bin`, STATE: m.state }, encoding: 'utf8', timeout: 60_000 })
    assert.equal(r.status, 0, r.stderr)
    assert.ok(await waitFor(() => !m.alive(pid)), 'its run loop stopped')
    assert.ok(!fs.existsSync(path.join(m.state, 'serving')), 'and the server got the TERM')
    assert.ok(!fs.existsSync(path.join(m.data, 'server.pid')))
    assert.equal(fs.readFileSync(path.join(m.res, 'node', 'bin', 'node'), 'utf8'), '#!/bin/bash\necho new\n', 'the new copy is in place')
    assert.deepEqual(staged(m.root), [])
  } finally {
    m.run('--stop')
    if (pid) try { process.kill(-pid, 'SIGKILL') } catch { /* gone */ }
  }
})
