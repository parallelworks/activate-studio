// The Workflows tab: a curated set of platform workflows, listed as tiles
// and run from their own forms. The pw CLI is replaced with a stub that
// records each call, so these run without a platform.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'wf-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'wf-ix-'))
process.env.PW_API_KEY = 'deployment-key'
process.env.STUDIO_WORKFLOWS = 'batch-job,missing-one'

const { default: Fastify } = await import('fastify')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const { workflowsTabRoutes, setWorkflowsCli, resetWorkflowsTabForTests, resolveStudioRefs, firstJson } = await import('../dist/workflowsTab.js')

const YAML = `
on:
  execute:
    inputs:
      resource:
        type: compute-clusters
        label: Cluster
      steps:
        type: number
        default: 2
jobs:
  main:
    steps:
      - run: echo hi
`
const calls = []
setWorkflowsCli(async args => {
  calls.push(args)
  const [, verb, name] = args
  if (verb === 'ls') return JSON.stringify([
    { name: 'batch-job', displayName: 'Batch job', description: 'Runs a script', tags: ['hpc'], configurations: [{ name: 'small' }] },
    { name: 'other', displayName: 'Another workflow', description: '' },
  ]) + '\nA newer CLI is available.'
  if (verb === 'get') return JSON.stringify({ name, displayName: 'Batch job', yaml: YAML, configurations: [{ name: 'small', inputs: { steps: 1 } }] })
  if (verb === 'run') return args.includes('--dry-run') ? 'inputs are valid' : JSON.stringify({ slug: 'batch-job-00001' })
  throw new Error(`unexpected ${args.join(' ')}`)
})

const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(workflowsTabRoutes)
await app.ready()
const j = async (method, url, body) => {
  const res = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: res.statusCode, body: res.json() }
}

test('the CLI notice after the JSON does not break parsing', () => {
  assert.deepEqual(firstJson('[{"a":"]"}]\nupdate available'), [{ a: ']' }])
})

test('tiles follow the curated order and mark what the platform lacks', async () => {
  resetWorkflowsTabForTests()
  const { status, body } = await j('GET', '/api/workflows/collection')
  assert.equal(status, 200)
  assert.equal(body.configured, true)
  assert.deepEqual(body.workflows.map(w => w.name), ['batch-job', 'missing-one'])
  assert.equal(body.workflows[0].displayName, 'Batch job')
  assert.deepEqual(body.workflows[0].configurations, ['small'])
  assert.equal(body.workflows[1].available, false)
})

test('the catalog lists the deployment account and flags the curated ones', async () => {
  resetWorkflowsTabForTests()
  const { body } = await j('GET', '/api/workflows/catalog')
  assert.deepEqual(body.workflows.map(w => [w.name, w.curated]), [['other', false], ['batch-job', true]])
  assert.deepEqual(body.missing, ['missing-one'])
})

test('the form converts the workflow inputs and carries saved configurations', async () => {
  const { status, body } = await j('GET', '/api/workflows/batch-job/form')
  assert.equal(status, 200)
  assert.ok(body.form && typeof body.form === 'object')
  assert.deepEqual(body.configurations, [{ name: 'small', inputs: { steps: 1 } }])
})

test('a workflow outside the collection is refused', async () => {
  const { status } = await j('POST', '/api/workflows/other/run', { inputs: {}, dryRun: true })
  assert.equal(status, 403)
})

test('a picker object is submitted as its pw:// reference', async () => {
  calls.length = 0
  const { body } = await j('POST', '/api/workflows/batch-job/run', {
    inputs: { resource: { name: 'gpu', user: 'someone', schedulerType: 'slurm', _studioRef: 'pw://someone/gpu' }, steps: 3 },
  })
  assert.equal(body.ok, true)
  assert.equal(body.slug, 'batch-job-00001')
  const run = calls.find(c => c[1] === 'run')
  const sent = JSON.parse(run[run.indexOf('-i') + 1])
  assert.equal(sent.resource, 'pw://someone/gpu')
})

test('a dry run validates without recording a run', async () => {
  const { body } = await j('POST', '/api/workflows/batch-job/run', { inputs: { steps: 1 }, dryRun: true })
  assert.equal(body.ok, true)
  assert.equal(body.dryRun, true)
})

test('references are resolved at any depth and other objects are left alone', () => {
  assert.deepEqual(
    resolveStudioRefs({ a: { _studioRef: 'pw://u/x', uri: 'pw://u/x' }, b: [{ _studioRef: 'y' }, 2], c: { d: 'e' } }),
    { a: 'pw://u/x', b: ['y', 2], c: { d: 'e' } },
  )
})

test('an expired platform credential is reported as such, not as an internal error', async () => {
  const { platformError } = await import('../dist/workflowsTab.js')
  const e = platformError(new Error("2026-10-02T14:33:54Z [ERROR] Authentication has expired. Please authenticate again using 'pw auth'."))
  assert.equal(e.status ?? e.statusCode, 401)
  assert.match(e.message, /credential has expired/)
  const other = platformError(new Error('2026-10-02T14:33:54Z [ERROR] connection reset by peer\nmore'))
  assert.equal(other.status ?? other.statusCode, 502)
  assert.equal(other.message, 'The platform did not answer: connection reset by peer')
})

test('with the deployment credential expired, the catalog lists the viewer\'s own account', async () => {
  const { setUserKey } = await import('../dist/credentials.js')
  setUserKey('viewer-1', 'viewer-key', false)
  const app2 = Fastify()
  app2.setErrorHandler(sanitizedErrorHandler(app2))
  app2.addHook('onRequest', async req => { req.user = { id: 'viewer-1', username: 'viewer' } })
  await app2.register(workflowsTabRoutes)
  await app2.ready()
  resetWorkflowsTabForTests()
  setWorkflowsCli(async (args, key) => {
    if (key === 'deployment-key') throw new Error("2026-10-02T14:33:54Z [ERROR] Authentication has expired. Please authenticate again using 'pw auth'.")
    if (args[1] === 'ls') return JSON.stringify([{ name: 'mine-only', displayName: 'Mine only' }])
    throw new Error(`unexpected ${args.join(' ')}`)
  })
  const res = await app2.inject({ method: 'GET', url: '/api/workflows/catalog' })
  assert.equal(res.statusCode, 200)
  const body = res.json()
  assert.equal(body.source, 'viewer')
  assert.deepEqual(body.workflows.map(w => w.name), ['mine-only'])
})
