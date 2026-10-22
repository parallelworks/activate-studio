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
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'

// The platform API returns a workflow's YAML with its inputs in declared
// order; `pw workflows get -o json` returns them sorted. Only ordered-job
// is served by the API here; the others fall back to the CLI.
const ORDERED = { on: { execute: { inputs: { zeta: { type: 'string', label: 'Zeta' }, alpha: { type: 'string', label: 'Alpha' }, mid: { type: 'number', label: 'Mid' } } } }, jobs: { main: { steps: [{ run: 'echo hi' }] } } }
// A workflow whose form has a layout, at the top and inside a group.
const LAID_OUT = { on: { execute: { inputs: {
  $meta: { layout: { type: 'grid', columns: { base: 1, md: 2 }, children: [{ type: 'field', field: 'study' }, { type: 'field', field: 'cases' }] } },
  cases: { type: 'number', label: 'Cases', default: 8 },
  study: { type: 'group', label: 'Study', items: {
    $meta: { layout: { type: 'stack', children: [{ type: 'field', field: 'high' }, { type: 'field', field: 'low' }] } },
    low: { type: 'number', label: 'Low' }, high: { type: 'number', label: 'High' } } },
} } }, jobs: { main: { steps: [{ run: 'echo hi' }] } } }
globalThis.fetch = async url => {
  if (String(url) === 'https://platform.test/api/workflows/laid-out') return Response.json({ name: 'laid-out', displayName: 'Laid out', yaml: LAID_OUT, configurations: [] })
  if (String(url) === 'https://platform.test/api/workflows/ordered-job') return Response.json({ name: 'ordered-job', displayName: 'Ordered', yaml: ORDERED, configurations: [] })
  return new Response('not found', { status: 404 })
}

const { default: Fastify } = await import('fastify')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const { workflowsTabRoutes, setWorkflowsCli, resetWorkflowsTabForTests, resolveStudioRefs, firstJson, recordSource } = await import('../dist/workflowsTab.js')

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
  if (verb === 'get' && name === 'ordered-job') return JSON.stringify({ name, displayName: 'Ordered', yaml: { on: { execute: { inputs: { alpha: ORDERED.on.execute.inputs.alpha, mid: ORDERED.on.execute.inputs.mid, zeta: ORDERED.on.execute.inputs.zeta } } }, jobs: ORDERED.jobs }, configurations: [] })
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
  const { status, body } = await j('GET', '/api/workflows/item/form?w=batch-job')
  assert.equal(status, 200)
  assert.ok(body.form && typeof body.form === 'object')
  assert.deepEqual(body.configurations, [{ name: 'small', inputs: { steps: 1 } }])
})

test('the form keeps the inputs in their declared order, read from the platform API', async () => {
  const { status, body } = await j('GET', '/api/workflows/item/form?w=ordered-job')
  assert.equal(status, 200)
  const order = JSON.stringify(body.form)
  assert.ok(order.indexOf('Zeta') < order.indexOf('Alpha') && order.indexOf('Alpha') < order.indexOf('Mid'),
    `declared order zeta, alpha, mid; got ${order.slice(0, 200)}`)
})

test('the form keeps the workflow\'s layout, at the top and inside a group, for the form to render', async () => {
  const { status, body } = await j('GET', '/api/workflows/item/form?w=laid-out')
  assert.equal(status, 200)
  assert.deepEqual(body.form.$meta, LAID_OUT.on.execute.inputs.$meta)
  assert.deepEqual(body.form.study.options.$meta, LAID_OUT.on.execute.inputs.study.items.$meta)
})

test('a workflow outside the collection is refused', async () => {
  const { status } = await j('POST', '/api/workflows/item/run?w=other', { inputs: {}, dryRun: true })
  assert.equal(status, 403)
})

test('a picker object is submitted as its pw:// reference', async () => {
  calls.length = 0
  const { body } = await j('POST', '/api/workflows/item/run?w=batch-job', {
    inputs: { resource: { name: 'gpu', user: 'someone', schedulerType: 'slurm', _studioRef: 'pw://someone/gpu' }, steps: 3 },
  })
  assert.equal(body.ok, true)
  assert.equal(body.slug, 'batch-job-00001')
  const run = calls.find(c => c[1] === 'run')
  const sent = JSON.parse(run[run.indexOf('-i') + 1])
  assert.equal(sent.resource, 'pw://someone/gpu')
})

test('a dry run validates without recording a run', async () => {
  const { body } = await j('POST', '/api/workflows/item/run?w=batch-job', { inputs: { steps: 1 }, dryRun: true })
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
  const other = platformError(new Error('2026-10-02T14:33:54Z [ERROR] invalid workflow name\nmore'))
  assert.equal(other.status ?? other.statusCode, 502)
  assert.equal(other.message, 'The platform did not answer: invalid workflow name')
  // A dropped connection is the network, said as such (net.ts).
  const offline = platformError(new Error('2026-10-02T14:33:54Z [ERROR] connection reset by peer\nmore'))
  assert.equal(offline.status ?? offline.statusCode, 503)
  assert.match(offline.message, /^Could not reach platform\.test\. This computer may be offline/)
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

test('a workflow record names its source: local, or the repository, branch, and file it is read from', () => {
  assert.deepEqual(recordSource({ type: 'local' }), { kind: 'local' })
  assert.deepEqual(recordSource({ type: 'remote', remote: { repo: 'https://github.com/org/repo.git', branch: 'main', yaml: 'workflows/a/workflow.yaml' } }),
    { kind: 'remote', repo: 'github.com/org/repo', branch: 'main', path: 'workflows/a/workflow.yaml' })
  assert.deepEqual(recordSource(null), { kind: 'local' })
})
