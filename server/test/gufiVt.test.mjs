// A query across libraries as one gufi_vt statement. The unit tests need
// nothing installed. The end-to-end tests need GUFI (GUFI_BIN, else
// /opt/gufi/bin) and skip without it: they index two synthetic trees, one
// as the Studio's own library and one as a user's library on a cluster,
// whose remote session is a local bash, and compare gufi_vt's answers with
// the per-library merge's.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { createHash } from 'node:crypto'
import { execFileSync, spawn } from 'node:child_process'

const gufiBin = process.env.GUFI_BIN || '/opt/gufi/bin'
const haveGufi = fs.existsSync(path.join(gufiBin, 'gufi_sqlite3')) && fs.existsSync(path.join(gufiBin, 'gufi_dir2index'))

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'vt-'))
const port = await new Promise(resolve => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)) }) })
process.env.PORT = String(port)
process.env.HOST = '127.0.0.1'
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.PW_API_KEY = 'deployment-key'

// Synthetic trees: sizes chosen so the overall order interleaves them.
const write = (f, bytes) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, Buffer.alloc(bytes, 97)) }
write(path.join(dir, 'kb', 'a', 'big.bin'), 3000)
write(path.join(dir, 'kb', 'a', 'note.txt'), 10)
write(path.join(dir, 'kb', 'mid.dat'), 2000)
write(path.join(dir, 'cluster', 'proj', 'huge.bin'), 5000)
write(path.join(dir, 'cluster', 'proj', 'runs', 'log.txt'), 2500)
write(path.join(dir, 'cluster', 'proj', 'tiny.txt'), 5)
if (haveGufi) {
  fs.mkdirSync(path.join(dir, 'ix', 'gufi'), { recursive: true })
  fs.mkdirSync(path.join(dir, 'cluster', 'idx'), { recursive: true })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'kb'), path.join(dir, 'ix', 'gufi')], { stdio: 'ignore' })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'cluster', 'proj'), path.join(dir, 'cluster', 'idx')], { stdio: 'ignore' })
}

// The user's library on "the cluster", and one whose index is gone.
const owner = 'single'
const userDir = path.join(dir, 'ix', 'users', createHash('sha256').update(owner).digest('hex').slice(0, 16))
fs.mkdirSync(userDir, { recursive: true })
fs.writeFileSync(path.join(userDir, 'owner'), owner)
const def = (id, indexRoot) => ({ id, label: id, site: 'cluster-a', resource: 'pw://jdoe/cluster-a', sourceDir: '~/proj', sourceRoot: null, indexRoot, gufiBin, state: 'ready', run: null, error: null, builtAt: null, directories: 1, files: 1, scheduler: null })
fs.writeFileSync(path.join(userDir, 'libraries.json'), JSON.stringify([def('u-proj', path.join(dir, 'cluster', 'idx', 'proj')), def('u-gone', path.join(dir, 'cluster', 'idx', 'gone'))]))

const R = await import('../dist/remoteExec.js')
const remoteCalls = []
R.setRemoteSpawn((site, key, command) => { remoteCalls.push({ site, key }); return spawn('bash', ['-c', command], { stdio: ['pipe', 'pipe', 'pipe'] }) })
const V = await import('../dist/gufiVt.js')
const W = await import('../dist/gufiVtRemote.js')
const T = await import('../dist/gufiTrace.js')
const Q = await import('../dist/query.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
await app.register(V.gufiVtRoutes)
await app.listen({ port, host: '127.0.0.1' })
test.after(async () => { R.closeRemoteChannels(); await app.close() })

test('the wrapper splits the line gufi_vt builds, taking quoted SQL literally', () => {
  assert.deepEqual(
    W.splitCommandLine(`gufi_query --print-tlv -x --threads 4 -E "SELECT 'a b', \`x\`, $HOME, \\n FROM t;" /idx/p `),
    ['gufi_query', '--print-tlv', '-x', '--threads', '4', '-E', "SELECT 'a b', `x`, $HOME, \\n FROM t;", '/idx/p'])
  assert.throws(() => W.splitCommandLine('gufi_query -E "unterminated'), /unterminated/)
})

test('only gufi_query with the stages gufi_vt sends, on the token\'s own index', () => {
  const ok = ['gufi_query', '--print-tlv', '-x', '--threads', '4', '-I', 'CREATE TABLE t (a);', '-E', 'SELECT 1;', '/idx/p']
  assert.deepEqual(V.checkRemoteArgs(ok, '/idx/p'), ok.slice(1))
  assert.equal(V.checkRemoteArgs(ok, '/idx/other'), null)
  assert.equal(V.checkRemoteArgs(['rm', '-rf', '/idx/p'], '/idx/p'), null)
  assert.equal(V.checkRemoteArgs(['gufi_query', '-o', '/tmp/out', '-E', 'SELECT 1;', '/idx/p'], '/idx/p'), null)
  assert.equal(V.checkRemoteArgs(['gufi_query', '--threads', '64', '-E', 'SELECT 1;', '/idx/p'], '/idx/p'), null)
  assert.equal(V.checkRemoteArgs(['gufi_query', '-E', '/idx/p'], '/idx/p'), null)
})

test('the script has a table per location and the order outside the union', () => {
  const plan = { stages: { E: "SELECT name FROM vrpentries;" }, columns: ['name'] }
  const s = V.vtScript([{ id: 'kb', index: '/ix/kb', plan }, { id: 'u-p', index: '/c/idx', plan, remote: { token: 'tok', site: 'cluster-a', gufiBin: '/c/bin' } }], { col: 1, desc: true, limit: 5 })
  assert.match(s, /CREATE VIRTUAL TABLE temp\.loc_0 USING gufi_vt\(index='\/ix\/kb', threads=4, E="SELECT name FROM vrpentries;"\);/)
  assert.match(s, /temp\.loc_1 USING gufi_vt\(index='\/c\/idx'.*remote_arg='tok'\);/)
  assert.match(s, /SELECT 'kb', \* FROM temp\.loc_0 UNION ALL SELECT 'u-p', \* FROM temp\.loc_1 ORDER BY 3 DESC LIMIT 5;$/)
  // As a person runs it: over pw ssh, with the cluster's GUFI on the PATH.
  assert.match(V.vtScript([{ id: 'u-p', index: '/c/idx', plan, remote: { token: 'tok', site: 'cluster-a', gufiBin: '/c/bin' } }], null, V.viaPwSsh),
    /remote_cmd='pw', remote_arg='ssh', remote_arg='cluster-a', remote_arg='env', remote_arg='PATH=\/c\/bin:\$PATH'\);/)
  assert.equal(V.vtEligible({ stages: { E: 'SELECT "name" FROM vrpentries;' }, columns: [] }), false)
})

test('commands are shown as a shell takes them', () => {
  assert.equal(T.shellQuote('-E'), '-E')
  assert.equal(T.shellQuote("SELECT 'a' FROM t;"), `'SELECT '\\''a'\\'' FROM t;'`)
  assert.equal(T.shellQuote('\x1e'), "$'\\x1e'")
  const text = T.formatCommands([{ engine: 'gufi_query', site: 'cluster-a', command: 'gufi_query -E x /i' }])
  assert.equal(text, '# on cluster-a (pw ssh cluster-a)\ngufi_query -E x /i')
})

test('the way in refuses a wrong secret, an unknown token, and other commands', async () => {
  const post = (headers, body) => fetch(`http://127.0.0.1:${port}/internal/gufi-vt`, { method: 'POST', headers: { 'Content-Type': 'application/json', ...headers }, body: JSON.stringify(body) })
  assert.equal((await post({ 'X-Studio-Secret': 'nope' }, { token: 't', args: [] })).status, 403)
  const secret = { 'X-Studio-Secret': V.gufiVtSecretForTests() }
  assert.equal((await post(secret, { token: 'missing', args: ['gufi_query', '/i'] })).status, 404)
  V.addLocationForTests('t1', { site: 'cluster-a', key: 'k', gufiBin: '/c/bin', index: '/i', label: 'p' })
  assert.equal((await post(secret, { token: 't1', args: ['cat', '/etc/passwd', '/i'] })).status, 400)
})

test('all libraries as one gufi_vt statement, the same rows as the merge', { skip: !haveGufi && 'GUFI not installed' }, async () => {
  V.resetGufiVtForTests()
  assert.equal(await V.gufiVtAvailable(), true)
  const { result: vt, commands } = await T.traced(() => Q.queryScope({ canned: 'largest', n: 3, library: 'all' }, null))
  assert.equal(vt.engine, 'gufi_vt')
  assert.deepEqual(vt.rows.map(r => [r[1], r[2]]), [['huge.bin', '5000'], ['a/big.bin', '3000'], ['runs/log.txt', '2500']])
  assert.deepEqual(vt.rowLibraries, ['u-proj', 'kb', 'u-proj'])
  // The gone library is left out and named, and the others still answer.
  assert.deepEqual(vt.failed.map(f => f.library), ['u-gone'])
  // The recorded statement uses gufi_vt's own remote form, and the
  // cluster's own command is recorded under its site.
  const vtCmd = commands.find(c => c.engine === 'gufi_vt')
  if (process.env.SHOW_GUFI) console.log(T.formatCommands(commands))
  assert.match(vtCmd.command, /^\S+gufi_sqlite3 -d x :memory: <<'SQL'\nCREATE VIRTUAL TABLE/)
  assert.ok(vtCmd.command.includes("remote_cmd='pw', remote_arg='ssh', remote_arg='cluster-a'"))
  assert.ok(commands.some(c => c.site === 'cluster-a' && /gufi_query --print-tlv -x --threads 4/.test(c.command)))

  V.forceGufiVtForTests(false)
  const merged = await Q.queryScope({ canned: 'largest', n: 3, library: 'all' }, null)
  V.forceGufiVtForTests(null)
  assert.equal(merged.engine, 'merge')
  assert.deepEqual(merged.rows, vt.rows)
})

test('grouped totals add up across libraries either way', { skip: !haveGufi && 'GUFI not installed' }, async () => {
  const body = { canned: 'by_extension', n: 10, library: 'all' }
  const vt = await Q.queryScope(body, null)
  V.forceGufiVtForTests(false)
  const merged = await Q.queryScope(body, null)
  V.forceGufiVtForTests(null)
  assert.equal(vt.engine, 'gufi_vt')
  const txt = vt.rows.find(r => r[1] === 'txt')
  assert.deepEqual(txt.slice(1), ['txt', '3', '2515'])
  assert.deepEqual(vt.rows, merged.rows)
})

test('a builder query with a size filter, and raw SQL, across libraries', { skip: !haveGufi && 'GUFI not installed' }, async () => {
  const b = await Q.queryScope({ library: 'all', builder: { source: 'files', filters: [{ field: 'size', op: 'gt', value: '2400' }], groupBy: 'none', sort: 'size', dir: 'asc', limit: 10 } }, null)
  assert.equal(b.engine, 'gufi_vt')
  assert.deepEqual(b.rows.map(r => r[2]), ['2500', '3000', '5000'])
  const raw = await Q.queryScope({ library: 'all', sql: "SELECT name FROM vrpentries WHERE size > 2400" }, null)
  assert.equal(raw.engine, 'gufi_vt')
  assert.deepEqual(raw.rows.map(r => r[1]).sort(), ['big.bin', 'huge.bin', 'log.txt'])
})
