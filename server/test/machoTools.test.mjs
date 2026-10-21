// The macOS app's Mach-O tools (deploy/mac): macho_check.py's checks of the
// macOS floor, signatures, library resolution and bundled libraries;
// macho_relink.py's rewrite of build-machine paths; check_gufi.sh's static
// checks of a GUFI tree; and build_gufi.sh's refusal of bad input. They run
// on any host, against small Mach-O files made here with ad hoc signatures.
import test from 'node:test'
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawnSync } from 'node:child_process'

const REPO = path.resolve(import.meta.dirname, '..', '..')
const MAC = path.join(REPO, 'deploy', 'mac')
const tmp = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'macho-')))
const hasPython = spawnSync('python3', ['--version']).status === 0

const LC_ID_DYLIB = 0xd, LC_LOAD_DYLIB = 0xc, LC_RPATH = 0x8000001c, LC_CODE_SIGNATURE = 0x1d, LC_BUILD_VERSION = 0x32
const PAGE = 4096
const align8 = n => (n + 7) & ~7
const version = v => { const [maj, min = 0] = v.split('.').map(Number); return (maj << 16) | (min << 8) }

/** A load command holding one string, as dylib and rpath commands do. */
function stringCommand(cmd, header, value) {
  const size = align8(header + Buffer.byteLength(value) + 1)
  const b = Buffer.alloc(size)
  b.writeUInt32LE(cmd, 0)
  b.writeUInt32LE(size, 4)
  b.writeUInt32LE(header, 8)
  if (header === 24) b.writeUInt32LE(2, 12)    // dylib: timestamp, versions
  b.write(value, header)
  return b
}

/**
 * A thin arm64 Mach-O: an executable, or a dylib when `id` is given, that
 * needs macOS `minos`, links `deps`, has `rpaths`, and carries an ad hoc
 * signature over its pages, as the linker leaves one.
 */
function macho({ minos = '14.0', deps = ['/usr/lib/libSystem.B.dylib'], rpaths = [], id = null }) {
  const build = Buffer.alloc(24)
  build.writeUInt32LE(LC_BUILD_VERSION, 0)
  build.writeUInt32LE(24, 4)
  build.writeUInt32LE(1, 8)    // macOS
  build.writeUInt32LE(version(minos), 12)
  build.writeUInt32LE(version('15.0'), 16)
  const cmds = [build]
  if (id) cmds.push(stringCommand(LC_ID_DYLIB, 24, id))
  for (const d of deps) cmds.push(stringCommand(LC_LOAD_DYLIB, 24, d))
  for (const r of rpaths) cmds.push(stringCommand(LC_RPATH, 12, r))
  const sig = Buffer.alloc(16)
  sig.writeUInt32LE(LC_CODE_SIGNATURE, 0)
  sig.writeUInt32LE(16, 4)
  cmds.push(sig)
  const sizeofcmds = cmds.reduce((n, c) => n + c.length, 0)
  const header = Buffer.alloc(32)
  header.writeUInt32LE(0xfeedfacf, 0)
  header.writeUInt32LE(0x0100000c, 4)    // arm64
  header.writeUInt32LE(id ? 6 : 2, 12)    // MH_DYLIB or MH_EXECUTE
  header.writeUInt32LE(cmds.length, 16)
  header.writeUInt32LE(sizeofcmds, 20)
  // Two pages of "code", then the signature.
  const code = Buffer.concat([header, ...cmds])
  const img = Buffer.alloc(2 * PAGE, 0x90)
  code.copy(img)
  const nCode = img.length / PAGE
  const ident = Buffer.from('test\0')
  const hashOffset = 48 + ident.length
  const cd = Buffer.alloc(hashOffset + nCode * 32)
  cd.writeUInt32BE(0xfade0c02, 0)
  cd.writeUInt32BE(cd.length, 4)
  cd.writeUInt32BE(0x20100, 8)
  cd.writeUInt32BE(0x2, 12)    // ad hoc
  cd.writeUInt32BE(hashOffset, 16)
  cd.writeUInt32BE(48, 20)
  cd.writeUInt32BE(0, 24)
  cd.writeUInt32BE(nCode, 28)
  cd.writeUInt32BE(img.length, 32)
  cd.writeUInt8(32, 36)    // hash size
  cd.writeUInt8(2, 37)    // SHA-256
  cd.writeUInt8(12, 39)    // 4096-byte pages
  ident.copy(cd, 48)
  const superBlob = Buffer.alloc(20)
  superBlob.writeUInt32BE(0xfade0cc0, 0)
  superBlob.writeUInt32BE(20 + cd.length, 4)
  superBlob.writeUInt32BE(1, 8)
  superBlob.writeUInt32BE(0, 12)    // slot 0: the CodeDirectory
  superBlob.writeUInt32BE(20, 16)
  // The signature's place, written into the command before the pages are hashed.
  const sigAt = 32 + sizeofcmds - 16
  img.writeUInt32LE(img.length, sigAt + 8)
  img.writeUInt32LE(superBlob.length + cd.length, sigAt + 12)
  for (let i = 0; i < nCode; i++) {
    crypto.createHash('sha256').update(img.subarray(i * PAGE, (i + 1) * PAGE)).digest().copy(cd, hashOffset + i * 32)
  }
  return Buffer.concat([img, superBlob, cd])
}

const write = (file, body, mode = 0o755) => {
  fs.mkdirSync(path.dirname(file), { recursive: true })
  fs.writeFileSync(file, body, { mode })
}
const py = (script, args) => spawnSync('python3', [path.join(MAC, script), ...args], { encoding: 'utf8' })
// A build-machine path long enough for its @loader_path form to fit in place.
const BUILT = '/Users/runner/work/studio/deploy/mac/out/.gufi-work.abc123/deps/lib'

test('macho_check prints the newest macOS a set of files needs, and refuses one newer than --max-minos', { skip: !hasPython }, () => {
  const dir = path.join(tmp, 'minos')
  write(path.join(dir, 'a'), macho({ minos: '13.5' }))
  write(path.join(dir, 'b'), macho({ minos: '14.0' }))
  write(path.join(dir, 'c'), '#!/bin/sh\n')    // not Mach-O: skipped
  const files = ['a', 'b', 'c'].map(f => path.join(dir, f))
  assert.equal(py('macho_check.py', ['--print-minos', ...files]).stdout.trim(), '14.0')
  assert.equal(py('macho_check.py', ['--max-minos', '14.0', ...files]).status, 0)
  assert.equal(py('macho_check.py', ['--max-minos', '26', ...files]).status, 0)
  const r = py('macho_check.py', ['--max-minos', '13.5', ...files])
  assert.equal(r.status, 1)
  assert.match(r.stdout, /\/b: needs macOS 14\.0, newer than 13\.5/)
  assert.doesNotMatch(r.stdout, /\/a: needs/)
  write(path.join(dir, 'new'), macho({ minos: '27.0' }))
  assert.match(py('macho_check.py', ['--max-minos', '26', path.join(dir, 'new')]).stdout, /needs macOS 27\.0, newer than 26/)
})

test('macho_check finds a page changed after signing', { skip: !hasPython }, () => {
  const file = path.join(tmp, 'sig', 'tool')
  const img = macho({})
  write(file, img)
  assert.equal(py('macho_check.py', [file]).status, 0)
  img[PAGE + 10] ^= 0xff
  write(file, img)
  const r = py('macho_check.py', [file])
  assert.equal(r.status, 1)
  assert.match(r.stdout, /code page 1 does not match its signature/)
})

test('macho_relink points build-machine paths into the tree and keeps the signature valid', { skip: !hasPython }, () => {
  const root = path.join(tmp, 'relink')
  write(path.join(root, 'lib', 'libfoo.dylib'), macho({ id: `${BUILT}/libfoo.dylib` }))
  write(path.join(root, 'lib', 'libbar.dylib'), macho({ id: '@rpath/libbar.dylib', deps: ['@rpath/libfoo.dylib', '/usr/lib/libSystem.B.dylib'], rpaths: [BUILT] }))
  write(path.join(root, 'bin', 'tool'), macho({ deps: [`${BUILT}/libfoo.dylib`, '@rpath/libbar.dylib', '/usr/lib/libSystem.B.dylib'], rpaths: [`${BUILT}/../../llama.cpp/lib`] }))
  const files = ['bin/tool', 'lib/libfoo.dylib', 'lib/libbar.dylib'].map(f => path.join(root, f))
  // As built, nothing resolves without the build machine.
  const before = py('macho_check.py', files)
  assert.equal(before.status, 1)
  assert.match(before.stdout, /tool: unresolved \/Users\/runner\/.*libfoo\.dylib/)
  assert.match(before.stdout, /tool: unresolved @rpath\/libbar\.dylib/)

  const r = py('macho_relink.py', [root])
  assert.equal(r.status, 0, r.stderr)
  assert.deepEqual(r.stdout.trim().split('\n').sort(), files.sort())
  const after = py('macho_check.py', ['--verbose', ...files])
  assert.equal(after.status, 0, after.stdout)
  assert.match(after.stdout, /tool: @loader_path\/\.\.\/lib\/libfoo\.dylib -> .*\/relink\/bin\/\.\.\/lib\/libfoo\.dylib/)
  assert.match(after.stdout, /tool: @rpath\/libbar\.dylib -> .*\/relink\/bin\/\.\.\/lib\/libbar\.dylib/)
  assert.match(after.stdout, /libbar\.dylib: @rpath\/libfoo\.dylib -> .*\/relink\/lib\/libfoo\.dylib/)
  assert.doesNotMatch(fs.readFileSync(path.join(root, 'lib', 'libfoo.dylib'), 'latin1'), /\/Users\/runner/)
  // A second run has nothing left to change.
  assert.equal(py('macho_relink.py', [root]).stdout, '')
})

test('macho_relink refuses a library the tree does not carry', { skip: !hasPython }, () => {
  const root = path.join(tmp, 'missing')
  write(path.join(root, 'bin', 'tool'), macho({ deps: [`${BUILT}/libgone.dylib`] }))
  fs.mkdirSync(path.join(root, 'lib'))
  const r = py('macho_relink.py', [root])
  assert.equal(r.status, 1)
  assert.match(r.stderr, /libgone\.dylib is not in .*\/missing\/lib/)
})

test('macho_check --bundled refuses a system copy of a library the tree carries', { skip: !hasPython }, () => {
  const root = path.join(tmp, 'bundled')
  const tool = path.join(root, 'lib', 'gufi_vt.dylib')
  write(tool, macho({ id: '@rpath/gufi_vt.dylib', deps: ['/usr/lib/libpcre2-8.0.dylib', '/usr/lib/libSystem.B.dylib'] }))
  const lib = path.join(root, 'lib')
  assert.equal(py('macho_check.py', ['--bundled', lib, tool]).status, 0, 'the tree has no pcre2 of its own yet')
  write(path.join(lib, 'libpcre2-8.0.dylib'), macho({ id: '@rpath/libpcre2-8.0.dylib' }))
  const r = py('macho_check.py', ['--bundled', lib, tool])
  assert.equal(r.status, 1)
  assert.match(r.stdout, /gufi_vt\.dylib: loads the system's \/usr\/lib\/libpcre2-8\.0\.dylib, not the one in .*\/bundled\/lib/)
  assert.equal(py('macho_check.py', [tool]).status, 0, 'without --bundled the system copy is accepted')
})

/** A GUFI tree as build_gufi.sh lays it out, with stand-in binaries. */
function gufiTree(root, { minos = '14.0', target = '14.0' } = {}) {
  const lib = l => path.join(root, 'lib', l)
  for (const l of ['libomp.dylib', 'libpcre2-8.0.dylib', 'vec0.dylib', 'lembed0.dylib', 'libsqlite3-pcre.dylib']) {
    write(lib(l), macho({ id: `@rpath/${l}` }))
  }
  write(lib('gufi_vt.dylib'), macho({ minos, id: '@rpath/gufi_vt.dylib', deps: ['@loader_path/libpcre2-8.0.dylib', '@loader_path/libomp.dylib', '/usr/lib/libSystem.B.dylib'] }))
  for (const b of ['gufi_query', 'gufi_dir2index', 'gufi_sqlite3']) {
    write(path.join(root, 'bin', b), macho({ deps: ['@loader_path/../lib/libpcre2-8.0.dylib', '@loader_path/../lib/libomp.dylib', '/usr/lib/libSystem.B.dylib'] }))
  }
  write(path.join(root, 'bin', 'gufi_find'), '#!/usr/bin/env python3\n')
  for (const l of ['GUFI-LICENSE.txt', 'sqlite-vec-LICENSE-MIT.txt', 'sqlite-lembed-LICENSE.txt', 'llama.cpp-LICENSE.txt',
    'jemalloc-COPYING.txt', 'libomp-LICENSE.txt', 'pcre2-LICENCE.md']) {
    write(path.join(root, 'licenses', l), 'license text\n', 0o644)
  }
  write(path.join(root, 'README.txt'), 'GUFI\n', 0o644)
  write(path.join(root, 'manifest.json'), JSON.stringify({ deploymentTarget: target }), 0o644)
}
const checkGufi = args => spawnSync('bash', [path.join(MAC, 'check_gufi.sh'), ...args], { encoding: 'utf8' })

test('check_gufi.sh passes a tree built for its target, and fails one that needs a newer macOS', { skip: !hasPython }, () => {
  const good = path.join(tmp, 'tree-good', 'gufi')
  gufiTree(good)
  let r = checkGufi(['--static', good])
  assert.equal(r.status, 0, r.stdout + r.stderr)
  assert.match(r.stdout, /ok +the manifest says it was built for macOS 14\.0/)
  assert.match(r.stdout, /ok +arm64, signed, runs on macOS 14\.0/)

  const late = path.join(tmp, 'tree-late', 'gufi')
  gufiTree(late, { minos: '27.0' })
  r = checkGufi(['--static', late])
  assert.equal(r.status, 1)
  assert.match(r.stdout, /gufi_vt\.dylib: needs macOS 27\.0, newer than 14\.0/)
  // --max-minos overrides the manifest.
  assert.equal(checkGufi(['--static', '--max-minos', '27', late]).status, 0)

  fs.rmSync(path.join(good, 'licenses', 'jemalloc-COPYING.txt'))
  r = checkGufi(['--static', good])
  assert.equal(r.status, 1)
  assert.match(r.stdout, /FAIL +licenses\/jemalloc-COPYING\.txt/)
})

test('check_gufi.sh unpacks an archive and refuses one whose .sha256 does not match', { skip: !hasPython }, () => {
  const src = path.join(tmp, 'archive')
  gufiTree(path.join(src, 'gufi'))
  const archive = path.join(tmp, 'gufi-macos-arm64.tar.gz')
  assert.equal(spawnSync('tar', ['-czf', archive, '-C', src, 'gufi']).status, 0)
  const sum = crypto.createHash('sha256').update(fs.readFileSync(archive)).digest('hex')
  fs.writeFileSync(`${archive}.sha256`, `${sum}  gufi-macos-arm64.tar.gz\n`)
  let r = checkGufi(['--static', archive])
  assert.equal(r.status, 0, r.stdout + r.stderr)
  fs.writeFileSync(`${archive}.sha256`, `${'0'.repeat(64)}  gufi-macos-arm64.tar.gz\n`)
  r = checkGufi(['--static', archive])
  assert.equal(r.status, 1)
  assert.match(r.stderr, /its \.sha256 says 0{64}/)
})

test('build_gufi.sh refuses a malformed deployment target or GUFI ref before doing anything', () => {
  const run = env => spawnSync('bash', [path.join(MAC, 'build_gufi.sh')], {
    env: { PATH: process.env.PATH, HOME: tmp, OUT_DIR: path.join(tmp, 'never'), ...env }, encoding: 'utf8',
  })
  let r = run({ MACOSX_DEPLOYMENT_TARGET: '14.0; touch /tmp/x' })
  assert.equal(r.status, 1)
  assert.match(r.stderr, /MACOSX_DEPLOYMENT_TARGET must look like 14\.0/)
  r = run({ MACOSX_DEPLOYMENT_TARGET: '14.0', GUFI_REF: '--upload-pack=touch /tmp/x' })
  assert.equal(r.status, 1)
  assert.match(r.stderr, /GUFI_REF is not a branch, tag or commit/)
  assert.ok(!fs.existsSync(path.join(tmp, 'never')), 'nothing was created')
  if (process.platform !== 'darwin') {
    r = run({ MACOSX_DEPLOYMENT_TARGET: '14.0' })
    assert.equal(r.status, 1)
    assert.match(r.stderr, /runs on a Mac with Apple silicon/)
  }
})

test.after(() => fs.rmSync(tmp, { recursive: true, force: true }))
