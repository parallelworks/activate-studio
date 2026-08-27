// Status Monitor discovery: the URL is found from running sessions and
// verified by probing, so a session that merely sounds like a monitor is
// not reported as one.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import http from 'node:http'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))

// A real monitor (answers JSON) and an impostor (answers HTML, as a
// platform login redirect would).
const monitor = http.createServer((q, s) => {
  if (q.url === '/api/fleet/summary') { s.writeHead(200, { 'content-type': 'application/json' }); return s.end('{"clusters":3}') }
  s.writeHead(404); s.end()
})
const impostor = http.createServer((q, s) => { s.writeHead(200, { 'content-type': 'text/html' }); s.end('<html>login</html>') })
await new Promise(r => monitor.listen(0, '127.0.0.1', r))
await new Promise(r => impostor.listen(0, '127.0.0.1', r))
const monUrl = `http://127.0.0.1:${monitor.address().port}`
const impUrl = `http://127.0.0.1:${impostor.address().port}`

// Stand in for the pw CLI: `sessions ls -o json` returns our fixtures,
// impostor first so ordering alone cannot pass the test.
const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-bin-'))
fs.writeFileSync(path.join(bin, 'pw'), `#!/bin/sh
cat <<'JSON'
[{"name":"hpc-status-fake","status":"running","externalHref":"${impUrl}/"},
 {"name":"status-monitor","status":"running","externalHref":"${monUrl}/"},
 {"name":"stopped-one","status":"stopped","externalHref":"${monUrl}/"}]
JSON
`, { mode: 0o755 })
process.env.PW_CLI = path.join(bin, 'pw')

const { discoverStatusMonitor } = await import('../dist/chat/tools.js')

test('discovery probes candidates and picks the one that answers as a monitor', async () => {
  const found = await discoverStatusMonitor()
  assert.equal(found, monUrl, 'the JSON-answering session wins over the HTML impostor')
})

test.after(() => { monitor.close(); impostor.close() })
