import Fastify from 'fastify'
import fastifyStatic from '@fastify/static'
import fs from 'node:fs'
import path from 'node:path'
import { HOST, PORT, PROJECT_ROOT, KB_ROOT, gufiAvailable } from './config.js'
import { kbRoutes } from './routes.js'
import { chatRoutes } from './chat/routes.js'
import { gatewayConfigured } from './chat/gateway.js'

const app = Fastify({ logger: { level: 'info' } })

const webDist = path.join(PROJECT_ROOT, 'web', 'dist')
if (fs.existsSync(webDist)) {
  await app.register(fastifyStatic, { root: webDist, wildcard: false })
  app.setNotFoundHandler((req, reply) => {
    if (req.url.startsWith('/api/')) return reply.status(404).send({ error: 'not found' })
    return reply.sendFile('index.html')
  })
}

await app.register(kbRoutes)
await app.register(chatRoutes)

await app.listen({ host: HOST, port: PORT })
app.log.info(`kb root: ${KB_ROOT}`)
app.log.info(`gufi index: ${gufiAvailable() ? 'available' : 'NOT BUILT (run indexer/reindex.sh)'}`)
app.log.info(`gateway: ${gatewayConfigured() ? 'configured' : 'PW_API_KEY NOT SET; chat disabled'}`)
