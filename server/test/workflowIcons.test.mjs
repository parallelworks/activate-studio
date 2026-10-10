// Workflow tile icons. A remote workflow's thumbnail can sit in a private
// repository on a GitLab server the Studio cannot read anonymously; the
// platform lists a fallback route that reads it with its own access.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'wi-kb-'))
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'wi-ix-'))
process.env.PW_API_KEY = 'viewer-key'
process.env.PW_GATEWAY_URL = 'https://platform.test/api/openai/v1'
process.env.STUDIO_WORKFLOWS = 'gitlab-wf,gitlab-old-cli-wf,github-wf,blob-wf,broken-wf'

const PNG = Buffer.from('89504e470d0a1a0a', 'hex')
const FALLBACK = '/api/repositories/thumbnail?path=thumbnails%2Fx.png&ref=main&repo=gitlab.example.mil%2Fteam%2Fflows'
const DERIVED = '/api/repositories/thumbnail?path=thumbnails%2Fy.png&ref=main&repo=gitlab.example.mil%2Fteam%2Fflows'
const seen = []
globalThis.fetch = async (url, init) => {
  const u = String(url)
  seen.push({ url: u, auth: init?.headers?.Authorization ?? null })
  if (u.startsWith('https://gitlab.example.mil/')) throw new Error('certificate not trusted')
  if (u === `https://platform.test${FALLBACK}` || u === `https://platform.test${DERIVED}`) return new Response(PNG, { status: 200, headers: { 'content-type': 'image/png' } })
  if (u === 'https://platform.test/api/blobs/abc') return new Response(PNG, { status: 200, headers: { 'content-type': 'image/png' } })
  if (u === 'https://raw.githubusercontent.com/o/r/main/thumbnail.png') return new Response(PNG, { status: 200, headers: { 'content-type': 'image/png' } })
  if (u.startsWith('https://broken.example.org/')) return new Response('<html>sign in</html>', { status: 200, headers: { 'content-type': 'text/html' } })
  return new Response('nope', { status: 404 })
}

const { workflowsTabRoutes, setWorkflowsCli, iconCandidates, thumbnailRoute } = await import('../dist/workflowsTab.js')
setWorkflowsCli(async args => {
  if (args[1] === 'ls') return JSON.stringify([
    { name: 'gitlab-wf', type: 'remote', imageUrl: 'https://gitlab.example.mil/team/flows/-/raw/main/thumbnails/x.png', imageFallbackUrl: FALLBACK },
    // What `workflows ls` returns from a CLI that drops imageFallbackUrl.
    { name: 'gitlab-old-cli-wf', type: 'remote', imageUrl: 'https://gitlab.example.mil/team/flows/-/raw/main/thumbnails/y.png' },
    { name: 'github-wf', type: 'remote', imageUrl: 'https://raw.githubusercontent.com/o/r/main/thumbnail.png' },
    { name: 'blob-wf', type: 'local', imageUrl: '/api/blobs/abc' },
    { name: 'broken-wf', type: 'remote', imageUrl: 'https://broken.example.org/x.png' },
  ])
  throw new Error(`unexpected ${args.join(' ')}`)
})
const { default: Fastify } = await import('fastify')
const app = Fastify()
await app.register(workflowsTabRoutes)
await app.ready()
const icon = name => app.inject({ method: 'GET', url: `/api/workflows/item/icon?w=${name}` })

test('a GitLab thumbnail comes through the platform fallback, with the key, before the direct address', async () => {
  seen.length = 0
  const r = await icon('gitlab-wf')
  assert.equal(r.statusCode, 200)
  assert.equal(r.headers['content-type'], 'image/png')
  assert.equal(seen[0].url, `https://platform.test${FALLBACK}`)
  assert.equal(seen[0].auth, 'Bearer viewer-key')
  assert.ok(!seen.some(x => x.url.startsWith('https://gitlab.example.mil/')), 'the unreachable host is not tried once the fallback answers')
})

test('without the listed fallback, the route is rebuilt from the raw address', async () => {
  seen.length = 0
  const r = await icon('gitlab-old-cli-wf')
  assert.equal(r.statusCode, 200)
  assert.equal(seen[0].url, `https://platform.test${DERIVED}`)
  assert.equal(seen[0].auth, 'Bearer viewer-key')
})

test('the rebuilt route matches the one the platform lists', () => {
  assert.equal(thumbnailRoute('https://gitlab.example.mil/team/flows/-/raw/main/thumbnails/x.png'), FALLBACK)
  assert.equal(thumbnailRoute('https://gitlab.example.mil/a/b/c/-/raw/v2/thumbs/my%20icon.png'),
    '/api/repositories/thumbnail?path=thumbs%2Fmy+icon.png&ref=v2&repo=gitlab.example.mil%2Fa%2Fb%2Fc')
  assert.equal(thumbnailRoute('https://raw.githubusercontent.com/parallelworks/workflows/canary/workflows/x/thumbnails/x.png'),
    '/api/repositories/thumbnail?path=workflows%2Fx%2Fthumbnails%2Fx.png&ref=canary&repo=parallelworks%2Fworkflows')
  assert.equal(thumbnailRoute('https://example.org/x.png'), undefined)
  assert.equal(thumbnailRoute('https://raw.githubusercontent.com/o/r/main'), undefined)
  assert.equal(thumbnailRoute('http://gitlab.example.mil/team/flows/-/raw/main/x.png'), undefined)
  assert.equal(thumbnailRoute('/api/blobs/abc'), undefined)
  assert.equal(thumbnailRoute(undefined), undefined)
})

test('GitHub is read anonymously, and platform icons with the key', async () => {
  seen.length = 0
  assert.equal((await icon('github-wf')).statusCode, 200)
  assert.equal(seen.find(x => x.url.includes('githubusercontent')).auth, null)
  assert.equal((await icon('blob-wf')).statusCode, 200)
  assert.equal(seen.find(x => x.url.includes('/api/blobs/')).auth, 'Bearer viewer-key')
})

test('a sign-in page is not an icon', async () => {
  const r = await icon('broken-wf')
  assert.equal(r.statusCode, 404)
})

test('the order: anonymous reads first only where they work', () => {
  const P = 'https://platform.test'
  assert.deepEqual(iconCandidates('https://gitlab.x/a.png', '/api/repositories/thumbnail?x', P), [`${P}/api/repositories/thumbnail?x`, 'https://gitlab.x/a.png'])
  assert.deepEqual(iconCandidates('https://raw.githubusercontent.com/a.png', '/fb', P), ['https://raw.githubusercontent.com/a.png', `${P}/fb`])
  assert.deepEqual(iconCandidates('/api/blobs/z', undefined, P), [`${P}/api/blobs/z`])
  assert.deepEqual(iconCandidates('http://plain.example/a.png', undefined, P), [])
  assert.deepEqual(iconCandidates(undefined, undefined, P), [])
})
