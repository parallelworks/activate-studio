import Fastify from 'fastify'
import fastifyStatic from '@fastify/static'
import fastifyMultipart from '@fastify/multipart'
import fs from 'node:fs'
import path from 'node:path'
import { HOST, PORT, PROJECT_ROOT, KB_ROOT, gufiAvailable } from './config.js'
import { kbRoutes } from './routes.js'
import { uploadRoutes } from './uploads.js'
import { queryRoutes } from './query.js'
import { tagRoutes } from './tags.js'
import { modelRoutes } from './model.js'
import { attachmentRoutes } from './attachments.js'
import { extensionRoutes, seedExtensions } from './extensions.js'
import { conversationRoutes } from './conversations.js'
import { authHook, authEnabled } from './auth.js'
import { settingsRoutes } from './settings.js'
import { chatRoutes } from './chat/routes.js'
import { ragProxyRoutes } from './ragProxy.js'
import { maybeAutoStart, ragEndpointRoutes } from './ragEndpoint.js'
import { gatewayConfigured } from './chat/gateway.js'
import { startSweepTimer } from './indexing.js'
import { seedKnowledgeBase } from './seed.js'

const app = Fastify({ logger: { level: 'info' } })
await app.register(fastifyMultipart)
await authHook(app)

const webDist = path.join(PROJECT_ROOT, 'web', 'dist')
if (fs.existsSync(webDist)) {
  // wildcard:true serves from disk per request, so a web rebuild with new
  // hashed asset names does not require a server restart.
  await app.register(fastifyStatic, { root: webDist, wildcard: true })
  app.setNotFoundHandler((req, reply) => {
    if (req.url.startsWith('/api/')) return reply.status(404).send({ error: 'not found' })
    return reply.sendFile('index.html')
  })
}

await app.register(kbRoutes)
await app.register(ragProxyRoutes)
await app.register(ragEndpointRoutes)
await app.register(uploadRoutes)
await app.register(queryRoutes)
await app.register(tagRoutes)
await app.register(modelRoutes)
await app.register(attachmentRoutes)
seedExtensions(m => app.log.info(m))
await app.register(extensionRoutes)
await app.register(conversationRoutes)
await app.register(settingsRoutes)
await app.register(chatRoutes)

// A brand-new deployment opens on a corpus with some shape rather than an
// empty tree; only ever runs when the knowledge base has nothing in it.
await seedKnowledgeBase(msg => app.log.info(msg))
startSweepTimer(msg => app.log.info(msg))
// Deploy-time full reindexes rebuild the index without overlay labels
// (xattr-less filesystems); restore them once the server is up.
void import('./tags.js').then(m => m.reapplyTagOverlay())
  .then(n => { if (n) app.log.info(`tag overlay: re-applied ${n} labeled paths`) })
  .catch(() => {})

await app.listen({ host: HOST, port: PORT })
maybeAutoStart(msg => app.log.info(msg))
app.log.info(`kb root: ${KB_ROOT}`)
app.log.info(`gufi index: ${gufiAvailable() ? 'available' : 'NOT BUILT (run indexer/reindex.sh)'}`)
app.log.info(`gateway: ${gatewayConfigured() ? 'configured' : 'PW_API_KEY NOT SET; chat disabled'}`)
app.log.info(`auth: ${authEnabled() ? 'JWT verification enabled' : 'open (no AUTH_JWKS_URL)'}`)
