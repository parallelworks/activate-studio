// The Studio's icon on a single-user install, uploaded in the setup wizard
// or Settings: PNG, JPEG, SVG or WebP by their bytes, at most 1 MB, stored
// under INDEX_BASE/branding by content hash and set as iconUrl or
// iconUrlDark. SVG with anything active or external is refused, and every
// brand image is served as an image only. The favicon follows the icon.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const IX = fs.mkdtempSync(path.join(os.tmpdir(), 'icon-ix-'))
process.env.KB_ROOT = fs.mkdtempSync(path.join(os.tmpdir(), 'icon-kb-'))
process.env.INDEX_BASE = IX
for (const k of ['AUTH_JWKS_URL', 'APP_ICON', 'APP_ICON_DARK', 'APP_FAVICON', 'PW_PLATFORM_HOST']) delete process.env[k]

const { default: Fastify } = await import('fastify')
const { default: multipart } = await import('@fastify/multipart')
const { kbRoutes, sanitizedErrorHandler } = await import('../dist/routes.js')
const { setupRoutes, svgProblem, iconType } = await import('../dist/setup.js')
const S = await import('../dist/settings.js')

const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(multipart)
await app.register(kbRoutes)
await app.register(setupRoutes)
await app.ready()

/** A multipart/form-data body with one file, as a browser sends it. */
function form(name, type, body) {
  const boundary = `----studio${Math.random().toString(16).slice(2)}`
  const head = Buffer.from(`--${boundary}\r\nContent-Disposition: form-data; name="file"; filename="${name}"\r\nContent-Type: ${type}\r\n\r\n`)
  return { payload: Buffer.concat([head, Buffer.from(body), Buffer.from(`\r\n--${boundary}--\r\n`)]), headers: { 'content-type': `multipart/form-data; boundary=${boundary}` } }
}
const upload = async (variant, name, type, body) => {
  const r = await app.inject({ method: 'POST', url: `/api/setup/icon${variant === 'dark' ? '?variant=dark' : ''}`, ...form(name, type, body) })
  return { status: r.statusCode, body: r.json() }
}
const get = url => app.inject({ method: 'GET', url })

const PNG = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==', 'base64')
const JPEG = Buffer.concat([Buffer.from([0xff, 0xd8, 0xff, 0xe0]), Buffer.from('JFIF jpeg body')])
const WEBP = Buffer.concat([Buffer.from('RIFF'), Buffer.alloc(4), Buffer.from('WEBPVP8 webp body')])
const SVG = '<?xml version="1.0"?>\n<svg xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink" viewBox="0 0 10 10"><defs><linearGradient id="g"/></defs><rect fill="url(#g)" width="10" height="10"/><use xlink:href="#g"/></svg>'
const branding = () => fs.readdirSync(path.join(IX, 'branding')).sort()

test('a PNG for light mode is stored by its content hash, set as iconUrl, and served as an image only; the favicon follows', async () => {
  const r = await upload('light', 'mark.png', 'image/png', PNG)
  assert.equal(r.status, 200, JSON.stringify(r.body))
  const [file] = branding()
  assert.match(file, /^icon-[0-9a-f]{16}\.png$/)
  assert.equal(S.effectiveSettings().iconUrl, path.join(IX, 'branding', file))
  assert.equal(r.body.iconUrl, path.join(IX, 'branding', file))
  assert.match(r.body.light, /^\/api\/brand-icon\?v=[0-9a-f]{10}$/)
  assert.equal(r.body.dark, null, 'no dark icon of its own')

  const cfg = (await get('/api/config')).json()
  assert.equal(cfg.iconUrl, r.body.light)
  assert.match(cfg.faviconUrl, /^\/api\/favicon\?v=/)
  for (const url of ['/api/brand-icon', '/api/favicon', '/api/brand-icon?variant=dark']) {
    const res = await get(url)
    assert.equal(res.statusCode, 200, url)
    assert.equal(res.headers['content-type'], 'image/png')
    assert.match(String(res.headers['content-security-policy']), /default-src 'none'.*sandbox/)
    assert.equal(res.headers['x-content-type-options'], 'nosniff')
    assert.deepEqual(res.rawPayload, PNG, `${url} serves the uploaded bytes`)
  }
  assert.deepEqual((await get('/api/setup/status')).json().branding, r.body)
})

test('a JPEG for dark mode and a WebP are taken; the type comes from the bytes, not the name', async () => {
  const dark = await upload('dark', 'night.png', 'image/png', JPEG)
  assert.equal(dark.status, 200, JSON.stringify(dark.body))
  assert.match(S.effectiveSettings().iconUrlDark, /icon-[0-9a-f]{16}\.jpg$/)
  assert.match(dark.body.dark, /^\/api\/brand-icon\?variant=dark&v=/)
  const res = await get('/api/brand-icon?variant=dark')
  assert.equal(res.headers['content-type'], 'image/jpeg')
  assert.match((await get('/api/config')).json().faviconUrlDark, /^\/api\/favicon\?variant=dark&v=/)

  const webp = await upload('dark', 'night.webp', 'image/webp', WEBP)
  assert.equal(webp.status, 200)
  assert.equal((await get('/api/brand-icon?variant=dark')).headers['content-type'], 'image/webp')
  assert.equal(iconType(PNG), 'png')
  assert.equal(iconType(Buffer.from('GIF89a')), null)
})

test('anything else is refused: another image type, text named .png, over 1 MB, or not multipart', async () => {
  const before = S.effectiveSettings().iconUrl
  const gif = await upload('light', 'mark.gif', 'image/gif', Buffer.from('GIF89a\x01\x00\x01\x00'))
  assert.equal(gif.status, 415)
  assert.equal(gif.body.error, 'The icon must be a PNG, JPEG, SVG or WebP image.')
  assert.equal((await upload('light', 'notes.png', 'image/png', 'just some text')).status, 415)
  const big = await upload('light', 'big.png', 'image/png', Buffer.concat([PNG, Buffer.alloc(1024 * 1024)]))
  assert.equal(big.status, 413)
  assert.equal(big.body.error, 'The icon is over 1 MB.')
  const json = await app.inject({ method: 'POST', url: '/api/setup/icon', payload: { file: 'x' } })
  assert.equal(json.statusCode, 400)
  assert.match(json.json().error, /multipart/)
  assert.equal(S.effectiveSettings().iconUrl, before, 'nothing changed')
})

test('a plain SVG is taken and served under a policy that runs nothing; one with anything active or external is refused', async () => {
  const ok = await upload('light', 'mark.svg', 'image/svg+xml', SVG)
  assert.equal(ok.status, 200, JSON.stringify(ok.body))
  const res = await get('/api/brand-icon')
  assert.equal(res.headers['content-type'], 'image/svg+xml')
  assert.match(String(res.headers['content-security-policy']), /sandbox/)
  // An embedded raster is a drawing too.
  assert.equal(svgProblem('<svg><image href="data:image/png;base64,AAAA"/></svg>'), null)

  const refused = {
    'a script': '<svg><script>alert(1)</script></svg>',
    'an event handler': '<svg onload="alert(1)"/>',
    'a foreignObject': '<svg><foreignObject><div/></foreignObject></svg>',
    'a link to another file': '<svg><image href="https://example.org/x.png"/></svg>',
    'a javascript: link': '<svg><a xlink:href="javascript:alert(1)"><rect/></a></svg>',
    'a reference to another file': '<svg><rect style="fill: url(https://example.org/f.svg#p)"/></svg>',
    'a DOCTYPE or entity': '<!DOCTYPE svg [<!ENTITY x SYSTEM "file:///etc/passwd">]><svg>&x;</svg>',
    'embedded content': '<svg><iframe src="about:blank"/></svg>',
  }
  for (const [what, svg] of Object.entries(refused)) {
    const r = await upload('light', 'bad.svg', 'image/svg+xml', svg)
    assert.equal(r.status, 415, what)
    assert.equal(r.body.error, `This SVG contains ${what}. Export it as a plain SVG, or as a PNG.`)
  }
  assert.match(S.effectiveSettings().iconUrl, /\.svg$/, 'the plain one stays')
})

test('replacing and removing leave only the files in use, and the dark icon falls back to the light one', async () => {
  await upload('light', 'mark.png', 'image/png', PNG)
  const files = branding()
  assert.equal(files.length, 2, files.join(' '))
  assert.deepEqual(files.map(f => path.extname(f)).sort(), ['.png', '.webp'])
  const r = await app.inject({ method: 'DELETE', url: '/api/setup/icon?variant=dark' })
  assert.equal(r.statusCode, 200)
  assert.equal(r.json().dark, null)
  assert.equal(S.effectiveSettings().iconUrlDark, '')
  assert.deepEqual(branding().map(f => path.extname(f)), ['.png'])
  assert.equal((await get('/api/brand-icon?variant=dark')).headers['content-type'], 'image/png')
  await app.inject({ method: 'DELETE', url: '/api/setup/icon' })
  assert.deepEqual(branding(), [])
  assert.equal((await get('/api/config')).json().iconUrl, null)
  assert.equal((await get('/api/brand-icon')).statusCode, 404)
})
