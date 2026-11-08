// The chat routes of server/dist, for internal/chat's golden tests.
// node chat.mjs <repo> serve: a Fastify app with the chat routes, the
//   conversation routes and the chat bridge (server/src/chat/goBridge.ts);
//   prints its port. The environment names the gateway (a fake the test
//   runs), INDEX_BASE, KB_ROOT and STUDIO_BRIDGE_SECRET.
// node chat.mjs <repo> citations < cases.json: each case's pieces pushed
//   through a CitationStream and flushed, the outputs in order.
import path from 'node:path'
import fs from 'node:fs'
import { createRequire } from 'node:module'
import { pathToFileURL } from 'node:url'
const [, , repo, op] = process.argv
const dist = p => path.join(path.resolve(repo), 'server/dist', p)

if (op === 'citations') {
  const { CitationStream } = await import(dist('chat/citations.js'))
  const cases = JSON.parse(fs.readFileSync(0, 'utf8'))
  const out = cases.map(c => {
    const s = new CitationStream({ known: new Map(Object.entries(c.known ?? {}).map(([k, v]) => [k, v || null])), library: c.library || null })
    return [...c.pieces.map(p => s.push(p)), s.flush()]
  })
  console.log(JSON.stringify(out))
} else if (op === 'serve') {
  const req = createRequire(path.join(path.resolve(repo), 'server/package.json'))
  const { default: Fastify } = await import(pathToFileURL(req.resolve('fastify')).href)
  const { chatRoutes } = await import(dist('chat/routes.js'))
  const { conversationRoutes } = await import(dist('conversations.js'))
  const { goChatBridgeRoutes } = await import(dist('chat/goBridge.js'))
  const { sanitizedErrorHandler } = await import(dist('routes.js'))
  const app = Fastify()
  app.setErrorHandler(sanitizedErrorHandler(app))
  await app.register(chatRoutes)
  await app.register(conversationRoutes)
  await app.register(goChatBridgeRoutes)
  await app.listen({ host: '127.0.0.1', port: 0 })
  console.log(app.server.address().port)
  // The test closes stdin when it is done.
  process.stdin.on('end', () => process.exit(0))
  process.stdin.resume()
}
