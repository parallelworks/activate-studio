// Outbound HTTPS on a network that inspects TLS with its own certificate
// authority, and through a proxy (netTrust.ts, net.ts). A local HTTPS server
// with a certificate from a throwaway authority stands in for the inspected
// connection, and a local CONNECT proxy for the proxy. Each request runs in a
// child Node, so it starts from Node's own certificate list as the server
// does; on Linux, SSL_CERT_FILE stands in for the system's trusted
// certificates, which is what the system store reads there.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import http from 'node:http'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { execFile } from 'node:child_process'
import { promisify } from 'node:util'
import { haveOpenssl, httpsServer, makeCerts } from './tlsFixture.mjs'

const DIST = path.resolve(import.meta.dirname, '..', 'dist')
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'outbound-'))
test.after(() => fs.rmSync(tmp, { recursive: true, force: true }))
const skip = !haveOpenssl && 'openssl not installed'
const onLinux = process.platform === 'linux'

const { trustFailure, trustMessage, proxyFor, proxyFailure, connectionProblem } = await import('../dist/net.js')

// The request a child Node makes: `trust` adds the system's certificates,
// `proxy` follows the proxy variables, as the server does at start.
const CHILD = path.join(tmp, 'child.mjs')
fs.writeFileSync(CHILD, `
const [url, mode] = process.argv.slice(2)
const nt = await import(${JSON.stringify(path.join(DIST, 'netTrust.js'))})
const n = await import(${JSON.stringify(path.join(DIST, 'net.js'))})
if (mode.includes('trust')) nt.trustSystemCertificates()
if (mode.includes('proxy')) nt.useProxyFromEnv()
try {
  const r = await fetch(url, { signal: AbortSignal.timeout(10000) })
  console.log(JSON.stringify({ ok: true, status: r.status, body: await r.text(), outbound: nt.outbound }))
} catch (e) {
  console.log(JSON.stringify({ ok: false, code: e.cause?.code ?? e.code, message: n.connectionProblem(e, new URL(url).host), outbound: nt.outbound }))
}
`)

// Asynchronous, so the servers in this process keep answering meanwhile.
async function request(url, mode, env = {}) {
  const base = { PATH: process.env.PATH, HOME: process.env.HOME }
  const { stdout } = await promisify(execFile)(process.execPath, [CHILD, url, mode], { env: { ...base, ...env }, timeout: 30_000 })
  return JSON.parse(stdout.trim().split('\n').at(-1))
}

const certs = haveOpenssl ? makeCerts(path.join(tmp, 'certs')) : null
const site = certs ? await httpsServer(certs, (req, res) => res.end('inspected but trusted')) : null
test.after(() => site?.close())
const url = site ? `https://localhost:${site.port}/` : ''
// A bundle with no authority in it, where a test wants the system store empty.
const EMPTY = path.join(tmp, 'empty.pem')
fs.writeFileSync(EMPTY, '')

test('plain Node does not trust a certificate from an authority the system trusts, and says why in plain words', { skip }, async () => {
  const r = await request(url, 'none', { SSL_CERT_FILE: certs.ca })
  assert.equal(r.ok, false)
  assert.match(r.code, /UNABLE_TO_(VERIFY_LEAF_SIGNATURE|GET_ISSUER_CERT_LOCALLY)/)
  assert.match(r.message, /^The connection to localhost:\d+ used a certificate this computer does not trust \(unable to verify the first certificate\)\./)
  assert.doesNotMatch(r.message, /--use-system-ca/, 'Node\'s advice about flags is left out')
})

test('with the system\'s certificates added, as the server does at start, the request succeeds', { skip: skip || (!onLinux && 'the system store is read from SSL_CERT_FILE on Linux only; check_app.sh covers the macOS keychain') }, async () => {
  const r = await request(url, 'trust', { SSL_CERT_FILE: certs.ca })
  assert.equal(r.ok, true, r.message)
  assert.equal(r.body, 'inspected but trusted')
  assert.ok(r.outbound.systemCertificates >= 1)
})

test('STUDIO_SYSTEM_CA=0 leaves Node\'s own list alone', { skip: skip || (!onLinux && 'Linux only') }, async () => {
  const r = await request(url, 'trust', { SSL_CERT_FILE: certs.ca, STUDIO_SYSTEM_CA: '0' })
  assert.equal(r.ok, false)
  assert.equal(r.outbound.systemCertificates, null)
})

test('certificates named by NODE_EXTRA_CA_CERTS are kept when the system\'s are added', { skip }, async () => {
  const r = await request(url, 'trust', { SSL_CERT_FILE: EMPTY, NODE_EXTRA_CA_CERTS: certs.ca })
  assert.equal(r.ok, true, r.message)
})

// A CONNECT proxy that tunnels every host to the HTTPS server above, and
// counts the tunnels; or one that refuses them with `refuse`.
async function connectProxy(refuse) {
  const tunnels = []
  const p = http.createServer((req, res) => { res.writeHead(400); res.end() })
  p.on('connect', (req, sock, head) => {
    tunnels.push(req.url)
    if (refuse) { sock.end(`HTTP/1.1 ${refuse} Refused\r\nProxy-Authenticate: Basic realm="proxy"\r\nContent-Length: 0\r\n\r\n`); return }
    const up = net.connect(site.port, '127.0.0.1', () => {
      sock.write('HTTP/1.1 200 Connection established\r\n\r\n')
      up.write(head)
      up.pipe(sock)
      sock.pipe(up)
    })
    up.on('error', () => sock.destroy())
    sock.on('error', () => up.destroy())
  })
  await new Promise(resolve => p.listen(0, '127.0.0.1', resolve))
  return { port: p.address().port, tunnels, close: () => new Promise(resolve => { p.closeAllConnections?.(); p.close(resolve) }) }
}

test('with HTTPS_PROXY set, requests go through the proxy; this computer is never proxied', { skip }, async () => {
  const proxy = await connectProxy()
  try {
    const env = { HTTPS_PROXY: `http://127.0.0.1:${proxy.port}`, NODE_EXTRA_CA_CERTS: certs.ca, SSL_CERT_FILE: EMPTY }
    const through = await request(`https://studio-test.invalid:${site.port}/`, 'trust proxy', env)
    assert.equal(through.ok, true, through.message)
    assert.deepEqual(proxy.tunnels, [`studio-test.invalid:${site.port}`])
    assert.equal(through.outbound.proxy, `http://127.0.0.1:${proxy.port}`)
    assert.equal(through.outbound.noProxy, 'localhost,127.0.0.1,::1')
    // localhost goes direct, with no NO_PROXY given.
    const direct = await request(url, 'trust proxy', env)
    assert.equal(direct.ok, true, direct.message)
    assert.equal(proxy.tunnels.length, 1)
    // A NO_PROXY given is kept, with this computer added.
    const kept = await request(`https://studio-test.invalid:${site.port}/`, 'trust proxy', { ...env, NO_PROXY: '.invalid' })
    assert.equal(kept.ok, false, 'direct, so the name is not found')
    assert.equal(kept.outbound.noProxy, '.invalid,localhost,127.0.0.1,::1')
    assert.equal(proxy.tunnels.length, 1)
  } finally { await proxy.close() }
})

test('a proxy that will not open the connection, and one that cannot be reached, are said as such', { skip }, async () => {
  const proxy = await connectProxy(407)
  try {
    const env = { HTTPS_PROXY: `http://127.0.0.1:${proxy.port}`, NODE_EXTRA_CA_CERTS: certs.ca, SSL_CERT_FILE: EMPTY }
    const refused = await request(`https://studio-test.invalid:${site.port}/`, 'trust proxy', env)
    assert.equal(refused.ok, false)
    assert.match(refused.message, new RegExp(`^The proxy at 127\\.0\\.0\\.1:${proxy.port} asks for a sign-in \\(407\\), so it did not open a connection to studio-test\\.invalid:${site.port}\\. The Studio signs in with a user name and password`))
  } finally { await proxy.close() }
  // A port nothing listens on.
  const free = await new Promise(resolve => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)) }) })
  const gone = await request(`https://studio-test.invalid:${site.port}/`, 'trust proxy', { HTTPS_PROXY: `http://127.0.0.1:${free}` })
  assert.match(gone.message, new RegExp(`^The proxy at 127\\.0\\.0\\.1:${free} could not be reached \\(ECONNREFUSED\\), so studio-test\\.invalid:${site.port} could not be either\\.`))
})

test('certificate failures map to a plain sentence for each kind, worded for the platform', () => {
  const wrap = (code, message) => Object.assign(new TypeError('fetch failed'), { cause: Object.assign(new Error(message), { code }) })
  for (const [code, kind] of [
    ['UNABLE_TO_GET_ISSUER_CERT_LOCALLY', 'untrusted'], ['UNABLE_TO_GET_ISSUER_CERT', 'untrusted'],
    ['UNABLE_TO_VERIFY_LEAF_SIGNATURE', 'untrusted'], ['SELF_SIGNED_CERT_IN_CHAIN', 'untrusted'],
    ['DEPTH_ZERO_SELF_SIGNED_CERT', 'untrusted'], ['CERT_UNTRUSTED', 'untrusted'],
    ['CERT_HAS_EXPIRED', 'expired'], ['CERT_NOT_YET_VALID', 'expired'], ['ERR_TLS_CERT_ALTNAME_INVALID', 'name'],
  ]) {
    const f = trustFailure(wrap(code, 'reason text'))
    assert.deepEqual(f, { code, kind, reason: 'reason text' }, code)
  }
  const f = trustFailure(wrap('UNABLE_TO_GET_ISSUER_CERT_LOCALLY', 'unable to get local issuer certificate'))
  assert.equal(trustMessage('api.github.com', f, 'darwin'),
    'The connection to api.github.com used a certificate this computer does not trust (unable to get local issuer certificate). If your network inspects HTTPS, its certificate authority has to be trusted by macOS (in Keychain Access, or installed by the Mac\'s management); the Studio uses the system\'s trusted certificates.')
  assert.match(trustMessage('api.github.com', f, 'linux'), /in this computer's trusted certificates \(the system store, or a file named by NODE_EXTRA_CA_CERTS\)/)
  assert.match(trustMessage('api.github.com', f, 'win32'), /trusted by Windows/)
  assert.match(trustMessage('h', trustFailure(wrap('CERT_HAS_EXPIRED', 'certificate has expired'))), /^The certificate h presented is out of date \(certificate has expired\)\. Check this computer's date and time/)
  assert.match(trustMessage('h', trustFailure(wrap('ERR_TLS_CERT_ALTNAME_INVALID', 'Hostname/IP does not match'))), /is for a different name/)
  // Not a certificate, and no proxy: left to the offline handling.
  assert.equal(connectionProblem(wrap('ENOTFOUND', 'getaddrinfo ENOTFOUND api.github.com'), 'api.github.com', {}), null)
})

test('NO_PROXY: names, domains, a leading star, ports and * are honoured', () => {
  const env = { HTTPS_PROXY: 'http://proxy.example:3128', NO_PROXY: 'intranet.example, .corp.example,*.lab.example,host.example:8443' }
  assert.equal(proxyFor('api.github.com', env).host, 'proxy.example:3128')
  assert.equal(proxyFor('intranet.example', env), null)
  assert.equal(proxyFor('a.corp.example', env), null)
  assert.equal(proxyFor('b.lab.example:443', env), null)
  assert.equal(proxyFor('host.example', env), null)
  assert.equal(proxyFor('github.com', { HTTPS_PROXY: 'proxy.example:3128', NO_PROXY: '*' }), null)
  assert.equal(proxyFor('github.com', { https_proxy: 'proxy.example:3128' }).host, 'proxy.example:3128', 'a bare host:port, in lowercase')
  assert.equal(proxyFor('github.com', {}), null)
  // An offline computer with no proxy is not a proxy failure.
  const offline = Object.assign(new TypeError('fetch failed'), { cause: Object.assign(new Error('getaddrinfo ENOTFOUND github.com'), { code: 'ENOTFOUND' }) })
  assert.equal(proxyFailure(offline, 'github.com', {}), null)
  assert.deepEqual(proxyFailure(offline, 'github.com', { HTTPS_PROXY: 'http://user:secret@proxy.example:3128' }), { proxy: 'proxy.example:3128', code: 'ENOTFOUND' }, 'the password is never shown')
})
