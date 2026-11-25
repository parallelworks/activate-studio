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
// A PNG header is all imageSizes reads.
const pngOf = (w, h) => {
  const b = Buffer.alloc(33)
  b.writeUInt32BE(0x89504e47, 0); b.writeUInt32BE(0x0d0a1a0a, 4)
  b.writeUInt32BE(13, 8); b.write('IHDR', 12); b.writeUInt32BE(w, 16); b.writeUInt32BE(h, 20)
  return b
}
const png = pngOf(256, 256)
const ICON = path.join(KB, 'brand.png')
fs.writeFileSync(ICON, png)
process.env.APP_ICON = ICON
process.env.APP_NAME = 'Example Research Studio'

const { default: Fastify } = await import('fastify')
const { kbRoutes, webManifest, imageSizes, installIcon, groundOf } = await import('../dist/routes.js')
const app = Fastify()
await app.register(kbRoutes)
await app.ready()

test('the route names the deployment, lists its icon at its real size, and is not cached', async () => {
  const res = await app.inject({ method: 'GET', url: '/manifest.webmanifest' })
  assert.equal(res.statusCode, 200)
  assert.match(res.headers['content-type'], /application\/manifest\+json/)
  assert.match(res.headers['cache-control'], /no-store/)
  const m = res.json()
  assert.equal(m.name, 'Example Research Studio')
  assert.ok(m.short_name.length <= 12)
  assert.equal(m.display, 'standalone')
  // Alone: browsers take the largest icon, and some the maskable one, so
  // the Studio's mark beside it was the one installed.
  assert.equal(m.icons.length, 1)
  assert.match(m.icons[0].src, /^api\/brand-icon\?v=[0-9a-f]{10}$/)
  assert.deepEqual({ ...m.icons[0], src: 'x' }, { src: 'x', type: 'image/png', sizes: '256x256' })
})

test('an icon an installed app cannot have leaves the Studio\'s mark', () => {
  assert.equal(installIcon('/api/brand-icon?v=1', pngOf(300, 200), 'image/png'), null)
  assert.equal(installIcon('/api/brand-icon?v=1', pngOf(128, 128), 'image/png'), null)
  assert.equal(installIcon('/api/brand-icon?v=1', Buffer.from('<svg/>'), 'image/svg+xml'), null)
  assert.equal(installIcon('/api/brand-icon?v=1', Buffer.from('<!doctype html>'), 'text/html'), null)
  assert.deepEqual(installIcon('/api/brand-icon?v=1', pngOf(144, 144), 'image/png'), { src: 'api/brand-icon?v=1', type: 'image/png', sizes: '144x144' })
})

test('no id, and start and scope stay under the mount path', () => {
  const m = webManifest({ name: 'Studio', dark: false, brand: null })
  assert.equal('id' in m, false)
  const mounted = 'https://host.example/tools/studio/manifest.webmanifest'
  assert.equal(new URL(m.start_url, mounted).pathname, '/tools/studio/')
  assert.equal(new URL(m.scope, mounted).pathname, '/tools/studio/')
  assert.ok(m.icons.some(i => i.sizes === '512x512' && i.purpose === 'maskable'))
  for (const i of [...m.icons, installIcon('/api/brand-icon?v=1', png, 'image/png')]) assert.match(new URL(i.src, mounted).pathname, /^\/tools\/studio\/(icon-|api\/brand-icon$)/)
})

test('the mark\'s addresses carry what src adds, so a new mark is a new address', () => {
  const m = webManifest({ name: 'Studio', dark: false, brand: null, src: f => `${f}?v=1` })
  assert.deepEqual(m.icons.map(i => i.src), ['icon-192.png?v=1', 'icon-512.png?v=1', 'icon-maskable-512.png?v=1'])
})

test('icon sizes: PNG from its header, SVG and unknown as any', () => {
  assert.equal(imageSizes(pngOf(300, 200), 'image/png'), '300x200')
  assert.equal(imageSizes(Buffer.from('<svg/>'), 'image/svg+xml'), 'any')
  assert.equal(imageSizes(Buffer.from('GIF89a'), 'image/gif'), 'any')
})

test('replacing the icon file changes its address, so browsers do not keep the old one', async () => {
  const first = (await app.inject({ method: 'GET', url: '/manifest.webmanifest' })).json().icons[0].src
  const later = new Date(Date.now() + 5000)
  fs.utimesSync(ICON, later, later)
  const second = (await app.inject({ method: 'GET', url: '/manifest.webmanifest' })).json().icons[0].src
  assert.notEqual(first, second)
})

test('with a classification banner the title bar takes its color; the splash keeps the page ground', () => {
  const plain = webManifest({ name: 'Studio', dark: false, brand: null })
  assert.equal(plain.theme_color, plain.background_color)
  const marked = webManifest({ name: 'Studio', dark: false, brand: null, banner: '#24612e' })
  assert.equal(marked.theme_color, '#24612e')
  assert.equal(marked.background_color, plain.background_color)
  assert.equal(webManifest({ name: 'Studio', dark: false, brand: null, banner: 'red;}' }).theme_color, plain.background_color)
})

test('only a Studio with a banner offers the window controls overlay', () => {
  assert.equal(webManifest({ name: 'Studio', dark: false, brand: null }).display_override, undefined)
  assert.deepEqual(webManifest({ name: 'Studio', dark: false, brand: null, banner: '#24612e' }).display_override, ['window-controls-overlay'])
})

test('the splash takes the ground the person last saw, from its cookie; the title bar keeps the banner', async () => {
  const res = await app.inject({ method: 'GET', url: '/manifest.webmanifest', headers: { cookie: 'a=1; ade_ground=0C1320' } })
  assert.equal(res.json().background_color, '#0c1320')
  for (const bad of ['ade_ground=red', 'ade_ground=%230c1320', 'ade_ground=0c1320aa', 'other=0c1320', '']) assert.equal(groundOf(bad), null, bad)
  const marked = webManifest({ name: 'Studio', dark: true, brand: null, banner: '#24612e', ground: '#ffffff' })
  assert.equal(marked.background_color, '#ffffff')
  assert.equal(marked.theme_color, '#24612e')
  const plain = webManifest({ name: 'Studio', dark: true, brand: null, ground: '#ffffff' })
  assert.equal(plain.theme_color, '#ffffff')
})
