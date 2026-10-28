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
// ssh's sockets (socketDir in remoteExec.ts) go in a folder of this test's
// own, not the user's runtime folder.
process.env.XDG_RUNTIME_DIR = fs.mkdtempSync('/tmp/sshrun-')
test.after(() => fs.rmSync(process.env.XDG_RUNTIME_DIR, { recursive: true, force: true }))

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

// A host with three processors whose gufi_query notes its arguments and,
// while slow.on exists, answers two seconds late; one with 64 processors;
// and one where no processor count can be read, which notes each try.
const slowGufi = path.join(dir, 'slow-gufi')
const slowLog = path.join(dir, 'slow.log'), slowOn = path.join(dir, 'slow.on'), cpuLog = path.join(dir, 'cpu.log')
exe(path.join(slowGufi, 'bin', 'gufi_query'), `#!/bin/sh\nprintf '%s\\n' "$*" >> ${slowLog}\n[ -f ${slowOn} ] && sleep 2\nexec ${gufiBin}/gufi_query "$@"\n`)
exe(path.join(dir, 'cpus-3', 'nproc'), '#!/bin/sh\necho 3\n')
exe(path.join(dir, 'cpus-64', 'nproc'), '#!/bin/sh\necho 64\n')
for (const tool of ['nproc', 'getconf', 'sysctl']) exe(path.join(dir, 'cpus-none', tool), `#!/bin/sh\necho ${tool} >> ${cpuLog}\nexit 1\n`)

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
  const first = { 'me@mac-box': [path.join(dir, 'mac-bin'), path.join(macGufi, 'bin')], 'me@slow-box': [path.join(dir, 'cpus-3')],
    'me@many-box': [path.join(dir, 'cpus-64')], 'me@none-box': [path.join(dir, 'cpus-none')] }[dest] ?? []
  const PATH = [...first, systemPath].join(':')
  return spawn('/bin/sh', ['-c', argv[argv.length - 1]], { stdio: ['pipe', 'pipe', 'pipe'], env: { HOME: home, PATH } })
})
const U = await import('../dist/userLibraries.js')
const L = await import('../dist/libraries.js')
const G = await import('../dist/gufi.js')
const V = await import('../dist/gufiVt.js')
const Q = await import('../dist/query.js')
const S = await import('../dist/libraryScope.js')
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
  const argv = mine[0].argv
  assert.deepEqual(argv.slice(0, 9), ['-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=30', '-o', 'Compression=yes'])
  // The connection is shared through a socket in a folder of the user's own.
  assert.deepEqual([argv[10], argv[14]], ['ControlMaster=auto', 'ControlPersist=600'])
  assert.ok(argv[12].startsWith(`ControlPath="${R.socketDir()}/`), argv[12])
  assert.deepEqual(argv.slice(15, 18), ['-p', '2222', 'me@linux-box'])
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

test('read_kb_file gives a document\'s text from the index on that host, and says so plainly when there is none', { skip }, async () => {
  // The files are on the host; their text is in the index's words, as a
  // build there leaves it. The Studio opens only plain text files there.
  write(path.join(home, 'proj', 'data', 'report.docx'), 'PK\x03\x04 not text')
  write(path.join(home, 'proj', 'data', 'photo.png'), '\x89PNG not text')
  execFileSync('python3', ['-c', `
import sqlite3, sys
db = sqlite3.connect(sys.argv[1])
db.execute('CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf)')
db.execute("INSERT INTO words VALUES ('1', 'report.docx', ?)", ('<!-- extracted by downmark 0.10.0 -->\\n\\n# Report\\n\\nIt\\'s the quarterly report.\\n',))
db.commit()
`, path.join(index, 'data', 'db.db')])
  const { executeTool } = await import('../dist/chat/tools.js')
  const read = async p => executeTool('read_kb_file', JSON.stringify({ path: p, library: lib.id }), {})
  const doc = await read('data/report.docx')
  assert.equal(doc.result, "# Report\n\nIt's the quarterly report.\n")
  assert.match(doc.summary, /chars \(index\)/)
  const img = await read('data/photo.png')
  assert.equal(img.result, 'The text of data/photo.png is not available: it is an image in Remote project on me@linux-box, where the Studio opens only plain text files, and the index there holds no text for it. Answer from the search snippets, and say that the file itself was not read.')
  assert.equal(img.summary, 'no text')
  assert.equal((await read('readme.txt')).result, 'hello from the remote\n', 'text files as before')
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
  assert.equal(added.body.label, 'proj')
  // The tree runs gufi_query from a bash script, which macOS starts without
  // DYLD_* variables; the script sets the path itself.
  fs.rmSync(dyldLog)
  const mac = L.listLibraries('kb', null).find(l => l.id === added.body.id)
  assert.deepEqual((await U.remoteTree(mac.indexRoot, '')).map(e => e.name), ['data', 'readme.txt'])
  assert.deepEqual(await G.searchNames('notes', 5, mac.indexRoot).then(h => h.map(x => x.path)), ['data/notes.md'])
  assert.ok(lines().length >= 3 && lines().every(l => l === path.join(macGufi, 'lib')), lines().join(' '))
})

test('a remote gufi_query runs with the host\'s processors up to 16, or STUDIO_REMOTE_THREADS', { skip }, async () => {
  const threads = dest => R.remoteThreads(R.sshSite(dest, null), '')
  assert.equal(await threads('me@slow-box'), 3)
  assert.equal(await threads('me@many-box'), 16)
  assert.equal(await threads('me@none-box'), 4)
  assert.equal(await threads('me@none-box'), 4)
  assert.deepEqual(fs.readFileSync(cpuLog, 'utf8').trim().split('\n'), ['nproc', 'getconf', 'sysctl', 'nproc', 'getconf', 'sysctl'],
    'a count that could not be read is asked again')
  process.env.STUDIO_REMOTE_THREADS = '6'
  try { assert.equal(await threads('me@many-box'), 6) } finally { delete process.env.STUDIO_REMOTE_THREADS }
})

test('adding takes the test that just passed, and a search across libraries leaves out one that is late', { skip }, async () => {
  const spec = { destination: 'me@slow-box', label: 'Slow index', indexRoot: index, gufiBin: path.join(slowGufi, 'bin') }
  const counts = () => fs.readFileSync(slowLog, 'utf8').split('\n').filter(l => l.includes('vrsummary'))
  assert.equal((await probe(spec)).ok, true)
  assert.equal(counts().length, 1)
  assert.match(counts()[0], /^-n 3 /, 'the count runs with the host\'s three processors')
  const added = await post('/api/user-libraries/ssh', spec)
  assert.equal(added.status, 200, JSON.stringify(added.body))
  assert.equal(counts().length, 1, 'the add did not count the index again')

  const libs = L.listLibraries('kb', null)
  const slow = libs.find(l => l.id === added.body.id)
  fs.writeFileSync(slowOn, '')
  try {
    const late = []
    const hits = await S.searchScope([lib, slow], 'notes', 10, { semantic: false, deadlineMs: 500, slow: late })
    assert.deepEqual(late, ['Slow index'])
    assert.deepEqual(hits.map(h => [h.library.id, h.path]), [[lib.id, 'data/notes.md']])
    // Searched alone, it is waited for.
    const alone = await S.searchScope([slow], 'notes', 10, { semantic: false, deadlineMs: 500, slow: late })
    assert.deepEqual(alone.map(h => h.path), ['data/notes.md'])
    assert.deepEqual(late, ['Slow index'])
  } finally { fs.rmSync(slowOn, { force: true }) }
  assert.ok(fs.readFileSync(slowLog, 'utf8').split('\n').filter(l => l.includes('notes')).every(l => /(^| )-n 3 /.test(l)))

  // Refresh tests again.
  assert.equal((await post(`/api/user-libraries/${slow.id}/refresh`, {})).status, 200)
  assert.equal(counts().length, 2)
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
