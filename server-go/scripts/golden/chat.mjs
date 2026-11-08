// The chat routes of server/dist, for internal/chat's golden tests.
// node chat.mjs <repo> serve: a Fastify app with the chat routes, the
//   conversation routes, the RAG proxy (/v1), /api/mcp and the chat bridge
//   (server/src/chat/goBridge.ts); prints its port. The environment names the gateway (a fake the test
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
} else if (op === 'catalog') {
  // The built-in tool catalog as tools.ts and platformTools.ts declare it:
  // the source of internal/chat/toolcatalog.json, and what the Go
  // catalog is checked against.
  const { TOOL_SPECS, TOOL_CALLS, TOOL_GROUPS } = await import(dist('chat/tools.js'))
  const { PLATFORM_TOOLS, toolHash } = await import(dist('chat/platformTools.js'))
  console.log(JSON.stringify({
    specs: TOOL_SPECS.map(t => t.function),
    calls: TOOL_CALLS,
    groups: Object.fromEntries(Object.entries(TOOL_GROUPS).map(([g, d]) => [g, { summary: d.summary, names: d.names, cue: d.cue.source }])),
    platform: PLATFORM_TOOLS,
    hashes: Object.fromEntries(PLATFORM_TOOLS.map(t => [t.name, toolHash(t.name)])),
  }, null, 1))
} else if (op === 'tools') {
  // What tools.ts, platformTools.ts, context.ts and approvals.ts work out
  // for the cases on stdin, over the INDEX_BASE and KB_ROOT of the
  // environment, and the catalog routes' answers.
  const req = createRequire(path.join(path.resolve(repo), 'server/package.json'))
  const { default: Fastify } = await import(pathToFileURL(req.resolve('fastify')).href)
  const t = await import(dist('chat/tools.js'))
  const p = await import(dist('chat/platformTools.js'))
  const { systemPrompt } = await import(dist('chat/context.js'))
  const { chatRoutes } = await import(dist('chat/routes.js'))
  const app = Fastify()
  await app.register(chatRoutes)
  const get = async url => JSON.parse((await app.inject({ method: 'GET', url })).body)
  const cases = JSON.parse(fs.readFileSync(0, 'utf8'))
  const all = await t.activeToolSpecsWithRemote()
  const out = {
    tools: await get('/api/chat/tools'),
    options: await get('/api/chat/tool-options'),
    active: t.activeToolSpecs().map(s => s.function.name),
    labels: Object.fromEntries(all.map(s => [s.function.name, t.toolOptionLabel(s.function.name)])),
    slash: cases.slash.map(s => t.expandSlashCommand(s)),
    groups: cases.texts.map(s => [...t.initialToolGroups(s, cases.used)].sort()),
    selected: cases.groupSets.map(g => t.selectToolSpecs(all, new Set(g))),
    prepared: cases.prepare.map(c => p.prepareArgs(c.tool, c.args, c.user)),
    approvals: cases.prepare.map(c => {
      const r = p.prepareArgs(c.tool, c.args, c.user)
      return 'args' in r ? p.approvalPart(c.tool, r.args, 'call_1', '2026-01-01T00:00:00.000Z') : null
    }),
    projected: cases.project.map(c => p.projectResult(p.platformTool(c.tool), c.value)),
  }
  out.prompts = []
  for (const [platform, connected] of [[true, true], [false, true], [false, false]]) out.prompts.push(await systemPrompt({ platform, connected }))
  // Written whole before the exit: console.log to a pipe is asynchronous.
  fs.writeSync(1, JSON.stringify(out) + '\n')
  process.exit(0)
} else if (op === 'mcp') {
  // mcpClient.ts over the servers saved in INDEX_BASE's settings: the
  // tools listed, each server's state, and the calls on stdin.
  const c = await import(dist('mcpClient.js'))
  const calls = JSON.parse(fs.readFileSync(0, 'utf8'))
  const out = { specs: await c.remoteToolSpecs(), status: await c.remoteStatus(), remote: calls.map(x => c.isRemoteTool(x.name)), calls: [] }
  for (const x of calls) {
    try { out.calls.push({ ok: await c.callRemoteTool(x.name, x.args) }) } catch (e) { out.calls.push({ error: String(e.message ?? e) }) }
  }
  fs.writeSync(1, JSON.stringify(out) + '\n')
  process.exit(0)
} else if (op === 'serve') {
  const req = createRequire(path.join(path.resolve(repo), 'server/package.json'))
  const { default: Fastify } = await import(pathToFileURL(req.resolve('fastify')).href)
  const { chatRoutes } = await import(dist('chat/routes.js'))
  const { conversationRoutes } = await import(dist('conversations.js'))
  const { goChatBridgeRoutes } = await import(dist('chat/goBridge.js'))
  const { ragProxyRoutes } = await import(dist('ragProxy.js'))
  const { mcpRoutes } = await import(dist('mcp.js'))
  const { sanitizedErrorHandler } = await import(dist('routes.js'))
  const app = Fastify()
  app.setErrorHandler(sanitizedErrorHandler(app))
  await app.register(chatRoutes)
  await app.register(conversationRoutes)
  await app.register(goChatBridgeRoutes)
  await app.register(ragProxyRoutes)
  await app.register(mcpRoutes)
  await app.listen({ host: '127.0.0.1', port: 0 })
  console.log(app.server.address().port)
  // The test closes stdin when it is done.
  process.stdin.on('end', () => process.exit(0))
  process.stdin.resume()
}
