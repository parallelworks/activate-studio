// A workflow's README beside its form: found from the record's repository,
// read through the platform with the viewer's key, its relative links and
// images made to work outside the repository.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'wr-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'wr-ix-'))
process.env.PW_API_KEY = 'viewer-key'
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'

const PNG = Buffer.from('89504e470d0a1a0a', 'hex')
const README = [
  '# Sweep',
  '![diagram](thumbnails/flow.png)',
  'See [the bounds format](docs/bounds.md#format), [the site](https://example.org), and [inputs](#inputs).',
  '```',
  '![not rewritten](thumbnails/flow.png)',
  '```',
  'Inline `![kept](a.png)` stays.',
  '<img src="../shared/logo.png" width="80">',
].join('\n')
const seen = []
globalThis.fetch = async (url, init) => {
  const u = new URL(String(url))
  seen.push({ url: String(url), auth: init?.headers?.Authorization ?? null })
  if (u.pathname === '/api/workflows/gitlab-flow') return Response.json({ name: 'gitlab-flow', type: 'remote', remote: { repo: 'https://gitlab.example.org/team/flows.git', branch: 'main', readme: 'sweep/README.md', yaml: 'sweep/workflow.yaml' } })
  if (u.pathname === '/api/workflows/local-flow') return Response.json({ name: 'local-flow', type: 'local' })
  if (u.pathname === '/api/repositories/readme') return Response.json({ markdown: `${README}\n<!-- ${u.searchParams.get('repo')} ${u.searchParams.get('ref')} ${u.searchParams.get('path')} -->` })
  if (u.pathname === '/api/repositories/thumbnail') return new Response(PNG, { headers: { 'content-type': 'image/png' } })
  return new Response('no', { status: 404 })
}

const tab = await import('../dist/workflowsTab.js')
tab.setWorkflowsCli(async args => {
  if (args[0] === 'marketplace' && args[1] === 'get') return JSON.stringify({ slug: args[2], versionTags: { latest: 'v2' }, versionOrder: ['v1', 'v2'],
    versions: { v1: { repo: 'https://github.com/acme/flows.git', branch: 'old', readme: 'x/README.md' }, v2: { repo: 'https://github.com/acme/flows.git', branch: 'canary', readme: 'workflows/naca/README.md' } } })
  throw new Error(`unexpected ${args.join(' ')}`)
})
const wr = await import('../dist/workflowReadme.js')
const { default: Fastify } = await import('fastify')
const app = Fastify()
await app.register(wr.workflowReadmeRoutes)
await app.ready()

test('repository addresses take the platform form', () => {
  assert.deepEqual(wr.repoKey('https://github.com/acme/flows.git'), { key: 'acme/flows', github: true })
  assert.deepEqual(wr.repoKey('acme/flows'), { key: 'acme/flows', github: true })
  assert.deepEqual(wr.repoKey('https://gitlab.example.org/team/sub/flows.git'), { key: 'gitlab.example.org/team/sub/flows', github: false })
  assert.equal(wr.repoKey('https://github.com/acme'), null)
  assert.equal(wr.repoKey('not a repo'), null)
})

test('a GitHub entry reads the README in its workflow directory', () => {
  const src = e => wr.githubReadmeSource({ kind: 'github', entry: 'x', owner: 'o', repo: 'r', ref: null, ...e })
  assert.equal(src({ path: 'workflows/naca/yamls/site.yaml' }).path, 'workflows/naca/README.md')
  assert.equal(src({ path: 'workflows/naca/workflow.yaml' }).path, 'workflows/naca/README.md')
  assert.equal(src({ path: 'workflows/naca' }).path, 'workflows/naca/README.md')
  assert.equal(src({ path: '' }).path, 'README.md')
  assert.equal(src({ path: 'a', ref: 'v1' }).ref, 'v1')
})

test('relative images go through the asset route, other links to the repository, code is left alone', () => {
  const src = { repo: 'gitlab.example.org/team/flows', ref: 'main', path: 'sweep/README.md', github: false }
  const out = wr.rewriteReadme(README, src, 'gitlab-flow')
  assert.match(out, /!\[diagram\]\(\/api\/workflows\/item\/asset\?w=gitlab-flow&path=sweep%2Fthumbnails%2Fflow\.png\)/)
  assert.match(out, /\[the bounds format\]\(https:\/\/gitlab\.example\.org\/team\/flows\/-\/blob\/main\/sweep\/docs\/bounds\.md\)/)
  assert.match(out, /\[the site\]\(https:\/\/example\.org\)/)
  assert.match(out, /\[inputs\]\(#inputs\)/)
  assert.match(out, /```\n!\[not rewritten\]\(thumbnails\/flow\.png\)\n```/)
  assert.match(out, /`!\[kept\]\(a\.png\)`/)
  assert.match(out, /<img src="\/api\/workflows\/item\/asset\?w=gitlab-flow&path=shared%2Flogo\.png"/)
  assert.equal(wr.resolveInRepo('README.md', '../outside.png'), null, 'nothing above the repository root')
})

test('an account workflow from a repository serves its README with the viewer key', async () => {
  wr.resetWorkflowReadmeForTests(); seen.length = 0
  const r = (await app.inject({ url: '/api/workflows/item/readme?w=gitlab-flow' })).json()
  assert.match(r.markdown, /^# Sweep/)
  assert.match(r.markdown, /gitlab\.example\.org\/team\/flows main sweep\/README\.md/, 'read from the record\'s repository, branch, and path')
  assert.equal(r.web, 'https://gitlab.example.org/team/flows/-/blob/main/sweep/README.md')
  assert.ok(seen.every(x => x.auth === 'Bearer viewer-key'))
})

test('a marketplace entry reads its latest version\'s README; a platform-only workflow has none', async () => {
  wr.resetWorkflowReadmeForTests()
  const m = (await app.inject({ url: '/api/workflows/item/readme?w=marketplace/naca' })).json()
  assert.match(m.markdown, /acme\/flows canary workflows\/naca\/README\.md/)
  const l = (await app.inject({ url: '/api/workflows/item/readme?w=local-flow' })).json()
  assert.deepEqual(l, { markdown: null, web: null })
})

test('a failed lookup is not remembered as no README', async () => {
  wr.resetWorkflowReadmeForTests()
  const saved = globalThis.fetch
  globalThis.fetch = async () => new Response('down', { status: 503 })
  assert.deepEqual((await app.inject({ url: '/api/workflows/item/readme?w=gitlab-flow' })).json(), { markdown: null, web: null })
  globalThis.fetch = saved
  assert.match((await app.inject({ url: '/api/workflows/item/readme?w=gitlab-flow' })).json().markdown, /^# Sweep/, 'asked again once the platform answers')
})

test('the asset route serves repository images only', async () => {
  const ok = await app.inject({ url: '/api/workflows/item/asset?w=gitlab-flow&path=sweep%2Fthumbnails%2Fflow.png' })
  assert.equal(ok.statusCode, 200)
  assert.equal(ok.headers['content-type'], 'image/png')
  assert.match(String(ok.headers['content-security-policy']), /sandbox/)
  assert.equal((await app.inject({ url: '/api/workflows/item/asset?w=gitlab-flow&path=..%2Fsecret.png' })).statusCode, 400)
  assert.equal((await app.inject({ url: '/api/workflows/item/asset?w=gitlab-flow&path=sweep%2Fworkflow.yaml' })).statusCode, 400)
})
