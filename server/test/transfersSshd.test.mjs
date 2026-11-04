// Copies over real ssh: two sshd processes run by this user on high ports,
// with no root and nothing installed. One host has a shell and holds a
// library. The other runs only gufi_query, so its library's files are copied
// through a transfer host, from another path there (Originals via, with
// `root`). Skipped unless STUDIO_TEST_SSHD=1.
//
//   STUDIO_TEST_SSHD=1 node --import ./test/setup.mjs --test test/transfersSshd.test.mjs
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

const dir = fs.realpathSync(fs.mkdtempSync('/tmp/mvssh-'))
const user = os.userInfo().username
const freePort = () => new Promise(resolve => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)) }) })
const [portShell, portGate] = [await freePort(), await freePort()]
process.env.HOST = '127.0.0.1'
process.env.HOME = path.join(dir, 'home')
process.env.KB_ROOT = path.join(dir, 'kb')
process.env.INDEX_BASE = path.join(dir, 'ix')
process.env.GUFI_BIN = path.join(dir, 'no-gufi')
process.env.XDG_RUNTIME_DIR = fs.mkdtempSync('/tmp/mvsshrun-')
process.env.STUDIO_TRANSFER_REPORT_MS = '60000'
delete process.env.AUTH_JWKS_URL
const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
const daemons = []
const data = path.join(dir, 'xfer', 'data')   // the files, on the host with a shell
const downloads = path.join(dir, 'home', 'Downloads')
const odd = ['with space.md', "it's.md", 'quo"te.md', 'café ☕.md', '-dash.md', '--rsh=touch pwned', '$(touch pwned).md', 'new\nline.md']
const BIG = 60 * 1024 * 1024

test.after(async () => {
  const R = await import('../dist/remoteExec.js')
  R.closeRemoteChannels()
  for (const d of daemons) d.kill()
  fs.rmSync(process.env.XDG_RUNTIME_DIR, { recursive: true, force: true })
  fs.rmSync(dir, { recursive: true, force: true })
})

if (!skip) {
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', path.join(dir, 'id')])
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', path.join(dir, 'host')])
  fs.copyFileSync(path.join(dir, 'id.pub'), path.join(dir, 'authorized_keys'))
  fs.chmodSync(path.join(dir, 'authorized_keys'), 0o600)
  write(path.join(dir, 'forced.sh'), '#!/bin/sh\necho "This account may run only gufi_query." >&2\nexit 1\n')
  fs.chmodSync(path.join(dir, 'forced.sh'), 0o755)
  for (const [port, extra] of [[portShell, ''], [portGate, `ForceCommand ${path.join(dir, 'forced.sh')}\n`]]) {
    const f = path.join(dir, `sshd-${port}.conf`)
    write(f, `Port ${port}\nListenAddress 127.0.0.1\nHostKey ${path.join(dir, 'host')}\nPidFile ${path.join(dir, `sshd-${port}.pid`)}\nAuthorizedKeysFile ${path.join(dir, 'authorized_keys')}\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nUsePAM no\nStrictModes no\nLogLevel ERROR\n${extra}`)
    daemons.push(spawn(SSHD, ['-D', '-e', '-f', f], { stdio: 'ignore' }))
    for (let i = 0; i < 100; i++) {
      if (await new Promise(r => { const s = net.connect(port, '127.0.0.1', () => { s.destroy(); r(true) }); s.on('error', () => r(false)) })) break
      await new Promise(r => setTimeout(r, 50))
    }
  }
  write(path.join(data, 'report.md'), 'the original\n')
  for (const n of odd) write(path.join(data, 'odd', n), `contents of ${n}\n`)
  const big = crypto.randomBytes(1024 * 1024)
  fs.mkdirSync(path.join(data, 'big'), { recursive: true })
  const fd = fs.openSync(path.join(data, 'big', 'large.bin'), 'w')
  for (let i = 0; i < BIG / big.length; i++) fs.writeSync(fd, big)
  fs.closeSync(fd)
  fs.symlinkSync(path.join(dir, 'home'), path.join(data, 'way-out'))
  fs.mkdirSync(process.env.KB_ROOT, { recursive: true })
  fs.mkdirSync(downloads, { recursive: true })
  const options = `-i ${path.join(dir, 'id')} -o IdentitiesOnly=yes -o UserKnownHostsFile=${path.join(dir, 'kh')} -o StrictHostKeyChecking=no -o ConnectTimeout=10`.split(' ')
  const def = extra => ({
    transport: 'ssh', site: `${user}@127.0.0.1`, destination: `${user}@127.0.0.1`, sshOptions: options, resource: 'x', indexRoot: path.join(dir, 'idx'), gufiBin: '',
    fullText: true, state: 'ready', run: null, error: null, builtAt: new Date().toISOString(), directories: 1, files: 1, scheduler: null, ...extra,
  })
  const hash = crypto.createHash('sha256').update('single').digest('hex').slice(0, 16)
  write(path.join(dir, 'ix', 'users', hash, 'owner'), 'single')
  write(path.join(dir, 'ix', 'users', hash, 'libraries.json'), JSON.stringify([
    def({ id: 'u-own', label: 'Own host', port: portShell, sourceDir: data, sourceRoot: data }),
    def({
      id: 'u-gate', label: 'Gated', port: portGate, sourceDir: '/proj/not/here', sourceRoot: '/proj/not/here', shell: false,
      readThrough: { destination: `${user}@127.0.0.1`, port: portShell, sshOptions: options, root: data },
    }),
  ]))
}

const P = skip ? {} : await import('../dist/movers/plan.js')
const J = skip ? {} : await import('../dist/movers/jobs.js')
let seq = 0
const plan = async args => {
  const p = await P.planTransfer(args, { viewerId: null, library: null })
  assert.ok(!('problem' in p), p.problem)
  return p
}
const start = p => J.startApproved({ conversationId: 'c', toolCallId: `t${++seq}`, viewer: 'local', args: p.canonical, plan: p.plan })
const fresh = () => { const f = path.join(downloads, `t${++seq}`); fs.mkdirSync(f); return f }
const sha = f => crypto.createHash('sha256').update(fs.readFileSync(f)).digest('hex')

test('a library on a host with a shell: odd names over real ssh', { skip }, async () => {
  const destination = fresh()
  const p = await plan({ library: 'u-own', paths: ['odd'], destination })
  assert.match(p.plan.source, /over ssh$/)
  const j = await J.settled(start(p), 60_000)
  assert.equal(j.state, 'done', j.message)
  assert.deepEqual(fs.readdirSync(path.join(destination, 'odd')).sort(), [...odd].sort())
  for (const n of odd) assert.equal(fs.readFileSync(path.join(destination, 'odd', n), 'utf8'), `contents of ${n}\n`)
  assert.ok(!fs.existsSync(path.join(process.env.HOME, 'pwned')) && !fs.existsSync(path.join(data, 'pwned')))
})

test('a host that runs only gufi_query: copied through Originals via, from its root', { skip }, async () => {
  const destination = fresh()
  const p = await plan({ library: 'u-gate', paths: ['report.md'], destination })
  assert.match(p.plan.source, /xfer\/data on .* over ssh \(Originals via\)/)
  const j = await J.settled(start(p), 60_000)
  assert.equal(j.state, 'done', j.message)
  assert.equal(fs.readFileSync(path.join(destination, 'report.md'), 'utf8'), 'the original\n')
})

test('past the 50 MB limit of the viewer\'s download, whole and identical', { skip }, async () => {
  const destination = fresh()
  const p = await plan({ library: 'u-own', paths: ['big/large.bin'], destination })
  const j = await J.settled(start(p), 120_000)
  assert.equal(j.state, 'done', j.message)
  assert.equal(j.bytes.done, BIG)
  assert.equal(sha(path.join(destination, 'large.bin')), sha(path.join(data, 'big', 'large.bin')))
})

test('a link out of the library is refused on the host', { skip }, async () => {
  const p = await P.planTransfer({ library: 'u-own', paths: ['way-out/Downloads'] }, { viewerId: null, library: null })
  assert.match(p.problem, /outside the library/)
})

test('a bad ssh option in a stored host never reaches ssh', { skip }, async () => {
  const O = await import('../dist/originals.js')
  assert.equal(O.storedReadThrough({ destination: 'x', sshOptions: ['-oProxyCommand=touch pwned'] }, 'lib'), null)
  assert.equal(O.storedReadThrough({ destination: '-oProxyCommand=x' }, 'lib'), null)
})
