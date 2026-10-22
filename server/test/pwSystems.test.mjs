// pw ssh as a choice for a remote library: offered on a one-person Studio
// even when it cannot be used yet, with the reason (no pw CLI, no sign-in),
// and the systems it can reach, listed with this computer's pw sign-in,
// running ones first. The pw CLI is a stand-in script; nothing reaches a
// platform.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'pwsys-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'pwsys-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'pwsys-ix-'))
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
// No sign-in to start with: no key in the environment, an empty credential store.
process.env.PW_CREDENTIALS_DIR = path.join(dir, 'creds')
delete process.env.PW_API_KEY
delete process.env.OPENAI_API_KEY
delete process.env.AUTH_JWKS_URL
// Where the pw CLI will be; not there yet.
const pwBin = path.join(dir, 'bin', 'pw')
process.env.PW_CLI = pwBin

const write = (f, body, mode) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body, mode ? { mode } : undefined) }
const log = path.join(dir, 'pw.log')
const failFlag = path.join(dir, 'fail')

const U = await import('../dist/userLibraries.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
app.setErrorHandler((err, _req, reply) => reply.status(err.status ?? 500).send({ error: err.message }))
await app.register(U.userLibraryRoutes)
await app.ready()
test.after(() => app.close())

const get = async url => (await app.inject({ method: 'GET', url })).json()

test('without the pw CLI, pw ssh is still offered, with the reason', async () => {
  const r = await get('/api/user-libraries')
  assert.equal(r.pw, false)
  assert.equal(r.pwOffered, true)
  assert.match(r.pwWhy, /does not find the pw CLI/)
  assert.ok(r.pwWhy.includes(pwBin))
  const s = await get('/api/user-libraries/pw/systems')
  assert.deepEqual({ ...s, why: undefined }, { usable: false, why: undefined, systems: [], error: null })
  assert.equal(s.why, r.pwWhy)
})

test('with the pw CLI but no sign-in, the reason says to sign it in', async () => {
  write(pwBin, `#!/bin/sh
echo "$PW_API_KEY $*" >> ${log}
if [ -f ${failFlag} ]; then echo "Error: unauthorized: the token has expired" >&2; exit 1; fi
case "$*" in
  "clusters ls"*) echo "checking for updates"; echo '[{"name":"beta","displayName":"Beta","status":"off","type":"aws-slurm"},{"name":"alpha","status":"active","type":"existing"},{"name":"bad name!","status":"active"},{"name":"gamma","displayName":"Gamma cluster","status":"active"}]' ;;
esac
`, 0o755)
  const r = await get('/api/user-libraries')
  assert.equal(r.pw, false)
  assert.equal(r.pwOffered, true)
  assert.match(r.pwWhy, /not signed in/)
  const s = await get('/api/user-libraries/pw/systems')
  assert.equal(s.usable, false)
  assert.equal(fs.existsSync(log), false, 'the CLI is not asked for systems without a sign-in')
})

test('signed in, the systems come from the platform with that key, running first', async () => {
  write(path.join(dir, 'creds', 'credentials'), JSON.stringify({ currentIdentity: 'test', identities: { test: { apikey: 'cli-key', server: 'platform.test' } } }))
  const r = await get('/api/user-libraries')
  assert.equal(r.pw, true)
  assert.equal(r.pwWhy, null)
  const s = await get('/api/user-libraries/pw/systems')
  assert.equal(s.usable, true)
  assert.equal(s.error, null)
  assert.deepEqual(s.systems, [
    { name: 'alpha', label: 'alpha', status: 'active', running: true, type: 'existing' },
    { name: 'gamma', label: 'Gamma cluster', status: 'active', running: true, type: null },
    { name: 'beta', label: 'Beta', status: 'off', running: false, type: 'aws-slurm' },
  ])
  assert.equal(fs.readFileSync(log, 'utf8').trim(), 'cli-key clusters ls -o json --platform-host platform.test')
})

test('a list the platform refuses comes back as an error to show, not a failure', async () => {
  write(failFlag, '')
  const s = await get('/api/user-libraries/pw/systems')
  assert.equal(s.usable, true)
  assert.deepEqual(s.systems, [])
  assert.match(s.error, /unauthorized: the token has expired/)
})
