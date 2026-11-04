// The data mover layer below the approval: argument checks, where files may
// be read and land, the copy mover over a stand-in for ssh (the real bash,
// find, stat and cat of the computer running the test, so macOS is covered
// by CI), jobs, restart, and the Studio's own check of a mover's result.
import test from 'node:test'
import assert from 'node:assert/strict'
import { spawn, execFileSync } from 'node:child_process'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const dir = fs.realpathSync(fs.mkdtempSync(path.join(os.tmpdir(), 'mv-')))
process.env.HOME = path.join(dir, 'home')
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.GUFI_BIN = path.join(dir, 'no-gufi')
process.env.STUDIO_TRANSFER_REPORT_MS = '30000'
delete process.env.AUTH_JWKS_URL
const hostRoot = path.join(dir, 'host', 'data')
const ownRoot = path.join(dir, 'gone', 'data')
const outside = path.join(dir, 'outside')
process.env.STUDIO_LIBRARIES = JSON.stringify([
  { id: 'local', label: 'Local', indexRoot: path.join(dir, 'idx-local'), sourceRoot: path.join(dir, 'local') },
  // On a host, with its files mounted at another path there (the root mapping).
  { id: 'far', label: 'Far', indexRoot: path.join(dir, 'idx-far'), sourceRoot: ownRoot, readThrough: { destination: 'files-host', root: hostRoot } },
  { id: 'noway', label: 'No way', indexRoot: path.join(dir, 'idx-none') },
])
const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
for (const d of ['kb', 'idx-local', 'idx-far', 'idx-none', 'home', 'ix']) fs.mkdirSync(path.join(dir, d), { recursive: true })
const local = path.join(dir, 'local')
const downloads = path.join(dir, 'home', 'Downloads')
fs.mkdirSync(downloads, { recursive: true })

const ODD = ['with space.txt', "it's.txt", 'say "hi".txt', '-rf', '--delete', '$(touch pwned).txt', '`touch pwned`.txt', 'a;touch pwned;.txt',
  'line\nbreak.txt', 'café ☕.txt', '*.txt', 'back\\slash.txt', ' leading.txt', 'trailing ', '%s%n.txt', '& | > <.txt']
for (const root of [local, hostRoot]) {
  write(path.join(root, 'one.txt'), 'one')
  write(path.join(root, 'tree', 'a.txt'), 'aaa')
  write(path.join(root, 'tree', 'sub', 'b.bin'), Buffer.alloc(70_000, 7))
  write(path.join(root, 'tree', '.hidden'), 'hidden')
  write(path.join(root, 'tree', '.git', 'config'), 'hidden')
  for (const n of ODD) write(path.join(root, 'odd', n), `body of ${n}`)
  fs.symlinkSync('/etc/hostname', path.join(root, 'tree', 'inner-link'))
  fs.symlinkSync(outside, path.join(root, 'out-dir'))
  fs.symlinkSync(path.join(outside, 'secret.txt'), path.join(root, 'out-file'))
}
write(path.join(outside, 'secret.txt'), 'secret')

const R = await import('../dist/remoteExec.js')
// Stands in for ssh: the command runs here, as a host's shell would run it.
const realSpawn = (site, key, command) => spawn('bash', ['-c', command], { stdio: ['pipe', 'pipe', 'pipe'], cwd: dir })
R.setRemoteSpawn(realSpawn)
const P = await import('../dist/movers/plan.js')
const J = await import('../dist/movers/jobs.js')
const D = await import('../dist/movers/destination.js')
const S = await import('../dist/movers/source.js')
const T = await import('../dist/movers/types.js')
const { copyMover } = await import('../dist/movers/copy.js')
const L = await import('../dist/libraries.js')

test.after(() => { R.closeRemoteChannels(); fs.rmSync(dir, { recursive: true, force: true }) })

const ctx = { viewerId: null, library: null }
let seq = 0
const start = plan => J.startApproved({ conversationId: 'c', toolCallId: `t${++seq}`, viewer: 'local', args: plan.canonical, plan: plan.plan })
const plan = async args => { const p = await P.planTransfer(args, ctx); assert.ok(!('problem' in p), p.problem); return p }
const refused = async (args, re) => { const p = await P.planTransfer(args, ctx); assert.ok('problem' in p, `planned: ${JSON.stringify(args)}`); assert.match(p.problem, re) }
const run = async args => J.settled(start(await plan(args)), 60_000)
const fresh = () => { const f = path.join(downloads, `t${++seq}`); fs.mkdirSync(f); return f }
const noStaging = folder => assert.deepEqual(fs.readdirSync(folder).filter(n => n.startsWith('.studio-transfer-')), [])

test('arguments keep their declared shape and refuse traversal', () => {
  assert.deepEqual(P.transferArgs({ paths: ['a/./b//c'], library: 'x', extra: 'ignored', mode: 'MOVE', delete: true }), { library: 'x', paths: ['a/b/c'] })
  assert.deepEqual(P.transferArgs('{"paths":"a"}'), { paths: ['a'] })
  for (const bad of ['../x', 'a/../../x', '/etc/passwd', '~/x', '.', '', 'a\0b', 'x/..']) assert.throws(() => P.transferArgs({ paths: [bad] }), { status: 400 }, JSON.stringify(bad))
  for (const bad of [[{}], [1], [null]]) assert.throws(() => P.transferArgs({ paths: bad }), { status: 400 })
  assert.throws(() => P.transferArgs({ paths: ['.ssh/id_rsa'] }), { status: 403 })
  assert.throws(() => P.transferArgs({ paths: [] }), /at least one/)
  assert.throws(() => P.transferArgs({ paths: Array.from({ length: 101 }, (_, i) => `f${i}`) }), /at most/)
  assert.throws(() => P.transferArgs('not json'), /JSON/)
  assert.throws(() => P.transferArgs({ paths: ['a'], destination: ['x'] }), /folder path/)
  assert.deepEqual(P.transferArgs({ paths: ['.agents/p.md'] }).paths, ['.agents/p.md'])
})

test('the tool has no way to name a move, a delete, a remote destination or an overwrite', () => {
  const props = Object.keys(P.TRANSFER_TOOL_SPEC.function.parameters.properties).sort()
  assert.deepEqual(props, ['destination', 'library', 'paths'])
  assert.equal(typeof copyMover.start, 'function')
  assert.deepEqual(Object.keys(copyMover).sort(), ['canReach', 'cancel', 'id', 'label', 'pollMs', 'start', 'status'])
})

test('a destination must be an allowed folder, whatever the path looks like', async () => {
  await refused({ library: 'local', paths: ['one.txt'], destination: '/tmp' }, /not an allowed folder/)
  await refused({ library: 'local', paths: ['one.txt'], destination: '~/.ssh' }, /not an allowed folder/)
  await refused({ library: 'local', paths: ['one.txt'], destination: '~/Downloads/../.ssh' }, /no "\.\."/)
  await refused({ library: 'local', paths: ['one.txt'], destination: 'relative/folder' }, /full path/)
  await refused({ library: 'local', paths: ['one.txt'], destination: 'user@host:/tmp' }, /full path/)
  await refused({ library: 'local', paths: ['one.txt'], destination: '/x\n/y' }, /not a folder name/)
  // A link inside an allowed folder does not carry a copy out of it.
  fs.symlinkSync(outside, path.join(downloads, 'link-out'))
  await refused({ library: 'local', paths: ['one.txt'], destination: '~/Downloads/link-out' }, /not an allowed folder/)
  await refused({ library: 'local', paths: ['one.txt'], destination: '~/Downloads/link-out/new/folder' }, /not an allowed folder/)
  fs.rmSync(path.join(downloads, 'link-out'))
  write(path.join(downloads, 'afile'), 'x')
  await refused({ library: 'local', paths: ['one.txt'], destination: '~/Downloads/afile' }, /is a file/)
  fs.rmSync(path.join(downloads, 'afile'))
})

test('Downloads is the default, from the desktop\'s own setting where there is one', async () => {
  assert.equal(D.downloadsFolder(), downloads)
  const home = path.join(dir, 'xdg')
  write(path.join(home, '.config', 'user-dirs.dirs'), '# comment\nXDG_DOWNLOAD_DIR="$HOME/Fetched"\n')
  assert.equal(D.downloadsFolder(home), path.join(home, 'Fetched'))
  const p = await plan({ library: 'local', paths: ['one.txt'] })
  assert.equal(p.plan.dest, downloads)
  assert.equal(p.plan.destShown, '~/Downloads')
})

test('another folder is allowed when the deployment or Settings names it', async () => {
  const extra = path.join(dir, 'extra')
  fs.mkdirSync(extra)
  await refused({ library: 'local', paths: ['one.txt'], destination: extra }, /not an allowed folder/)
  process.env.STUDIO_TRANSFER_FOLDERS = extra
  try {
    const j = await run({ library: 'local', paths: ['one.txt'], destination: path.join(extra, 'new', 'deep') })
    assert.equal(j.state, 'done', j.message)
    assert.equal(fs.readFileSync(path.join(extra, 'new', 'deep', 'one.txt'), 'utf8'), 'one')
  } finally { delete process.env.STUDIO_TRANSFER_FOLDERS }
})

test('a destination inside the Studio\'s data folder is refused', async () => {
  process.env.STUDIO_TRANSFER_FOLDERS = dir
  try { await refused({ library: 'local', paths: ['one.txt'], destination: path.join(dir, 'ix', 'x') }, /data folder/) } finally { delete process.env.STUDIO_TRANSFER_FOLDERS }
})

test('a source must be inside a library this viewer has, and stay inside it', async () => {
  await refused({ library: 'nobody', paths: ['one.txt'] }, /no library named/)
  await refused({ library: 'all', paths: ['one.txt'] }, /one library/)
  await refused({ library: 'noway', paths: ['one.txt'] }, /could not be reached/)
  await refused({ library: 'local', paths: ['missing.txt'] }, /no such file or folder/)
  for (const library of ['local', 'far']) {
    await refused({ library, paths: ['out-file'] }, /outside the library/)
    await refused({ library, paths: ['out-dir/secret.txt'] }, /outside the library/)
  }
})

test('a folder copies its files, leaving out links and hidden entries, and says so', async () => {
  for (const library of ['local', 'far']) {
    const destination = fresh()
    const j = await run({ library, paths: ['tree'], destination })
    assert.equal(j.state, 'done', j.message)
    assert.deepEqual(j.files, { done: 2, total: 2 })
    assert.deepEqual(j.skipped, { links: 1, hidden: 2 })
    assert.match(j.message, /checked the file count and sizes/)
    assert.match(j.message, /1 link or special file not copied; 2 hidden entries not copied/)
    assert.equal(fs.readFileSync(path.join(destination, 'tree', 'a.txt'), 'utf8'), 'aaa')
    assert.equal(fs.statSync(path.join(destination, 'tree', 'sub', 'b.bin')).size, 70_000)
    assert.ok(!fs.existsSync(path.join(destination, 'tree', '.hidden')))
    assert.ok(!fs.existsSync(path.join(destination, 'tree', 'inner-link')))
    noStaging(destination)
  }
  // The source is still there, every byte.
  assert.equal(fs.readFileSync(path.join(hostRoot, 'tree', 'a.txt'), 'utf8'), 'aaa')
})

test('odd file names arrive as they are, and nothing in a name runs', async () => {
  for (const library of ['local', 'far']) {
    // By folder, and each by its own path.
    const whole = fresh()
    const a = await run({ library, paths: ['odd'], destination: whole })
    assert.equal(a.state, 'done', a.message)
    assert.equal(a.files.done, ODD.length)
    assert.deepEqual(fs.readdirSync(path.join(whole, 'odd')).sort(), [...ODD].sort())
    for (const n of ODD) assert.equal(fs.readFileSync(path.join(whole, 'odd', n), 'utf8'), `body of ${n}`, n)
    const each = fresh()
    const b = await run({ library, paths: ODD.map(n => `odd/${n}`), destination: each })
    assert.equal(b.state, 'done', b.message)
    assert.deepEqual(fs.readdirSync(each).sort(), [...ODD].sort())
  }
  for (const where of [dir, hostRoot, local, path.join(hostRoot, 'odd')]) assert.ok(!fs.existsSync(path.join(where, 'pwned')), `${where}/pwned`)
})

test('a name cannot change what the approval appears to say', async () => {
  const name = 'x\n\u202Eevil\u200b.txt'
  write(path.join(local, name), 'x')
  const p = await plan({ library: 'local', paths: [name] })
  const part = P.transferApprovalPart(p.plan, 'id', new Date().toISOString())
  const text = JSON.stringify(part.plan) + part.command + part.header
  assert.ok(!text.includes('\u202E') && !text.includes('\u200b'))
  assert.match(part.plan.find(r => r.label === 'Items').value, /x\\n\\u202eevil\\u200b\.txt/)
  fs.rmSync(path.join(local, name))
})

test('the plan says what the approval needs, with the mapped source', async () => {
  const p = await plan({ library: 'far', paths: ['one.txt'] })
  assert.equal(p.plan.mover, 'studio-copy')
  assert.match(p.plan.source, /host\/data on files-host over ssh \(Originals via\)/)
  assert.equal(p.plan.dest, downloads)
  const rows = Object.fromEntries(P.transferApprovalPart(p.plan, 'id', 'x').plan.map(r => [r.label, r.value]))
  assert.equal(rows.Mover, 'Studio copy')
  assert.match(rows.To, /Downloads on this computer/)
  const q = await plan({ library: 'local', paths: ['tree'] })
  assert.deepEqual([q.plan.files, q.plan.counted], [2, 'source'])
  // A folder the index has not reached has no count, and never a count of 0.
  assert.deepEqual([p.plan.files, p.plan.counted], [null, null])
  // A folder as a person names it.
  const named = await plan({ library: 'local', paths: ['tree'], destination: 'documents' })
  assert.equal(named.plan.dest, path.join(os.homedir(), 'Documents'))
  assert.equal(S.routeSummary(L.getLibrary('far'), null), `this computer, then ssh from files-host (Originals via)`)
})

test('nothing already at the destination is replaced', async () => {
  const destination = fresh()
  write(path.join(destination, 'one.txt'), 'mine')
  await refused({ library: 'local', paths: ['one.txt'], destination }, /already exists/)
  // Something that appears after the approval stops the job the same way.
  const other = fresh()
  const p = await plan({ library: 'local', paths: ['one.txt'], destination: other })
  write(path.join(other, 'one.txt'), 'mine')
  const j = await J.settled(start(p), 60_000)
  assert.equal(j.state, 'failed')
  assert.match(j.message, /already exists/)
  assert.equal(fs.readFileSync(path.join(other, 'one.txt'), 'utf8'), 'mine')
  await refused({ library: 'local', paths: ['tree/a.txt', 'a.txt'] }, /(same name|no such file)/)
  write(path.join(local, 'a.txt'), 'a')
  await refused({ library: 'local', paths: ['tree/a.txt', 'a.txt'] }, /same name/)
  fs.rmSync(path.join(local, 'a.txt'))
})

test('a held plan runs only as it was shown', async () => {
  const p = await plan({ library: 'far', paths: ['one.txt'], destination: fresh() })
  assert.throws(() => J.startApproved({ conversationId: 'c', toolCallId: 'x1', viewer: 'local', args: p.canonical }), /no plan/)
  assert.throws(() => J.startApproved({ conversationId: 'c', toolCallId: 'x2', viewer: 'local', args: p.canonical, plan: { ...p.plan, items: ['tree'] } }), /does not match/)
  assert.throws(() => J.startApproved({ conversationId: 'c', toolCallId: 'x3', viewer: 'local', args: p.canonical, plan: { ...p.plan, route: '["ssh","ssh://elsewhere","/data","library"]' } }), /changed after this was asked/)
  assert.throws(() => J.startApproved({ conversationId: 'c', toolCallId: 'x4', viewer: 'local', args: p.canonical, plan: { ...p.plan, mover: 'gone' } }), /no longer available/)
  // One held call starts one job, even if something asks twice.
  const rec = { conversationId: 'c', toolCallId: 'x5', viewer: 'local', args: p.canonical, plan: p.plan }
  const j = J.startApproved(rec)
  assert.throws(() => J.startApproved(rec), /already ran/)
  await J.settled(j, 60_000)
})

test('a file that fails stops the job and leaves nothing at the destination', async () => {
  const destination = fresh()
  const p = await plan({ library: 'far', paths: ['tree'], destination })
  R.setRemoteSpawn((site, key, command) => command.includes('b.bin')
    ? spawn('bash', ['-c', 'echo "cat: disk error" >&2; exit 1'], { stdio: ['pipe', 'pipe', 'pipe'] })
    : realSpawn(site, key, command))
  try {
    const j = await J.settled(start(p), 60_000)
    assert.equal(j.state, 'failed')
    assert.match(j.message, /disk error/)
    assert.deepEqual(fs.readdirSync(destination), [])
  } finally { R.setRemoteSpawn(realSpawn) }
})

test('output a login prints ahead of the file is not part of it', async () => {
  const destination = fresh()
  const p = await plan({ library: 'far', paths: ['one.txt'], destination })
  R.setRemoteSpawn((site, key, command) => realSpawn(site, key, /^sh -c/.test(command) ? `echo "Welcome to the cluster"; ${command}` : command))
  try {
    const j = await J.settled(start(p), 60_000)
    assert.equal(j.state, 'done', j.message)
    assert.equal(fs.readFileSync(path.join(destination, 'one.txt'), 'utf8'), 'one')
  } finally { R.setRemoteSpawn(realSpawn) }
})

test('a path from a host is not believed: one outside what was asked stops the job', async () => {
  const sneaky = Buffer.from('F\x003 0 644\x00tree/../../evil\x00')
  const route = { kind: 'ssh', site: 'ssh://liar', key: '', root: hostRoot, name: 'liar', via: 'library' }
  R.setRemoteSpawn(() => spawn('bash', ['-c', `read line; printf '0 o%s e\\n' '${sneaky.toString('base64')}'; sleep 1`], { stdio: ['pipe', 'pipe', 'pipe'] }))
  try {
    await assert.rejects(S.listSource(route, ['tree']), /not under what was asked/)
    R.closeRemoteChannels()
    const other = Buffer.from('F\x003 0 644\x00other/x\x00')
    R.setRemoteSpawn(() => spawn('bash', ['-c', `read line; printf '0 o%s e\\n' '${other.toString('base64')}'; sleep 1`], { stdio: ['pipe', 'pipe', 'pipe'] }))
    await assert.rejects(S.listSource(route, ['tree']), /not under what was asked/)
  } finally { R.closeRemoteChannels(); R.setRemoteSpawn(realSpawn) }
})

test('a file that became a link since it was listed is not copied', async () => {
  const swap = path.join(hostRoot, 'swap.txt')
  write(swap, 'fine')
  const destination = fresh()
  const p = await plan({ library: 'far', paths: ['swap.txt'], destination })
  R.setRemoteSpawn((site, key, command) => {
    if (/^sh -c/.test(command)) { fs.rmSync(swap); fs.symlinkSync(path.join(outside, 'secret.txt'), swap) }
    return realSpawn(site, key, command)
  })
  try {
    const j = await J.settled(start(p), 60_000)
    assert.equal(j.state, 'failed')
    assert.match(j.message, /not a plain file/)
    assert.deepEqual(fs.readdirSync(destination), [])
  } finally { R.setRemoteSpawn(realSpawn); fs.rmSync(swap) }
})

test('a file that changes size while copied fails the check', async () => {
  const destination = fresh()
  const p = await plan({ library: 'far', paths: ['one.txt'], destination })
  R.setRemoteSpawn((site, key, command) => {
    if (/^sh -c/.test(command)) fs.writeFileSync(path.join(hostRoot, 'one.txt'), 'much longer now')
    return realSpawn(site, key, command)
  })
  try {
    const j = await J.settled(start(p), 60_000)
    assert.equal(j.state, 'failed')
    assert.match(j.message, /reported success, but the copy does not match/)
    assert.match(j.message, /15 bytes, expected 3/)
    assert.deepEqual(fs.readdirSync(destination), [])
  } finally { R.setRemoteSpawn(realSpawn); fs.writeFileSync(path.join(hostRoot, 'one.txt'), 'one') }
})

test('cancel stops a copy and leaves nothing behind', async () => {
  const destination = fresh()
  const p = await plan({ library: 'far', paths: ['tree'], destination })
  // A file that never finishes arriving.
  R.setRemoteSpawn((site, key, command) => /^sh -c/.test(command)
    ? spawn('bash', ['-c', `${command.replace(/exec cat/, 'head -c 10')}; exec sleep 30`], { stdio: ['pipe', 'pipe', 'pipe'] })
    : realSpawn(site, key, command))
  try {
    const j = start(p)
    while (j.phase !== 'copying' || !j.ref) await new Promise(r => setTimeout(r, 20))
    assert.ok(fs.readdirSync(destination).some(n => n.startsWith('.studio-transfer-')))
    assert.equal(J.cancelJob(j), true)
    await J.settled(j, 30_000)
    assert.equal(j.state, 'cancelled')
    assert.deepEqual(fs.readdirSync(destination), [])
    assert.equal(J.cancelJob(j), false)
  } finally { R.setRemoteSpawn(realSpawn) }
})

// ---- a mover that is not this process: the shape of a remote job service ----

/** Accepts at once, answers a status poll, and can say done on a bad result. */
function jobService(id, finish, o = {}) {
  const transfers = new Map()
  return {
    id, label: 'Job service', pollMs: 5,
    canReach: s => (s.kind === 'ssh' ? { ok: true } : { ok: false, why: 'it reads only hosts' }),
    async start(req) { const ref = `xfer-${transfers.size + 1}`; transfers.set(ref, { req, polls: 0 }); return ref },
    async status(ref) {
      const t = transfers.get(ref)
      if (o.throws && t.polls >= 1) throw new Error('401 invalid token')
      if (++t.polls < 3) return { state: 'running', files: 0 }
      await finish(t.req)
      return { state: 'done', bytes: 999, files: 999 }
    },
    async cancel() { return false },
  }
}
const asMover = async (mover, args) => {
  T.registerMover(mover)
  const p = await plan(args)
  return { ...p, plan: { ...p.plan, mover: mover.id } }
}
const copyAll = async req => {
  for (const f of req.files) { write(path.join(req.dest, f.out), fs.readFileSync(path.join(req.source.root, f.rel))) }
}

test('a job service fits the interface: id at once, polled, checked by the Studio', async () => {
  const destination = fresh()
  const p = await asMover(jobService('fine', copyAll), { library: 'far', paths: ['tree'], destination })
  const j = await J.settled(start(p), 30_000)
  T.unregisterMover('fine')
  assert.equal(j.state, 'done', j.message)
  // What the job reports is what the Studio counted, not the mover's 999.
  assert.deepEqual([j.files.done, j.bytes.done], [2, 70_003])
  assert.equal(j.ref, 'xfer-1')
})

test('a mover that says done on a bad result is a failure, and nothing appears', async () => {
  const cases = {
    // The file under a temporary name, as pftool left a large file.
    'temp name': [async req => { await copyAll(req); fs.renameSync(path.join(req.dest, 'tree', 'a.txt'), path.join(req.dest, 'tree', 'a.txt+20261104_172323')) }, /1 missing \(tree\/a\.txt\).*1 not expected \(tree\/a\.txt\+20261104/],
    'short file': [async req => { await copyAll(req); fs.truncateSync(path.join(req.dest, 'tree', 'sub', 'b.bin'), 10) }, /10 bytes, expected 70000/],
    'nothing at all': [async () => {}, /2 missing/],
    'a link': [async req => { await copyAll(req); fs.symlinkSync('/etc/hostname', path.join(req.dest, 'planted')) }, /1 not expected \(planted\)/],
  }
  let n = 0
  for (const [name, [finish, why]] of Object.entries(cases)) {
    const destination = fresh()
    const id = `bad${++n}`
    const p = await asMover(jobService(id, finish), { library: 'far', paths: ['tree'], destination })
    const j = await J.settled(start(p), 30_000)
    T.unregisterMover(id)
    assert.equal(j.state, 'failed', name)
    assert.match(j.message, /reported success, but the copy does not match/, name)
    assert.match(j.message, why, name)
    assert.deepEqual(fs.readdirSync(destination), [], name)
  }
})

test('a mover that stops answering loses the job, which is not done', async () => {
  const destination = fresh()
  const p = await asMover(jobService('mute', copyAll, { throws: true }), { library: 'far', paths: ['tree'], destination })
  const j = await J.settled(start(p), 30_000)
  T.unregisterMover('mute')
  assert.equal(j.state, 'interrupted')
  assert.match(j.message, /401 invalid token/)
  assert.deepEqual(fs.readdirSync(destination), [])
})

test('movers are chosen by what they can reach', () => {
  const fake = jobService('hosts-only', copyAll)
  assert.equal(T.moverFor({ kind: 'local', root: '/', name: 'x', via: 'library' }).mover.id, 'studio-copy')
  T.unregisterMover('studio-copy')
  T.registerMover(fake)
  try {
    assert.match(T.moverFor({ kind: 'local', root: '/', name: 'x', via: 'library' }).why, /reads only hosts/)
    assert.equal(T.moverFor({ kind: 'ssh', site: 'ssh://h', key: '', root: '/', name: 'h', via: 'library' }).mover.id, 'hosts-only')
  } finally { T.unregisterMover('hosts-only'); T.registerMover(copyMover) }
})

test('a host that runs only gufi_query cannot be copied from', () => {
  R.setDirect('ssh://locked', true)
  assert.match(copyMover.canReach({ kind: 'ssh', site: 'ssh://locked', key: '', root: '/', name: 'locked', via: 'library' }).why, /only gufi_query/)
  R.setDirect('ssh://locked', false)
})

test('a job found running at a start is interrupted, never done, and its staging goes', async () => {
  const destination = fresh()
  const p = await plan({ library: 'far', paths: ['tree'], destination })
  R.setRemoteSpawn((site, key, command) => /^sh -c/.test(command) ? spawn('sleep', ['30'], { stdio: ['pipe', 'pipe', 'pipe'] }) : realSpawn(site, key, command))
  const j = start(p)
  while (j.phase !== 'copying' || !j.ref) await new Promise(r => setTimeout(r, 20))
  await new Promise(r => setTimeout(r, 1100))
  const stage = fs.readdirSync(destination).find(n => n.startsWith('.studio-transfer-'))
  assert.ok(stage)
  J.cancelJob(j)
  await J.settled(j, 30_000)
  R.setRemoteSpawn(realSpawn)
  // The file as a process killed mid-copy left it, with its staging folder.
  fs.mkdirSync(path.join(destination, stage, 'data'), { recursive: true })
  const file = path.join(process.env.INDEX_BASE, 'transfers.json')
  await J.transfersWritten()
  const all = JSON.parse(fs.readFileSync(file, 'utf8'))
  const rec = all.find(x => x.id === j.id)
  Object.assign(rec, { state: 'running', phase: 'copying', finishedAt: null, message: null, stage: path.join(destination, stage) })
  fs.writeFileSync(file, JSON.stringify(all))
  J.resetTransfersForTests({ reread: true })
  J.recoverTransfers()
  const again = J.jobOf('local', j.id)
  assert.equal(again.state, 'interrupted')
  assert.match(again.message, /stopped before this finished/)
  assert.equal(again.phase, null)
  await new Promise(r => setTimeout(r, 100))
  assert.deepEqual(fs.readdirSync(destination), [])
  // A reload finds the finished jobs too.
  assert.ok(J.jobsOf('local').length > 5)
  assert.equal(J.jobOf('someone-else', j.id), null)
  assert.ok(!('stage' in J.publicJob(again)) && !('viewer' in J.publicJob(again)) && !('ref' in J.publicJob(again)))
})

test('only staging folders are ever removed', async () => {
  const keep = path.join(dir, 'keep')
  fs.mkdirSync(keep)
  await D.removeStaging(keep)
  await D.removeStaging(path.join(keep, '.studio-transfer-../..'))
  assert.ok(fs.existsSync(keep))
})

test('too much for the disk, or too many files, is refused with the limit stated', () => {
  assert.throws(() => D.assertFits(2_000, 1_000, downloads), /will not fit in ~\/Downloads, which has 1000 B free/)
  assert.doesNotThrow(() => D.assertFits(900, 1_000, downloads))
  assert.doesNotThrow(() => D.assertFits(10, null, downloads))
  assert.equal(S.MAX_FILES, 50_000)
})

test('the tools a copy runs are on this computer', t => {
  for (const tool of ['ssh', 'bash', 'sh', 'cat', 'find', 'stat', 'base64']) execFileSync('sh', ['-c', `command -v ${tool}`], { stdio: 'pipe' })
  // Recorded for the choice of command; not a requirement.
  for (const [name, args] of [['rsync', ['--version']], ['scp', ['-V']], ['sftp', ['-V']], ['tar', ['--version']]]) {
    let said = 'not found'
    try { said = execFileSync('sh', ['-c', `${name} ${args.join(' ')} 2>&1 | head -1`]).toString().trim() } catch { /* not found */ }
    t.diagnostic(`${name}: ${said}`)
  }
})
