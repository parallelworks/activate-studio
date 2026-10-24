// One library or all of them: queries re-rank across libraries where the
// order is known, grouped totals add up, a library that cannot answer is
// reported and the rest answer, search hits carry their library, and the
// combined tree has a folder per library. Two fake cluster libraries stand
// in, through the remote session with a local bash and a fake gufi_query.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { createHash } from 'node:crypto'
import { spawn } from 'node:child_process'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'ls-'))
const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'ls-ix-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'ls-kb-'))
process.env.INDEX_BASE = IX
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.PW_API_KEY = 'deployment-key'
process.env.GUFI_BIN = path.join(dir, 'no-local-gufi')

const home = path.join(dir, 'home')
const bin = path.join(home, 'gufi', 'bin')
fs.mkdirSync(bin, { recursive: true })
for (const n of ['a', 'b']) fs.mkdirSync(path.join(home, 'idx', n), { recursive: true })
// Rows depend on which index is asked (the last argument) and what.
fs.writeFileSync(path.join(bin, 'gufi_query'), `#!/bin/sh
args="$*"
for last; do :; done
R=$(printf '\\036')
lib=$(basename "$last")
row() { printf '%s' "$1"; shift; for c in "$@"; do printf '%s%s' "$R" "$c"; done; printf '\\n'; }
case "$args" in
  *"--max-level 0"*) row "$lib-file.txt" f 5 1700000000 ;;
  *"--max-level 1"*) row "sub" 1700000000 ;;
  *"ORDER BY size DESC"*)
    if [ "$lib" = a ]; then row "$last/big.bin" 100 "2026-01-01 00:00:00"; row "$last/mid.bin" 50 "2026-01-02 00:00:00"
    else row "$last/large.dat" 80 "2026-01-03 00:00:00"; row "$last/small.dat" 10 "2026-01-04 00:00:00"; fi ;;
  *"GROUP BY ext"*)
    if [ "$lib" = a ]; then row pdf 2 300; row txt 1 10; else row pdf 1 100; fi ;;
  *"INSERT INTO tn"*) row n 2 "$last/notes/$lib-report.md" "$lib-report.md" 7 1700000000 "" ;;
esac
`, { mode: 0o755 })

const owner = 'single'
const hash = createHash('sha256').update(owner).digest('hex').slice(0, 16)
fs.mkdirSync(path.join(IX, 'users', hash), { recursive: true })
fs.writeFileSync(path.join(IX, 'users', hash, 'owner'), owner)
const def = (id, label, n) => ({ id, label, site: `cluster-${n}`, resource: `pw://u/cluster-${n}`, sourceDir: '~', sourceRoot: path.join(home, 'src', n), indexRoot: path.join(home, 'idx', n), gufiBin: bin, state: 'ready', run: null, error: null, builtAt: null, directories: 1, files: 1, scheduler: null })
fs.writeFileSync(path.join(IX, 'users', hash, 'libraries.json'), JSON.stringify([def('u-a', 'Library A', 'a'), def('u-b', 'Library B', 'b')]))

const R = await import('../dist/remoteExec.js')
R.setRemoteSpawn((site, key, command) => spawn('bash', ['-c', command], { stdio: ['pipe', 'pipe', 'pipe'], env: { ...process.env, HOME: home } }))
const Q = await import('../dist/query.js')
const S = await import('../dist/libraryScope.js')
const L = await import('../dist/libraries.js')
test.after(() => R.closeRemoteChannels())

test('"all" means every library the viewer can read; one id means that one', () => {
  assert.deepEqual(S.scopeLibraries('all', null, 'kb').map(l => l.id), ['kb', 'u-a', 'u-b'])
  assert.deepEqual(S.scopeLibraries('u-b', null, 'kb').map(l => l.id), ['u-b'])
  assert.deepEqual(S.scopeLibraries(undefined, null, 'kb').map(l => l.id), ['kb'])
})

test('a ranked query across libraries is the overall top rows, re-ranked', async () => {
  const r = await Q.queryScope({ canned: 'largest', n: 3, library: 'all' }, null)
  assert.deepEqual(r.columns, ['library', 'path', 'bytes', 'modified'])
  assert.deepEqual(r.rows.map(x => [x[0], x[1], x[2]]), [['Library A', 'big.bin', '100'], ['Library B', 'large.dat', '80'], ['Library A', 'mid.bin', '50']])
  assert.deepEqual(r.rowLibraries, ['u-a', 'u-b', 'u-a'])
  assert.deepEqual(r.failed.map(f => f.library), [path.basename(process.env.KB_ROOT)], 'the knowledge base (labeled by its folder) has no index here, and says so')
})

test('grouped totals add up across libraries', async () => {
  const r = await Q.queryScope({ canned: 'by_extension', n: 10, library: 'all' }, null)
  assert.deepEqual(r.rows.map(x => x.slice(1)), [['pdf', '3', '400'], ['txt', '1', '10']])
})

test('one library answers alone, with its own columns', async () => {
  const r = await Q.queryScope({ canned: 'largest', n: 5, library: 'u-b' }, null)
  assert.deepEqual(r.columns, ['path', 'bytes', 'modified'])
  assert.deepEqual(r.rows.map(x => x[0]), ['large.dat', 'small.dat'])
})

test('search across libraries tags each hit with its library', async () => {
  const libs = S.scopeLibraries('all', null, 'kb').filter(l => l.site)
  const hits = await S.searchScope(libs, 'report', 10, { semantic: false })
  assert.deepEqual(hits.map(h => [h.library.id, h.path]).sort(), [['u-a', 'notes/a-report.md'], ['u-b', 'notes/b-report.md']])
})

test('the combined tree has a folder per library, and each opens its own tree', async () => {
  const { default: Fastify } = await import('fastify')
  const { kbRoutes } = await import('../dist/routes.js')
  const app = Fastify(); await app.register(kbRoutes); await app.ready()
  const root = (await app.inject({ url: '/api/kb/tree?library=all' })).json()
  assert.deepEqual(root.entries.map(e => [e.path, e.type]), [['kb', 'dir'], ['u-a', 'dir'], ['u-b', 'dir']])
  assert.match(root.entries[1].name, /Library A \(on cluster-a\)/)
  const a = (await app.inject({ url: '/api/kb/tree?library=all&path=u-a' })).json()
  assert.deepEqual(a.entries.map(e => e.path), ['u-a/sub', 'u-a/a-file.txt'])
  assert.equal((await app.inject({ url: '/api/kb/tree?library=all&path=nope' })).statusCode, 404)
  assert.equal(L.listLibraries('kb').length, 1, 'the deployment listing is unchanged')
})
