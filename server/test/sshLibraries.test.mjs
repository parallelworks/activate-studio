// A library over plain ssh: an existing GUFI index on another host, added
// after a connection test and then browsed, searched, previewed, counted,
// and queried with gufi_vt like a cluster library. The hosts are local:
// the transport's spawn seam runs each session in /bin/sh with that host's
// HOME and PATH, as sshd would run the login shell. One host says it is
// macOS (uname prints Darwin) and has its GUFI on the PATH; the others
// fail the way ssh fails. Needs GUFI (GUFI_BIN, else /opt/gufi/bin) and
// skips without it.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { execFileSync, spawn } from 'node:child_process'

const gufiBin = process.env.GUFI_BIN || '/opt/gufi/bin'
const haveGufi = fs.existsSync(path.join(gufiBin, 'gufi_query')) && fs.existsSync(path.join(gufiBin, 'gufi_dir2index'))
const skip = !haveGufi && 'GUFI not installed'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sshlib-'))
const port = await new Promise(resolve => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)) }) })
process.env.PORT = String(port)
process.env.HOST = '127.0.0.1'
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')

const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
const exe = (f, body) => { write(f, body); fs.chmodSync(f, 0o755) }

// The Studio's own library, and on the remote host a source tree with its
// index beside it, both under the ssh login's home.
write(path.join(dir, 'kb', 'a', 'big.bin'), Buffer.alloc(3000, 97))
write(path.join(dir, 'kb', 'note.txt'), 'kb note\n')
const home = path.join(dir, 'home')
write(path.join(home, 'proj', 'readme.txt'), 'hello from the remote\n')
write(path.join(home, 'proj', 'data', 'huge.bin'), Buffer.alloc(5000, 98))
write(path.join(home, 'proj', 'data', 'notes.md'), '# notes\n')
// A directory whose index is one level down.
write(path.join(home, 'holder', 'sub', 'db.db'), '')
fs.mkdirSync(path.join(home, 'idx'), { recursive: true })
fs.mkdirSync(path.join(dir, 'ix', 'gufi'), { recursive: true })
if (haveGufi) {
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(home, 'proj'), path.join(home, 'idx')], { stdio: 'ignore' })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'kb'), path.join(dir, 'ix', 'gufi')], { stdio: 'ignore' })
}
const index = path.join(home, 'idx', 'proj')

// The macOS host: uname says Darwin, and its GUFI, found on the PATH, is
// a wrapper that notes the library path it was started with.
const macGufi = path.join(dir, 'mac-gufi')
const dyldLog = path.join(dir, 'dyld.log')
exe(path.join(dir, 'mac-bin', 'uname'), '#!/bin/sh\necho Darwin\n')
exe(path.join(macGufi, 'bin', 'gufi_query'), `#!/bin/sh\nprintf '%s\\n' "\${DYLD_LIBRARY_PATH:-none}" >> ${dyldLog}\nexec ${gufiBin}/gufi_query "$@"\n`)

const systemPath = '/usr/bin:/bin'
const failing = {
  'me@nokey': 'me@nokey: Permission denied (publickey,password).',
  'me@nohost': 'ssh: Could not resolve hostname nohost: Name or service not known',
  'me@newhost': 'Host key verification failed.',
  'me@closed': 'ssh: connect to host closed port 22: Connection refused',
}
const R = await import('../dist/remoteExec.js')
const sessions = []
R.setRemoteSpawn((site, key, command) => {
  const argv = R.sshArgv(site, command)
  sessions.push({ site, key, argv })
  const dest = R.parseSshSite(site).destination
  if (failing[dest]) return spawn('/bin/sh', ['-c', `echo "${failing[dest]}" >&2; exit 255`], { stdio: ['pipe', 'pipe', 'pipe'] })
  const PATH = dest === 'me@mac-box' ? `${path.join(dir, 'mac-bin')}:${path.join(macGufi, 'bin')}:${systemPath}` : systemPath
  return spawn('/bin/sh', ['-c', argv[argv.length - 1]], { stdio: ['pipe', 'pipe', 'pipe'], env: { HOME: home, PATH } })
})
const U = await import('../dist/userLibraries.js')
const L = await import('../dist/libraries.js')
const G = await import('../dist/gufi.js')
const V = await import('../dist/gufiVt.js')
const Q = await import('../dist/query.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
app.setErrorHandler((err, _req, reply) => reply.status(err.status ?? 500).send({ error: err.message }))
await app.register(U.userLibraryRoutes)
await app.register(V.gufiVtRoutes)
await app.listen({ port, host: '127.0.0.1' })
test.after(async () => { R.closeRemoteChannels(); await app.close() })

const post = async (url, body) => { const r = await app.inject({ method: 'POST', url, payload: body }); return { status: r.statusCode, body: r.json() } }
const probe = body => post('/api/user-libraries/ssh/test', body).then(r => r.body)
const linux = { destination: 'me@linux-box', port: 2222 }

test('the connection test passes step by step, finding GUFI and counting the index', { skip }, async () => {
  const r = await probe({ ...linux, indexRoot: '~/idx/proj', sourceRoot: '~/proj' })
  assert.equal(r.ok, true, JSON.stringify(r.steps))
  assert.deepEqual(r.steps.map(s => [s.name, s.ok]), [['ssh', true], ['gufi', true], ['index', true], ['source', true]])
  assert.match(r.steps[0].detail, /^Connected to me@linux-box on port 2222/)
  // Not on the login's PATH: found where GUFI installs.
  assert.equal(r.gufiBin, gufiBin)
  assert.match(r.gufiVersion, /^\S+$/)
  assert.equal(r.directories, 2)
  assert.match(r.steps[2].detail, /2 directories and 3 files/)
  // One session per transport and destination, with the user's own ssh.
  const mine = sessions.filter(s => s.site === 'ssh://me@linux-box:2222')
  assert.equal(mine.length, 1)
  assert.equal(mine[0].key, '')
  assert.deepEqual(mine[0].argv.slice(0, 10), ['-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=30', '-p', '2222', 'me@linux-box'])
})

test('each failing step says what to change', { skip }, async () => {
  const ssh = async (destination, re) => {
    const r = await probe({ destination, indexRoot: index })
    assert.equal(r.ok, false)
    assert.deepEqual(r.steps.map(s => s.name), ['ssh'])
    assert.match(r.steps[0].detail, re)
  }
  await ssh('me@nokey', /asked for a password.*Set up key access \(for example ssh-copy-id me@nokey\)/)
  await ssh('me@nohost', /does not resolve/)
  await ssh('me@newhost', /host key of me@newhost is not known here yet/)
  await ssh('me@closed', /Nothing accepted the connection on port 22/)

  const noGufi = await probe({ ...linux, indexRoot: index, gufiBin: '/nowhere/bin' })
  assert.equal(noGufi.ok, false)
  assert.deepEqual(noGufi.steps.map(s => [s.name, s.ok]), [['ssh', true], ['gufi', false], ['index', true]])
  assert.match(noGufi.steps[1].detail, /There is no gufi_query in \/nowhere\/bin/)
  assert.match(noGufi.steps[2].detail, /has a db\.db; its directories are counted once gufi_query runs/)

  const notDir = await probe({ ...linux, indexRoot: '/nowhere/index' })
  assert.match(notDir.steps.find(s => s.name === 'index').detail, /\/nowhere\/index is not a directory on me@linux-box/)
  const oneUp = await probe({ ...linux, indexRoot: '~/holder' })
  assert.match(oneUp.steps.find(s => s.name === 'index').detail, new RegExp(`no db\\.db.*one level down: ${path.join(home, 'holder', 'sub')}`))
  const noSource = await probe({ ...linux, indexRoot: index, sourceRoot: '/nowhere/src' })
  assert.equal(noSource.ok, false)
  assert.match(noSource.steps.find(s => s.name === 'source').detail, /not a directory.*names only/)

  const bad = await post('/api/user-libraries/ssh/test', { destination: '-oProxyCommand=sh', indexRoot: index })
  assert.equal(bad.status, 400)
  assert.match(bad.body.error, /destination/)
})

let lib
test('added over ssh, the library is ready, read only, and answers the tree, search, previews, and statistics', { skip }, async () => {
  const refused = await post('/api/user-libraries/ssh', { ...linux, indexRoot: '/nowhere/index' })
  assert.equal(refused.status, 400)
  assert.match(refused.body.error, /did not pass.*not a directory/)

  const added = await post('/api/user-libraries/ssh', { ...linux, label: 'Remote project', indexRoot: '~/idx/proj', sourceRoot: '~/proj' })
  assert.equal(added.status, 200, JSON.stringify(added.body))
  const d = added.body
  assert.deepEqual([d.transport, d.state, d.destination, d.port, d.site, d.label], ['ssh', 'ready', 'me@linux-box', 2222, 'me@linux-box', 'Remote project'])
  assert.deepEqual([d.indexRoot, d.sourceRoot, d.directories, d.files, d.run], [index, path.join(home, 'proj'), 2, 3, null])
  assert.ok(d.builtAt)
  const listed = (await app.inject({ method: 'GET', url: '/api/user-libraries' })).json()
  assert.equal(listed.ssh, true)
  assert.deepEqual(listed.libraries.map(l => [l.id, l.transport]), [[d.id, 'ssh']])
  assert.equal((await post('/api/user-libraries/ssh', { ...linux, indexRoot: index })).status, 409, 'the same index twice')

  lib = L.listLibraries('kb', null).find(l => l.id === d.id)
  assert.ok(lib, 'it joins the library list')
  assert.equal(lib.site, 'me@linux-box'); assert.equal(lib.writable, false); assert.match(lib.indexRoot, /^remote:/)
  const tree = await U.remoteTree(lib.indexRoot, '')
  assert.deepEqual(tree.map(e => [e.type, e.name]), [['dir', 'data'], ['file', 'readme.txt']])
  assert.deepEqual((await U.remoteTree(lib.indexRoot, 'data')).map(e => [e.name, e.size]), [['huge.bin', 5000], ['notes.md', 8]])
  const hits = await G.searchNames('huge', 5, lib.indexRoot)
  assert.deepEqual(hits.map(h => h.path), ['data/huge.bin'])
  const stats = await G.corpusStats(lib.indexRoot)
  assert.deepEqual([stats.available, stats.files, stats.dirs, stats.totalBytes], [true, 3, 2, 5000 + 8 + 22])
  const f = await U.remoteFile(lib.indexRoot, 'readme.txt')
  assert.deepEqual([f.content, f.size, f.source], ['hello from the remote\n', 22, 'raw'])
  const dl = await U.remoteDownload(lib.indexRoot, 'data/huge.bin')
  assert.equal(dl.body.length, 5000)
  await assert.rejects(U.remoteFile(lib.indexRoot, '../../etc/passwd'), /invalid path/)
})

test('one gufi_vt statement over the knowledge base and the library over ssh', { skip }, async t => {
  V.resetGufiVtForTests()
  if (!(await V.gufiVtAvailable())) return t.skip('this GUFI has no gufi_vt')
  const vt = await Q.queryScope({ canned: 'largest', n: 2, library: 'all' }, null)
  assert.equal(vt.engine, 'gufi_vt', JSON.stringify(vt.failed ?? vt))
  assert.deepEqual(vt.rows.map(r => r[1]), ['data/huge.bin', 'a/big.bin'])
  assert.deepEqual(vt.rowLibraries, [lib.id, 'kb'])
})

test('on a macOS host the test finds GUFI on the PATH and every GUFI command gets its library path', { skip }, async () => {
  fs.rmSync(dyldLog, { force: true })
  const r = await probe({ destination: 'me@mac-box', indexRoot: index })
  assert.equal(r.ok, true, JSON.stringify(r.steps))
  assert.match(r.steps[0].detail, /a macOS host/)
  assert.equal(r.gufiBin, path.join(macGufi, 'bin'))
  const lines = () => fs.readFileSync(dyldLog, 'utf8').trim().split('\n')
  assert.ok(lines().length >= 2, 'the version and the count')
  assert.ok(lines().every(l => l === path.join(macGufi, 'lib')), lines().join(' '))
  const added = await post('/api/user-libraries/ssh', { destination: 'me@mac-box', indexRoot: index, sourceRoot: path.join(home, 'proj') })
  assert.equal(added.status, 200, JSON.stringify(added.body))
  assert.equal(added.body.label, 'proj on mac-box')
  // The tree runs gufi_query from a bash script, which macOS starts without
  // DYLD_* variables; the script sets the path itself.
  fs.rmSync(dyldLog)
  const mac = L.listLibraries('kb', null).find(l => l.id === added.body.id)
  assert.deepEqual((await U.remoteTree(mac.indexRoot, '')).map(e => e.name), ['data', 'readme.txt'])
  assert.deepEqual(await G.searchNames('notes', 5, mac.indexRoot).then(h => h.map(x => x.path)), ['data/notes.md'])
  assert.ok(lines().length >= 3 && lines().every(l => l === path.join(macGufi, 'lib')), lines().join(' '))
})

test('refresh checks again, sign-in keeps ssh out, and removing forgets the library', { skip }, async () => {
  const again = await post(`/api/user-libraries/${lib.id}/refresh`, {})
  assert.equal(again.status, 200)
  assert.deepEqual([again.body.state, again.body.error, again.body.directories], ['ready', null, 2])
  assert.equal(U.sshAllowed('someone'), false)
  await assert.rejects(U.addFromSsh('someone', { ...linux, indexRoot: index }), /sign-in off/)
  assert.equal(U.publicUserLibrary({ id: 'u-old', label: 'old', site: 'cluster-a', resource: 'pw://jdoe/cluster-a', sourceDir: '$WORKDIR', sourceRoot: null, indexRoot: null, gufiBin: null, state: 'building', run: null, error: null, builtAt: null, directories: null, files: null, scheduler: null }).transport, 'pw',
    'a library saved before ssh is a cluster one')
  const del = await app.inject({ method: 'DELETE', url: `/api/user-libraries/${lib.id}` })
  assert.equal(del.statusCode, 200)
  assert.equal(L.listLibraries('kb', null).some(l => l.id === lib.id), false)
})
