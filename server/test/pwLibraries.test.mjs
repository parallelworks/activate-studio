// An existing GUFI index on a cluster, connected over pw ssh: the same
// connection test as a library over ssh, then a read-only library that is
// ready at once and is checked again rather than rebuilt. The cluster is a
// stand-in: each pw ssh session runs in a local bash whose HOME holds the
// GUFI the index workflow installs (a fake gufi_query) and an index; the
// clusters named in `failing` fail the way pw ssh does. Nothing reaches a
// platform or a cluster.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { spawn } from 'node:child_process'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'pwlib-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'pwlib-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'pwlib-ix-'))
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
// Stands in for this computer's pw sign-in.
process.env.PW_API_KEY = 'cli-key'
delete process.env.AUTH_JWKS_URL

const write = (f, body, mode) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body, mode ? { mode } : undefined) }

// The cluster's side: the GUFI the workflow installs, an index (a db.db is
// what the check looks for), one with its db.db a level down, and files.
const home = path.join(dir, 'home')
const bin = path.join(home, '.activate-studio', 'gufi', 'abc123', 'bin')
write(path.join(bin, 'gufi_query'), `#!/bin/sh
args="$*"
for last; do :; done
R=$(printf '\\036')
case "$args" in
  *--version*) echo "gufi_query 0.6.9" ;;
  *"-G "*) echo "2|3" ;;
  *"--max-level 0"*) printf 'readme.txt%sf%s22%s1700000000\\n' "$R" "$R" "$R" ;;
  *"--max-level 1"*) printf 'data%s1700000001\\n' "$R" ;;
  *) printf '%s/data/notes.md%snotes.md%s8%s1700000002\\n' "$last" "$R" "$R" "$R" ;;
esac
`, 0o755)
const index = path.join(home, 'idx', 'proj')
write(path.join(index, 'db.db'), '')
write(path.join(home, 'holder', 'sub', 'db.db'), '')
write(path.join(home, 'proj', 'readme.txt'), 'hello from the cluster\n')

// A pw CLI on this machine, which must not be asked to build anything.
const pwLog = path.join(dir, 'pw.log')
write(path.join(dir, 'pw'), `#!/bin/sh\necho "$@" >> ${pwLog}\n`, 0o755)
process.env.PW_CLI = path.join(dir, 'pw')

const failing = {
  'stopped-one': 'Error: resource stopped-one not found',
  'expired-one': 'Error: authentication has expired, please authenticate',
}
const R = await import('../dist/remoteExec.js')
const sessions = []
R.setRemoteSpawn((site, key, command) => {
  sessions.push({ site, key })
  if (failing[site]) return spawn('/bin/sh', ['-c', `echo "${failing[site]}" >&2; exit 1`], { stdio: ['pipe', 'pipe', 'pipe'] })
  return spawn('bash', ['-c', command], { stdio: ['pipe', 'pipe', 'pipe'], env: { HOME: home, PATH: '/usr/bin:/bin' } })
})
const U = await import('../dist/userLibraries.js')
const L = await import('../dist/libraries.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
app.setErrorHandler((err, _req, reply) => reply.status(err.status ?? 500).send({ error: err.message }))
await app.register(U.userLibraryRoutes)
await app.ready()
test.after(async () => { R.closeRemoteChannels(); await app.close() })

const post = async (url, body) => { const r = await app.inject({ method: 'POST', url, payload: body }); return { status: r.statusCode, body: r.json() } }
const probe = body => post('/api/user-libraries/pw/test', body).then(r => r.body)
const cluster = 'cluster-a'

test('the listing offers indexes on a cluster where the pw CLI is here and signed in', async () => {
  const r = (await app.inject({ method: 'GET', url: '/api/user-libraries' })).json()
  assert.equal(r.pw, true)
  assert.equal(r.ssh, true)
  assert.equal(U.pwCliFound(), true)
})

test('the connection test reaches the cluster with pw ssh and passes step by step', async () => {
  const r = await probe({ cluster, indexRoot: '~/idx/proj', sourceRoot: '~/proj' })
  assert.equal(r.ok, true, JSON.stringify(r.steps))
  assert.deepEqual(r.steps.map(s => [s.name, s.ok]), [['ssh', true], ['gufi', true], ['index', true], ['source', true]])
  assert.equal(r.steps[0].detail, 'Connected to cluster-a, a Linux host.')
  // Not on the login's PATH: found where the index workflow installs it.
  assert.equal(r.gufiBin, bin)
  assert.equal(r.gufiVersion, 'gufi_query 0.6.9')
  assert.equal(r.directories, 2)
  assert.match(r.steps[2].detail, /2 directories and 3 files/)
  // The session is pw ssh to the cluster, with this computer's key.
  assert.ok(sessions.some(s => s.site === cluster && s.key === 'cli-key'))
})

test('each failing step says what to change', async () => {
  const first = async (c, re) => {
    const r = await probe({ cluster: c, indexRoot: index })
    assert.equal(r.ok, false)
    assert.deepEqual(r.steps.map(s => s.name), ['ssh'])
    assert.match(r.steps[0].detail, re)
  }
  await first('stopped-one', /^stopped-one is not one of your running clusters\. Start it on ACTIVATE, or choose another\.$/)
  await first('expired-one', /sign-in is missing or has expired\. Run pw auth/)

  const noGufi = await probe({ cluster, indexRoot: index, gufiBin: '/nowhere/bin' })
  assert.deepEqual(noGufi.steps.map(s => [s.name, s.ok]), [['ssh', true], ['gufi', false], ['index', true]])
  assert.match(noGufi.steps[1].detail, /There is no gufi_query in \/nowhere\/bin on cluster-a/)
  const notDir = await probe({ cluster, indexRoot: '/nowhere/index' })
  assert.match(notDir.steps.find(s => s.name === 'index').detail, /\/nowhere\/index is not a directory on cluster-a/)
  const oneUp = await probe({ cluster, indexRoot: '~/holder' })
  assert.match(oneUp.steps.find(s => s.name === 'index').detail, new RegExp(`no db\\.db.*one level down: ${path.join(home, 'holder', 'sub')}`))
  const noSource = await probe({ cluster, indexRoot: index, sourceRoot: '/nowhere/src' })
  assert.equal(noSource.ok, false)
  assert.match(noSource.steps.find(s => s.name === 'source').detail, /not a directory on cluster-a/)

  // pw ssh would read a leading dash as an option.
  const bad = await post('/api/user-libraries/pw/test', { cluster: '-oProxyCommand=sh', indexRoot: index })
  assert.equal(bad.status, 400)
  assert.match(bad.body.error, /^cluster:/)
  assert.equal((await post('/api/user-libraries/pw/test', { cluster, indexRoot: 'idx' })).status, 400)
})

let lib
test('added, the library is ready and read only, and reads the tree and files on the cluster', async () => {
  const refused = await post('/api/user-libraries/pw', { cluster, indexRoot: '/nowhere/index' })
  assert.equal(refused.status, 400)
  assert.match(refused.body.error, /did not pass.*not a directory/)

  const added = await post('/api/user-libraries/pw', { cluster, indexRoot: '~/idx/proj', sourceRoot: '~/proj' })
  assert.equal(added.status, 200, JSON.stringify(added.body))
  const d = added.body
  assert.deepEqual([d.transport, d.existing, d.state, d.site, d.label, d.run], ['pw', true, 'ready', cluster, 'proj', null])
  assert.deepEqual([d.indexRoot, d.sourceRoot, d.directories, d.files], [index, path.join(home, 'proj'), 2, 3])
  assert.equal((await post('/api/user-libraries/pw', { cluster, indexRoot: index })).status, 409, 'the same index twice')

  lib = L.listLibraries('kb', null).find(l => l.id === d.id)
  assert.ok(lib, 'it joins the library list')
  assert.equal(lib.site, cluster); assert.equal(lib.writable, false); assert.match(lib.indexRoot, /^remote:/)
  assert.deepEqual((await U.remoteTree(lib.indexRoot, '')).map(e => [e.type, e.name]), [['dir', 'data'], ['file', 'readme.txt']])
  const f = await U.remoteFile(lib.indexRoot, 'readme.txt')
  assert.equal(f.content, 'hello from the cluster\n')
})

test('refresh checks again rather than starting a build, and removing forgets it', async () => {
  const again = await post(`/api/user-libraries/${lib.id}/refresh`, {})
  assert.equal(again.status, 200, JSON.stringify(again.body))
  assert.deepEqual([again.body.state, again.body.error, again.body.directories, again.body.run], ['ready', null, 2, null])
  assert.equal(fs.existsSync(pwLog), false, 'the pw CLI was not asked to run a workflow')
  const del = await app.inject({ method: 'DELETE', url: `/api/user-libraries/${lib.id}` })
  assert.equal(del.statusCode, 200)
  assert.equal(L.listLibraries('kb', null).some(l => l.id === lib.id), false)
})
