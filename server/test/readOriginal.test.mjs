// Reading files for the assistant: read_kb_file returns the text the index
// holds (and works when the original cannot be reached), read_original opens
// the original at the library's own location and then on the host the
// library names for reading originals. Nothing here needs GUFI or a network:
// a stand-in gufi_query answers from per-folder sqlite databases, and each
// ssh host is a local shell reached through the transport's spawn seam, with
// a home of its own, as sshd would run a login. The host that holds only the
// index runs only gufi_query, as a restricted login does; the host that has
// the files runs a shell. Needs python3 with sqlite3 (fts5), as the
// extraction tests do.
import test from 'node:test'
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { execFileSync, spawn } from 'node:child_process'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'readorig-'))
process.env.HOST = '127.0.0.1'
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.GUFI_BIN = path.join(dir, 'fake-gufi')
process.env.XDG_RUNTIME_DIR = fs.mkdtempSync('/tmp/readorigrun-')
test.after(() => { fs.rmSync(process.env.XDG_RUNTIME_DIR, { recursive: true, force: true }); fs.rmSync(dir, { recursive: true, force: true }) })

const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
const exe = (f, body) => { write(f, body); fs.chmodSync(f, 0o755) }

// gufi_query, answering what the Studio asks of a words table: the SQL after
// -E, run against <folder>/db.db, one row a line. A missing table is an
// error on stderr with exit status 0, as GUFI does it.
exe(path.join(dir, 'fake-gufi', 'gufi_query'), `#!/usr/bin/env python3
import sqlite3, sys
a = sys.argv[1:]
if a[:1] == ['--version']:
    print('fake 0.0'); sys.exit(0)
sql = None
i = 0
while i < len(a) - 1:
    if a[i] == '-E': sql = a[i + 1]; i += 2
    elif a[i].startswith('-'): i += 2
    else: i += 1
db = sqlite3.connect('file:' + a[-1] + '/db.db?mode=ro', uri=True)
try:
    for row in db.execute(sql): print('|'.join(str(c) for c in row))
except sqlite3.Error as e:
    print('Error: %s' % e, file=sys.stderr)
`)

/** A folder's index database: the words rows [name, text], as the Studio's text pass writes them. */
const indexDir = (folder, rows) => {
  fs.mkdirSync(folder, { recursive: true })
  execFileSync('python3', ['-c', `
import sqlite3, sys, json
db = sqlite3.connect(sys.argv[1] + '/db.db')
db.execute('CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf)')
for n, (name, text) in enumerate(json.load(sys.stdin)):
    db.execute('INSERT INTO words VALUES (?, ?, ?)', (str(n), name, text))
db.commit()
`, folder], { input: JSON.stringify(rows) })
}

const LONG = 'x'.repeat(500_000)
// ---- a library on this computer: its index, and the files, some of which are not there ----
const docsIndex = path.join(dir, 'docs-index'), docsFiles = path.join(dir, 'docs-files')
indexDir(docsIndex, [['top.md', 'top, from the index\n']])
indexDir(path.join(docsIndex, 'notes'), [
  ['plan.md', 'plan as indexed (old)\n'], ['gone.md', 'gone.md text, only in the index\n'], ['big.txt', LONG],
  ['report.docx', '<!-- extracted by downmark 0.10.0 -->\n\n# Report\n\nquarterly figures\n'],
])
write(path.join(docsFiles, 'notes', 'plan.md'), 'plan as it is now (new)\n')
write(path.join(docsFiles, 'notes', 'unindexed.md'), 'on disk, not in the index\n')
write(path.join(docsFiles, 'notes', 'report.docx'), 'PK not text')
write(path.join(docsFiles, 'notes', 'scan.pdf'), '%PDF not text')
write(path.join(docsFiles, 'notes', 'long.md'), 'L'.repeat(30_000))
write(path.join(docsFiles, 'secret', 'outside.md'), 'not for the library')
// An index that has no words table at all.
const bareIndex = path.join(dir, 'bare-index')
fs.mkdirSync(path.join(bareIndex, 'notes'), { recursive: true })
for (const d of [bareIndex, path.join(bareIndex, 'notes')]) execFileSync('python3', ['-c', 'import sqlite3,sys; sqlite3.connect(sys.argv[1]+"/db.db").execute("CREATE TABLE t(x)")', d])
// A library with an index and no files here.
const indexOnly = path.join(dir, 'indexonly-index')
indexDir(indexOnly, [['a.md', 'a, from the index\n']])

fs.mkdirSync(process.env.KB_ROOT, { recursive: true })
write(path.join(dir, 'ix', 'libraries.json'), JSON.stringify([
  { id: 'docs', label: 'Docs', indexRoot: docsIndex, sourceRoot: docsFiles },
  { id: 'bare', label: 'Bare', indexRoot: bareIndex, sourceRoot: docsFiles },
  { id: 'idxonly', label: 'Index only', indexRoot: indexOnly, sourceRoot: null },
  { id: 'throughlocal', label: 'Through', indexRoot: indexOnly, sourceRoot: null, readThrough: { destination: 'me@xfer', root: path.join(dir, 'xfer', 'data') } },
]))

// ---- hosts over ssh ----
const projFiles = path.join(dir, 'gufihost', 'proj')           // the path the index host knows; nothing is there
const xferFiles = path.join(dir, 'xfer', 'data')                  // where the file transfer host mounts the same files
const odd = ['with space.md', "it's.md", 'quote".md', 'café ☕.md', '-dash.md', '$(touch pwned).md', '; touch pwned2.md', 'a\\b.md']
const remoteIndex = path.join(dir, 'gufihost', 'idx')
indexDir(remoteIndex, [['gufi-only.md', 'indexed on the gufi host\n']])
indexDir(path.join(remoteIndex, 'sub'), [['deep.md', 'deep, from the index\n']])
write(path.join(xferFiles, 'readme.md'), 'readme on the transfer host\n')
write(path.join(xferFiles, 'gufi-only.md'), 'the original of gufi-only\n')
write(path.join(xferFiles, 'sub', 'deep.md'), 'deep original\n')
write(path.join(xferFiles, 'sub', 'pic.png'), '\x89PNG not text')
write(path.join(xferFiles, 'sub', 'doc.docx'), 'PK not text')
for (const n of odd) write(path.join(xferFiles, 'sub', n), `contents of ${n}\n`)
write(path.join(dir, 'xfer', 'etc', 'passwd'), 'not the library\n')
// The same files, mounted at the path the library knows, on a second host.
const mountedFiles = path.join(dir, 'mounted', 'proj')
write(path.join(mountedFiles, 'readme.md'), 'readme on a host with the same path\n')

const log = path.join(dir, 'commands.log')
const QUERY_ONLY = `printf '%s\\n' "$CMD" >> ${log}
case "$CMD" in
  gufi_query|"gufi_query "*) eval "$CMD" ;;
  */*) echo "restricted: cannot specify '/' in command names" >&2; exit 1 ;;
  *) echo "This account may run only gufi_query." >&2; exit 1 ;;
esac`
const SHELL_HOST = 'eval "$CMD"'
const DOWN = 'echo "ssh: connect to host xfer-down port 22: Connection refused" >&2; exit 255'
const hosts = {
  'me@gufi-host': QUERY_ONLY,
  'me@xfer': SHELL_HOST,
  'me@xfer-too': SHELL_HOST,
  'me@xfer-down': DOWN,
  'me@restricted-xfer': QUERY_ONLY,
}
const R = await import('../dist/remoteExec.js')
const seen = []
R.setRemoteSpawn((site, key, command) => {
  const h = R.parseSshSite(site)
  seen.push(h)
  const script = hosts[h.destination] ?? DOWN
  return spawn('/bin/sh', ['-c', script], { stdio: ['pipe', 'pipe', 'pipe'], env: { HOME: path.join(dir, 'home'), PATH: `${process.env.GUFI_BIN}:/usr/bin:/bin`, CMD: command } })
})
test.after(() => R.closeRemoteChannels())

// The library over ssh whose index host runs only gufi_query.
const owner = 'single'
const ownerHash = crypto.createHash('sha256').update(owner).digest('hex').slice(0, 16)
const remoteDef = (id, extra = {}) => ({
  id, label: `Proj ${id}`, transport: 'ssh', site: 'me@gufi-host', destination: 'me@gufi-host', port: null, resource: 'x', sourceDir: projFiles,
  sourceRoot: projFiles, indexRoot: remoteIndex, gufiBin: '', shell: false, fullText: true, state: 'ready', run: null, error: null,
  builtAt: new Date().toISOString(), directories: 2, files: 3, scheduler: null, ...extra,
})
const libsFile = path.join(dir, 'ix', 'users', ownerHash, 'libraries.json')
const saveRemote = defs => { write(libsFile, JSON.stringify(defs)); write(path.join(dir, 'ix', 'users', ownerHash, 'owner'), owner) }
saveRemote([
  remoteDef('u-plain'),
  remoteDef('u-through', { readThrough: { destination: 'me@xfer', root: xferFiles } }),
  remoteDef('u-same-path', { sourceRoot: mountedFiles, readThrough: { destination: 'me@xfer-too' } }),
  remoteDef('u-down', { readThrough: { destination: 'me@xfer-down', root: xferFiles } }),
  remoteDef('u-restricted', { readThrough: { destination: 'me@restricted-xfer', root: xferFiles } }),
  remoteDef('u-badroot', { readThrough: { destination: 'me@xfer' } , sourceRoot: null }),
])

const L = await import('../dist/libraries.js')
const O = await import('../dist/originals.js')
const { executeTool, TOOL_SPECS } = await import('../dist/chat/tools.js')
const { default: Fastify } = await import('fastify')
const { kbRoutes } = await import('../dist/routes.js')
const { userLibraryRoutes } = await import('../dist/userLibraries.js')
for (const id of ['docs', 'bare', 'idxonly', 'throughlocal']) {
  const l = L.listLibraries().find(x => x.id === id)
  L.setKnownCapsForTests(l.indexRoot, { index: true, fullText: id !== 'bare', vectors: false })
}

const call = (name, args, ctx = {}) => executeTool(name, JSON.stringify(args), ctx)
const read = (args, ctx) => call('read_kb_file', args, ctx)
const orig = (args, ctx) => call('read_original', args, ctx)
const body = r => r.result.split('\n').slice(1).join('\n')

// ---- read_kb_file: the text the index holds ----

test('read_kb_file gives the text in the index, and says it came from there', async () => {
  const r = await read({ path: 'notes/plan.md', library: 'docs' })
  assert.equal(r.result, '[source: index; it can lag behind the file]\nplan as indexed (old)\n')
  assert.match(r.summary, /chars \(index\)/)
  assert.deepEqual(r.files, [{ path: 'notes/plan.md', library: 'docs' }])
})

test('read_kb_file works with the original missing, and when the library has no files here at all', async () => {
  assert.equal(fs.existsSync(path.join(docsFiles, 'notes', 'gone.md')), false)
  assert.equal(body(await read({ path: 'notes/gone.md', library: 'docs' })), 'gone.md text, only in the index\n')
  assert.equal(body(await read({ path: 'a.md', library: 'idxonly' })), 'a, from the index\n')
  // A document's text, with no converter line, though its file is not text.
  assert.equal(body(await read({ path: 'notes/report.docx', library: 'docs' })), '# Report\n\nquarterly figures\n')
})

test('read_kb_file says so when the index keeps only the start of a long file', async () => {
  const r = await read({ path: 'notes/big.txt', library: 'docs' })
  assert.match(r.result, /^\[source: index; the index keeps at most the first 500,000 characters of a file\]\nx{100}/)
  assert.match(r.result, /\[truncated; continue with offset=24000\]$/)
  const more = await read({ path: 'notes/big.txt', library: 'docs', offset: 24_000 })
  assert.match(more.result, /^\[source: index.*\]\nx{100}/)
})

test('read_kb_file says plainly when the index holds no text for a file it cannot read as text', async () => {
  const r = await read({ path: 'notes/scan.pdf', library: 'docs' })
  assert.match(r.result, /^No text has been read out of notes\/scan\.pdf \(a document, \d+ bytes\): Docs has not extracted it, or could not\. Answer from the search snippets, and say that the file itself was not read\.$/)
  assert.equal(r.summary, 'no text')
  // No text in the index, no extraction, and no file: the reasons, not an empty answer.
  const none = await read({ path: 'nothing.pdf', library: 'idxonly' })
  assert.match(none.result, /^Tool error: The index holds no text for it, and the original could not be reached\. nothing\.pdf could not be opened\. Tried only this computer: this library has no files here\./)
})

test('a plain text file the index has no text for is read from the original, and the result says so', async () => {
  const r = await read({ path: 'notes/unindexed.md', library: 'docs' })
  assert.equal(r.result, '[source: the original file on this computer; the index holds no text for it]\non disk, not in the index\n')
  assert.match(r.summary, /\(original\)/)
  // An index with no words table at all: the file is read as it is.
  const bare = await read({ path: 'notes/plan.md', library: 'bare' })
  assert.equal(bare.result, '[source: the original file on this computer; the index holds no text for it]\nplan as it is now (new)\n')
})

test('read_kb_file names the reason when neither the index nor the original has the file', async () => {
  const r = await read({ paths: ['notes/missing.md'], library: 'docs' })
  assert.match(r.result, /^### notes\/missing\.md\nnot read: The index holds no text for it, and the original could not be reached\. notes\/missing\.md could not be opened\. Tried only this computer: no such file\./)
})

test('read_kb_file reads several files in one call, each with its source, and a failure does not stop the rest', async () => {
  const r = await read({ paths: ['notes/plan.md', 'notes/unindexed.md', 'notes/gone.md', 'notes/nope.md', '../outside.md'], library: 'docs' })
  const parts = r.result.split(/\n\n(?=### )/)
  assert.equal(parts.length, 5)
  assert.match(parts[0], /^### notes\/plan\.md\n\[source: index.*\]\nplan as indexed/)
  assert.match(parts[1], /^### notes\/unindexed\.md\n\[source: the original file on this computer.*\]\non disk/)
  assert.match(parts[2], /^### notes\/gone\.md\n\[source: index/)
  assert.match(parts[3], /^### notes\/nope\.md\nnot read: /)
  assert.match(parts[4], /^### \.\.\/outside\.md\nnot read: invalid path/)
  assert.equal(r.summary, '5 files')
  assert.deepEqual(r.files.map(f => f.path), ['notes/plan.md', 'notes/unindexed.md', 'notes/gone.md'])
})

// ---- read_original: this computer ----

test('read_original opens the file itself, not the index\'s copy, and says where it read it', async () => {
  const r = await orig({ path: 'notes/plan.md', library: 'docs' })
  assert.equal(r.result, '[source: the original file on this computer]\nplan as it is now (new)\n')
  assert.match(r.summary, /chars \(original\)/)
  // What the index holds and the file does not have.
  const gone = await orig({ path: 'notes/gone.md', library: 'docs' })
  assert.match(gone.result, /^Tool error: notes\/gone\.md could not be opened\. Tried only this computer: no such file\./)
})

test('read_original on a document or image here gives the extracted text it has, and says when there is none', async () => {
  const doc = await orig({ path: 'notes/scan.pdf', library: 'docs' })
  assert.match(doc.result, /^notes\/scan\.pdf is there \(a document, \d+ bytes on this computer\) but Docs has no extracted text for it, and reading a file does not extract it\. read_kb_file returns the index's text/)
  assert.equal(doc.summary, 'no text')
})

test('read_original with paths and offset reads several, and pages through a long file', async () => {
  const r = await orig({ paths: ['notes/plan.md', 'notes/nope.md'], library: 'docs' })
  assert.match(r.result, /^### notes\/plan\.md\n\[source: the original file on this computer\]\nplan as it is now \(new\)\n\n\n### notes\/nope\.md\nnot read: notes\/nope\.md could not be opened/)
  const first = await orig({ path: 'notes/long.md', library: 'docs' })
  assert.match(first.result, /\[truncated; continue with offset=24000\]$/)
  const rest = await orig({ path: 'notes/long.md', library: 'docs', offset: 24_000 })
  assert.equal(rest.result, `[source: the original file on this computer]\n${'L'.repeat(6000)}`)
})

test('paths that leave the library are refused, in one file and in several, on both tools', async () => {
  for (const bad of ['../secret/outside.md', 'notes/../../secret/outside.md', '..', 'notes/..']) {
    for (const tool of [orig, read]) {
      const r = await tool({ path: bad, library: 'docs' })
      assert.equal(r.summary, 'error', bad)
      assert.match(r.result, /^Tool error: invalid path/, bad)
      assert.doesNotMatch(r.result, /not for the library/)
    }
  }
  const many = await orig({ paths: ['../secret/outside.md', 'notes/plan.md'], library: 'docs' })
  assert.match(many.result, /### \.\.\/secret\/outside\.md\nnot read: invalid path/)
  assert.doesNotMatch(many.result, /not for the library/)
  // An absolute path is a path inside the library, never the host's.
  const abs = await orig({ path: '/etc/passwd', library: 'docs' })
  assert.doesNotMatch(abs.result, /root:/)
  assert.match(abs.result, /could not be opened/)
})

test('a library with no files here and no other host says what to do', async () => {
  const r = await orig({ path: 'a.md', library: 'idxonly' })
  assert.match(r.result, /^Tool error: Index only has no files on this host; it can be searched and read from its index \(read_kb_file\), but its originals cannot be opened\. Settings, Indexing, Libraries can name another host that has them\./)
})

// ---- read_original: a library over ssh, and the host named for reading originals ----

test('a library whose host runs only gufi_query reads from its index, and read_original says why it cannot open the file', async () => {
  const idx = await read({ path: 'gufi-only.md', library: 'u-plain' })
  assert.equal(idx.result, '[source: index; it can lag behind the file]\nindexed on the gufi host\n')
  const o = await orig({ path: 'gufi-only.md', library: 'u-plain' })
  assert.match(o.result, /^Tool error: gufi-only\.md could not be opened\. Tried only me@gufi-host: me@gufi-host runs only gufi_query for this login, so its files cannot be opened from the Studio; open them on that host\. Answer from/)
  // Every command that reached that host was gufi_query: nothing else was sent to it.
  const sent = fs.readFileSync(log, 'utf8').split('\n').filter(Boolean).filter(c => !c.startsWith('bash -c '))
  assert.ok(sent.length > 0 && sent.every(c => c.startsWith('gufi_query')), sent.join('\n'))
})

test('read_original falls back to the host named for reading originals, and says which host gave the file', async () => {
  const r = await orig({ path: 'gufi-only.md', library: 'u-through' })
  assert.equal(r.result, '[source: the original file on me@xfer, after me@gufi-host could not give it]\nthe original of gufi-only\n')
  assert.match(r.summary, /\(original\)/)
  // A file in a subfolder, and a library whose root has the same path on the other host.
  assert.equal(body(await orig({ path: 'sub/deep.md', library: 'u-through' })), 'deep original\n')
  assert.equal(body(await orig({ path: 'readme.md', library: 'u-same-path' })), 'readme on a host with the same path\n')
})

test('the host\'s own copy is tried first: a library host with a shell gives its file and the other host is not asked', async () => {
  const before = seen.length
  const saved = fs.readFileSync(libsFile, 'utf8')
  saveRemote([remoteDef('u-shell', { shell: undefined, destination: 'me@xfer', site: 'me@xfer', sourceRoot: xferFiles, readThrough: { destination: 'me@xfer-too', root: '/nowhere' } })])
  try {
    const r = await orig({ path: 'readme.md', library: 'u-shell' })
    assert.equal(r.result, '[source: the original file on me@xfer]\nreadme on the transfer host\n')
    assert.ok(seen.slice(before).every(h => h.destination === 'me@xfer'), 'only the library\'s own host was reached')
  } finally {
    write(libsFile, saved)
  }
})

test('when both hosts fail, the message names each host and why', async () => {
  const r = await orig({ path: 'nothing-here.md', library: 'u-through' })
  assert.match(r.result, /^Tool error: nothing-here\.md could not be opened\. Tried me@gufi-host: .*only gufi_query.*; me@xfer: .*(No such file|cannot stat)/s)
  assert.match(r.result, /Answer from the search snippets or read_kb_file, and say that the file itself was not read\.$/)
  const down = await orig({ path: 'gufi-only.md', library: 'u-down' })
  assert.match(down.result, /Tried me@gufi-host: .*only gufi_query.*; me@xfer-down: .*Connection refused/s)
  const restricted = await orig({ path: 'gufi-only.md', library: 'u-restricted' })
  assert.match(restricted.result, /Tried me@gufi-host: .*; me@restricted-xfer: .*only gufi_query/s)
  // read_kb_file, for a file the index has no text of, says the same.
  const k = await read({ path: 'nothing-here.md', library: 'u-through' })
  assert.match(k.result, /^Tool error: The index holds no text for it, and the original could not be reached\. nothing-here\.md could not be opened\. Tried me@gufi-host/)
})

test('read_kb_file reads the index when the original is out of reach, and the original when a plain file has no index text', async () => {
  assert.equal(body(await read({ path: 'sub/deep.md', library: 'u-down' })), 'deep, from the index\n')
  const r = await read({ path: 'readme.md', library: 'u-through' })
  assert.equal(r.result, '[source: the original file on me@xfer; the index holds no text for it]\nreadme on the transfer host\n')
})

test('file names with spaces, quotes, non-ASCII, a leading dash, shell syntax, and a backslash are read as they are', async () => {
  for (const n of odd) {
    const r = await orig({ path: `sub/${n}`, library: 'u-through' })
    assert.equal(body(r), `contents of ${n}\n`, n)
  }
  assert.equal(fs.existsSync(path.join(process.cwd(), 'pwned')), false)
  for (const d of [xferFiles, path.join(xferFiles, 'sub'), process.cwd(), path.join(dir, 'home'), dir]) {
    assert.equal(fs.existsSync(path.join(d, 'pwned')) || fs.existsSync(path.join(d, 'pwned2')), false, `nothing was run in ${d}`)
  }
  // Several at once, with the odd names among them.
  const many = await orig({ paths: odd.map(n => `sub/${n}`), library: 'u-through' })
  for (const n of odd) assert.ok(many.result.includes(`### sub/${n}\n`) && many.result.includes(`contents of ${n}\n`), n)
  assert.equal(many.summary, `${odd.length} files`)
})

test('a file on the other host that is not text says what it is and where read_kb_file can help', async () => {
  const pic = await orig({ path: 'sub/pic.png', library: 'u-through' })
  assert.match(pic.result, /^sub\/pic\.png is there \(an image, \d+ bytes on me@xfer, after me@gufi-host could not give it\), but the Studio opens only plain text files over ssh and does not extract documents or images from another host\. read_kb_file returns the index's text for it/)
})

test('paths cannot leave the folder on the other host', async () => {
  for (const bad of ['../etc/passwd', 'sub/../../etc/passwd', '..']) {
    const r = await orig({ paths: [bad], library: 'u-through' })
    assert.match(r.result, /### .*\nnot read: invalid path/, bad)
  }
  const abs = await orig({ path: '/etc/passwd', library: 'u-through' })
  assert.doesNotMatch(abs.result, /root:|not the library/)
  await assert.rejects(O.openOriginal(L.listLibraries('kb', null).find(l => l.id === 'u-through'), '../etc/passwd'), /invalid path/)
})

test('a library with no folder on the other host says so', async () => {
  const r = await orig({ path: 'gufi-only.md', library: 'u-badroot' })
  assert.match(r.result, /Tried me@gufi-host: .*; me@xfer: .*folder on that host is not known/s)
})

test('a library here with a host named, and no files here, reads originals there', async () => {
  const r = await orig({ path: 'readme.md', library: 'throughlocal' })
  assert.equal(r.result, '[source: the original file on me@xfer, after this computer could not give it]\nreadme on the transfer host\n')
})

// ---- the host string and folder ----

test('a host that ssh could take for an option, or a shell could split, is refused', () => {
  const bad = ['-oProxyCommand=touch pwned', '-J evil', '--', '-', 'a b', 'host;id', 'host && id', '$(id)', '`id`', 'me@host\nid', 'me@', '@host', 'me@-host', 'ho$st', 'host|id', 'host>f', "ho'st", 'ho"st', 'host*', 'host?', 'ho\\st', 'host\tname', ' -oBad', 'ho\0st', 'me@host:99999', 'me@host:0', 'ssh://host', 'user@host/path', '[::1]']
  for (const destination of bad) assert.throws(() => O.parseReadThrough({ destination }), /destination|port|invalid/, JSON.stringify(destination))
  // What it accepts.
  for (const destination of ['me@xfer', 'xfer', 'me.name@xfer-1.example.org', 'alias_1', 'me@10.0.0.7']) {
    assert.equal(O.parseReadThrough({ destination }).destination, destination)
  }
  assert.deepEqual(O.parseReadThrough({ destination: 'me@xfer:2200' }), { destination: 'me@xfer', port: 2200, root: null })
  assert.equal(O.parseReadThrough({ destination: '' }), null)
  assert.equal(O.parseReadThrough({}), null)
  assert.equal(O.parseReadThrough(null), null)
})

test('ssh options for the host are held to what libraries over ssh may carry', () => {
  assert.deepEqual(O.parseReadThrough({ destination: 'xfer', sshOptions: '-J me@jump -o ConnectTimeout=20' }).sshOptions, ['-J', 'me@jump', '-o', 'ConnectTimeout=20'])
  for (const sshOptions of ['-o ProxyCommand=id', '-o LocalCommand=id', '-o RemoteCommand=id', '-J -oProxyCommand=id', '-F relative', "-i 'a b'", '-X', '-L 1:2:3', '-o Foo=bar', '-o Port=abc']) {
    assert.throws(() => O.parseReadThrough({ destination: 'xfer', sshOptions }), /ssh options/, sshOptions)
  }
})

test('the folder on the other host is an absolute path with no ..', () => {
  assert.equal(O.parseReadThrough({ destination: 'xfer', root: '/mnt//proj/' }).root, '/mnt/proj')
  assert.equal(O.parseReadThrough({ destination: 'xfer', root: '' }).root, null)
  assert.equal(O.parseReadThrough({ destination: 'xfer', root: '/path with space' }).root, '/path with space')
  for (const root of ['relative/dir', '~/proj', '/a/../b', '/a/..', '/a\nb', '/a\0b', `/${'x'.repeat(1100)}`]) {
    assert.throws(() => O.parseReadThrough({ destination: 'xfer', root }), /folder/, JSON.stringify(root))
  }
})

// ---- Settings: saving the host after a connection test, and the viewer ----

const app = Fastify()
app.setErrorHandler((err, _req, reply) => reply.status(err.status ?? 500).send({ error: err.message }))
await app.register(kbRoutes)
await app.register(userLibraryRoutes)
await app.register(O.originalsRoutes)
test.after(() => app.close())
const put = (id, payload) => app.inject({ method: 'PUT', url: `/api/libraries/${id}/read-through`, payload }).then(r => ({ status: r.statusCode, body: r.json() }))
const post = (id, payload) => app.inject({ method: 'POST', url: `/api/libraries/${id}/read-through/test`, payload }).then(r => ({ status: r.statusCode, body: r.json() }))

test('the connection test runs ssh, a shell, and the folder, and says what is wrong', async () => {
  const ok = await post('docs', { destination: 'me@xfer', root: xferFiles })
  assert.equal(ok.status, 200)
  assert.deepEqual(ok.body.steps.map(s => [s.name, s.ok]), [['ssh', true], ['source', true]])
  assert.match(ok.body.steps[0].detail, /^Connected to me@xfer/)
  assert.match(ok.body.steps[1].detail, new RegExp(`^${xferFiles} is readable on me@xfer\\.`))
  const nofolder = await post('docs', { destination: 'me@xfer', root: path.join(xferFiles, 'nowhere') })
  assert.equal(nofolder.body.ok, false)
  assert.match(nofolder.body.steps.at(-1).detail, /is not a directory on me@xfer/)
  const down = await post('docs', { destination: 'me@xfer-down', root: xferFiles })
  assert.equal(down.body.ok, false)
  assert.match(down.body.steps[0].detail, /Nothing accepted the connection/)
  const restricted = await post('docs', { destination: 'me@restricted-xfer', root: xferFiles })
  assert.equal(restricted.body.ok, false)
  assert.match(restricted.body.steps[0].detail, /runs only gufi_query for this login \(no shell\), so it cannot open files/)
  // No folder given and none of the library's own to use: ask for one.
  const none = await post('idxonly', { destination: 'me@xfer' })
  assert.match(none.body.steps.at(-1).detail, /give the folder on that host/)
  // Taken from the library's own folder when it has one, and a host's trouble is a 400 elsewhere.
  const own = await post('docs', { destination: 'me@xfer' })
  assert.equal(own.body.ok, true, 'the library\'s own folder, at the same path on that host')
  assert.match(own.body.steps.at(-1).detail, /is readable on me@xfer \(the same path as the library's own\)\.$/)
  assert.equal((await post('docs', { destination: '-oProxyCommand=id' })).status, 400)
})

test('saving needs a passing test, is kept with the library, and shows in the viewer\'s file and download', async () => {
  const bad = await put('idxonly', { destination: 'me@xfer-down', root: xferFiles })
  assert.equal(bad.status, 400)
  assert.match(bad.body.error, /^The connection test did not pass\. Nothing accepted the connection/)
  assert.equal(L.listLibraries().find(l => l.id === 'idxonly').readThrough ?? null, null, 'not saved')
  const inject = await put('idxonly', { destination: '-oProxyCommand=id', root: xferFiles })
  assert.equal(inject.status, 400)
  const ok = await put('idxonly', { destination: 'me@xfer', root: xferFiles })
  assert.equal(ok.status, 200, JSON.stringify(ok.body))
  assert.deepEqual(ok.body.readThrough, { destination: 'me@xfer', port: null, sshOptions: null, root: xferFiles })
  // Stored with the library's own settings.
  const stored = JSON.parse(fs.readFileSync(path.join(dir, 'ix', 'libraries.json'), 'utf8')).find(d => d.id === 'idxonly')
  assert.deepEqual(stored.readThrough, { destination: 'me@xfer', port: null, root: xferFiles })
  assert.equal(stored.sourceRoot, null)
  assert.equal(L.listLibraries().find(l => l.id === 'idxonly').readThrough.destination, 'me@xfer')
  const file = await app.inject({ method: 'GET', url: '/api/kb/file?library=idxonly&path=readme.md' })
  assert.equal(file.statusCode, 200, file.body)
  assert.equal(file.json().content, 'readme on the transfer host\n')
  const dl = await app.inject({ method: 'GET', url: '/api/kb/download?library=idxonly&path=sub/with%20space.md' })
  assert.equal(dl.statusCode, 200, dl.body)
  assert.equal(dl.body, 'contents of with space.md\n')
  const missing = await app.inject({ method: 'GET', url: '/api/kb/file?library=idxonly&path=nope.md' })
  assert.equal(missing.statusCode, 404)
  assert.match(missing.json().error, /^nope\.md could not be opened\. Tried this computer: this library has no files here; me@xfer: /)
  const outside = await app.inject({ method: 'GET', url: '/api/kb/file?library=idxonly&path=../etc/passwd' })
  assert.equal(outside.statusCode, 400)
  // The settings listing carries it, and clearing removes it.
  const cleared = await put('idxonly', { destination: '' })
  assert.deepEqual(cleared.body, { readThrough: null })
  assert.ok(!('readThrough' in JSON.parse(fs.readFileSync(path.join(dir, 'ix', 'libraries.json'), 'utf8')).find(d => d.id === 'idxonly')))
  const gone = await app.inject({ method: 'GET', url: '/api/kb/file?library=idxonly&path=readme.md' })
  assert.equal(gone.statusCode, 409)
})

test('a library over ssh keeps the host with its other settings, and the viewer reads through it', async () => {
  const ok = await put('u-plain', { destination: 'me@xfer', root: xferFiles })
  assert.equal(ok.status, 200, JSON.stringify(ok.body))
  const stored = JSON.parse(fs.readFileSync(libsFile, 'utf8')).find(d => d.id === 'u-plain')
  assert.equal(stored.readThrough.destination, 'me@xfer')
  assert.equal(stored.transport, 'ssh', 'its other settings stay')
  const listed = (await app.inject({ method: 'GET', url: '/api/user-libraries' })).json().libraries.find(l => l.id === 'u-plain')
  assert.deepEqual(listed.readThrough, { destination: 'me@xfer', port: null, sshOptions: null, root: xferFiles })
  const file = await app.inject({ method: 'GET', url: '/api/kb/file?library=u-plain&path=sub/deep.md' })
  assert.equal(file.json().content, 'deep original\n')
  const all = await app.inject({ method: 'GET', url: '/api/kb/file?library=all&path=u-plain/readme.md' })
  assert.equal(all.json().content, 'readme on the transfer host\n')
  assert.equal(all.json().path, 'u-plain/readme.md')
  assert.equal((await put('u-plain', { destination: '' })).status, 200)
})

test('the main folder, a deployment\'s pinned library, and one that does not exist take no host', async () => {
  assert.equal((await put('kb', { destination: 'me@xfer', root: xferFiles })).status, 409)
  assert.equal((await put('no-such', { destination: 'me@xfer', root: xferFiles })).status, 404)
})

// ---- the tools as the model and MCP clients see them ----

test('read_original is a knowledge base tool with its own description, and read_kb_file keeps its arguments', () => {
  const spec = n => TOOL_SPECS.find(t => t.function.name === n).function
  assert.deepEqual(Object.keys(spec('read_original').parameters.properties), ['path', 'paths', 'offset', 'library'])
  assert.deepEqual(Object.keys(spec('read_kb_file').parameters.properties), ['path', 'paths', 'offset', 'library'])
  assert.match(spec('read_kb_file').description, /text the knowledge base holds.*the index.*works when the original file cannot be reached/s)
  assert.match(spec('read_original').description, /original file itself.*other host the library names for reading originals/s)
})
