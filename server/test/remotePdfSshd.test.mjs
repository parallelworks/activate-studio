// PDFs in a library built on a remote system, over real ssh: one sshd per
// stand-in host, run by this user on a high port, each login forced into a
// HOME and PATH of its own, so what a host has is what its PATH holds. No
// root, nothing installed, and no host or Studio reaches the network: the
// reader wheels are fetched once, before the way out is closed. Each case is
// confirmed by counting rows in the index itself. Skipped unless
// STUDIO_TEST_SSHD=1, and without GUFI.
//
//   STUDIO_TEST_SSHD=1 node --import ./test/setup.mjs --test test/remotePdfSshd.test.mjs
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import net from 'node:net'
import os from 'node:os'
import path from 'node:path'
import { execFileSync, spawn, spawnSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

const gufiBin = process.env.GUFI_BIN || '/opt/gufi/bin'
const SSHD = ['/usr/sbin/sshd', '/usr/local/sbin/sshd'].find(p => fs.existsSync(p))
const python = ['/usr/bin/python3', '/usr/local/bin/python3'].find(p => fs.existsSync(p))
const skip = process.env.STUDIO_TEST_SSHD !== '1' ? 'set STUDIO_TEST_SSHD=1 to start local sshd processes'
  : !SSHD ? 'no sshd on this host' : !python ? 'no python3' : !fs.existsSync(path.join(gufiBin, 'gufi_dir2index')) ? 'GUFI not installed' : false

const dir = fs.mkdtempSync('/tmp/rpdf-')
const user = os.userInfo().username
const write = (f, body) => { fs.mkdirSync(path.dirname(f), { recursive: true }); fs.writeFileSync(f, body) }
const exe = (f, body) => { write(f, body); fs.chmodSync(f, 0o755) }
const freePort = () => new Promise(resolve => { const s = net.createServer().listen(0, '127.0.0.1', () => { const p = s.address().port; s.close(() => resolve(p)) }) })

/** A one-page PDF with a text layer. */
function pdf(text) {
  const stream = `BT /F1 12 Tf 72 720 Td (${text}) Tj ET`
  const objs = ['<< /Type /Catalog /Pages 2 0 R >>', '<< /Type /Pages /Kids [3 0 R] /Count 1 >>',
    '<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>',
    `<< /Length ${stream.length} >>\nstream\n${stream}\nendstream`, '<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>']
  let out = '%PDF-1.4\n'
  const at = objs.map((o, i) => { const n = out.length; out += `${i + 1} 0 obj\n${o}\nendobj\n`; return n })
  const xref = out.length
  out += `xref\n0 ${objs.length + 1}\n0000000000 65535 f \n${at.map(n => `${String(n).padStart(10, '0')} 00000 n \n`).join('')}`
  return `${out}trailer\n<< /Size ${objs.length + 1} /Root 1 0 R >>\nstartxref\n${xref}\n%%EOF\n`
}

const BODY = 'The zeppelin hangar manual describes mooring procedures for the airship crew'
const daemons = []
const hosts = {}
const NO_NETWORK = { https_proxy: 'http://127.0.0.1:9', HTTPS_PROXY: 'http://127.0.0.1:9', http_proxy: 'http://127.0.0.1:9', HTTP_PROXY: 'http://127.0.0.1:9' }

/** A host of its own: its PATH is every tool here but those `leave` matches
 *  and the poppler tools, with a Python that sees no site-packages, or none
 *  with `noPython`. A `pdftotext` stand-in with `poppler`. */
async function host(name, { noPython = false, poppler = false } = {}) {
  const h = path.join(dir, name), bin = path.join(h, 'bin'), home = path.join(h, 'home')
  fs.mkdirSync(bin, { recursive: true })
  const leave = /^(python.*|pydoc.*|pip.*|pdftotext|pdfinfo|pdftoppm|tesseract|soffice|libreoffice)$/
  for (const d of ['/bin', '/usr/bin']) for (const n of fs.readdirSync(d)) {
    if (!leave.test(n) && !fs.existsSync(path.join(bin, n))) fs.symlinkSync(path.join(d, n), path.join(bin, n))
  }
  if (!noPython) exe(path.join(bin, 'python3'), `#!/bin/sh\nexec ${python} -S "$@"\n`)
  if (poppler) exe(path.join(bin, 'pdftotext'), `#!/bin/sh\necho '${BODY}'\n`)
  write(path.join(home, 'docs', 'manuals', 'hangar.pdf'), pdf(BODY))
  write(path.join(home, 'docs', 'notes.md'), '# Notes\nA short note about the schedule.\n')
  exe(path.join(h, 'login.sh'), `#!/bin/bash
export HOME=${home} PATH=${bin}
${Object.entries(NO_NETWORK).map(([k, v]) => `export ${k}=${v}`).join('\n')}
unset PYTHONPATH
if [ -n "$SSH_ORIGINAL_COMMAND" ]; then exec bash -c "$SSH_ORIGINAL_COMMAND"; fi
exec bash
`)
  const port = await freePort()
  const conf = path.join(h, 'sshd.conf')
  write(conf, `Port ${port}\nListenAddress 127.0.0.1\nHostKey ${path.join(dir, 'host')}\nPidFile ${path.join(h, 'sshd.pid')}\nAuthorizedKeysFile ${path.join(dir, 'authorized_keys')}\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPubkeyAuthentication yes\nUsePAM no\nStrictModes no\nLogLevel ERROR\nForceCommand ${path.join(h, 'login.sh')}\n`)
  daemons.push(spawn(SSHD, ['-D', '-e', '-f', conf], { stdio: 'ignore' }))
  for (let i = 0; i < 100; i++) {
    if (await new Promise(r => { const s = net.connect(port, '127.0.0.1', () => { s.destroy(); r(true) }); s.on('error', () => r(false)) })) break
    await new Promise(r => setTimeout(r, 50))
  }
  hosts[name] = { port, home, bin }
  return hosts[name]
}

let wheels = false
if (!skip) {
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', path.join(dir, 'id')])
  execFileSync('ssh-keygen', ['-q', '-t', 'ed25519', '-N', '', '-f', path.join(dir, 'host')])
  fs.copyFileSync(path.join(dir, 'id.pub'), path.join(dir, 'authorized_keys'))
  fs.chmodSync(path.join(dir, 'authorized_keys'), 0o600)
  process.env.GUFI_BIN = gufiBin
  process.env.KB_ROOT = path.join(dir, 'kb'); fs.mkdirSync(process.env.KB_ROOT)
  process.env.INDEX_BASE = path.join(dir, 'ix')
  process.env.XDG_RUNTIME_DIR = fs.mkdtempSync('/tmp/rpdfrun-')
  process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
  process.env.PW_API_KEY = 'cli-key'
  delete process.env.AUTH_JWKS_URL
  const script = fileURLToPath(new URL('../../indexer/fetch_embed.py', import.meta.url))
  wheels = spawnSync('python3', [script, '--download-readers', '--cache', path.join(process.env.INDEX_BASE, 'embed-wheels')]).status === 0
  // From here on, nothing downloads: not the Studio, not the hosts.
  Object.assign(process.env, NO_NETWORK)
  delete process.env.no_proxy; delete process.env.NO_PROXY
}
const needWheels = skip || (!wheels && 'the reader wheels could not be fetched')

const R = skip ? {} : await import('../dist/remoteExec.js')
const U = skip ? {} : await import('../dist/userLibraries.js')
const L = skip ? {} : await import('../dist/libraries.js')
const S = skip ? {} : await import('../dist/libraryScope.js')
const { default: Fastify } = skip ? {} : await import('fastify')
const app = skip ? null : Fastify()
if (!skip) {
  U.setBuildPollMs(50)
  app.setErrorHandler((err, _req, reply) => reply.status(err.status ?? 500).send({ error: err.message }))
  await app.register(U.userLibraryRoutes)
  await app.ready()
}
test.after(async () => {
  if (!skip) { R.closeRemoteChannels(); await app.close() }
  for (const d of daemons) d.kill()
  if (process.env.XDG_RUNTIME_DIR?.startsWith('/tmp/rpdfrun-')) fs.rmSync(process.env.XDG_RUNTIME_DIR, { recursive: true, force: true })
  fs.rmSync(dir, { recursive: true, force: true })
})

const post = async (url, body) => { const r = await app.inject({ method: 'POST', url, payload: body ?? {} }); return { status: r.statusCode, body: r.json() } }
const listing = async () => (await app.inject({ method: 'GET', url: '/api/user-libraries' })).json().libraries
const finished = async id => {
  for (let i = 0; i < 1200; i++) {
    const l = (await listing()).find(x => x.id === id)
    if (l && (l.state === 'ready' || l.state === 'failed')) return l
    await new Promise(r => setTimeout(r, 50))
  }
  throw new Error(`${id} did not finish`)
}
const build = async (name, depth = 'text') => {
  const h = hosts[name]
  const r = await post('/api/user-libraries/ssh/build', {
    destination: `${user}@127.0.0.1`, port: h.port, sourceRoot: path.join(h.home, 'docs'), indexRoot: path.join(h.home, 'docs-index'), gufiBin, label: name, create: true, depth,
    sshOptions: `-i ${path.join(dir, 'id')} -o IdentitiesOnly=yes -o UserKnownHostsFile=${path.join(dir, 'kh')} -o StrictHostKeyChecking=no -o ConnectTimeout=10`,
  })
  assert.equal(r.status, 200, JSON.stringify(r.body))
  return finished(r.body.id)
}
const rebuild = async id => {
  await new Promise(r => setTimeout(r, 1100))
  const r = await post(`/api/user-libraries/${id}/refresh`)
  assert.equal(r.status, 200, JSON.stringify(r.body))
  return finished(id)
}
/** PDF rows in the index's own words table, read from its db.db files. */
const pdfRows = root => Number(execFileSync('python3', ['-c', `
import os, sqlite3, sys
n = 0
for d, _, fs in os.walk(sys.argv[1]):
    if 'db.db' in fs:
        db = sqlite3.connect('file:' + os.path.join(d, 'db.db') + '?mode=ro', uri=True)
        if db.execute("select 1 from sqlite_master where name = 'words'").fetchone():
            n += db.execute("select count(*) from words where fname like '%.pdf'").fetchone()[0]
print(n)
`, root], { encoding: 'utf8' }).trim())
const pdfHits = async (id, q) => {
  const lib = L.listLibraries('kb', null).find(x => x.id === id)
  return (await S.searchScope([lib], q, 10, { semantic: false })).filter(h => h.path.endsWith('.pdf'))
}
const readersThere = h => path.join(h.home, '.activate-studio', 'readers')

test('a host with pdftotext reads its PDFs with it, and has no gaps', { skip: needWheels, timeout: 120_000 }, async () => {
  await host('poppler', { poppler: true })
  const l = await build('poppler')
  assert.equal(l.state, 'ready', l.error ?? '')
  assert.equal(pdfRows(l.indexRoot), 1)
  assert.deepEqual(l.gaps, [])
  assert.equal((await pdfHits(l.id, 'zeppelin')).length, 1)
})

test('a host with neither reader reads its PDFs with the pypdf the Studio sends, with no network there', { skip: needWheels, timeout: 120_000 }, async () => {
  const h = await host('neither')
  const l = await build('neither')
  assert.equal(l.state, 'ready', l.error ?? '')
  assert.equal(pdfRows(l.indexRoot), 1)
  assert.deepEqual(l.gaps, [])
  assert.equal((await pdfHits(l.id, 'zeppelin')).length, 1)
  const [v] = fs.readdirSync(readersThere(h)).filter(n => !n.includes('.'))
  assert.ok(fs.existsSync(path.join(readersThere(h), v, 'ok')))
  assert.ok(fs.existsSync(path.join(readersThere(h), v, 'deps', 'pypdf')))
  assert.deepEqual(fs.readdirSync(readersThere(h)), [v], 'the sent files and the marker are gone once installed')
})

test('where the readers cannot be sent, the row says so, and a Rebuild with them recovers the library', { skip: needWheels, timeout: 180_000 }, async () => {
  await host('unsent')
  const wheelDir = path.join(process.env.INDEX_BASE, 'embed-wheels'), away = `${wheelDir}.away`
  fs.renameSync(wheelDir, away)
  let l
  try { l = await build('unsent') } finally { fs.rmSync(wheelDir, { recursive: true, force: true }); fs.renameSync(away, wheelDir) }
  assert.equal(l.state, 'ready', l.error ?? '')
  assert.equal(l.fullText, true)
  assert.equal(pdfRows(l.indexRoot), 0)
  assert.deepEqual(l.gaps, ['1 PDF has no text: this system has no PDF reader'])
  // The one Rebuild: the words are there, and the note has gone.
  const again = await rebuild(l.id)
  assert.equal(pdfRows(again.indexRoot), 1)
  assert.deepEqual(again.gaps, [])
})

test('a host with no Python says why it has no text, and a Rebuild after one is installed finds it', { skip: needWheels, timeout: 180_000 }, async () => {
  const h = await host('nopython', { noPython: true })
  const l = await build('nopython', 'meaning-documents')
  assert.equal(l.state, 'ready', l.error ?? '')
  assert.equal(l.fullText, false)
  assert.equal(l.vectors, false)
  assert.match(l.gaps[0], /^No text in this index: this system has no Python 3\.10 or later/)
  assert.match(l.gaps[1], /^No search by meaning: there is no text to embed/)
  exe(path.join(h.bin, 'python3'), `#!/bin/sh\nexec ${python} -S "$@"\n`)
  const again = await rebuild(l.id)
  assert.equal(again.fullText, true)
  assert.equal(pdfRows(again.indexRoot), 1)
})

test('a Meaning depth with no embedding model to be had says so; the words are still there', { skip: needWheels, timeout: 180_000 }, async () => {
  await host('nomodel')
  const l = await build('nomodel', 'meaning-documents')
  assert.equal(l.state, 'ready', l.error ?? '')
  assert.equal(pdfRows(l.indexRoot), 1)
  assert.equal(l.vectors, false)
  assert.equal(l.gaps.length, 1)
  assert.match(l.gaps[0], /^No search by meaning: /)
})

test('a PDF with no text layer is counted, with the reader that found nothing', { skip: needWheels, timeout: 120_000 }, async () => {
  const h = await host('blank')
  write(path.join(h.home, 'docs', 'manuals', 'scan.pdf'), pdf(' '))
  const l = await build('blank')
  assert.equal(pdfRows(l.indexRoot), 1)
  assert.deepEqual(l.gaps, ['1 PDF has no text: pypdf found none, as with scanned pages, and this depth runs no OCR'])
})
