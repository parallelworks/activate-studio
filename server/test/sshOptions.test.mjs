// ssh options a library over ssh carries (a jump host, a port, an
// identity, -o keywords from an allowlist), checked strictly and passed on
// every ssh the library makes; the shared connection's lifetime and its
// warming ahead of a search; and Query stacking libraries on one host into
// one gufi_vt location, so the host gets one gufi_query for all of them.
// The hosts are local, as in sshLibraries.test.mjs: the transport's spawn
// seam runs each command in /bin/sh with that host's HOME and PATH. One
// host's login runs only gufi_query, so each command there is its own call
// and can be counted. The end-to-end tests need GUFI (GUFI_BIN, else
// /opt/gufi/bin) and skip without it.
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

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sshopt-'))
const port = await new Promise(resolve => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)) }) })
process.env.PORT = String(port)
process.env.HOST = '127.0.0.1'
process.env.GUFI_BIN = gufiBin
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.XDG_RUNTIME_DIR = fs.mkdtempSync('/tmp/sshrun-')
test.after(() => fs.rmSync(process.env.XDG_RUNTIME_DIR, { recursive: true, force: true }))

const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
write(path.join(dir, 'kb', 'note.txt'), 'kb note\n')
const home = path.join(dir, 'home')
write(path.join(home, 'proj', 'data', 'huge.bin'), Buffer.alloc(5000, 98))
write(path.join(home, 'proj', 'readme.txt'), 'hello\n')
write(path.join(home, 'archive', 'old', 'tape.bin'), Buffer.alloc(9000, 99))
write(path.join(home, 'archive', 'list.txt'), 'list\n')
write(path.join(home, 'scratch', 'tmp', 'spill.bin'), Buffer.alloc(7000, 97))
for (const d of ['idx', 'arch-idx', 'scr-idx']) fs.mkdirSync(path.join(home, d), { recursive: true })
fs.mkdirSync(path.join(dir, 'ix', 'gufi'), { recursive: true })
if (haveGufi) {
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(home, 'proj'), path.join(home, 'idx')], { stdio: 'ignore' })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(home, 'archive'), path.join(home, 'arch-idx')], { stdio: 'ignore' })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(home, 'scratch'), path.join(home, 'scr-idx')], { stdio: 'ignore' })
  execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(dir, 'kb'), path.join(dir, 'ix', 'gufi')], { stdio: 'ignore' })
}

const R = await import('../dist/remoteExec.js')
// Every ssh the Studio starts, with its argv; and the commands that reached
// a host, for counting.
const sessions = []
const ran = []
R.setRemoteSpawn((site, key, command) => {
  const argv = R.sshArgv(site, command)
  sessions.push({ site, argv })
  const dest = R.parseSshSite(site).destination
  ran.push({ dest, command })
  // A login that runs only gufi_query refuses anything else, as a forced
  // command or a restricted shell would.
  if (dest.includes('restricted') && !/^\S*gufi_query /.test(command)) {
    return spawn('/bin/sh', ['-c', 'echo "only gufi_query is allowed" >&2; exit 1'], { stdio: ['pipe', 'pipe', 'pipe'] })
  }
  return spawn('/bin/sh', ['-c', command], { stdio: ['pipe', 'pipe', 'pipe'], env: { HOME: home, PATH: `${gufiBin}:/usr/bin:/bin` } })
})
// ssh on this computer: `ssh -G` says what the config would do, and the
// shared connection's check and warming are noted.
const local = []
R.setSshLocal(async args => {
  local.push(args)
  if (args[0] === '-G') {
    // As ssh prints it: the user from the destination, else from the
    // config ('me' here), and the host name without the user.
    const dest = args[args.length - 1]
    const at = dest.indexOf('@')
    const user = at >= 0 ? dest.slice(0, at) : 'me', host = at >= 0 ? dest.slice(at + 1) : dest
    return { code: 0, stdout: `user ${user}\nhostname ${host}\nport 22\n${dest.includes('viajump') ? 'proxyjump gw@bastion.example\n' : ''}`, stderr: '' }
  }
  if (args.includes('-O')) return { code: 255, stdout: '', stderr: 'Control socket connect: No such file or directory' }
  return { code: 0, stdout: '', stderr: '' }
})
const U = await import('../dist/userLibraries.js')
const V = await import('../dist/gufiVt.js')
const Q = await import('../dist/query.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
app.setErrorHandler((err, _req, reply) => reply.status(err.status ?? 500).send({ error: err.message }))
await app.register(U.userLibraryRoutes)
await app.register(V.gufiVtRoutes)
await app.listen({ port, host: '127.0.0.1' })
test.after(async () => { R.closeRemoteChannels(); R.setSshLocal(null); await app.close() })

const call = async (method, url, body) => { const r = await app.inject({ method, url, payload: body }); return { status: r.statusCode, body: r.json() } }

test('the allowed options parse to the argv ssh gets', () => {
  assert.deepEqual(R.parseSshOptions(''), [])
  assert.deepEqual(R.parseSshOptions('  -J me@jump.example -p 2222 -o ConnectTimeout=20 '),
    ['-J', 'me@jump.example', '-p', '2222', '-o', 'ConnectTimeout=20'])
  assert.deepEqual(R.parseSshOptions('-Jgw@a:2200,b.example -i ~/.ssh/id_lab -l me -F ~/.ssh/lab_config -4 -C'),
    ['-J', 'gw@a:2200,b.example', '-i', '~/.ssh/id_lab', '-l', 'me', '-F', '~/.ssh/lab_config', '-4', '-C'])
  assert.deepEqual(R.parseSshOptions('-o ProxyJump=gw@bastion -oUser=me -o StrictHostKeyChecking=accept-new -o GSSAPIAuthentication=yes'),
    ['-o', 'ProxyJump=gw@bastion', '-o', 'User=me', '-o', 'StrictHostKeyChecking=accept-new', '-o', 'GSSAPIAuthentication=yes'])
  assert.deepEqual(R.parseSshOptions('-o KexAlgorithms=sntrup761x25519-sha512@openssh.com,curve25519-sha256 -o UserKnownHostsFile=/tmp/kh'),
    ['-o', 'KexAlgorithms=sntrup761x25519-sha512@openssh.com,curve25519-sha256', '-o', 'UserKnownHostsFile=/tmp/kh'])
  assert.equal(R.jumpOf(R.parseSshOptions('-J gw@bastion')), 'gw@bastion')
  assert.equal(R.jumpOf(R.parseSshOptions('-o proxyjump=gw@b2')), 'gw@b2')
  assert.equal(R.jumpOf(R.parseSshOptions('-p 22')), null)
})

test('anything that runs a command, forwards, or reaches a shell is refused, with the reason', () => {
  const refused = (text, re) => assert.throws(() => R.parseSshOptions(text), e => e.status === 400 && re.test(e.message), text)
  refused('-o ProxyCommand=nc', /ProxyCommand runs a local command: use -J/)
  refused('-o LocalCommand=id', /LocalCommand runs a local command/)
  refused('-o PermitLocalCommand=yes', /runs a local command/)
  refused('-o RemoteCommand=sh', /replaces the command/)
  refused('-o KnownHostsCommand=x', /runs a local command/)
  refused('-o BatchMode=no', /without prompts/)
  refused('-o ControlPath=/tmp/x', /shares connections itself/)
  refused('-o SendEnv=LANG', /SendEnv is not allowed here; a Host entry in ~\/.ssh\/config can set it/)
  refused('-o LocalForward=8080', /not allowed here/)
  refused('-o Include=/etc/x', /not allowed here/)
  refused('-L 8080:localhost:80', /is not one of -J, -p, -i, -l, -F, -o/)
  refused('-A', /is not one of/)
  refused('-t', /is not one of/)
  refused('me@box', /is not one of/)
  refused('-o User', /write it as Keyword=value/)
  refused('-J', /-J needs a value/)
  refused('-p 70000', /a port from 1 to 65535/)
  refused('-p 22 -p', /needs a value/)
  // Injection attempts: no value may carry shell text or a second option.
  refused('-J a;id', /-J a;id/)
  refused('-J -oProxyCommand=id', /-J -oProxyCommand=id/)
  refused('-i /tmp/$(id)', /an absolute path/)
  refused('-i relative/key', /an absolute path/)
  refused('-o HostName=a|b', /not a valid value for HostName/)
  refused('-o Port=22x', /not a valid value/)
  refused('-o User=me`id`', /no quotes or backslashes/)
  refused('-o "User=me"', /no quotes or backslashes/)
  refused('-o User=me\\x', /no quotes or backslashes/)
  refused('-l me@x', /a login name/)
  refused('x'.repeat(501), /at most 500 characters/)
  // The same checks hold on argv already split, as a saved library has it.
  assert.throws(() => R.checkSshOptions(['-o', 'ProxyCommand=nc %h %p']), /ProxyCommand/)
  assert.throws(() => R.checkSshOptions(['-J', 'a b']), /-J a b/)
})

test('a site carries its options: read back, shown, a socket per place reached, the port given once', () => {
  const opts = R.parseSshOptions('-J gw@bastion -o ConnectTimeout=20')
  const site = R.sshSite('me@box', 2222, opts)
  assert.match(site, /^ssh:\/\/me@box:2222#[A-Za-z0-9_-]+$/)
  assert.deepEqual(R.parseSshSite(site), { destination: 'me@box', port: 2222, options: opts })
  assert.equal(R.siteName(site), 'me@box:2222', 'messages name the host, not the options')
  assert.deepEqual(R.shellFor(site), ['ssh', '-J', 'gw@bastion', '-o', 'ConnectTimeout=20', '-p', '2222', 'me@box'])
  // The library's options come right after -T, ahead of the defaults, so
  // its ConnectTimeout is the one ssh uses (ssh keeps a keyword's first value).
  const argv = R.sshArgv(site, 'CMD')
  assert.deepEqual(argv.slice(0, 5), ['-T', '-J', 'gw@bastion', '-o', 'ConnectTimeout=20'])
  assert.deepEqual(argv.slice(5, 7), ['-o', 'BatchMode=yes'])
  assert.deepEqual(argv.slice(-4), ['-p', '2222', 'me@box', 'CMD'])
  // The socket is named by ssh's %C, a hash of where the connection lands
  // (user, host name, port, jump host), which ssh works out itself, and a
  // hash of the library's options when it has any: options apply when ssh
  // logs in, so a set of options gets a connection of its own.
  const sockOf = argv => execFileSync('ssh', ['-G', ...argv], { stdio: ['ignore', 'pipe', 'ignore'] }).toString().split('\n').find(l => l.startsWith('controlpath '))
  const withJump = R.sshArgv(site, 'CMD').slice(1, -1)
  const plain = R.sshArgv(R.sshSite('me@box', 2222), 'CMD').slice(1, -1)
  const timeoutOnly = R.sshArgv(R.sshSite('me@box', 2222, R.parseSshOptions('-o ConnectTimeout=20')), 'CMD').slice(1, -1)
  if (R.controlArgs(R.parseSshSite(site)).some(a => a.includes('/%C'))) {
    assert.notEqual(sockOf(withJump), sockOf(plain), 'a jump host is another way of reaching the host')
    assert.notEqual(sockOf(timeoutOnly), sockOf(plain), 'options of its own, a connection of its own')
    assert.notEqual(sockOf(timeoutOnly), sockOf(withJump))
  }
  assert.equal(R.sshSite('me@box'), 'ssh://me@box', 'no options, the site as before')
  assert.throws(() => R.sshSite('me@box', 2222, ['-p', '2200']), /give the port once/)
  assert.throws(() => R.sshSite('me@box', 2222, ['-o', 'Port=2200']), /give the port once/)
  // A site whose options were tampered with is refused, not run.
  const bad = `ssh://me@box#${Buffer.from(JSON.stringify(['-o', 'ProxyCommand=id'])).toString('base64url')}`
  assert.throws(() => R.parseSshSite(bad), /ProxyCommand/)
  assert.throws(() => R.parseSshSite('ssh://me@box#notbase64json'), /invalid ssh site/)
})

test('a socket per account reached and set of options: the same options share one, other options get their own', () => {
  const sock = site => R.sshArgv(site, 'x').find(a => a.startsWith('ControlPath='))
  const kh = ['-o', 'UserKnownHostsFile=/tmp/kh']
  assert.notEqual(sock(R.sshSite('me@box', null, kh)), sock(R.sshSite('me@box')), 'options of its own, a connection of its own')
  assert.equal(sock(R.sshSite('me@box', null, ['-o', 'userknownhostsfile=/tmp/kh'])), sock(R.sshSite('me@box', null, kh)), 'a keyword in any case is the same option')
  assert.notEqual(sock(R.sshSite('me@box', null, ['-o', 'UserKnownHostsFile=/tmp/kh2'])), sock(R.sshSite('me@box', null, kh)))
  assert.equal(sock(R.sshSite('box')), sock(R.sshSite('me@box')), 'no options: the name is ssh\'s alone')
  if (!sock(R.sshSite('me@box', null, kh)).includes('%C')) return
  assert.match(sock(R.sshSite('me@box')), /\/%C"$/)
  assert.match(sock(R.sshSite('me@box', null, kh)), /\/%C-[0-9a-f]{8}"$/)
  // As ssh expands them, with no ~/.ssh/config: an alias and user@host
  // with no options are one socket, and the same with options another.
  const me = os.userInfo().username
  const expand = site => execFileSync('ssh', ['-G', '-F', '/dev/null', ...R.sshArgv(site, 'x').slice(1, -1)], { stdio: ['ignore', 'pipe', 'ignore'] })
    .toString().split('\n').find(l => l.startsWith('controlpath '))
  assert.equal(expand(R.sshSite('127.0.0.1')), expand(R.sshSite(`${me}@127.0.0.1`)))
  assert.notEqual(expand(R.sshSite(`${me}@127.0.0.1`, null, kh)), expand(R.sshSite(`${me}@127.0.0.1`)))
  assert.equal(expand(R.sshSite('127.0.0.1', null, kh)), expand(R.sshSite(`${me}@127.0.0.1`, null, kh)))
})

test('the shared connection stays ten minutes after its last session, or STUDIO_SSH_PERSIST seconds', () => {
  const persist = () => R.controlArgs({ destination: 'me@box', port: null }).find(a => a.startsWith('ControlPersist='))
  assert.equal(persist(), 'ControlPersist=600')
  process.env.STUDIO_SSH_PERSIST = '1800'
  assert.equal(persist(), 'ControlPersist=1800')
  process.env.STUDIO_SSH_PERSIST = '0'
  assert.equal(persist(), 'ControlPersist=no')
  process.env.STUDIO_SSH_PERSIST = '999999'
  assert.equal(persist(), 'ControlPersist=86400', 'at most a day')
  process.env.STUDIO_SSH_PERSIST = 'soon'
  assert.equal(persist(), 'ControlPersist=600')
  delete process.env.STUDIO_SSH_PERSIST
})

let lib
test('a library over ssh keeps its options and every ssh it makes carries them', { skip }, async () => {
  const refused = await call('POST', '/api/user-libraries/ssh/test', { destination: 'me@linux-box', indexRoot: '~/idx/proj', sshOptions: '-o ProxyCommand=nc' })
  assert.equal(refused.status, 400)
  assert.match(refused.body.error, /ssh options: ProxyCommand is not allowed here/)

  const body = { destination: 'me@linux-box', indexRoot: '~/idx/proj', sshOptions: '-J me@jump.example -o ConnectTimeout=20' }
  const probe = await call('POST', '/api/user-libraries/ssh/test', body)
  assert.equal(probe.body.ok, true, JSON.stringify(probe.body.steps))
  assert.match(probe.body.steps[0].detail, /^Connected to me@linux-box through me@jump\.example, a /)

  const added = await call('POST', '/api/user-libraries/ssh', { ...body, label: 'Project' })
  assert.equal(added.status, 200, JSON.stringify(added.body))
  lib = added.body
  assert.deepEqual([lib.sshOptions, lib.jump], ['-J me@jump.example -o ConnectTimeout=20', 'me@jump.example'])
  const listed = (await call('GET', '/api/user-libraries')).body.libraries.find(l => l.id === lib.id)
  assert.equal(listed.sshOptions, '-J me@jump.example -o ConnectTimeout=20')

  // A tree, a search and a query each go over ssh with the options first.
  const L = await import('../dist/libraries.js')
  const G = await import('../dist/gufi.js')
  const root = L.listLibraries('kb', null).find(l => l.id === lib.id).indexRoot
  R.closeRemoteChannels()
  sessions.length = 0
  await U.remoteTree(root, '')
  await G.searchNames('huge', 5, root)
  assert.ok(sessions.length >= 1)
  for (const s of sessions) {
    assert.equal(R.parseSshSite(s.site).destination, 'me@linux-box')
    assert.deepEqual(s.argv.slice(0, 5), ['-T', '-J', 'me@jump.example', '-o', 'ConnectTimeout=20'], s.argv.join(' '))
  }
})

test('options can be changed on a library, after a test with them passes', { skip }, async () => {
  const bad = await call('PUT', `/api/user-libraries/${lib.id}/ssh-options`, { sshOptions: '-o LocalCommand=id' })
  assert.equal(bad.status, 400)
  assert.match(bad.body.error, /LocalCommand runs a local command/)
  const twice = await call('PUT', `/api/user-libraries/${lib.id}/ssh-options`, { sshOptions: '-p 2222' })
  assert.equal(twice.status, 200, 'no port on the library, so -p is fine')
  assert.equal(twice.body.sshOptions, '-p 2222')
  const cleared = await call('PUT', `/api/user-libraries/${lib.id}/ssh-options`, { sshOptions: '' })
  assert.equal(cleared.status, 200)
  assert.deepEqual([cleared.body.sshOptions, cleared.body.jump], [null, null])
})

test('the ssh step names a jump host from ~/.ssh/config, read with ssh -G', { skip }, async () => {
  const r = await call('POST', '/api/user-libraries/ssh/test', { destination: 'me@viajump-box', indexRoot: '~/idx/proj' })
  assert.equal(r.body.ok, true, JSON.stringify(r.body.steps))
  assert.match(r.body.steps[0].detail, /^Connected to me@viajump-box through gw@bastion\.example, a /)
  const g = local.filter(a => a[0] === '-G').pop()
  assert.equal(g[g.length - 1], 'me@viajump-box')
})

test('warming opens each host\'s shared connection with its options, at most once a minute', async () => {
  local.length = 0
  const site = R.sshSite('me@warm-box', null, ['-J', 'gw@bastion'])
  assert.equal(await R.warmSsh(site), 'opened')
  const [check, open] = local
  assert.ok(check.includes('-O') && check.includes('check'), check.join(' '))
  assert.deepEqual(open.slice(0, 3), ['-T', '-J', 'gw@bastion'])
  assert.ok(open.includes('-N') && open.includes('-f'), open.join(' '))
  assert.ok(open.some(a => a.startsWith('ControlPath=')), 'the same socket later sessions use')
  assert.equal(open[open.length - 1], 'me@warm-box')
  assert.equal(await R.warmSsh(site), 'opened')
  assert.equal(local.length, 2, 'not again within the minute')
  // A cluster over pw ssh, or with sharing off, is not warmed this way.
  assert.equal(await R.warmSsh('cluster-a'), 'off')
  process.env.STUDIO_SSH_SHARE = '0'
  assert.equal(await R.warmSsh(R.sshSite('me@other-box')), 'off')
  delete process.env.STUDIO_SSH_SHARE
})

test('the warm route asks each of the viewer\'s hosts once', { skip }, async () => {
  local.length = 0
  const r = await call('POST', '/api/user-libraries/warm', {})
  assert.equal(r.status, 200)
  assert.equal(r.body.hosts, 1, 'the one library over ssh')
  await new Promise(res => setTimeout(res, 50))
  assert.ok(local.some(a => a.includes('-O')), 'its connection was checked')
})

test('Query stacks libraries on one host into one gufi_vt location: one command there, rows back to their library', { skip }, async t => {
  V.resetGufiVtForTests()
  if (!(await V.gufiVtAvailable())) return t.skip('this GUFI has no gufi_vt')
  // Two indexes on a host whose login runs only gufi_query, so each
  // command is its own call and can be counted.
  const host = { destination: 'me@restricted-box', gufiBin }
  for (const [label, indexRoot] of [['Home', path.join(home, 'idx', 'proj')], ['Archive', path.join(home, 'arch-idx', 'archive')]]) {
    const r = await call('POST', '/api/user-libraries/ssh', { ...host, label, indexRoot })
    assert.equal(r.status, 200, JSON.stringify(r.body))
  }
  const L = await import('../dist/libraries.js')
  const ids = Object.fromEntries(L.listLibraries('kb', null).filter(l => l.site === 'me@restricted-box').map(l => [l.label, l.id]))
  const count = () => ran.filter(r => r.dest === 'me@restricted-box' && /gufi_query /.test(r.command)).length
  const query = async () => {
    const before = count()
    const t0 = Date.now()
    const r = await Q.queryScope({ canned: 'largest', n: 10, library: 'all' }, null)
    return { r, commands: count() - before, ms: Date.now() - t0 }
  }
  process.env.STUDIO_STACK_REMOTE = '0'
  const apart = await query()
  delete process.env.STUDIO_STACK_REMOTE
  const stacked = await query()
  for (const { r } of [apart, stacked]) {
    assert.equal(r.engine, 'gufi_vt', JSON.stringify(r.failed ?? r))
    const byLib = Object.fromEntries(r.rows.map((row, i) => [row[1], r.rowLibraries[i]]))
    assert.equal(byLib['old/tape.bin'], ids.Archive, 'a row goes back to its own library')
    assert.equal(byLib['data/huge.bin'], ids.Home)
    assert.equal(r.rows.find(row => row[1] === 'old/tape.bin')[0], 'Archive')
  }
  assert.deepEqual(stacked.r.rows, apart.r.rows, 'the same answer')
  assert.equal(apart.commands, 4, 'apart: a check and a gufi_query for each library')
  assert.equal(stacked.commands, 2, 'stacked: one check and one gufi_query for both')
  console.log(`  query over two libraries on one host: ${apart.commands} commands in ${apart.ms} ms apart, ${stacked.commands} in ${stacked.ms} ms stacked`)

  // One of the two gone: it is reported, the other still answers.
  fs.renameSync(path.join(home, 'arch-idx'), path.join(home, 'arch-idx.moved'))
  try {
    const r = await Q.queryScope({ canned: 'largest', n: 10, library: 'all' }, null)
    assert.ok(r.rowLibraries.includes(ids.Home))
    assert.ok(!r.rowLibraries.includes(ids.Archive))
    assert.ok((r.failed ?? []).some(f => f.library === 'Archive'), JSON.stringify(r.failed))
    assert.ok((r.omitted ?? []).some(o => o.id === ids.Archive), JSON.stringify(r.omitted))
  } finally {
    fs.renameSync(path.join(home, 'arch-idx.moved'), path.join(home, 'arch-idx'))
  }
})

test('two of three libraries on one host, chosen alone: still one location, and the third not asked', { skip }, async t => {
  V.resetGufiVtForTests()
  if (!(await V.gufiVtAvailable())) return t.skip('this GUFI has no gufi_vt')
  const host = { destination: 'me@restricted-box', gufiBin }
  const r0 = await call('POST', '/api/user-libraries/ssh', { ...host, label: 'Scratch', indexRoot: path.join(home, 'scr-idx', 'scratch') })
  assert.equal(r0.status, 200, JSON.stringify(r0.body))
  const L = await import('../dist/libraries.js')
  const ids = Object.fromEntries(L.listLibraries('kb', null).filter(l => l.site === 'me@restricted-box').map(l => [l.label, l.id]))
  assert.deepEqual(Object.keys(ids).sort(), ['Archive', 'Home', 'Scratch'])
  const queries = () => ran.filter(r => r.dest === 'me@restricted-box' && /gufi_query /.test(r.command))
  const before = queries().length
  const r = await Q.queryScope({ canned: 'largest', n: 10, library: [ids.Home, ids.Archive] }, null)
  const mine = queries().slice(before)
  assert.equal(r.engine, 'gufi_vt', JSON.stringify(r.failed ?? r))
  assert.deepEqual([...new Set(r.rowLibraries)].sort(), [ids.Archive, ids.Home].sort())
  assert.equal(mine.length, 2, 'one check and one gufi_query for the two chosen')
  const last = mine[mine.length - 1].command
  assert.ok(last.includes('arch-idx/archive') && last.includes('idx/proj'), last)
  assert.ok(!last.includes('scr-idx'), 'the library not chosen is not in the stacked location')
  // The Search page's refined search over the same two: one walk on the
  // host, the name filter in it, the third library not asked.
  const S = await import('../dist/libraryScope.js')
  const libs = S.scopeLibraries([ids.Home, ids.Archive], null, 'kb')
  const b2 = queries().length
  const hits = await S.searchScope(libs, '', 20, { semantic: false, refine: { name: '*.bin', text: '', match: 'all' } })
  const walks = queries().slice(b2)
  assert.deepEqual(hits.map(h => [h.library.id, h.path]).sort(), [[ids.Archive, 'old/tape.bin'], [ids.Home, 'data/huge.bin']].sort())
  assert.equal(walks.length, 1, 'one walk on the host for both')
  // Over ssh the line is quoted for the remote shell, so match the filter, not its quoting.
  assert.ok(/name LIKE .{0,6}%\.bin.{0,6} ESCAPE/.test(walks[0].command), `the name filter went with it: ${walks[0].command.slice(0, 400)}`)
  assert.ok(!walks[0].command.includes('scr-idx'))
  // And with text and the name filter both given, the words statement
  // carries the filter in the same remote walk: these connected indexes
  // have no words table of their own, so it is the one that reads an
  // external database in the shared schema.
  const b3 = queries().length
  await S.searchScope(libs, '', 20, { semantic: false, refine: { name: '*.bin', text: 'hello', match: 'all' } })
  const walk = queries().slice(b3)
  assert.equal(walk.length, 1)
  assert.match(walk[0].command, /ext_bm25_by_file MATCH .{0,6}hello.{0,6} AND \(e\.name LIKE .{0,6}%\.bin.{0,6} ESCAPE/)
  assert.ok(!/SELECT .{0,6}n.{0,6}, /.test(walk[0].command.split(' -E ')[1] ?? ''), 'no name rows when both fields must match')
})

test('a library that names the user and one that takes it from ssh config are one host: one location, one walk, one socket', { skip }, async t => {
  V.resetGufiVtForTests()
  if (!(await V.gufiVtAvailable())) return t.skip('this GUFI has no gufi_vt')
  // The same account: the user typed in one, taken from ~/.ssh/config
  // (ssh -G) in the other.
  // Indexes of their own: a folder can be one library only.
  for (const [src, to] of [['proj', 't-idx'], ['scratch', 'c-idx']]) {
    fs.mkdirSync(path.join(home, to), { recursive: true })
    execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(home, src), path.join(home, to)], { stdio: 'ignore' })
  }
  for (const [label, destination, indexRoot] of [['Typed', 'me@restricted-box', path.join(home, 't-idx', 'proj')], ['Config', 'restricted-box', path.join(home, 'c-idx', 'scratch')]]) {
    const r = await call('POST', '/api/user-libraries/ssh', { destination, gufiBin, label, indexRoot })
    assert.equal(r.status, 200, JSON.stringify(r.body))
  }
  const L = await import('../dist/libraries.js')
  const all = L.listLibraries('kb', null)
  const ids = Object.fromEntries(all.filter(l => ['Typed', 'Config'].includes(l.label)).map(l => [l.label, l.id]))
  assert.deepEqual(all.filter(l => ['Typed', 'Config'].includes(l.label)).map(l => l.site), ['me@restricted-box', 'me@restricted-box'], 'both shown as the account reached')
  const pub = (await call('GET', '/api/user-libraries')).body.libraries.find(l => l.label === 'Config')
  assert.equal(pub.reached, 'me@restricted-box')
  const queries = () => ran.filter(r => r.dest.endsWith('restricted-box') && /gufi_query /.test(r.command))

  let before = queries().length
  const r = await Q.queryScope({ canned: 'largest', n: 10, library: [ids.Typed, ids.Config] }, null)
  const mine = queries().slice(before)
  assert.equal(r.engine, 'gufi_vt', JSON.stringify(r.failed ?? r))
  assert.deepEqual([...new Set(r.rowLibraries)].sort(), [ids.Config, ids.Typed].sort())
  assert.equal(mine.length, 2, 'one check and one gufi_query for both')
  assert.ok(mine[1].command.includes('t-idx/proj') && mine[1].command.includes('c-idx/scratch'), mine[1].command)

  const S = await import('../dist/libraryScope.js')
  before = queries().length
  const hits = await S.searchScope(S.scopeLibraries([ids.Typed, ids.Config], null, 'kb'), '', 20, { semantic: false, refine: { name: '*.bin', text: '', match: 'all' } })
  assert.deepEqual(hits.map(h => [h.library.id, h.path]).sort(), [[ids.Config, 'tmp/spill.bin'], [ids.Typed, 'data/huge.bin']].sort())
  assert.equal(queries().length - before, 1, 'one walk on the host for both')

  // One shared connection: ssh names the socket by %C, which it works out
  // from where the connection lands, the same for both.
  const sock = site => R.sshArgv(site, 'x').find(a => a.startsWith('ControlPath='))
  const typed = sock(R.sshSite('me@restricted-box')), config = sock(R.sshSite('restricted-box'))
  assert.equal(typed, config)
  assert.match(typed, /\/%C"$/)
})

test('libraries on one host with different ssh options are a connection and a walk each, and each one\'s options reach ssh', { skip }, async t => {
  V.resetGufiVtForTests()
  if (!(await V.gufiVtAvailable())) return t.skip('this GUFI has no gufi_vt')
  for (const [src, to] of [['proj', 'p-idx'], ['archive', 'o-idx']]) {
    fs.mkdirSync(path.join(home, to), { recursive: true })
    execFileSync(path.join(gufiBin, 'gufi_dir2index'), ['-x', path.join(home, src), path.join(home, to)], { stdio: 'ignore' })
  }
  const kh = path.join(dir, 'known_hosts_opts')
  const opts = `-o UserKnownHostsFile=${kh} -o StrictHostKeyChecking=accept-new`
  for (const [label, sshOptions, indexRoot] of [['Plain', '', path.join(home, 'p-idx', 'proj')], ['Options', opts, path.join(home, 'o-idx', 'archive')]]) {
    const r = await call('POST', '/api/user-libraries/ssh', { destination: 'me@restricted-two', gufiBin, label, indexRoot, ...(sshOptions ? { sshOptions } : {}) })
    assert.equal(r.status, 200, JSON.stringify(r.body))
  }
  const L = await import('../dist/libraries.js')
  const ids = Object.fromEntries(L.listLibraries('kb', null).filter(l => ['Plain', 'Options'].includes(l.label)).map(l => [l.label, l.id]))
  // The gufi_query runs on that host, each with the ssh argv it went by.
  const runs = from => sessions.slice(from).filter(x => R.parseSshSite(x.site).destination === 'me@restricted-two' && /gufi_query /.test(x.argv[x.argv.length - 1]))
  const carries = x => x.argv.includes(`UserKnownHostsFile=${kh}`)

  let mark = sessions.length
  const r = await Q.queryScope({ canned: 'largest', n: 10, library: [ids.Plain, ids.Options] }, null)
  assert.equal(r.engine, 'gufi_vt', JSON.stringify(r.failed ?? r))
  assert.deepEqual([...new Set(r.rowLibraries)].sort(), [ids.Options, ids.Plain].sort())
  let mine = runs(mark)
  assert.ok(mine.every(x => !(x.argv.at(-1).includes('p-idx/proj') && x.argv.at(-1).includes('o-idx/archive'))), 'not stacked into one location')
  const queried = mine.filter(x => /o-idx\/archive/.test(x.argv.at(-1)))
  assert.ok(queried.length > 0 && queried.every(carries), 'the options reach every ssh for the library that has them')
  assert.ok(mine.filter(x => /p-idx\/proj/.test(x.argv.at(-1))).every(x => !carries(x)), 'and no other')
  const path0 = x => x.argv.find(a => a.startsWith('ControlPath='))
  assert.notEqual(path0(queried[0]), path0(mine.find(x => /p-idx\/proj/.test(x.argv.at(-1)))), 'a connection each')

  const S = await import('../dist/libraryScope.js')
  mark = sessions.length
  const hits = await S.searchScope(S.scopeLibraries([ids.Plain, ids.Options], null, 'kb'), '', 20, { semantic: false, refine: { name: '*.bin', text: '', match: 'all' } })
  assert.deepEqual(hits.map(h => [h.library.id, h.path]).sort(), [[ids.Options, 'old/tape.bin'], [ids.Plain, 'data/huge.bin']].sort())
  mine = runs(mark)
  assert.equal(mine.length, 2, 'a walk each')
  assert.deepEqual(mine.map(carries).sort(), [false, true])
})

test('the wrapper accepts a stacked location\'s roots in order, and nothing else', () => {
  const roots = ['/c/idx/proj', '/c/arch idx/archive']
  const line = ['gufi_query', '--print-tlv', '-x', '--threads', '4', '-E', 'SELECT 1;', '/c/idx/proj', '/c/arch', 'idx/archive']
  assert.deepEqual(V.checkRemoteArgs(line, roots), ['--print-tlv', '-x', '--threads', '4', '-E', 'SELECT 1;', '/c/idx/proj', '/c/arch idx/archive'])
  assert.equal(V.checkRemoteArgs(line, [roots[1], roots[0]]), null, 'in order')
  assert.equal(V.checkRemoteArgs([...line, '/etc'], roots), null)
  const plan = { stages: { E: 'SELECT 1;' }, columns: ['x'] }
  const s = V.vtScript([{ id: 'stack0', index: roots[0], indexes: roots, plan, remote: { token: 't', site: 'cluster-a', gufiBin: '' } }], null)
  assert.match(s, /gufi_vt\(index='\/c\/idx\/proj', index='\/c\/arch idx\/archive', threads=4/)
})
