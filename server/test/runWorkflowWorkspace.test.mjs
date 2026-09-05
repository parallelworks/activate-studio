// run_workflow through a fake CLI whose workspace is down until started.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'bin-'))
const calls = path.join(bin, 'calls.log')
const marker = path.join(bin, 'workspace-up')
fs.writeFileSync(path.join(bin, 'pw'), `#!/usr/bin/env node
const fs = require('fs'); const a = process.argv.slice(2)
fs.appendFileSync(${JSON.stringify(calls)}, JSON.stringify(a) + '\\n')
if (a[0] === 'workspace' && a[1] === 'start') { fs.writeFileSync(${JSON.stringify(marker)}, '1'); process.stdout.write('Workspace start requested'); process.exit(0) }
if (a[0] === 'workflows' && a[1] === 'run') {
  if (!fs.existsSync(${JSON.stringify(marker)})) { process.stderr.write('[ERROR] User workspace not found or is not running'); process.exit(1) }
  process.stdout.write(JSON.stringify({ run: { slug: 'activatebatch-00010', status: 'running' } })); process.exit(0)
}
process.stdout.write('{}')
`, { mode: 0o755 })
process.env.PW_CLI = path.join(bin, 'pw')
process.env.STUDIO_WORKSPACE_RETRY_MS = '1'
const { executeTool } = await import('../dist/chat/tools.js')

test('run_workflow starts a stopped workspace, retries, and says it did', async () => {
  const r = await executeTool('run_workflow', JSON.stringify({ name: 'activatebatch', dry_run: false, inputs: '{"commands":"hostname"}' }))
  assert.equal(r.summary, 'run submitted')
  assert.match(r.result, /Run activatebatch-00010/)
  assert.match(r.result, /workspace was not running; it was started and the launch retried/)
  const seq = fs.readFileSync(calls, 'utf8').trim().split('\n').map(l => JSON.parse(l).slice(0, 2).join(' '))
  assert.deepEqual(seq, ['workflows run', 'workspace start', 'workflows run'])
})
