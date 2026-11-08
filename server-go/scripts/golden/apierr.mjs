// Golden error bodies for internal/apierr: what Fastify sends through
// routes.ts's sanitizedErrorHandler for a KbError with each message in
// internal/apierr/testdata/messages.json, a validation error, and an
// internal error (its reference masked). Usage:
// node apierr.mjs <messages.json> <repo root>
import fs from 'node:fs'
import path from 'node:path'

const [, , msgArg, repoArg] = process.argv
const root = path.resolve(repoArg)
const messages = JSON.parse(fs.readFileSync(msgArg, 'utf8'))
const { default: Fastify } = await import(path.join(root, 'server/node_modules/fastify/fastify.js'))
const { sanitizedErrorHandler } = await import(path.join(root, 'server/dist/routes.js'))
const { KbError } = await import(path.join(root, 'server/dist/kb.js'))

const app = Fastify({ logger: false })
app.setErrorHandler(sanitizedErrorHandler(app))
app.get('/kb/:i', async req => { const i = Number(req.params.i); throw new KbError(400 + i, messages[i]) })
app.post('/valid', { schema: { body: { type: 'object', required: ['x'] } } }, async () => ({}))
app.get('/internal', async () => { throw new Error('secret detail') })
await app.ready()

const shape = (r, mask) => {
  let body = r.rawPayload.toString('latin1')
  if (mask) body = body.replace(/\(ref [0-9a-z]+\)/, '(ref <ref>)')
  return { status: r.statusCode, contentType: r.headers['content-type'], body: Buffer.from(body, 'latin1').toString('base64') }
}
const out = { kb: [] }
for (let i = 0; i < messages.length; i++) out.kb.push(shape(await app.inject({ url: `/kb/${i}` })))
const v = await app.inject({ method: 'POST', url: '/valid', payload: {} })
out.validation = { message: v.json().error, ...shape(v) }
out.internal = shape(await app.inject({ url: '/internal' }), true)
process.stdout.write(JSON.stringify(out, null, 1) + '\n')
