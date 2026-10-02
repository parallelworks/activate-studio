// Collection entries beyond account workflows: marketplace slugs and
// workflow.yaml files in GitHub repositories, shown and run from the tab.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'we-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'we-ix-'))
process.env.PW_API_KEY = 'deployment-key'
process.env.STUDIO_WORKFLOWS = 'marketplace/jupyter,github.com/example-org/batch-flow@v2'

const { parseWorkflowEntry, githubYamlUrls, githubThumbnail } = await import('../dist/workflowEntries.js')

test('entries parse into their three kinds, and anything else is refused', () => {
  assert.deepEqual(parseWorkflowEntry('activatebatch'), { kind: 'account', entry: 'activatebatch', name: 'activatebatch' })
  assert.deepEqual(parseWorkflowEntry('marketplace/jupyter'), { kind: 'marketplace', entry: 'marketplace/jupyter', slug: 'jupyter' })
  assert.deepEqual(parseWorkflowEntry('https://github.com/example-org/flows/examples/slurm@main'),
    { kind: 'github', entry: 'github.com/example-org/flows/examples/slurm@main', owner: 'example-org', repo: 'flows', path: 'examples/slurm', ref: 'main' })
  for (const bad of ['', 'github.com/only-owner', 'github.com/a/b/../c', 'gitlab.com/a/b', 'marketplace/', 'a b', 'x;rm -rf /']) {
    assert.equal(parseWorkflowEntry(bad), null, bad)
  }
})

test('GitHub entries resolve to raw file URLs for the form and the thumbnail', () => {
  const dir = parseWorkflowEntry('github.com/o/r/sub@v1')
  assert.deepEqual(githubYamlUrls(dir), ['https://raw.githubusercontent.com/o/r/v1/sub/workflow.yaml', 'https://raw.githubusercontent.com/o/r/v1/sub/workflow.yml'])
  assert.equal(githubThumbnail(dir), 'https://raw.githubusercontent.com/o/r/v1/sub/thumbnail.png')
  const file = parseWorkflowEntry('github.com/o/r/flows/a.yaml')
  assert.deepEqual(githubYamlUrls(file), ['https://raw.githubusercontent.com/o/r/HEAD/flows/a.yaml'])
})

const YAML = 'on:\n  execute:\n    inputs:\n      message:\n        type: string\n        label: Message\njobs:\n  main:\n    steps:\n      - run: echo hi\n'
const calls = []
const { workflowsTabRoutes, setWorkflowsCli, resetWorkflowsTabForTests } = await import('../dist/workflowsTab.js')
setWorkflowsCli(async args => {
  calls.push(args)
  if (args[0] === 'marketplace' && args[1] === 'ls') return JSON.stringify([{ slug: 'jupyter', name: 'Jupyter', description: 'Notebook server', type: 'workflow' }, { slug: 'a-bucket', type: 'storage' }])
  if (args[0] === 'marketplace' && args[1] === 'get') return YAML
  if (args[0] === 'workflows' && args[1] === 'run') return JSON.stringify({ run: { slug: 'run-00001' } })
  throw new Error(`unexpected ${args.join(' ')}`)
})
const realFetch = globalThis.fetch
globalThis.fetch = async (url, init) => {
  if (String(url) === 'https://raw.githubusercontent.com/example-org/batch-flow/v2/workflow.yaml') return new Response(YAML, { status: 200 })
  if (String(url).startsWith('https://raw.githubusercontent.com/')) return new Response('nope', { status: 404 })
  return realFetch(url, init)
}

const { default: Fastify } = await import('fastify')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(workflowsTabRoutes)
await app.ready()
const j = async (method, url, body) => {
  const res = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: res.statusCode, body: res.json() }
}
const q = s => encodeURIComponent(s)

test('tiles for marketplace and GitHub entries need no copy in the account', async () => {
  resetWorkflowsTabForTests()
  const { body } = await j('GET', '/api/workflows/collection')
  assert.deepEqual(body.workflows.map(w => [w.name, w.kind, w.displayName, w.installed, w.available]), [
    ['marketplace/jupyter', 'marketplace', 'Jupyter', true, true],
    ['github.com/example-org/batch-flow@v2', 'github', 'batch-flow', true, true],
  ])
})

test('the form comes from the marketplace YAML and from the repository file', async () => {
  const mp = await j('GET', `/api/workflows/item/form?w=${q('marketplace/jupyter')}`)
  assert.equal(mp.status, 200)
  assert.ok(mp.body.form && Object.keys(mp.body.form).length)
  const gh = await j('GET', `/api/workflows/item/form?w=${q('github.com/example-org/batch-flow@v2')}`)
  assert.equal(gh.status, 200)
  assert.ok(gh.body.form && Object.keys(gh.body.form).length)
  const missing = await j('GET', `/api/workflows/item/form?w=${q('github.com/example-org/absent')}`)
  assert.equal(missing.status, 404)
  assert.match(missing.body.error, /No workflow\.yaml/)
})

test('runs pass the entry to pw workflows run as written', async () => {
  calls.length = 0
  const { body } = await j('POST', `/api/workflows/item/run?w=${q('github.com/example-org/batch-flow@v2')}`, { inputs: { message: 'hi' } })
  assert.equal(body.ok, true)
  const run = calls.find(c => c[1] === 'run')
  assert.equal(run[2], 'github.com/example-org/batch-flow@v2')
  const mp = await j('POST', `/api/workflows/item/run?w=${q('marketplace/jupyter')}`, { inputs: {}, dryRun: true })
  assert.equal(mp.body.ok, true)
  assert.equal(calls.filter(c => c[1] === 'run').pop()[2], 'marketplace/jupyter')
})

test('a malformed entry is refused, and installing is only for account workflows', async () => {
  assert.equal((await j('GET', `/api/workflows/item/form?w=${q('gitlab.com/a/b')}`)).status, 400)
  assert.equal((await j('POST', `/api/workflows/item/install?w=${q('marketplace/jupyter')}`)).status, 409)
})

test('Settings accepts all three kinds and drops the rest', async () => {
  const { settingsRoutes, effectiveSettings } = await import('../dist/settings.js')
  const s = Fastify(); await s.register(settingsRoutes); await s.ready()
  const res = await s.inject({ method: 'PUT', url: '/api/settings', payload: { workflowCollection: ['batch', 'marketplace/jupyter', 'https://github.com/o/r@main', 'bad entry', 'gitlab.com/x/y'] } })
  assert.equal(res.statusCode, 200)
  assert.deepEqual(effectiveSettings().workflowCollection, ['batch', 'marketplace/jupyter', 'github.com/o/r@main'])
})
