import Fastify from 'fastify'
import { setRunsLog, watchRegisteredRuns } from './runs.js'
import { streamsInFlight } from './chat/routes.js'
import fastifyStatic from '@fastify/static'
import fastifyMultipart from '@fastify/multipart'
import fs from 'node:fs'
import path from 'node:path'
import { HOST, PORT, PROJECT_ROOT, KB_ROOT, gufiAvailable } from './config.js'
import { kbRoutes, sanitizedErrorHandler } from './routes.js'
import { uploadRoutes } from './uploads.js'
import { queryRoutes } from './query.js'
import { topologyRoutes } from './topology.js'
import { historyRoutes } from './history.js'
import { tagRoutes } from './tags.js'
import { modelRoutes } from './model.js'
import { attachmentRoutes } from './attachments.js'
import { extensionRoutes, seedExtensions } from './extensions.js'
import { conversationRoutes } from './conversations.js'
import { authHook, authEnabled } from './auth.js'
import { hydrateFromCookie, scrubPersonalVaultEntries } from './credentials.js'
import { settingsRoutes } from './settings.js'
import { chatRoutes } from './chat/routes.js'
import { mcpRoutes } from './mcp.js'
import { taskRoutes } from './tasks.js'
import { ragProxyRoutes } from './ragProxy.js'
import { maybeAutoStart, ragEndpointRoutes } from './ragEndpoint.js'
import { gatewayConfigured } from './chat/gateway.js'
import { startSweepTimer } from './indexing.js'
import { seedKnowledgeBase } from './seed.js'

const app = Fastify({ logger: { level: 'info' } })
await app.register(fastifyMultipart)
await authHook(app)
// Browser-held personal keys: a request from a verified user whose memory
// entry has expired rehydrates it from the sealed key cookie.
app.addHook('onRequest', async req => {
  if (req.user?.id) hydrateFromCookie(req.user.id, req.headers.cookie)
})
app.log.info(`personal key vault scrub: ${scrubPersonalVaultEntries()} legacy entries removed`)

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

app.setErrorHandler(sanitizedErrorHandler(app))
await app.register(kbRoutes)
await app.register(ragProxyRoutes)
await app.register(ragEndpointRoutes)
await app.register(uploadRoutes)
await app.register(queryRoutes)
await app.register(topologyRoutes)
await app.register(historyRoutes)
await app.register(tagRoutes)
await app.register(modelRoutes)
await app.register(attachmentRoutes)
seedExtensions(m => app.log.info(m))
await app.register(extensionRoutes)
await app.register(conversationRoutes)
await app.register(settingsRoutes)
await app.register(chatRoutes)
await app.register(mcpRoutes)
await app.register(taskRoutes)

// A brand-new deployment opens on a corpus with some shape rather than an
// empty tree; only ever runs when the knowledge base has nothing in it.
await seedKnowledgeBase(msg => app.log.info(msg))
startSweepTimer(msg => app.log.info(msg))
// Deploy-time full reindexes rebuild the index without overlay labels
// (xattr-less filesystems); restore them once the server is up.
void import('./extensions.js').then(m => m.labelPersonas())
  .then(n => { if (n) app.log.info(`personas: labeled ${n} definitions`) })
  .catch(() => {})
void import('./tags.js').then(m => m.reapplyTagOverlay())
  .then(n => { if (n) app.log.info(`tag overlay: re-applied ${n} labeled paths`) })
  .catch(() => {})

await app.listen({ host: HOST, port: PORT })

// Runs the assistant launched before the last restart are followed again,
// so their end still reaches the conversations that asked for them.
setRunsLog(msg => app.log.info(msg))
{
  const n = watchRegisteredRuns()
  if (n) app.log.info(`following ${n} workflow run(s) registered before this start`)
}

// A rollout stops this process with SIGTERM. Streams that are mid-answer
// finish their server-side work (a tool loop, a run registration) for up
// to twenty seconds before the process goes; the tunnel in front of them
// is replaced by the deploy regardless, so this is about not losing what
// the server was in the middle of recording, not about keeping clients.
let stopping = false
const stop = async (sig: string) => {
  if (stopping) return
  stopping = true
  const t0 = Date.now()
  app.log.info(`${sig}: draining ${streamsInFlight()} in-flight stream(s)`)
  while (streamsInFlight() > 0 && Date.now() - t0 < 20_000) await new Promise(r => setTimeout(r, 250))
  await app.close().catch(() => {})
  process.exit(0)
}
process.on('SIGTERM', () => { void stop('SIGTERM') })
process.on('SIGINT', () => { void stop('SIGINT') })

/**
 * Stay up. The Studio runs under the platform endpoint agent, which owns
 * the process tree: when this process exits the endpoint is removed, the
 * scheduler job ends, and any model serving beside it on the same node
 * dies too. That makes the blast radius of one unhandled error the whole
 * session, and it has already cost one: a spawn of a CLI that was not in
 * the container emitted an unhandled 'error' event and took everything
 * with it.
 *
 * Keeping a process alive after an uncaught exception is normally poor
 * practice, because the state that threw may be inconsistent. Here the
 * alternative is worse by a wide margin: a failed request should cost the
 * request, not the deployment and the GPU allocation behind it. Both
 * handlers are installed after listen, so a genuine startup failure still
 * exits rather than being swallowed.
 */
process.on('unhandledRejection', reason => {
  app.log.error({ err: reason }, 'unhandled rejection; the deployment stays up')
})
process.on('uncaughtException', err => {
  app.log.error({ err }, 'uncaught exception; the deployment stays up')
})

maybeAutoStart(msg => app.log.info(msg))
app.log.info(`kb root: ${KB_ROOT}`)
app.log.info(`gufi index: ${gufiAvailable() ? 'available' : 'NOT BUILT (run indexer/reindex.sh)'}`)
app.log.info(`gateway: ${gatewayConfigured() ? 'configured' : 'PW_API_KEY NOT SET; chat disabled'}`)
app.log.info(`auth: ${authEnabled() ? 'JWT verification enabled' : 'open (no AUTH_JWKS_URL)'}`)
