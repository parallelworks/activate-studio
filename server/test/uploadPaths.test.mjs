// A folder's structure has to survive the upload: the multipart parser cuts
// every filename to its base name, so the paths travel in their own field.
import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'

const KB = fs.mkdtempSync(path.join(os.tmpdir(), 'up-kb-'))
process.env.KB_ROOT = KB
process.env.INDEX_BASE = fs.mkdtempSync(path.join(os.tmpdir(), 'up-ix-'))

const { default: Fastify } = await import('fastify')
const { default: fastifyMultipart } = await import('@fastify/multipart')
const { sanitizedErrorHandler } = await import('../dist/routes.js')
const { uploadRoutes } = await import('../dist/uploads.js')

const app = Fastify()
app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(fastifyMultipart)
await app.register(uploadRoutes)
await app.ready()

const boundary = 'studio-test'
const part = (head, body) => `--${boundary}\r\n${head}\r\n\r\n${body}\r\n`
const file = (name, body) => part(`Content-Disposition: form-data; name="file"; filename="${name}"\r\nContent-Type: text/plain`, body)
const post = (dir, payload) => app.inject({
  method: 'POST', url: `/api/kb/upload?dir=${dir}&index=0`, payload: `${payload}--${boundary}--\r\n`,
  headers: { 'content-type': `multipart/form-data; boundary=${boundary}` },
})

test('an upload keeps the folders named in its paths field', async () => {
  const paths = part('Content-Disposition: form-data; name="paths"', JSON.stringify(['top/one/a.md', 'top/one/two/b.md']))
  const res = await post('nested', paths + file('a.md', 'a') + file('b.md', 'b'))
  assert.equal(res.statusCode, 200, res.body)
  assert.deepEqual(res.json().saved, ['nested/top/one/a.md', 'nested/top/one/two/b.md'])
  assert.equal(fs.readFileSync(path.join(KB, 'nested/top/one/two/b.md'), 'utf8'), 'b')
})

test('a path cannot climb out of the destination', async () => {
  const paths = part('Content-Disposition: form-data; name="paths"', JSON.stringify(['../../escape.md']))
  const res = await post('nested', paths + file('escape.md', 'x'))
  assert.equal(fs.existsSync(path.join(KB, '..', 'escape.md')), false)
  assert.equal(res.statusCode, 400, res.body)
})

test('a request with no paths field still saves by filename', async () => {
  const res = await post('plain', file('c.md', 'c'))
  assert.equal(res.statusCode, 200, res.body)
  assert.deepEqual(res.json().saved, ['plain/c.md'])
})
