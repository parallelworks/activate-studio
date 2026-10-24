// A throwaway certificate authority and a server certificate from it, made
// with the openssl command (OpenSSL or LibreSSL), for tests that stand in
// for a network that inspects HTTPS with its own authority. Null where there
// is no openssl.
import fs from 'node:fs'
import https from 'node:https'
import path from 'node:path'
import { spawnSync } from 'node:child_process'

export const haveOpenssl = spawnSync('openssl', ['version'], { stdio: 'ignore' }).status === 0

/** CA and server files in `dir`, the server's names given as `names`. */
export function makeCerts(dir, names = ['localhost', 'studio-test.invalid']) {
  if (!haveOpenssl) return null
  fs.mkdirSync(dir, { recursive: true })
  const f = n => path.join(dir, n)
  fs.writeFileSync(f('ca.cnf'), [
    '[req]', 'distinguished_name = dn', '[dn]',
    '[v3_ca]', 'basicConstraints = critical,CA:TRUE', 'keyUsage = critical,keyCertSign,cRLSign', 'subjectKeyIdentifier = hash', '',
  ].join('\n'))
  fs.writeFileSync(f('server.ext'), [
    `subjectAltName = ${[...names.map(n => `DNS:${n}`), 'IP:127.0.0.1'].join(',')}`,
    'basicConstraints = CA:FALSE', 'extendedKeyUsage = serverAuth', '',
  ].join('\n'))
  const run = args => {
    const r = spawnSync('openssl', args, { cwd: dir, encoding: 'utf8' })
    if (r.status !== 0) throw new Error(`openssl ${args[0]}: ${r.stderr}`)
  }
  run(['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'ca.key', '-out', 'ca.pem', '-days', '2',
    '-subj', '/CN=Studio Test Authority', '-config', 'ca.cnf', '-extensions', 'v3_ca'])
  run(['req', '-newkey', 'rsa:2048', '-nodes', '-keyout', 'server.key', '-out', 'server.csr', '-subj', '/CN=localhost', '-config', 'ca.cnf'])
  run(['x509', '-req', '-in', 'server.csr', '-CA', 'ca.pem', '-CAkey', 'ca.key', '-CAcreateserial', '-out', 'server.pem', '-days', '2', '-extfile', 'server.ext'])
  return { ca: f('ca.pem'), key: f('server.key'), cert: f('server.pem') }
}

/** An HTTPS server on 127.0.0.1 with that certificate, answering with `handler`. */
export async function httpsServer(certs, handler) {
  const s = https.createServer({ key: fs.readFileSync(certs.key), cert: fs.readFileSync(certs.cert) }, handler)
  await new Promise(resolve => s.listen(0, '127.0.0.1', resolve))
  return { server: s, port: s.address().port, close: () => new Promise(resolve => { s.closeAllConnections?.(); s.close(resolve) }) }
}
