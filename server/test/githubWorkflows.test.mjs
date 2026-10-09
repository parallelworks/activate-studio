// GitHub workflows kept as components in a repository (a directory's
// yamls/<variant>.yaml rather than a workflow.yaml), found by browsing,
// shown with their README, and run only once the viewer approves the
// account access they declare.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'gw-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'gw-ix-'))
process.env.PW_API_KEY = 'deployment-key'
process.env.STUDIO_WORKFLOWS = [
  'github.com/example-org/components/workflows/doe/yamls/general.yaml@canary',
  'github.com/example-org/components/workflows/explorer/yamls/site.yaml@canary',
  'github.com/example-org/components/workflows/plain@canary',
].join(',')

const YAML = (perms) => `${perms ? "permissions:\n  - '*'\n" : ''}on:\n  execute:\n    inputs:\n      n_cases:\n        type: number\n        label: Cases\n        default: 8\njobs:\n  main:\n    steps:\n      - run: echo hi\n`
const TREE = [
  'README.md',
  'docs/notes.md',
  'workflows/doe/README.md', 'workflows/doe/yamls/general.yaml', 'workflows/doe/thumbnails/methods.svg', 'workflows/doe/app/doe.py',
  'workflows/explorer/README.md', 'workflows/explorer/yamls/general.yaml', 'workflows/explorer/yamls/site.yaml', 'workflows/explorer/thumbnails/explorer.png',
  'workflows/myworkflow.yaml',
  'workflows/plain/README.md', 'workflows/plain/workflow.yaml',
]
const RAW = 'https://raw.githubusercontent.com/example-org/components/canary/'
const FILES = {
  'workflows/doe/README.md': '# DOE: Design of Experiments\n\nSpreads designs over the [bounds](x.md) of the\ndesign variables.\n\n## Inputs\n',
  'workflows/plain/README.md': `# Plain\n\n${'Runs one step on a cluster and reports what it found. '.repeat(8)}Then lists:\n\n- a\n`,
  'workflows/explorer/README.md': '# Design Explorer\n\n![shot](thumbnails/explorer.png)\n\nBrowses a study in a *browser*.\n',
  'workflows/doe/yamls/general.yaml': YAML(true),
  'workflows/explorer/yamls/general.yaml': YAML(true),
  'workflows/explorer/yamls/site.yaml': YAML(true),
  'workflows/plain/workflow.yaml': YAML(false),
  'workflows/explorer/thumbnails/explorer.png': 'PNG',
}
let treeCalls = 0
globalThis.fetch = async (url) => {
  const u = String(url)
  if (u.startsWith('https://api.github.com/repos/example-org/components/git/trees/canary')) {
    treeCalls++
    return new Response(JSON.stringify({ tree: TREE.map(p => ({ path: p, type: 'blob' })) }), { status: 200 })
  }
  if (u.startsWith('https://api.github.com/repos/example-org/limited/')) return new Response('{}', { status: 403 })
  if (u.startsWith(RAW) && FILES[u.slice(RAW.length)]) {
    return new Response(FILES[u.slice(RAW.length)], { status: 200, headers: { 'content-type': u.endsWith('.png') ? 'image/png' : 'text/plain' } })
  }
  return new Response('nope', { status: 404 })
}

const calls = []
const { workflowsTabRoutes, setWorkflowsCli, resetWorkflowsTabForTests } = await import('../dist/workflowsTab.js')
const { resetGithubCaches } = await import('../dist/githubWorkflows.js')
setWorkflowsCli(async args => {
  calls.push(args)
  if (args[0] === 'workflows' && args[1] === 'run') {
    if (args[2].includes('/plain/')) throw new Error(`\x1b[90m2026-10-09T12:45:44Z\x1b[0m [\x1b[31mERROR\x1b[0m] \x1b[1mUnable to find workflow ${args[2]}\x1b[0m`)
    if (!args.includes('--trust')) throw new Error('github.com/example-org/components wants access to:\n  permission  *\n\nRe-run with --trust to approve them.\n2026-10-09T12:40:34Z [ERROR] run cancelled')
    return JSON.stringify({ run: { slug: 'general-00001' } })
  }
  throw new Error(`unexpected ${args.join(' ')}`)
})
const { default: Fastify } = await import('fastify')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(workflowsTabRoutes)
await app.ready()
const j = async (method, url, body) => {
  const res = await app.inject({ method, url, ...(body ? { payload: body } : {}) })
  return { status: res.statusCode, body: res.headers['content-type']?.startsWith('application/json') ? res.json() : res.body, headers: res.headers }
}
const q = s => encodeURIComponent(s)
const DOE = 'github.com/example-org/components/workflows/doe/yamls/general.yaml@canary'
const SITE = 'github.com/example-org/components/workflows/explorer/yamls/site.yaml@canary'

test('browsing a directory lists one entry per workflow file, titled from the README', async () => {
  resetGithubCaches()
  const { status, body } = await j('GET', `/api/workflows/github/browse?repo=${q('github.com/example-org/components/workflows@canary')}`)
  assert.equal(status, 200)
  assert.deepEqual(body.workflows.map(w => [w.entry, w.title]), [
    [DOE, 'DOE: Design of Experiments'],
    ['github.com/example-org/components/workflows/explorer/yamls/general.yaml@canary', 'Design Explorer (general)'],
    [SITE, 'Design Explorer (site)'],
    ['github.com/example-org/components/workflows/plain/workflow.yaml@canary', 'Plain'],
  ])
  assert.equal(body.workflows[0].description, 'Spreads designs over the bounds of the design variables.')
  assert.equal(body.workflows[1].description, 'Browses a study in a browser.')
  // The whole repository finds the same four; a file merely ending in workflow.yaml is not one.
  const all = await j('GET', `/api/workflows/github/browse?repo=${q('github.com/example-org/components@canary')}`)
  assert.equal(all.body.workflows.length, 4)
  assert.ok(!all.body.workflows.some(w => /myworkflow|workflows\/my/.test(w.entry)))
  assert.equal(treeCalls, 1, 'the tree is read once and cached')
})

test('a directory resolves to its files, and one with several cannot be run by directory', async () => {
  const one = await j('GET', `/api/workflows/github/resolve?w=${q('github.com/example-org/components/workflows/doe@canary')}`)
  assert.deepEqual(one.body.entries, [DOE])
  const two = await j('GET', `/api/workflows/github/resolve?w=${q('github.com/example-org/components/workflows/explorer@canary')}`)
  assert.equal(two.body.entries.length, 2)
  const form = await j('GET', `/api/workflows/item/form?w=${q('github.com/example-org/components/workflows/explorer@canary')}`)
  assert.equal(form.status, 409)
  assert.match(form.body.error, /several workflows; name one/)
  const dirForm = await j('GET', `/api/workflows/item/form?w=${q('github.com/example-org/components/workflows/doe@canary')}`)
  assert.equal(dirForm.status, 200)
  assert.equal(dirForm.body.runAs, DOE)
})

test('tiles take the README title and a PNG thumbnail, and skip SVG', async () => {
  resetWorkflowsTabForTests()
  const { body } = await j('GET', '/api/workflows/collection')
  assert.deepEqual(body.workflows.map(w => [w.name, w.displayName, w.icon !== null]), [
    [DOE, 'DOE: Design of Experiments', false],
    [SITE, 'Design Explorer (site)', true],
    ['github.com/example-org/components/workflows/plain@canary', 'Plain', false],
  ])
  // A long first paragraph ends at a sentence.
  assert.match(body.workflows[2].description, /found\.$/)
  assert.ok(body.workflows[2].description.length <= 320)
  const icon = await j('GET', `/api/workflows/item/icon?w=${q(SITE)}`)
  assert.equal(icon.status, 200)
  assert.equal(icon.headers['content-type'], 'image/png')
  assert.match(icon.headers['content-security-policy'], /sandbox/)
})

test('the form carries the declared permissions, and a run needs the viewer to approve them', async () => {
  const form = await j('GET', `/api/workflows/item/form?w=${q(DOE)}`)
  assert.equal(form.status, 200)
  assert.deepEqual(form.body.permissions, ['*'])
  assert.equal(form.body.displayName, 'DOE: Design of Experiments')

  calls.length = 0
  const refused = await j('POST', `/api/workflows/item/run?w=${q(DOE)}`, { inputs: { n_cases: 4 }, dryRun: true })
  assert.equal(refused.body.ok, false)
  assert.equal(refused.body.needsTrust, true)
  assert.match(refused.body.message, /Approve the access/)
  assert.ok(!calls[0].includes('--trust'))

  const approved = await j('POST', `/api/workflows/item/run?w=${q(DOE)}`, { inputs: { n_cases: 4 }, dryRun: false, trust: true })
  assert.equal(approved.body.ok, true)
  const run = calls.at(-1)
  assert.equal(run[2], DOE)
  assert.ok(run.includes('--trust'))
})

test('an older CLI that cannot run a workflow file says so, without color codes', async () => {
  const r = await j('POST', `/api/workflows/item/run?w=${q('github.com/example-org/components/workflows/plain@canary')}`, { inputs: {}, dryRun: true })
  assert.equal(r.body.ok, false)
  assert.ok(!r.body.message.includes('\x1b'))
  assert.match(r.body.message, /^Unable to find workflow .*too old to run a GitHub workflow file/)
})

test('GitHub rate limiting reads as a message the admin can act on', async () => {
  const r = await j('GET', `/api/workflows/github/browse?repo=${q('github.com/example-org/limited')}`)
  assert.equal(r.status, 503)
  assert.match(r.body.error, /rate limiting/)
  assert.equal((await j('GET', `/api/workflows/github/browse?repo=${q('marketplace/jupyter')}`)).status, 400)
})
