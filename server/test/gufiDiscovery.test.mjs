// Where the Studio finds GUFI, and the environment its tools run with:
// GUFI_BIN, then a copy bundled with the macOS app, then installs near the
// app, PATH, and the usual prefixes; GUFI's lib directory on the loader
// path for its tools (DYLD_LIBRARY_PATH on macOS), never for Python.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const C = await import('../dist/config.js')
const tmp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'gufi-find-')))

/** A GUFI install under tmp: <name>/bin/gufi_query and <name>/lib. */
function install(name, { lib = true } = {}) {
  const prefix = path.join(tmp, name)
  fs.mkdirSync(path.join(prefix, 'bin'), { recursive: true })
  fs.writeFileSync(path.join(prefix, 'bin', 'gufi_query'), '#!/bin/sh\n', { mode: 0o755 })
  if (lib) fs.mkdirSync(path.join(prefix, 'lib'), { recursive: true })
  return prefix
}
const empty = (name) => { const d = path.join(tmp, name); fs.mkdirSync(d, { recursive: true }); return d }

const bundled = install('app/Contents/Resources/gufi')
const project = empty('project')
const nearby = install('project/gufi-install')
const usrLocal = install('usr-local')
const homeLocal = install('home/usr/local')
const none = { projectRoot: empty('bare'), cwd: empty('cwd'), onPath: () => null, prefixes: [] }

test('GUFI_BIN wins, whether or not it exists', () => {
  assert.equal(C.findGufiBin({ GUFI_BIN: '/nowhere/bin', STUDIO_GUFI_PREFIX: bundled }, none), '/nowhere/bin')
})

test('the copy bundled with the app comes next, ahead of PATH and the prefixes', () => {
  const search = { projectRoot: project, cwd: project, onPath: () => '/on/path', prefixes: [path.join(usrLocal, 'bin')] }
  assert.equal(C.findGufiBin({ STUDIO_GUFI_PREFIX: bundled }, search), path.join(bundled, 'bin'))
  // A bundled prefix without gufi_query is passed over.
  assert.equal(C.findGufiBin({ STUDIO_GUFI_PREFIX: empty('hollow') }, search), path.join(nearby, 'bin'))
})

test('an install near the app, then PATH, then the prefixes in order', () => {
  assert.equal(C.findGufiBin({}, { ...none, projectRoot: project }), path.join(nearby, 'bin'))
  assert.equal(C.findGufiBin({}, { ...none, onPath: () => '/on/path', prefixes: [path.join(usrLocal, 'bin')] }), '/on/path')
  const prefixes = [path.join(empty('opt-gufi'), 'bin'), path.join(usrLocal, 'bin'), path.join(homeLocal, 'bin')]
  assert.equal(C.findGufiBin({}, { ...none, prefixes }), path.join(usrLocal, 'bin'))
  assert.equal(C.findGufiBin({}, { ...none, prefixes: prefixes.slice(2) }), path.join(homeLocal, 'bin'))
})

test('with nothing found it falls back to /opt/gufi/bin', () => {
  assert.equal(C.findGufiBin({}, none), '/opt/gufi/bin')
})

test('the usual prefixes are the container, then GUFI\'s macOS package for all users and for one', () => {
  assert.deepEqual(C.GUFI_PREFIXES, ['/opt/gufi/bin', '/usr/local/bin', path.join(os.homedir(), 'usr', 'local', 'bin')])
})

test('lib directory and gufi_vt module follow the prefix and the platform', () => {
  assert.equal(C.gufiLibDir('/usr/local/bin'), '/usr/local/lib')
  assert.equal(C.gufiVtModule('/usr/local/lib', 'darwin'), '/usr/local/lib/gufi_vt.dylib')
  assert.equal(C.gufiVtModule('/opt/gufi/lib', 'linux'), '/opt/gufi/lib/gufi_vt.so')
  assert.equal(C.libraryPathVar('darwin'), 'DYLD_LIBRARY_PATH')
  assert.equal(C.libraryPathVar('linux'), 'LD_LIBRARY_PATH')
})

test('a GUFI tool runs with GUFI first on PATH and its lib first on the loader path', () => {
  const bin = path.join(bundled, 'bin')
  const lib = path.join(bundled, 'lib')
  const base = { PATH: '/usr/bin:/bin', DYLD_LIBRARY_PATH: '/elsewhere', HOME: '/home/x' }
  const mac = C.gufiEnvFor(bin, base, { STUDIO_GUFI_VT_URL: 'u' }, { platform: 'darwin' })
  assert.equal(mac.GUFI_BIN, bin)
  assert.equal(mac.PATH, `${bin}:/usr/bin:/bin`)
  assert.equal(mac.DYLD_LIBRARY_PATH, `${lib}:/elsewhere`)
  assert.equal(mac.STUDIO_GUFI_VT_URL, 'u')
  assert.equal(mac.HOME, '/home/x')
  const linux = C.gufiEnvFor(bin, { PATH: '/usr/bin' }, {}, { platform: 'linux' })
  assert.equal(linux.LD_LIBRARY_PATH, lib)
  assert.equal(linux.DYLD_LIBRARY_PATH, undefined)
  // The caller's environment is not modified.
  assert.equal(base.PATH, '/usr/bin:/bin')
})

test('no loader path without a lib directory, or for a system one', () => {
  const nolib = install('nolib', { lib: false })
  assert.equal(C.gufiEnvFor(path.join(nolib, 'bin'), {}, {}, { platform: 'linux' }).LD_LIBRARY_PATH, undefined)
  assert.equal(C.gufiEnvFor('/usr/bin', {}, {}, { platform: 'linux' }).LD_LIBRARY_PATH, undefined)
})

test('the indexer scripts get GUFI_BIN and nothing on the loader path', () => {
  const bin = path.join(bundled, 'bin')
  const env = C.gufiEnvFor(bin, { PATH: '/usr/bin', LD_LIBRARY_PATH: '/keep' }, { ADE_VISION_MODEL: 'm' }, { libraries: false, platform: 'linux' })
  assert.equal(env.GUFI_BIN, bin)
  assert.equal(env.PATH, '/usr/bin')
  assert.equal(env.LD_LIBRARY_PATH, '/keep')
  assert.equal(env.ADE_VISION_MODEL, 'm')
  assert.equal(C.indexerEnv().GUFI_BIN, C.GUFI_BIN)
  assert.equal(C.gufiEnv().PATH.split(path.delimiter)[0], C.GUFI_BIN)
})

test('the project root survives a space in the install path, as in "ACTIVATE Studio.app"', async () => {
  const dist = path.join(tmp, 'ACTIVATE Studio.app', 'Contents', 'Resources', 'app', 'server', 'dist')
  fs.mkdirSync(dist, { recursive: true })
  fs.copyFileSync(new URL('../dist/config.js', import.meta.url), path.join(dist, 'config.js'))
  const moved = await import(path.join(dist, 'config.js'))
  assert.equal(moved.PROJECT_ROOT, path.join(tmp, 'ACTIVATE Studio.app', 'Contents', 'Resources', 'app'))
})

test.after(() => fs.rmSync(tmp, { recursive: true, force: true }))
