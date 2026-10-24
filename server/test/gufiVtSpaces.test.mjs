// gufi_vt where every path has a space in it, as in the macOS app: node and
// the wrapper inside "ACTIVATE Studio.app", the index under "Application
// Support/ACTIVATE Studio", GUFI under the app's Resources. gufi_vt runs its
// commands with execvp and strips only a value's outer quotes, so these
// paths are quoted, not refused. The end-to-end tests need GUFI (GUFI_BIN,
// else /opt/gufi/bin) and skip without it; the app's layout is made of
// links to it, a link to this node, and a copy of the wrapper.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { createHash } from 'node:crypto'
import { execFileSync, spawn } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const realBin = process.env.GUFI_BIN || '/opt/gufi/bin'
const haveGufi = fs.existsSync(path.join(realBin, 'gufi_sqlite3')) && fs.existsSync(path.join(realBin, 'gufi_dir2index'))

const base = fs.mkdtempSync(path.join(os.tmpdir(), 'vt-spaces-'))
test.after(() => fs.rmSync(base, { recursive: true, force: true }))
const res = path.join(base, 'Applications', 'ACTIVATE Studio.app', 'Contents', 'Resources')
const data = path.join(base, 'Library', 'Application Support', 'ACTIVATE Studio')
const gufiBin = path.join(res, 'gufi', 'bin')
fs.mkdirSync(gufiBin, { recursive: true })
if (haveGufi) {
  for (const f of fs.readdirSync(realBin)) fs.symlinkSync(path.join(realBin, f), path.join(gufiBin, f))
  const lib = path.join(path.dirname(realBin), 'lib')
  if (fs.existsSync(lib)) fs.symlinkSync(lib, path.join(res, 'gufi', 'lib'))
}
// node and the wrapper where the app has them.
const node = path.join(res, 'node', 'bin', 'node')
fs.mkdirSync(path.dirname(node), { recursive: true })
fs.symlinkSync(process.execPath, node)
const wrapper = path.join(res, 'app', 'server', 'dist', 'gufiVtRemote.js')
fs.mkdirSync(path.dirname(wrapper), { recursive: true })
fs.copyFileSync(fileURLToPath(new URL('../dist/gufiVtRemote.js', import.meta.url)), wrapper)

const port = await new Promise(resolve => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)) }) })
process.env.PORT = String(port)
process.env.HOST = '127.0.0.1'
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = path.join(base, 'Documents', 'ACTIVATE Studio')
process.env.INDEX_BASE = path.join(data, 'index')
process.env.XDG_RUNTIME_DIR = fs.mkdtempSync('/tmp/sshrun-')
test.after(() => fs.rmSync(process.env.XDG_RUNTIME_DIR, { recursive: true, force: true }))
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.PW_API_KEY = 'deployment-key'

const write = (f, bytes) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, Buffer.alloc(bytes, 97)) }
// The main folder; a library on this computer whose index path has a
// space and a single quote; one on "a cluster" (a local bash) likewise;
// and one whose path holds both quotes, which gufi_vt cannot be given.
write(path.join(process.env.KB_ROOT, 'a b', 'big.bin'), 3000)
write(path.join(process.env.KB_ROOT, 'note.txt'), 10)
write(path.join(base, 'src', "local it's", 'mid.dat'), 2000)
write(path.join(base, 'src', "it's proj", 'huge.bin'), 5000)
write(path.join(base, 'src', "it's proj", 'runs', 'log.txt'), 2500)
write(path.join(base, 'src', 'both', 'odd.bin'), 4000)
const localIdx = path.join(base, 'other idx', "local it's")
const remoteIdx = path.join(base, 'cluster idx', "it's proj")
const oddIdx = path.join(base, `odd "x" it's`, 'both')
if (haveGufi) {
  const index = (src, dest) => { fs.mkdirSync(dest, { recursive: true }); execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', src, dest], { stdio: 'ignore' }) }
  index(process.env.KB_ROOT, path.join(process.env.INDEX_BASE, 'gufi'))
  index(path.join(base, 'src', "local it's"), path.join(base, 'other idx'))
  index(path.join(base, 'src', "it's proj"), path.join(base, 'cluster idx'))
  index(path.join(base, 'src', 'both'), path.join(base, `odd "x" it's`))
}
fs.writeFileSync(path.join(process.env.INDEX_BASE, 'libraries.json'), JSON.stringify([{ id: 'other', label: 'Other', indexRoot: localIdx }]))
const owner = 'single'
const userDir = path.join(process.env.INDEX_BASE, 'users', createHash('sha256').update(owner).digest('hex').slice(0, 16))
fs.mkdirSync(userDir, { recursive: true })
fs.writeFileSync(path.join(userDir, 'owner'), owner)
fs.writeFileSync(path.join(userDir, 'libraries.json'), JSON.stringify([{ id: 'u-proj', label: 'u-proj', site: 'cluster-a', resource: 'pw://jdoe/cluster-a', sourceDir: '~/proj', sourceRoot: null, indexRoot: remoteIdx, gufiBin, state: 'ready', run: null, error: null, builtAt: null, directories: 1, files: 1, scheduler: null }]))

const R = await import('../dist/remoteExec.js')
R.setRemoteSpawn((site, key, command) => spawn('bash', ['-c', command], { stdio: ['pipe', 'pipe', 'pipe'] }))
const V = await import('../dist/gufiVt.js')
const W = await import('../dist/gufiVtRemote.js')
const Q = await import('../dist/query.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
await app.register(V.gufiVtRoutes)
await app.listen({ port, host: '127.0.0.1' })
V.setVtCommandForTests(node, wrapper)
test.after(async () => { R.closeRemoteChannels(); await app.close() })

test('a path is quoted for gufi_vt, not refused for its spaces', () => {
  assert.equal(V.vtPath('/Applications/ACTIVATE Studio.app/node'), "'/Applications/ACTIVATE Studio.app/node'")
  assert.equal(V.vtPath("/idx/it's here"), `"/idx/it's here"`)
  assert.equal(V.vtPath('/idx/say "hi"/x'), `'/idx/say "hi"/x'`)
  assert.equal(V.vtPath('/idx/odd "x" it\'s'), null, 'both quotes: gufi_vt does not undo doubled ones')
  assert.equal(V.vtPath("/idx/ends'"), null, 'gufi_vt strips quotes at either end')
  assert.equal(V.vtPath('/idx/a\nb'), null)
  assert.equal(V.vtPath('/idx/a\0b'), null)
  assert.equal(V.vtRemoteIndex("/c/cluster idx/it's proj"), true)
  assert.equal(V.vtRemoteIndex('/c/two  spaces'), false, 'the wrapper splits on spaces and joins with one')
  assert.equal(V.vtRemoteIndex("/c/x 'quoted"), false, 'the wrapper would read a quoted word')
  assert.equal(V.vtRemoteIndex("/c/it's x"), true, 'a quote inside a word is read as it is')
  assert.equal(V.vtRemoteIndex('/c/tab\there'), false)
})

test('the script quotes the index, remote_cmd and remote_arg', () => {
  const plan = { stages: { E: 'SELECT name FROM vrpentries;' }, columns: ['name'] }
  const s = V.vtScript([
    { id: 'kb', index: '/Library/Application Support/ACTIVATE Studio/index/gufi/kb', plan },
    { id: 'o', index: "/x/other idx/local it's", plan },
    { id: 'u', index: '/c/idx', plan, remote: { token: 'tok', site: 'cluster-a', gufiBin: '/c/bin' } },
  ], null)
  assert.match(s, /gufi_vt\(index='\/Library\/Application Support\/ACTIVATE Studio\/index\/gufi\/kb', threads=4/)
  assert.match(s, /gufi_vt\(index="\/x\/other idx\/local it's", threads=4/)
  assert.ok(s.includes(`remote_cmd='${node}', remote_arg='${wrapper}', remote_arg='tok'`), s)
  assert.throws(() => V.vtScript([{ id: 'x', index: '/a "b" it\'s', plan }], null), /cannot be passed to gufi_vt/)
})

test('an index with spaces comes back from the wrapper as one argument', () => {
  const index = "/c/cluster idx/it's proj"
  // gufi_vt's line for a remote location, index unquoted at the end.
  const line = `gufi_query --print-tlv -x --threads 4 -E "SELECT name FROM vrpentries;" ${index} `
  const args = W.splitCommandLine(line)
  assert.deepEqual(V.checkRemoteArgs(args, index), ['--print-tlv', '-x', '--threads', '4', '-E', 'SELECT name FROM vrpentries;', index])
  assert.equal(V.checkRemoteArgs(args, '/c/cluster idx'), null)
  assert.equal(V.checkRemoteArgs([...args, '/etc'], index), null)
  assert.equal(V.checkRemoteArgs(['gufi_query', '-E', index], index), null)
})

test('with the app\'s paths, gufi_vt is available', { skip: !haveGufi && 'GUFI not installed' }, async () => {
  V.forceGufiVtForTests(null)
  assert.equal(await V.gufiVtAvailable(), true)
})

test('a query across libraries runs through gufi_vt with the app\'s paths, with the rows the merge gives', { skip: !haveGufi && 'GUFI not installed' }, async () => {
  const body = { canned: 'largest', n: 10, library: 'all' }
  const vt = await Q.queryScope(body, null)
  assert.equal(vt.engine, 'gufi_vt', JSON.stringify(vt.failed))
  // The library whose path holds both quotes is not among these yet.
  assert.deepEqual(vt.failed, [])
  assert.deepEqual(vt.rows.map(r => [r[1], r[2]]), [['huge.bin', '5000'], ['a b/big.bin', '3000'], ['runs/log.txt', '2500'], ['mid.dat', '2000'], ['note.txt', '10']])
  assert.deepEqual([...new Set(vt.rowLibraries)].sort(), ['kb', 'other', 'u-proj'])
  V.forceGufiVtForTests(false)
  const merged = await Q.queryScope(body, null)
  V.forceGufiVtForTests(null)
  assert.equal(merged.engine, 'merge')
  assert.deepEqual(merged.rows, vt.rows)
})

test('a library gufi_vt cannot be given sends the query to the merge, with every library in it', { skip: !haveGufi && 'GUFI not installed' }, async () => {
  const file = path.join(process.env.INDEX_BASE, 'libraries.json')
  const before = fs.readFileSync(file, 'utf8')
  fs.writeFileSync(file, JSON.stringify([...JSON.parse(before), { id: 'odd', label: 'Odd', indexRoot: oddIdx }]))
  // libraries.js keeps what it read; start it again from the file.
  const L = await import('../dist/libraries.js')
  L.resetLibrariesForTests()
  try {
    const r = await Q.queryScope({ canned: 'largest', n: 10, library: 'all' }, null)
    assert.equal(r.engine, 'merge')
    assert.deepEqual(r.failed, [])
    assert.ok(r.rows.some(row => row[1] === 'odd.bin'), JSON.stringify(r.rows))
    assert.ok(r.rowLibraries.includes('odd'))
  } finally {
    fs.writeFileSync(file, before)
    L.resetLibrariesForTests()
  }
})

test('without gufi_vt the query still merges', { skip: !haveGufi && 'GUFI not installed' }, async () => {
  V.forceGufiVtForTests(false)
  const r = await Q.queryScope({ canned: 'largest', n: 3, library: 'all' }, null)
  V.forceGufiVtForTests(null)
  assert.equal(r.engine, 'merge')
  assert.equal(r.rows[0][1], 'huge.bin')
})
