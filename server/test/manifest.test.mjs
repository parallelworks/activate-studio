// The installable-app manifest: built per deployment, never cached, and
// with no id, so a Studio mounted under a path keeps its own identity.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const KB = fs.mkdtempSync(path.join(os.tmpdir(), 'mf-kb-'))
process.env.KB_ROOT = KB
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'mf-ix-'))
// A 300x200 PNG header is all imageSizes reads.
const png = Buffer.alloc(33)
png.writeUInt32BE(0x89504e47, 0); png.writeUInt32BE(0x0d0a1a0a, 4)
png.writeUInt32BE(13, 8); png.write('IHDR', 12); png.writeUInt32BE(300, 16); png.writeUInt32BE(200, 20)
const ICON = path.join(KB, 'brand.png')
fs.writeFileSync(ICON, png)
process.env.APP_ICON = ICON
process.env.APP_NAME = 'Example Research Studio'

const { default: Fastify } = await import('fastify')
const { kbRoutes, webManifest, imageSizes } = await import('../dist/routes.js')
const app = Fastify()
await app.register(kbRoutes)
await app.ready()

test('the route names the deployment, puts its icon first at its real size, and is not cached', async () => {
  const res = await app.inject({ method: 'GET', url: '/manifest.webmanifest' })
  assert.equal(res.statusCode, 200)
  assert.match(res.headers['content-type'], /application\/manifest\+json/)
  assert.match(res.headers['cache-control'], /no-store/)
  const m = res.json()
  assert.equal(m.name, 'Example Research Studio')
  assert.ok(m.short_name.length <= 12)
  assert.equal(m.display, 'standalone')
  assert.deepEqual(m.icons[0], { src: '/api/brand-icon', type: 'image/png', sizes: '300x200' })
  assert.ok(m.icons.some(i => i.sizes === '512x512' && i.purpose === 'maskable'))
})

test('no id, and start and scope stay under the mount path', () => {
  const m = webManifest({ name: 'Studio', dark: false, brand: null })
  assert.equal('id' in m, false)
  const mounted = 'https://host.example/tools/studio/manifest.webmanifest'
  assert.equal(new URL(m.start_url, mounted).pathname, '/tools/studio/')
  assert.equal(new URL(m.scope, mounted).pathname, '/tools/studio/')
})

test('icon sizes: PNG from its header, SVG and unknown as any', () => {
  assert.equal(imageSizes(png, 'image/png'), '300x200')
  assert.equal(imageSizes(Buffer.from('<svg/>'), 'image/svg+xml'), 'any')
  assert.equal(imageSizes(Buffer.from('GIF89a'), 'image/gif'), 'any')
})
