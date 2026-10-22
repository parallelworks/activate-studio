// Model access from the pw CLI: its contexts, the one the Studio uses, a
// sign-in the CLI renews, signing in with the CLI's device flow, and the
// status the Model access section shows. A stand-in CLI and credential
// file replace the real ones, so nothing here reaches a platform.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'access-'))
process.env.KB_ROOT = path.join(tmp, 'kb')
process.env.INDEX_BASE = path.join(tmp, 'ix')
fs.mkdirSync(process.env.KB_ROOT)
fs.mkdirSync(process.env.INDEX_BASE)
process.env.PW_CREDENTIALS_DIR = path.join(tmp, 'pw')
fs.mkdirSync(process.env.PW_CREDENTIALS_DIR)
delete process.env.PW_GATEWAY_URL
delete process.env.OPENAI_BASE_URL
delete process.env.PW_API_KEY
delete process.env.OPENAI_API_KEY
delete process.env.PW_CONTEXT
const CREDS = path.join(process.env.PW_CREDENTIALS_DIR, 'credentials')
const LOG = path.join(tmp, 'cli.log')

const jwt = exp => ['e30', Buffer.from(JSON.stringify({ exp })).toString('base64url'), 'sig'].join('.')
const past = new Date(Date.now() - 3600_000).toISOString()
fs.writeFileSync(CREDS, JSON.stringify({
  identities: {
    work: { apikey: 'work-api-key', server: 'platform-a.example.com', organization: 'org-a' },
    lab: { token: 'lab-stale-token', server: 'platform-b.example.com', organization: 'org-b', oauth: { clientId: 'pw-cli', expiresAt: past, refreshToken: 'r1' } },
    old: { token: jwt(Math.floor(Date.now() / 1000) - 60), server: 'platform-a.example.com', organization: 'org-a' },
  },
  currentIdentity: 'work',
}, null, 1))

// The stand-in CLI: --version, auth token --print, and auth login --device.
const CLI = path.join(tmp, 'pw-fake.mjs')
fs.writeFileSync(CLI, `#!/usr/bin/env node
import fs from 'node:fs'
const a = process.argv.slice(2)
const log = l => fs.appendFileSync(${JSON.stringify(LOG)}, l + '\\n')
if (a[0] === '--version') { console.log('pw version v7.1.0'); process.exit(0) }
if (a[0] === 'auth' && a[1] === 'token') {
  const ctx = a[a.indexOf('--context') + 1]
  log('token ' + ctx + ' key=' + (process.env.PW_API_KEY ?? ''))
  if (process.env.FAKE_PW_OFFLINE === '1') { console.error('Error: Post "https://x/api/oidc/token": dial tcp: lookup x: no such host'); process.exit(1) }
  console.log(JSON.stringify({ access_token: 'fresh-' + ctx, token_type: 'Bearer', expires_in: 3600 }))
  console.log('A new version of the PW CLI is available.')
  process.exit(0)
}
if (a[0] === 'auth' && a[1] === 'login') {
  const host = a[a.length - 1]
  log('login ' + a.join(' '))
  if (host.startsWith('offline')) { console.error('Error: dial tcp: lookup ' + host + ': no such host'); process.exit(1) }
  console.error('To sign in, open this page on any device:\\n\\n  https://' + host + '/device\\n\\nand enter the code:\\n\\n  ABCD-EFGH\\n\\nWaiting for approval...')
  const dir = ${JSON.stringify(tmp)}
  const end = Date.now() + 10000
  const tick = () => {
    if (fs.existsSync(dir + '/deny')) { console.error('Error: the sign-in was denied'); process.exit(1) }
    if (fs.existsSync(dir + '/approve')) {
      const f = ${JSON.stringify(CREDS)}
      const d = JSON.parse(fs.readFileSync(f, 'utf8'))
      d.identities['alex@' + host] = { token: 'signed-in-token', server: host, organization: 'org-c', oauth: { clientId: 'pw-cli', expiresAt: new Date(Date.now() + 3600000).toISOString() } }
      d.currentIdentity = 'alex@' + host
      fs.writeFileSync(f, JSON.stringify(d))
      console.error('Signed in as alex')
      process.exit(0)
    }
    if (Date.now() > end) process.exit(2)
    setTimeout(tick, 50)
  }
  tick()
}
`, { mode: 0o755 })
process.env.PW_CLI = CLI

const cliLog = () => { try { return fs.readFileSync(LOG, 'utf8') } catch { return '' } }

const config = await import('../dist/config.js')
const access = await import('../dist/access.js')
const { gatewayKey } = await import('../dist/chat/gateway.js')
const { setUserKey, clearUserKey, SINGLE_OWNER } = await import('../dist/credentials.js')
const { platformKeyFor } = await import('../dist/workflowsTab.js')
const { accessRoutes } = await import('../dist/accessRoutes.js')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(accessRoutes)
await app.ready()
const get = async url => (await app.inject({ method: 'GET', url })).json()
const send = async (method, url, payload) => { const r = await app.inject({ method, url, payload }); return { status: r.statusCode, body: r.json() } }

test('the contexts are listed with how each signs in, and no key material', () => {
  const ctx = access.listCliContexts()
  assert.deepEqual(ctx.map(c => [c.name, c.server, c.kind, c.current]), [
    ['work', 'platform-a.example.com', 'api-key', true],
    ['lab', 'platform-b.example.com', 'sign-in', false],
    ['old', 'platform-a.example.com', 'token', false],
  ])
  assert.equal(ctx.find(c => c.name === 'old').expired, true, 'a platform token past its exp is expired')
  assert.equal(ctx.find(c => c.name === 'lab').expired, false, 'a sign-in renews, so it is never shown as expired')
  assert.doesNotMatch(JSON.stringify(ctx), /work-api-key|lab-stale-token|r1/)
})

test('with no choice, the Studio follows the CLI current context, gateway and credential both', () => {
  assert.equal(access.activeContextName(), 'work')
  assert.equal(new URL(config.GATEWAY_BASE).host, 'platform-a.example.com')
  assert.equal(gatewayKey(), 'work-api-key')
  assert.equal(process.env.PW_CONTEXT, undefined, 'the CLI is left to its own current context')
})

test('choosing a sign-in context moves the gateway, sets PW_CONTEXT, and asks the CLI for a renewed token', async () => {
  access.chooseContext('lab')
  assert.equal(new URL(config.GATEWAY_BASE).host, 'platform-b.example.com')
  assert.equal(process.env.PW_CONTEXT, 'lab', 'pw commands the Studio runs use the same context')
  assert.equal(JSON.parse(fs.readFileSync(path.join(process.env.INDEX_BASE, 'access.json'), 'utf8')).pwContext, 'lab')
  // The file's token expired an hour ago: the CLI is asked, once, without a PW_API_KEY that it would echo back.
  process.env.PW_API_KEY = 'must-not-reach-the-cli'
  await access.ensureFreshAccess()
  delete process.env.PW_API_KEY
  assert.match(cliLog(), /^token lab key=$/m)
  assert.equal(gatewayKey(), 'fresh-lab')
  const before = cliLog()
  await access.ensureFreshAccess()
  assert.equal(cliLog(), before, 'a token with an hour left is not renewed again')
})

test('following the CLI again restores its own context; an unknown context is refused', () => {
  access.chooseContext(null)
  assert.equal(access.activeContextName(), 'work')
  assert.equal(new URL(config.GATEWAY_BASE).host, 'platform-a.example.com')
  assert.equal(process.env.PW_CONTEXT, undefined)
  assert.throws(() => access.chooseContext('nope'), /no context named nope/)
})

test('a renewal that fails for lack of network keeps the current token and is not retried on every request', async () => {
  access.resetAccessForTests()
  access.chooseContext('lab')
  process.env.FAKE_PW_OFFLINE = '1'
  try {
    const t0 = Date.now()
    await access.ensureFreshAccess()
    assert.ok(Date.now() - t0 < 5000, 'fails fast')
    assert.equal(gatewayKey(), 'lab-stale-token', 'the file token is used as it is')
    const lab = access.listCliContexts().find(c => c.name === 'lab')
    assert.match(lab.renewError, /Could not reach platform-b\.example\.com/)
    const n = cliLog().split('\n').length
    await access.ensureFreshAccess()
    assert.equal(cliLog().split('\n').length, n, 'not asked again within 30 seconds')
  } finally {
    delete process.env.FAKE_PW_OFFLINE
    access.chooseContext(null)
  }
})

test('a platform key stored with sign-in off reaches the Workflows page, an endpoint key does not', () => {
  setUserKey(SINGLE_OWNER, 'my-platform-key', true)
  assert.deepEqual(platformKeyFor(undefined), { key: 'my-platform-key', own: true })
  setUserKey(SINGLE_OWNER, 'local', true, 'http://127.0.0.1:11434/v1')
  assert.deepEqual(platformKeyFor(undefined), { key: 'work-api-key', own: false }, 'the CLI context covers the platform')
  clearUserKey(SINGLE_OWNER)
})

test('GET /api/access names the source of models and of platform calls', async () => {
  let s = await get('/api/access')
  assert.equal(s.single, true)
  assert.deepEqual([s.models.source, s.models.context, s.models.host], ['cli', 'work', 'platform-a.example.com'])
  assert.deepEqual([s.platform.source, s.platform.context], ['cli', 'work'])
  assert.equal(s.cli.available, true)
  assert.equal(s.cli.version, 'v7.1.0')
  assert.equal(s.cli.contexts.length, 3)
  assert.equal(s.cli.chosen, null)
  assert.doesNotMatch(JSON.stringify(s), /work-api-key|lab-stale-token/)

  setUserKey(SINGLE_OWNER, 'local', true, 'http://127.0.0.1:11434/v1')
  s = await get('/api/access')
  assert.deepEqual([s.models.source, s.models.host, s.models.baseUrl], ['endpoint', '127.0.0.1:11434', 'http://127.0.0.1:11434/v1'])
  assert.equal(s.platform.source, 'cli', 'an endpoint covers models only; the platform stays with the CLI')

  // Choosing a context with clearKey drops the endpoint, which would otherwise keep winning.
  const r = await send('PUT', '/api/access/source', { context: 'lab', clearKey: true })
  assert.equal(r.status, 200)
  assert.equal(r.body.chosen, 'lab')
  assert.deepEqual([r.body.models.source, r.body.models.context], ['cli', 'lab'])
  const back = await send('PUT', '/api/access/source', { context: null })
  assert.equal(back.body.chosen, null)
  assert.equal((await send('PUT', '/api/access/source', { context: 'nope' })).status, 400)
})

async function approveAndSettle() {
  fs.writeFileSync(path.join(tmp, 'approve'), '')
  let st
  for (let i = 0; i < 100; i++) {
    st = (await get('/api/access/cli/login')).login
    if (st.state !== 'waiting') break
    await new Promise(r => setTimeout(r, 50))
  }
  fs.rmSync(path.join(tmp, 'approve'))
  return st
}

test('signing in with the CLI device flow relays the address and code, then uses the new context', async () => {
  const start = await send('POST', '/api/access/cli/login', { host: 'https://platform-c.example.com/some/page' })
  assert.equal(start.status, 200)
  assert.deepEqual([start.body.login.state, start.body.login.url, start.body.login.code, start.body.login.host],
    ['waiting', 'https://platform-c.example.com/device', 'ABCD-EFGH', 'platform-c.example.com'])
  assert.match(cliLog(), /login auth login --device platform-c\.example\.com/)
  const st = await approveAndSettle()
  assert.equal(st.state, 'done')
  assert.equal(st.context, 'alex@platform-c.example.com')
  // Following the CLI, which made the sign-in its current context.
  assert.equal(access.chosenContext(), null)
  assert.equal(access.activeContextName(), 'alex@platform-c.example.com')
  assert.equal(new URL(config.GATEWAY_BASE).host, 'platform-c.example.com')
  assert.equal(gatewayKey(), 'signed-in-token')
})

test('a Studio pinned to a context moves to the one a sign-in makes', async () => {
  access.chooseContext('work')
  await send('POST', '/api/access/cli/login', { host: 'platform-e.example.com' })
  const st = await approveAndSettle()
  assert.equal(st.state, 'done')
  assert.equal(access.chosenContext(), 'alex@platform-e.example.com')
  assert.equal(new URL(config.GATEWAY_BASE).host, 'platform-e.example.com')
  access.chooseContext(null)
})

test('a sign-in that cannot reach the platform says so plainly', async () => {
  const r = await send('POST', '/api/access/cli/login', { host: 'offline.example.com' })
  assert.equal(r.status, 200)
  assert.equal(r.body.login.state, 'failed')
  assert.match(r.body.login.message, /^Could not reach offline\.example\.com\. This computer may be offline/)
  assert.equal((await send('POST', '/api/access/cli/login', { host: 'not a host!' })).status, 400)
})

test('a sign-in can be canceled while it waits', async () => {
  const r = await send('POST', '/api/access/cli/login', { host: 'platform-d.example.com' })
  assert.equal(r.body.login.state, 'waiting')
  const c = await send('POST', '/api/access/cli/login/cancel', {})
  assert.equal(c.body.login.state, 'canceled')
})

test('the device prompt is read from the CLI output whatever surrounds it', () => {
  assert.deepEqual(access.parseDevicePrompt('\x1b[1mTo sign in\x1b[0m open\n  https://h.example/device\n\nand enter the code:\n\n  QQKK-TSXD\n'), { url: 'https://h.example/device', code: 'QQKK-TSXD' })
  assert.deepEqual(access.parseDevicePrompt('nothing yet'), { url: null, code: null })
  assert.equal(access.normalizeHost('HTTPS://Platform.Example.com/x'), 'platform.example.com')
  assert.equal(access.normalizeHost('a b'), null)
})

test('the Studio says where it runs the pw CLI from, and takes a location chosen in Model access', async () => {
  let s = await get('/api/access')
  assert.deepEqual([s.cli.path, s.cli.source], [CLI, 'env'])
  assert.ok(s.cli.commonDirs.includes('/usr/local/bin'))

  // Without PW_CLI, PATH comes first; a pw on it is found and run.
  const bin = path.join(tmp, 'bin')
  fs.mkdirSync(bin, { recursive: true })
  fs.copyFileSync(CLI, path.join(bin, 'pw'))
  fs.chmodSync(path.join(bin, 'pw'), 0o755)
  const savedPath = process.env.PATH
  delete process.env.PW_CLI
  process.env.PATH = `${bin}${path.delimiter}${savedPath}`
  try {
    s = await get('/api/access?fresh=1')
    assert.deepEqual([s.cli.path, s.cli.source, s.cli.available], [path.join(bin, 'pw'), 'path', true])
    assert.equal(config.PW_CLI, path.join(bin, 'pw'), 'every caller runs the one found')

    // A location chosen here wins over PATH, and is kept beside the index.
    let r = await send('PUT', '/api/access/cli/path', { path: CLI })
    assert.equal(r.status, 200, JSON.stringify(r.body))
    assert.deepEqual([r.body.cli.path, r.body.cli.source, r.body.cli.version], [CLI, 'chosen', 'v7.1.0'])
    assert.equal(config.PW_CLI, CLI)
    assert.equal(JSON.parse(fs.readFileSync(path.join(process.env.INDEX_BASE, 'pw-cli.json'), 'utf8')).path, CLI)

    // Refused: a relative path, nothing there, a program that is not the CLI. The choice stays as it was.
    assert.equal((await send('PUT', '/api/access/cli/path', { path: 'bin/pw' })).status, 400)
    assert.equal((await send('PUT', '/api/access/cli/path', { path: path.join(tmp, 'nothing') })).status, 400)
    const other = path.join(tmp, 'not-pw')
    fs.writeFileSync(other, '#!/bin/sh\necho hello\n', { mode: 0o755 })
    r = await send('PUT', '/api/access/cli/path', { path: other })
    assert.equal(r.status, 400)
    assert.match(r.body.error, /not kept/)
    s = await get('/api/access?fresh=1')
    assert.notEqual(s.cli.path, other)

    // Forgetting the choice looks for it by itself again.
    r = await send('PUT', '/api/access/cli/path', { path: null })
    assert.deepEqual([r.body.cli.path, r.body.cli.source], [path.join(bin, 'pw'), 'path'])
    assert.equal(fs.existsSync(path.join(process.env.INDEX_BASE, 'pw-cli.json')), false)
  } finally {
    process.env.PATH = savedPath
    process.env.PW_CLI = CLI
    config.findPwCli()
  }
})
