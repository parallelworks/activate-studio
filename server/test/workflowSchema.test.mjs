// Composed workflows must be executable, and the platform's own schema is
// the authority on that. The fixture is the schema as published at
// /workflow.schema.json; refresh it when the platform's changes.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const idx = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-idx-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-kb-'))
process.env.INDEX_BASE = idx
// An unreachable gateway forces the validator onto the disk cache, which is
// the fixture: the test never touches the network.
process.env.PW_GATEWAY_URL = 'http://127.0.0.1:1/api/openai/v1'
const here = path.dirname(fileURLToPath(import.meta.url))
fs.copyFileSync(path.join(here, 'fixtures', 'workflow.schema.json'), path.join(idx, 'workflow.schema.json'))

// A fake pw CLI serving two small workflows for the composer to merge.
const bin = fs.mkdtempSync(path.join(os.tmpdir(), 'studio-bin-'))
const WF = name => JSON.stringify({ name, yaml: { on: { execute: { inputs: {
  resource: { type: 'compute-clusters', label: 'Resource' },
} } }, jobs: { main: { steps: [{ run: 'true' }] } } } })
fs.writeFileSync(path.join(bin, 'pw'), `#!/usr/bin/env node
const name = process.argv[process.argv.indexOf('get') + 1]
process.stdout.write(${JSON.stringify(WF('X'))}.replace('"X"', JSON.stringify(name)))
`, { mode: 0o755 })
process.env.PW_CLI = path.join(bin, 'pw')

const { validateWorkflowDoc } = await import('../dist/workflowSchema.js')
const { composeWorkflows } = await import('../dist/workflowCompose.js')

test('what the composer emits passes the platform schema', async () => {
  const c = await composeWorkflows([{ uses: 'cloudinit' }, { uses: 'Hello.World' }])
  const v = await validateWorkflowDoc(c.yaml)
  assert.equal(v.ok, true, `composer output must be executable: ${v.errors.join('; ')}`)
  const jobs = c.yaml.jobs
  for (const [key, job] of Object.entries(jobs)) {
    assert.match(key, /^[a-z0-9_-]{1,255}$/, 'job keys are lowercase in the schema')
    assert.match(job.steps[0].uses, /^(workflow|marketplace|github|parallelworks)\//,
      'a bare name is not a legal uses reference')
  }
})

test('a bare uses reference is what the schema rejects', async () => {
  const v = await validateWorkflowDoc({ jobs: { a: { steps: [{ uses: 'cloudinit' }] } } })
  assert.equal(v.ok, false)
  assert.ok(v.errors.some(e => e.includes('/jobs/a/steps/0')), v.errors.join('; '))
})

test('marketplace and github references pass through unprefixed', async () => {
  const c = await composeWorkflows([{ uses: 'marketplace/cloudinit/v1.0.1' }, { uses: 'github/org/repo/wf' }])
  const uses = Object.values(c.yaml.jobs).map(j => j.steps[0].uses)
  assert.deepEqual(uses.sort(), ['github/org/repo/wf', 'marketplace/cloudinit/v1.0.1'])
  assert.equal((await validateWorkflowDoc(c.yaml)).ok, true)
})

test('a known-good workflow validates, so the gate is not simply strict', async () => {
  const { parse } = await import('yaml')
  const doc = parse(fs.readFileSync(path.join(here, '..', '..', 'deploy', 'workflow.yaml'), 'utf8'))
  const v = await validateWorkflowDoc(doc)
  assert.equal(v.ok, true, v.errors.join('; '))
})

test('no schema anywhere is reported as unvalidated, never as a pass', async () => {
  fs.rmSync(path.join(idx, 'workflow.schema.json'))
  // A fresh import so the compiled-validator cache cannot answer.
  const fresh = await import(`../dist/workflowSchema.js?t=${Date.now()}`)
  const v = await fresh.validateWorkflowDoc({ jobs: {} })
  assert.equal(v.ok, null)
  assert.match(v.errors[0], /not validated/)
})
