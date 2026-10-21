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
    assert.ok(await waitFor(() => fs.existsSync(path.join(out, 'args.3'))), 'three starts')
    const first = envOf(path.join(out, 'env.1'))
    assert.equal(first.HOST, '127.0.0.1')
    assert.equal(first.PORT, '4180')
    assert.equal(first.STUDIO_RESTARTABLE, '1')
    assert.equal(first.STUDIO_CONFIG_FILE, path.join(data, 'studio.env'))
    assert.equal(first.INDEX_BASE, path.join(data, 'index'))
    assert.equal(first.PYTHON_BIN, path.join(res, 'python', 'bin', 'python3'))
    assert.equal(first.STUDIO_GUFI_PREFIX, path.join(res, 'gufi'))
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
