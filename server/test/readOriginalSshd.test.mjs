// End to end over real ssh: two sshd processes run by this user on high
// ports, with no root and nothing installed. The index host runs only
// gufi_query (a ForceCommand, as a restricted login does) and does not have
// the library's files. The transfer host has a shell and has the same files
// mounted at another path. The Studio reaches both as this user with the
// ssh machinery the libraries over ssh use: a key named in the library's ssh
// options, a shared connection, no prompts. Skipped unless STUDIO_TEST_SSHD=1
// (it starts daemons and needs /usr/sbin/sshd and ssh-keygen), and the
// index is read by a stand-in gufi_query, so no GUFI is needed.
//
//   STUDIO_TEST_SSHD=1 node --import ./test/setup.mjs --test test/readOriginalSshd.test.mjs
import test from 'node:test'
import assert from 'node:assert/strict'
import crypto from 'node:crypto'
import fs from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { execFileSync, spawn } from 'node:child_process'

const SSHD = ['/usr/sbin/sshd', '/usr/local/sbin/sshd'].find(p => fs.existsSync(p))
const skip = process.env.STUDIO_TEST_SSHD !== '1' ? 'set STUDIO_TEST_SSHD=1 to start local sshd processes' : !SSHD ? 'no sshd on this host' : false

const dir = fs.mkdtempSync('/tmp/readorigssh-')
const user = os.userInfo().username
const freePort = () => new Promise(resolve => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)) }) })
const [portIndex, portFiles] = [await freePort(), await freePort()]
process.env.HOST = '127.0.0.1'
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.GUFI_BIN = path.join(dir, 'fake-gufi')
process.env.XDG_RUNTIME_DIR = fs.mkdtempSync('/tmp/readorigsshrun-')
const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
const exe = (f, body) => { write(f, body); fs.chmodSync(f, 0o755) }
const daemons = []
test.after(async () => {
  const R = await import('../dist/remoteExec.js')
  R.closeRemoteChannels()
  for (const d of daemons) d.kill()
  fs.rmSync(process.env.XDG_RUNTIME_DIR, { recursive: true, force: true })
  fs.rmSync(dir, { recursive: true, force: true })
})

const proj = path.join(dir, 'gufihost', 'proj')       // the library's folder on the index host: not there
const data = path.join(dir, 'xfer', 'data')            // the same files on the transfer host
const idx = path.join(dir, 'gufihost', 'idx')
const forcedLog = path.join(dir, 'forced.log')
const odd = ['with space.md', "it's.md", 'café ☕.md', '-dash.md', '$(touch pwned).md']

if (!skip) {
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', path.join(dir, 'id')])
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', path.join(dir, 'host')])
  fs.copyFileSync(path.join(dir, 'id.pub'), path.join(dir, 'authorized_keys'))
  fs.chmodSync(path.join(dir, 'authorized_keys'), 0o600)
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
  // The index host's login: gufi_query, and nothing else.
  exe(path.join(dir, 'forced.sh'), `#!/bin/sh
printf '%s\\n' "$SSH_ORIGINAL_COMMAND" >> ${forcedLog}
PATH=${path.join(dir, 'fake-gufi')}:/usr/bin:/bin
export PATH
case "$SSH_ORIGINAL_COMMAND" in
  gufi_query|"gufi_query "*) eval "$SSH_ORIGINAL_COMMAND" ;;
  *) echo "This account may run only gufi_query." >&2; exit 1 ;;
esac
`)
  const config = (port, extra) => {
    const f = path.join(dir, `sshd-${port}.conf`)
    write(f, `Port ${port}\nListenAddress 127.0.0.1\nHostKey ${path.join(dir, 'host')}\nPidFile ${path.join(dir, `sshd-${port}.pid`)}\nAuthorizedKeysFile ${path.join(dir, 'authorized_keys')}\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nUsePAM no\nStrictModes no\nLogLevel ERROR\nPermitUserEnvironment no\n${extra}`)
    return f
  }
  for (const [port, extra] of [[portIndex, `ForceCommand ${path.join(dir, 'forced.sh')}\n`], [portFiles, '']]) {
    daemons.push(spawn(SSHD, ['-D', '-e', '-f', config(port, extra)], { stdio: 'ignore' }))
  }
  for (const port of [portIndex, portFiles]) {
    for (let i = 0; i < 100; i++) {
      if (await new Promise(r => { const s = net.connect(port, '127.0.0.1', () => { s.destroy(); r(true) }); s.on('error', () => r(false)) })) break
      await new Promise(r => setTimeout(r, 50))
    }
  }
  const idxFolder = (folder, rows) => {
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
  idxFolder(idx, [['report.md', 'report, from the index\n']])
  write(path.join(data, 'report.md'), 'report, the original file\n')
  write(path.join(data, 'sub', 'note.md'), 'a note\n')
  write(path.join(data, 'sub', 'pic.png'), '\x89PNG not text')
  for (const n of odd) write(path.join(data, 'sub', n), `contents of ${n}\n`)
  fs.mkdirSync(process.env.KB_ROOT, { recursive: true })
  const options = `-i ${path.join(dir, 'id')} -o IdentitiesOnly=yes -o UserKnownHostsFile=${path.join(dir, 'kh')} -o StrictHostKeyChecking=no -o ConnectTimeout=10`
  const def = extra => ({
    id: 'u-site', label: 'Site', transport: 'ssh', site: `${user}@127.0.0.1`, destination: `${user}@127.0.0.1`, port: portIndex, sshOptions: options.split(' '),
    resource: 'x', sourceDir: proj, sourceRoot: proj, indexRoot: idx, gufiBin: '', shell: false, fullText: true, state: 'ready', run: null, error: null,
    builtAt: new Date().toISOString(), directories: 1, files: 1, scheduler: null, ...extra,
  })
  const hash = crypto.createHash('sha256').update('single').digest('hex').slice(0, 16)
  write(path.join(dir, 'ix', 'users', hash, 'owner'), 'single')
  write(path.join(dir, 'ix', 'users', hash, 'libraries.json'), JSON.stringify([def({})]))
  process.env.SSH_TEST_OPTIONS = options
}

const { executeTool } = skip ? {} : await import('../dist/chat/tools.js')
const { default: Fastify } = skip ? {} : await import('fastify')
const O = skip ? {} : await import('../dist/originals.js')
const { kbRoutes } = skip ? {} : await import('../dist/routes.js')
const { userLibraryRoutes } = skip ? {} : await import('../dist/userLibraries.js')
const app = skip ? null : Fastify()
if (app) {
  app.setErrorHandler((err, _req, reply) => reply.status(err.status ?? 500).send({ error: err.message }))
  await app.register(kbRoutes)
  await app.register(userLibraryRoutes)
  await app.register(O.originalsRoutes)
  test.after(() => app.close())
}
const call = (name, args) => executeTool(name, JSON.stringify(args), {})
const put = payload => app.inject({ method: 'PUT', url: '/api/libraries/u-site/read-through', payload }).then(r => ({ status: r.statusCode, body: r.json() }))
const forcedCommands = () => (fs.existsSync(forcedLog) ? fs.readFileSync(forcedLog, 'utf8').split('\n').filter(Boolean) : [])
const hostOptions = () => process.env.SSH_TEST_OPTIONS

test('the index host answers read_kb_file from its index, and read_original says it cannot open files there', { skip }, async () => {
  const k = await call('read_kb_file', { path: 'report.md', library: 'u-site' })
  assert.equal(k.result, '[source: index; it can lag behind the file]\nreport, from the index\n')
  const o = await call('read_original', { path: 'report.md', library: 'u-site' })
  assert.match(o.result, new RegExp(`^Tool error: report\\.md could not be opened\\. Tried only ${user}@127\\.0\\.0\\.1: .*runs only gufi_query`), o.result)
  // Nothing but gufi_query ever ran on the restricted host (the first try was the session loop, refused).
  const sent = forcedCommands().filter(c => !c.startsWith('bash -c '))
  assert.ok(sent.length > 0 && sent.every(c => c.startsWith('gufi_query')), sent.join('\n'))
})

test('the connection test passes for the transfer host and fails, with a reason, for the wrong folder and for a host that runs only gufi_query', { skip }, async () => {
  const body = { destination: `${user}@127.0.0.1`, port: portFiles, sshOptions: hostOptions(), root: data }
  const ok = await app.inject({ method: 'POST', url: '/api/libraries/u-site/read-through/test', payload: body }).then(r => r.json())
  assert.equal(ok.ok, true, JSON.stringify(ok))
  assert.match(ok.steps[0].detail, new RegExp(`^Connected to ${user}@127\\.0\\.0\\.1 on port ${portFiles}`))
  const wrong = await app.inject({ method: 'POST', url: '/api/libraries/u-site/read-through/test', payload: { ...body, root: path.join(data, 'missing') } }).then(r => r.json())
  assert.equal(wrong.ok, false)
  assert.match(wrong.steps.at(-1).detail, /is not a directory on /)
  const restricted = await app.inject({ method: 'POST', url: '/api/libraries/u-site/read-through/test', payload: { ...body, port: portIndex } }).then(r => r.json())
  assert.equal(restricted.ok, false)
  assert.match(restricted.steps[0].detail, /runs only gufi_query for this login \(no shell\), so it cannot open files/)
})

test('with the transfer host named, read_original and the viewer read the file the index host does not have', { skip }, async () => {
  const saved = await put({ destination: `${user}@127.0.0.1`, port: portFiles, sshOptions: hostOptions(), root: data })
  assert.equal(saved.status, 200, JSON.stringify(saved.body))
  assert.equal(saved.body.readThrough.root, data)
  const o = await call('read_original', { path: 'report.md', library: 'u-site' })
  assert.equal(o.result, `[source: the original file on ${user}@127.0.0.1:${portFiles}, after ${user}@127.0.0.1 could not give it]\nreport, the original file\n`)
  // The index copy and the original differ, and each tool gives its own.
  assert.match((await call('read_kb_file', { path: 'report.md', library: 'u-site' })).result, /report, from the index/)
  const file = await app.inject({ method: 'GET', url: '/api/kb/file?library=u-site&path=sub/note.md' })
  assert.equal(file.json().content, 'a note\n')
  const dl = await app.inject({ method: 'GET', url: '/api/kb/download?library=u-site&path=sub/pic.png' })
  assert.equal(dl.body, '\x89PNG not text')
})

test('file names with spaces, quotes, non-ASCII, a leading dash and shell syntax are read as they are over real ssh', { skip }, async () => {
  const many = await call('read_original', { paths: odd.map(n => `sub/${n}`), library: 'u-site' })
  for (const n of odd) assert.ok(many.result.includes(`### sub/${n}\n`) && many.result.includes(`contents of ${n}\n`), n)
  for (const d of [data, path.join(data, 'sub'), os.homedir(), process.cwd(), dir]) assert.equal(fs.existsSync(path.join(d, 'pwned')), false, d)
})

test('paths that leave the folder are refused, and a file neither host has names both hosts', { skip }, async () => {
  const bad = await call('read_original', { paths: ['../hostkey', 'sub/../../host', 'sub/..'], library: 'u-site' })
  assert.equal([...bad.result.matchAll(/not read: invalid path/g)].length, 3, bad.result)
  const none = await call('read_original', { path: 'nothing.md', library: 'u-site' })
  assert.match(none.result, new RegExp(`^Tool error: nothing\\.md could not be opened\\. Tried ${user}@127\\.0\\.0\\.1: .*runs only gufi_query.*; ${user}@127\\.0\\.0\\.1:${portFiles}: .*(No such file|cannot)`, 's'), none.result)
  const pic = await call('read_original', { path: 'sub/pic.png', library: 'u-site' })
  assert.match(pic.result, /but the Studio opens only plain text files over ssh/)
})

test('a host that is not listening is named with the reason, and the file transfer host that works is not blamed', { skip }, async () => {
  const dead = await freePort()
  // Written straight into the library: a saved host would have to pass its test.
  const hash = crypto.createHash('sha256').update('single').digest('hex').slice(0, 16)
  const f = path.join(dir, 'ix', 'users', hash, 'libraries.json')
  const defs = JSON.parse(fs.readFileSync(f, 'utf8'))
  defs[0].readThrough = { destination: `${user}@127.0.0.1`, port: dead, sshOptions: hostOptions().split(' '), root: data }
  fs.writeFileSync(f, JSON.stringify(defs))
  const r = await call('read_original', { path: 'report.md', library: 'u-site' })
  assert.match(r.result, new RegExp(`Tried ${user}@127\\.0\\.0\\.1: .*runs only gufi_query.*; ${user}@127\\.0\\.0\\.1:${dead}: .*(refused|Connection)`, 's'), r.result)
})

test('clearing the host stops the fallback', { skip }, async () => {
  const hash = crypto.createHash('sha256').update('single').digest('hex').slice(0, 16)
  const f = path.join(dir, 'ix', 'users', hash, 'libraries.json')
  const defs = JSON.parse(fs.readFileSync(f, 'utf8'))
  delete defs[0].readThrough
  fs.writeFileSync(f, JSON.stringify(defs))
  const cleared = await put({ destination: '' })
  assert.equal(cleared.status, 200)
  assert.match((await call('read_original', { path: 'report.md', library: 'u-site' })).result, /Tried only /)
})
