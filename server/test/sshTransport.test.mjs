// The plain ssh transport under the remote session (remoteExec.ts): the
// arguments ssh gets, what a destination may be, and the session itself,
// run through the default spawn with a stand-in `ssh` program first on the
// PATH. The stand-in logs its arguments and hands the command to /bin/sh,
// as sshd hands it to the login shell. One host behaves like an older
// macOS: uname says Darwin and base64 decodes with -D only.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'sshx-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'sshx-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'sshx-ix-'))
// ssh's sockets (socketDir in remoteExec.ts) go in a folder of this test's
// own, not the user's runtime folder.
process.env.XDG_RUNTIME_DIR = fs.mkdtempSync('/tmp/sshrun-')
test.after(() => fs.rmSync(process.env.XDG_RUNTIME_DIR, { recursive: true, force: true }))

const exe = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body, { mode: 0o755 }) }
const realBase64 = ['/usr/bin/base64', '/bin/base64'].find(f => fs.existsSync(f))
const realEnv = ['/usr/bin/env', '/bin/env'].find(f => fs.existsSync(f))
const sshLog = path.join(dir, 'ssh.log')
const envLog = path.join(dir, 'env.log')
const macBin = path.join(dir, 'mac-bin')
exe(path.join(dir, 'bin', 'ssh'), `#!/bin/sh
printf '%s\\n' "$@" > ${sshLog}
for last; do :; done
for dest; do case "$dest" in -*|BatchMode=*|ConnectTimeout=*|ServerAliveInterval=*|Control*=*|[0-9]*) ;; *) break ;; esac; done
case "$dest" in *mac*) PATH=${macBin}:$PATH; export PATH ;; esac
exec /bin/sh -c "$last"
`)
exe(path.join(macBin, 'uname'), '#!/bin/sh\necho Darwin\n')
exe(path.join(macBin, 'base64'), `#!/bin/sh
case "$1" in
  -d) echo "base64: invalid option -- d" >&2; exit 64 ;;
  -D) exec ${realBase64} -d ;;
  *) exec ${realBase64} "$@" ;;
esac
`)
exe(path.join(macBin, 'env'), `#!/bin/sh\nprintf '%s\\n' "$*" >> ${envLog}\nexec ${realEnv} "$@"\n`)
// A GUFI tool under a prefix, which prints the library path it was given.
exe(path.join(dir, 'gufi', 'bin', 'gufi_query'), '#!/bin/sh\nprintf "%s" "$DYLD_LIBRARY_PATH"\n')
process.env.PATH = `${path.join(dir, 'bin')}:${process.env.PATH}`

const R = await import('../dist/remoteExec.js')
const U = await import('../dist/userLibraries.js')
test.after(() => R.closeRemoteChannels())
// ssh's shared connection for a host (largeIndexes.test.mjs checks them).
const share = (destination, port = null) => R.controlArgs({ destination, port })

test('ssh gets no prompts, a bounded connect, keepalives, the port, then the destination and command', () => {
  assert.deepEqual(R.sshArgv(R.sshSite('me@box.example', 2222), 'CMD'),
    ['-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=30', ...share('me@box.example', 2222), '-p', '2222', 'me@box.example', 'CMD'])
  assert.deepEqual(R.sshArgv(R.sshSite('lab-index'), 'CMD'),
    ['-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=30', ...share('lab-index'), 'lab-index', 'CMD'])
  assert.notDeepEqual(share('me@box.example', 2222), share('me@box.example'), 'a socket per host and port')
  assert.throws(() => R.sshArgv('cluster-a', 'CMD'), /not an ssh site/)
})

test('a site names its transport and destination, and reads as the destination', () => {
  assert.equal(R.sshSite('me@box', 2222), 'ssh://me@box:2222')
  assert.deepEqual(R.parseSshSite('ssh://me@box:2222'), { destination: 'me@box', port: 2222, options: [] })
  assert.deepEqual(R.parseSshSite('ssh://lab-index'), { destination: 'lab-index', port: null, options: [] })
  assert.equal(R.parseSshSite('cluster-a'), null)
  assert.equal(R.siteName('ssh://me@box:2222'), 'me@box:2222')
  assert.equal(R.siteName('cluster-a'), 'cluster-a')
})

test('a destination is user@host or an alias, never an option or shell text', () => {
  for (const ok of ['me@box', 'box', 'first.last@host-1.example.org', 'lab_index', '10.0.0.5']) assert.doesNotThrow(() => R.sshSite(ok), ok)
  for (const bad of ['', '-oProxyCommand=sh', 'me@-x', 'a b', 'a;b', 'me@$(id)', 'me@box`x`', 'me@', '@box', 'me@box:22', 'me@[::1]', 'a\nb', 'me@box|x']) {
    assert.throws(() => R.sshSite(bad), /destination/, JSON.stringify(bad))
  }
  assert.throws(() => R.sshSite('box', 0), /port/)
  assert.throws(() => R.sshSite('box', 70000), /port/)
  assert.throws(() => R.parseSshSite('ssh://-oProxyCommand=sh'), /ssh site/)
})

test('the library fields are checked: destination, port, and paths on the host', () => {
  const base = { destination: 'me@box', indexRoot: '/data/index/proj' }
  assert.deepEqual(U.sshSpec({ ...base, port: '2222', sourceRoot: '/data/proj/', gufiBin: '~/usr/local/bin' }),
    { destination: 'me@box', port: 2222, sshOptions: [], indexRoot: '/data/index/proj', sourceRoot: '/data/proj', gufiBin: '~/usr/local/bin' })
  assert.deepEqual(U.sshSpec({ ...base, port: '' }), { destination: 'me@box', port: null, sshOptions: [], indexRoot: '/data/index/proj', sourceRoot: null, gufiBin: null })
  assert.equal(U.sshSpec({ ...base, indexRoot: '/Users/me/Library/Application Support/index' }).indexRoot, '/Users/me/Library/Application Support/index')
  assert.throws(() => U.sshSpec({ ...base, destination: '-oProxyCommand=x' }), /destination/)
  assert.throws(() => U.sshSpec({ ...base, port: '22x' }), /port/)
  assert.throws(() => U.sshSpec({ ...base, port: 0 }), /port/)
  assert.throws(() => U.sshSpec({ destination: 'me@box' }), /index directory/)
  assert.throws(() => U.sshSpec({ ...base, indexRoot: 'data/index' }), /index directory: an absolute path/)
  assert.throws(() => U.sshSpec({ ...base, indexRoot: '/data/../etc' }), /index directory/)
  assert.throws(() => U.sshSpec({ ...base, sourceRoot: '/data/a\nb' }), /source directory/)
  assert.throws(() => U.sshSpec({ ...base, gufiBin: 'bin' }), /GUFI bin directory/)
})

test('the loop starts through the login shell and runs argv as given', async () => {
  const site = R.sshSite('me@linux-box', 2222)
  const r = await R.remoteRun(site, '', ['printf', '%s|%s', "it's $HOME", ''])
  assert.equal(r.code, 0)
  assert.equal(r.stdout, "it's $HOME|")
  const argv = fs.readFileSync(sshLog, 'utf8').trimEnd().split('\n')
  const n = share('me@linux-box', 2222).length
  assert.deepEqual(argv.slice(0, 9 + n), ['-T', '-o', 'BatchMode=yes', '-o', 'ConnectTimeout=15', '-o', 'ServerAliveInterval=30', ...share('me@linux-box', 2222), '-p', '2222'])
  assert.equal(argv[9 + n], 'me@linux-box')
  assert.match(argv[10 + n], /^bash -c "\$\(echo [A-Za-z0-9+/=]+ \| base64 -D 2>\/dev\/null \|\| echo [A-Za-z0-9+/=]+ \| base64 -d\)"$/)
  // A large output comes back whole: base64 wraps lines on Linux.
  const big = await R.remoteRun(site, '', ['head', '-c', '100000', '/dev/zero'])
  assert.equal(big.raw.length, 100000)
  const failed = await R.remoteRun(site, '', ['sh', '-c', 'echo nope >&2; exit 3'])
  assert.deepEqual([failed.code, failed.stderr], [3, 'nope\n'])
})

test('a cluster keeps its pw ssh bootstrap', () => {
  assert.match(R.bootstrapCommand('cluster-a'), /^bash -c "\$\(echo [A-Za-z0-9+/=]+ \| base64 -d\)"$/)
})

test('on a macOS host the loop decodes with -D and starts GUFI tools with their library path', async () => {
  const site = R.sshSite('me@mac-box')
  const tool = path.join(dir, 'gufi', 'bin', 'gufi_query')
  const r = await R.remoteRun(site, '', [tool, '-E', 'SELECT 1;'])
  assert.equal(r.code, 0, r.stderr)
  assert.equal(r.stdout, path.join(dir, 'gufi', 'lib'))
  assert.equal(fs.readFileSync(envLog, 'utf8').trim(), `DYLD_LIBRARY_PATH=${path.join(dir, 'gufi', 'lib')} ${tool} -E SELECT 1;`)
  // Other commands run as they are.
  const plain = await R.remoteRun(site, '', ['printf', 'x'])
  assert.equal(plain.stdout, 'x')
  assert.equal(fs.readFileSync(envLog, 'utf8').trim().split('\n').length, 1)
})

test('a session that cannot start fails its requests with what ssh said', async () => {
  exe(path.join(dir, 'bin', 'ssh'), '#!/bin/sh\necho "me@nokey: Permission denied (publickey,password)." >&2\nexit 255\n')
  await assert.rejects(R.remoteRun(R.sshSite('me@nokey'), '', ['true']), /me@nokey: me@nokey: Permission denied \(publickey,password\)\./)
})
