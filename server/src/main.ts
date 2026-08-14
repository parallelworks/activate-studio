import Fastify from 'fastify'
import fastifyStatic from '@fastify/static'
import fastifyMultipart from '@fastify/multipart'
import fs from 'node:fs'
import path from 'node:path'
import { HOST, PORT, PROJECT_ROOT, KB_ROOT, gufiAvailable } from './config.js'
import { kbRoutes } from './routes.js'
import { uploadRoutes } from './uploads.js'
import { queryRoutes } from './query.js'
import { chatRoutes } from './chat/routes.js'
import { gatewayConfigured } from './chat/gateway.js'
import { startSweepTimer } from './indexing.js'

const app = Fastify({ logger: { level: 'info' } })
await app.register(fastifyMultipart)

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
await app.register(uploadRoutes)
await app.register(queryRoutes)
await app.register(chatRoutes)

startSweepTimer(msg => app.log.info(msg))

await app.listen({ host: HOST, port: PORT })
app.log.info(`kb root: ${KB_ROOT}`)
app.log.info(`gufi index: ${gufiAvailable() ? 'available' : 'NOT BUILT (run indexer/reindex.sh)'}`)
app.log.info(`gateway: ${gatewayConfigured() ? 'configured' : 'PW_API_KEY NOT SET; chat disabled'}`)
