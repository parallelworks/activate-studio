// An existing GUFI index on a host whose login runs only gufi_query: a
// forced command or a restricted shell that refuses everything else,
// including bash, env, sh -c, and a path to a program. The host is local:
// the transport's spawn seam runs each ssh command through a fake login
// that writes OpenSSH's post-quantum warning on stderr (as ssh does on
// many hosts), logs the command line, and runs it only when its first word
// is gufi_query, from the login's PATH. A second host has a shell but no
// bash. The Studio must connect, browse, describe, search, query (gufi_vt)
// and refresh there, after a restart too, and must refuse a build there
// with a clear reason. Needs GUFI (GUFI_BIN, else /opt/gufi/bin).
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

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'qonly-'))
const port = await new Promise(resolve => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)) }) })
process.env.PORT = String(port)
process.env.HOST = '127.0.0.1'
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.XDG_RUNTIME_DIR = fs.mkdtempSync('/tmp/qonlyrun-')
test.after(() => fs.rmSync(process.env.XDG_RUNTIME_DIR, { recursive: true, force: true }))

const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }

write(path.join(dir, 'kb', 'note.txt'), 'kb note\n')
const home = path.join(dir, 'home')
write(path.join(home, 'proj', 'readme.txt'), 'hello from the remote\n')
write(path.join(home, 'proj', 'data', 'huge.bin'), Buffer.alloc(5000, 98))
write(path.join(home, 'proj', 'data', 'notes.md'), '# notes\n')
fs.mkdirSync(path.join(home, 'idx'), { recursive: true })
fs.mkdirSync(path.join(home, 'plain'), { recursive: true })
fs.mkdirSync(path.join(dir, 'ix', 'gufi'), { recursive: true })
const index = path.join(home, 'idx', 'proj')
if (haveGufi) {
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(home, 'proj'), path.join(home, 'idx')], { stdio: 'ignore' })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'kb'), path.join(dir, 'ix', 'gufi')], { stdio: 'ignore' })
  // Full-text tables in the data folder's database, as the Studio's text
  // pass writes them: the words of notes.md under its inode.
  const db = path.join(index, 'data', 'db.db')
  execFileSync(path.join(gufiBin, 'gufi_sqlite3'), [db,
    "CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf); INSERT INTO words SELECT inode, name, 'quokka field notes' FROM entries WHERE name = 'notes.md';"])
}

// The fake logins. CMD is the command line ssh sends; the log keeps each.
const log = path.join(dir, 'commands.log')
const WARN = `printf '%s\\n' '** WARNING: connection is not using a post-quantum key exchange algorithm.' '** This session may be vulnerable to "store now, decrypt later" attacks.' >&2`
const QUERY_ONLY = `${WARN}
printf '%s\\n' "$CMD" >> ${log}
case "$CMD" in
  gufi_query|"gufi_query "*) eval "$CMD" ;;
  */*) echo "restricted: cannot specify '/' in command names" >&2; exit 1 ;;
  *) echo "This account may run only gufi_query." >&2; exit 1 ;;
esac`
const NO_BASH = `case "$CMD" in
  bash*) echo "sh: 1: bash: not found" >&2; exit 127 ;;
  *) eval "$CMD" ;;
esac`
const R = await import('../dist/remoteExec.js')
// A cluster over pw ssh whose login has no bash: pw ssh reports the
// remote error on stderr, without ssh's exit status 255 to tell them apart.
const PW_NO_BASH = `case "$CMD" in
  bash*) echo "bash: command not found" >&2; exit 127 ;;
  *) eval "$CMD" ;;
esac`
R.setRemoteSpawn((site, key, command) => {
  const dest = R.parseSshSite(site)?.destination ?? `pw:${site}`
  const script = { 'me@query-only': QUERY_ONLY, 'me@no-bash': NO_BASH, 'pw:cluster-q': PW_NO_BASH }[dest] ?? 'exit 255'
  return spawn('/bin/sh', ['-c', script], { stdio: ['pipe', 'pipe', 'pipe'], env: { HOME: home, PATH: `${gufiBin}:/usr/bin:/bin`, CMD: command } })
})
const U = await import('../dist/userLibraries.js')
const L = await import('../dist/libraries.js')
const G = await import('../dist/gufi.js')
const V = await import('../dist/gufiVt.js')
const Q = await import('../dist/query.js')
const S = await import('../dist/libraryScope.js')
const D = await import('../dist/libraryDetails.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
app.setErrorHandler((err, _req, reply) => reply.status(err.status ?? 500).send({ error: err.message }))
await app.register(U.userLibraryRoutes)
await app.register(V.gufiVtRoutes)
await app.listen({ port, host: '127.0.0.1' })
test.after(async () => { R.closeRemoteChannels(); await app.close() })

const post = async (url, body) => { const r = await app.inject({ method: 'POST', url, payload: body }); return { status: r.statusCode, body: r.json() } }
const probe = body => post('/api/user-libraries/ssh/test', body).then(r => r.body)
const host = { destination: 'me@query-only' }
const commands = () => (fs.existsSync(log) ? fs.readFileSync(log, 'utf8').split('\n').filter(Boolean) : [])
const clearLog = () => fs.rmSync(log, { force: true })
let lib

test('arguments are written as shell words that a POSIX shell reads back as they were', () => {
  const argv = ['printf', '%s|', '-n', '/a b/c', "it's", 'SELECT "x" FROM t WHERE a = \'1\';', '$HOME `id`', '\x1e', '']
  const out = execFileSync('/bin/sh', ['-c', R.directCommand(argv)], { encoding: 'utf8' })
  assert.equal(out, argv.slice(2).map(a => `${a}|`).join(''))
  // The program and plain words stay bare, so a forced command that looks
  // for a leading "gufi_query " finds it.
  assert.equal(R.directCommand(['gufi_query', '-n', '4', '-E', 'SELECT 1;', '/search']), "gufi_query -n 4 -E 'SELECT 1;' /search")
  assert.equal(R.quietStderr('** WARNING: connection is not using a post-quantum key exchange algorithm.\n** The server may need to be upgraded.\nreal error\n'), 'real error')
})

test('the connection test passes where the login runs only gufi_query, and says what that allows', { skip }, async () => {
  const r = await probe({ ...host, indexRoot: index })
  assert.equal(r.ok, true, JSON.stringify(r.steps))
  assert.deepEqual(r.steps.map(s => [s.name, s.ok, !!s.warn]), [['ssh', true, true], ['gufi', true, false], ['index', true, false]])
  assert.match(r.steps[0].detail, /^Connected to me@query-only\. This host runs only gufi_query: connect existing indexes read only; building an index there is not available\.$/)
  assert.match(r.steps[1].detail, /^gufi_query \S+ on the login's PATH\.$/)
  assert.match(r.steps[2].detail, /2 directories and 3 files, with full-text tables/)
  assert.equal(r.shell, false)
  assert.equal(r.gufiBin, '')
  // The session was tried once and refused; from then on every command
  // was gufi_query, bare, starting with its version.
  const sent = commands()
  assert.match(sent[0], /^bash -c /)
  assert.equal(sent[1], 'gufi_query --version')
  assert.deepEqual(sent.slice(1).filter(c => !c.startsWith('gufi_query ')), [])
})

test('each failing step on such a host says what to change', { skip }, async () => {
  const step = async (body, name, re) => {
    const r = await probe({ ...host, ...body })
    assert.equal(r.ok, false)
    const s = r.steps.find(x => x.name === name)
    assert.equal(s.ok, false, JSON.stringify(r.steps))
    assert.match(s.detail, re)
  }
  await step({ indexRoot: '~/idx/proj' }, 'index', /runs no shell, so ~ cannot be resolved there: give the index directory as an absolute path/)
  await step({ indexRoot: path.join(home, 'nowhere') }, 'index', /is not a directory on me@query-only/)
  await step({ indexRoot: path.join(home, 'plain') }, 'index', /has no db\.db the login can read, so it is not the top of a GUFI index/)
  // A path to gufi_query is refused by a restricted login: leave it empty.
  await step({ indexRoot: index, gufiBin: gufiBin }, 'gufi', /did not run on me@query-only: restricted: cannot specify.*leave the GUFI bin directory empty/)
  await step({ indexRoot: index, gufiBin: '~/bin' }, 'gufi', /~ cannot be resolved there/)
  // A source directory cannot be checked or read there: a note, not a failure.
  const r = await probe({ ...host, indexRoot: index, sourceRoot: path.join(home, 'proj') })
  assert.equal(r.ok, true)
  const src = r.steps.find(s => s.name === 'source')
  assert.equal(src.warn, true)
  assert.match(src.detail, /runs only gufi_query, so files cannot be opened from the Studio/)
})

test('added, the library is browsed, described, searched by name and words, and queried with gufi_query alone', { skip }, async () => {
  const added = await post('/api/user-libraries/ssh', { ...host, label: 'Restricted', indexRoot: index, sourceRoot: path.join(home, 'proj') })
  assert.equal(added.status, 200, JSON.stringify(added.body))
  const def = U.loadUserLibraries('single').find(d => d.id === added.body.id)
  assert.equal(def.shell, false)
  assert.equal(def.gufiBin, '')
  assert.equal(def.fullText, true)
  assert.equal(def.sourceRoot, null)
  lib = L.listLibraries('kb', null).find(l => l.id === added.body.id)
  assert.ok(lib, 'listed')
  assert.equal(lib.caps.fullText, true)

  clearLog()
  assert.deepEqual((await U.remoteTree(lib.indexRoot, '')).map(e => e.name), ['data', 'readme.txt'])
  assert.deepEqual((await U.remoteTree(lib.indexRoot, 'data')).map(e => [e.name, e.size]), [['huge.bin', 5000], ['notes.md', 8]])
  const card = await D.describe(lib, 'data/notes.md', null)
  assert.equal(card.name, 'notes.md')
  assert.equal((await G.searchNames('huge', 5, lib.indexRoot)).map(h => h.path).join(), 'data/huge.bin')
  const stats = await G.corpusStats(lib.indexRoot)
  assert.equal(stats.files, 3)
  const omitted = []
  const words = await S.searchScope([lib], 'quokka', 10, { semantic: false, omitted })
  assert.ok(words.some(h => h.path === 'data/notes.md'), JSON.stringify(words))
  assert.deepEqual(omitted, [])
  // gufi_vt over the main index and this host, where gufi_sqlite3 has it.
  const vt = await Q.queryScope({ canned: 'largest', n: 2, library: 'all' }, null)
  assert.deepEqual(vt.failed ?? [], [])
  assert.ok(vt.rows.some(r => r[0] === 'Restricted'), JSON.stringify(vt.rows))
  if (await V.gufiVtAvailable()) assert.equal(vt.engine, 'gufi_vt')
  // Nothing but gufi_query reached the host, and nothing tried the shell
  // again once the library said the login has none.
  const sent = commands()
  assert.ok(sent.length >= 6)
  assert.deepEqual(sent.filter(c => !c.startsWith('gufi_query ')), [])
  // Files cannot be read there, and the reason says so.
  await assert.rejects(U.remoteFile(lib.indexRoot, 'readme.txt'), /runs only gufi_query for this login, so its files cannot be opened/)
  await assert.rejects(U.remoteDownload(lib.indexRoot, 'data/huge.bin'), /runs only gufi_query/)
})

test('refresh checks again, and after a restart the first command is gufi_query, not the shell', { skip }, async () => {
  const again = await post(`/api/user-libraries/${lib.id}/refresh`, {})
  assert.equal(again.status, 200, JSON.stringify(again.body))
  const def = U.loadUserLibraries('single').find(d => d.id === lib.id)
  assert.equal(def.shell, false)
  assert.equal(def.error, null)
  // A restart: the sessions and what was learned about hosts are gone.
  R.closeRemoteChannels()
  clearLog()
  assert.deepEqual((await U.remoteTree(lib.indexRoot, '')).map(e => e.name), ['data', 'readme.txt'])
  assert.deepEqual(commands().filter(c => !c.startsWith('gufi_query ')), [])
})

test('a build on such a host is refused before it starts, with the reason', { skip }, async () => {
  const r = await post('/api/user-libraries/ssh/build/test', { ...host, sourceRoot: path.join(home, 'proj') })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  assert.equal(r.body.ok, false)
  assert.deepEqual(r.body.steps.map(s => [s.name, s.ok]), [['ssh', false]])
  assert.equal(r.body.steps[0].detail, U.ONLY_GUFI_QUERY)
})

test('a host with a shell but no bash is treated the same way', { skip }, async () => {
  const r = await probe({ destination: 'me@no-bash', indexRoot: index })
  assert.equal(r.ok, true, JSON.stringify(r.steps))
  assert.equal(r.shell, false)
  assert.match(r.steps[0].detail, /This host runs only gufi_query/)
  assert.match(r.steps[2].detail, /2 directories and 3 files/)
})

test('over pw ssh a login without bash is found the same way, and only query tools run there', { skip }, async () => {
  const site = 'cluster-q'
  const v = await R.remoteRun(site, 'key', ['gufi_query', '--version'])
  assert.equal(v.code, 0)
  assert.match(v.stdout.trim(), /^\S+$/)
  assert.equal(R.isDirect(site), true)
  await assert.rejects(R.remoteRun(site, 'key', ['sh', '-c', 'true']), /cluster-q runs only gufi_query for this login \(no shell\)/)
  assert.equal(await R.remoteThreads(site, 'key'), 4)
})
