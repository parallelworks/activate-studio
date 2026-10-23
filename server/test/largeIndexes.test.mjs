// What keeps a large index quick: ssh's shared connection with a socket in
// a folder of the user's own (and none where no folder will do), the
// folder sweep's walk, a search that brings back only the rows it keeps,
// and counts that are made once per index. The GUFI parts need GUFI
// (GUFI_BIN, else /opt/gufi/bin) and skip without it.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import net from 'node:net'
import { execFileSync, spawnSync } from 'node:child_process'

const realGufi = process.env.GUFI_BIN || '/opt/gufi/bin'
const haveGufi = fs.existsSync(path.join(realGufi, 'gufi_query')) && fs.existsSync(path.join(realGufi, 'gufi_dir2index'))
const skip = !haveGufi && 'GUFI not installed'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'large-'))
// GUFI through a wrapper that notes each gufi_query it runs.
const gufiBin = path.join(dir, 'gufi')
const gufiLog = path.join(dir, 'gufi.log')
fs.mkdirSync(gufiBin)
if (haveGufi) {
  for (const f of fs.readdirSync(realGufi)) if (f !== 'gufi_query') fs.symlinkSync(path.join(realGufi, f), path.join(gufiBin, f))
  fs.writeFileSync(path.join(gufiBin, 'gufi_query'), `#!/bin/sh\nprintf '%s\\n' "$*" >> ${gufiLog}\nexec ${realGufi}/gufi_query "$@"\n`, { mode: 0o755 })
}
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
fs.mkdirSync(process.env.KB_ROOT)

// The socket tests want a short path; the temporary folder on macOS is long.
const short = fs.mkdtempSync('/tmp/studio-')
test.after(() => { fs.rmSync(short, { recursive: true, force: true }); fs.rmSync(dir, { recursive: true, force: true }) })
const folder = (name, mode = 0o700) => { const d = path.join(short, name); fs.mkdirSync(d, { recursive: true }); fs.chmodSync(d, mode); return d }

const dist = f => new URL(`../dist/${f}`, import.meta.url).href

/** controlArgs as a Studio with this data folder and environment would
 *  give them; an empty value leaves the variable out. */
const controlArgs = (indexBase, env = {}) => {
  const all = { ...process.env, INDEX_BASE: indexBase, ...env }
  for (const k of Object.keys(all)) if (all[k] === '') delete all[k]
  return JSON.parse(execFileSync(process.execPath, ['--input-type=module', '-e',
    `const R = await import(${JSON.stringify(dist('remoteExec.js'))}); process.stdout.write(JSON.stringify(R.controlArgs({ destination: 'me@host', port: 2222 })))`],
  { env: all }).toString())
}
const socketOf = args => args[3].slice('ControlPath="'.length, -1)
// A data folder too long for a socket, as under a long macOS user name:
// /Users/<name>/Library/Application Support/ACTIVATE Studio/index.
const longBase = path.join(short, 'Users', 'firstname.lastname.example', 'Library', 'Application Support', 'ACTIVATE Studio', 'index')
const uid = process.getuid()
// The tests may make /tmp/activate-studio-<uid>, as the Studio would; one
// that was not there before is removed after, when empty.
const tmpSockets = `/tmp/activate-studio-${uid}`
if (!fs.existsSync(tmpSockets)) test.after(() => { try { fs.rmdirSync(tmpSockets) } catch { /* in use */ } })

const haveSsh = (() => { try { execFileSync('ssh', ['-V'], { stdio: 'ignore' }); return true } catch { return false } })()

test('the socket goes in the runtime folder, quoted for a folder with spaces, however long the data folder', { skip: !haveSsh && 'no ssh' }, () => {
  assert.ok(Buffer.byteLength(path.join(longBase, 'ssh', 'x'.repeat(12))) > 90)
  const run = folder('run time')
  const args = controlArgs(longBase, { XDG_RUNTIME_DIR: run })
  const sock = socketOf(args)
  assert.deepEqual(args, ['-o', 'ControlMaster=auto', '-o', `ControlPath="${sock}"`, '-o', 'ControlPersist=600'])
  assert.equal(path.dirname(sock), path.join(run, 'activate-studio'))
  assert.match(path.basename(sock), /^[0-9a-f]{12}$/)
  assert.equal(fs.lstatSync(path.dirname(sock)).mode & 0o777, 0o700, 'the socket folder is the user\'s alone')
  // ssh reads the options as given: -G prints its configuration and stops.
  const conf = execFileSync('ssh', ['-G', ...args, '-p', '2222', 'me@host'], { stdio: ['ignore', 'pipe', 'ignore'] }).toString()
  assert.match(conf, /^controlmaster auto$/m)
  assert.ok(conf.split('\n').includes(`controlpath ${sock}`), conf.split('\n').filter(l => l.startsWith('control')).join('\n'))
  assert.match(conf, /^controlpersist 600$/m)
})

test('without a runtime folder, or with a link in its place, the socket goes in /tmp/activate-studio-<uid>', () => {
  const tmp = tmpSockets
  assert.equal(path.dirname(socketOf(controlArgs(longBase, { XDG_RUNTIME_DIR: '' }))), tmp)
  const run = folder('linked')
  fs.symlinkSync(folder('elsewhere'), path.join(run, 'activate-studio'))
  assert.equal(path.dirname(socketOf(controlArgs(longBase, { XDG_RUNTIME_DIR: run }))), tmp)
  const s = fs.lstatSync(tmp)
  assert.deepEqual([s.isDirectory(), s.uid, s.mode & 0o777], [true, uid, 0o700])
  assert.deepEqual(controlArgs(longBase, { STUDIO_SSH_SHARE: '0' }), [])
})

test('a socket folder that is a link, open to others, someone else\'s, or too long is passed over', async () => {
  const R = await import(dist('remoteExec.js'))
  const good = folder('good')
  const link = path.join(short, 'link')
  fs.symlinkSync(good, link)
  const open = folder('open', 0o755)
  const long = path.join(short, 'x'.repeat(80))
  assert.equal(R.socketDir([link], uid), null, 'a link, even to a good folder')
  assert.equal(R.socketDir([open], uid), null, 'mode 755')
  assert.equal(fs.statSync(open).mode & 0o777, 0o755, 'left as it was')
  assert.equal(R.socketDir([good], uid + 1), null, 'owned by another user')
  assert.equal(R.socketDir(['/tmp'], uid), null, 'a shared folder')
  assert.equal(R.socketDir([long], uid), null, 'too long for a socket')
  assert.equal(fs.existsSync(long), false, 'and not made')
  // In order, down to the data folder, which is made when missing.
  const data = path.join(short, 'data', 'ssh')
  assert.equal(R.socketDir([link, open, long, data], uid), data)
  assert.equal(fs.statSync(data).mode & 0o777, 0o700)
  assert.equal(R.socketDir([link, open, long], uid), null)
})

test('a socket left by a master that died is cleared by the next ssh, which connects on its own', { skip: !haveSsh && 'no ssh' }, async () => {
  const args = controlArgs(longBase, { XDG_RUNTIME_DIR: folder('stale') })
  const sock = socketOf(args)
  // A master killed outright leaves its socket with nothing listening.
  spawnSync(process.execPath, ['-e', `require('net').createServer().listen(${JSON.stringify(sock)}, () => process.kill(process.pid, 'SIGKILL'))`])
  assert.ok(fs.lstatSync(sock).isSocket())
  const port = await new Promise(resolve => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)) }) })
  const r = spawnSync('ssh', ['-F', '/dev/null', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=5', ...args, '-p', String(port), 'me@127.0.0.1', 'true'], { encoding: 'utf8' })
  assert.equal(r.status, 255)
  assert.match(r.stderr, /Connection refused/)
  assert.doesNotMatch(r.stderr, /control ?socket|controlpath/i)
  assert.equal(fs.existsSync(sock), false)
})

test('the sweep finds each folder\'s latest change, passing over dot names and excluded folders', async () => {
  const { scanLatest } = await import(dist('dirScan.js'))
  const root = path.join(dir, 'tree')
  const at = s => new Date(Date.UTC(2026, 0, 1, 0, 0, s))
  const file = (rel, s) => { const f = path.join(root, rel); fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, rel); fs.utimesSync(f, at(s), at(s)) }
  file('top.txt', 10)
  file('a/one.txt', 40)
  file('a/b/two.txt', 5)
  file('.hidden/x.txt', 90)
  file('a/.dot.txt', 95)
  file('node_modules/y.txt', 99)
  fs.mkdirSync(path.join(root, 'empty'))
  // Folder times last, deepest first: making a child changes its parent.
  for (const [rel, s] of [['a/b', 20], ['a', 1], ['empty', 30], ['', 2]]) fs.utimesSync(path.join(root, rel), at(s), at(s))
  const got = await scanLatest(root, ['node_modules'])
  const want = { '': at(10), a: at(40), 'a/b': at(20), empty: at(30) }
  assert.deepEqual(Object.fromEntries([...got].sort()), Object.fromEntries(Object.entries(want).map(([k, d]) => [k, d.getTime()]).sort()))
  assert.deepEqual([...(await scanLatest(path.join(dir, 'missing'), []))], [])
})

test('a name search over many folders brings back only the rows it keeps', { skip }, async () => {
  const src = path.join(dir, 'src')
  for (let i = 0; i < 40; i++) fs.mkdirSync(path.join(src, `d${i}`), { recursive: true })
  for (let i = 0; i < 40; i++) for (let j = 0; j < 3; j++) fs.writeFileSync(path.join(src, `d${i}`, `match-${j}.txt`), 'x')
  fs.writeFileSync(path.join(src, 'd7', 'lonely.txt'), 'x')
  const ix = path.join(dir, 'srcix')
  fs.mkdirSync(ix)
  execFileSync(path.join(realGufi, 'gufi_dir2index'), ['-x', src, ix], { stdio: 'ignore' })
  const G = await import(dist('gufi.js'))
  const root = path.join(ix, 'src')
  const hits = await G.searchNames('match', 7, root)
  assert.equal(hits.length, 7)
  assert.ok(hits.every(h => /^d\d+\/match-\d\.txt$/.test(h.path)), hits.map(h => h.path).join(' '))
  assert.equal(new Set(hits.map(h => h.path)).size, 7)
  assert.deepEqual((await G.searchNames('lonely', 7, root)).map(h => h.path), ['d7/lonely.txt'])
  assert.deepEqual(await G.searchNames('nowhere', 7, root), [])
  // The limit is applied where the index is: in each thread and in the merge.
  const last = fs.readFileSync(gufiLog, 'utf8').trim().split('\n').filter(l => l.includes('lonely')).pop()
  assert.match(last, / -a 1 /)
  assert.match(last, /SELECT \* FROM agg LIMIT 7;/)
})

test('a folder\'s counts are made once until the index changes', { skip }, async () => {
  const G = await import(dist('gufi.js'))
  const root = path.join(dir, 'srcix', 'src')
  const runs = () => fs.readFileSync(gufiLog, 'utf8').trim().split('\n').length
  const before = runs()
  const [a, b] = await Promise.all([G.treeStats(root, ''), G.treeStats(root, '')])
  const once = runs()
  assert.ok(once > before)
  assert.deepEqual(a, b)
  assert.equal(a.files, 121)
  assert.deepEqual(await G.treeStats(root, ''), a)
  assert.equal(runs(), once, 'asked again, the counts are not made again')
  assert.equal((await G.treeStats(root, 'd7')).files, 4, 'a folder of it is counted on its own')
  const withFolder = runs()
  G.invalidateDbList()
  assert.deepEqual(await G.treeStats(root, ''), a)
  assert.ok(runs() > withFolder, 'after an index change it counts again')
})
