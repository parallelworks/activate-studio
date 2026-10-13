// run_workflow on a workflow with saved configurations: a launch that names
// neither a configuration nor a system is refused before submission, with
// the configurations to choose from; naming one launches as before.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'ix-'))
const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'bin-'))
const calls = path.join(bin, 'calls.log')
const doc = {
  name: 'study-doe',
  yaml: { on: { execute: { inputs: { resource: { type: 'compute-clusters', label: 'Cluster' }, doe: { type: 'group', items: { samples: { type: 'number', default: 8 } } } } } } },
  configurations: [{ name: 'SiteA', inputs: { resource: { name: 'site-a', provider: 'existing' } } }, { name: 'SiteB', inputs: {} }],
}
fs.writeFileSync(path.join(bin, 'pw'), `#!/usr/bin/env node
const fs = require('fs'); const a = process.argv.slice(2)
fs.appendFileSync(${JSON.stringify(calls)}, JSON.stringify(a) + '\\n')
if (a[0] === 'workflows' && a[1] === 'get') { process.stdout.write(${JSON.stringify(JSON.stringify(doc))}); process.exit(0) }
if (a[0] === 'workflows' && a[1] === 'run') { process.stdout.write(JSON.stringify({ run: { slug: 'study-doe-00007', status: 'running' } })); process.exit(0) }
process.stdout.write('{}')
`, { mode: 0o755 })
process.env.PW_CLI = path.join(bin, 'pw')
const { executeTool } = await import('../dist/chat/tools.js')
const runs = () => fs.existsSync(calls) ? fs.readFileSync(calls, 'utf8').trim().split('\n').map(l => JSON.parse(l)).filter(a => a[1] === 'run') : []

test('a launch with no configuration and no system is refused, naming the configurations', async () => {
  const r = await executeTool('run_workflow', JSON.stringify({ name: 'study-doe', dry_run: false, inputs: '{"doe":{"samples":16}}' }))
  assert.equal(r.summary, 'needs a configuration or a system')
  assert.match(r.result, /config set to one of: SiteA, SiteB/)
  assert.match(r.result, /Nothing was submitted/)
  assert.equal(runs().length, 0)
})

test('naming a configuration, a system, or giving the resource input launches', async () => {
  for (const extra of [{ config: 'SiteA' }, { inputs: '{"resource":{"name":"site-a","provider":"existing"}}' }]) {
    const r = await executeTool('run_workflow', JSON.stringify({ name: 'study-doe', dry_run: false, ...extra }))
    assert.equal(r.summary, 'run submitted: study-doe-00007', JSON.stringify(extra))
  }
  assert.equal(runs().length, 2)
})
