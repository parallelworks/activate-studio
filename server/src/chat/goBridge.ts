import crypto from 'node:crypto'
import type { FastifyInstance } from 'fastify'
import { attachmentContext } from '../attachments.js'
import { agentPrompt } from '../extensions.js'
import { platformAvailable } from '../workflowsTab.js'
import { cliCredential, credentialContextName, ensureFreshAccess } from '../access.js'
import { GATEWAY_BASE, PW_API_KEY } from '../config.js'
import { answerHeld, changesAvailable, heldCallsIn, holdCall, newHold, viewerOf } from './approvals.js'
import { systemPrompt } from './context.js'
import { approvalPart, asksFirst, isPlatformTool, platformTool, prepareArgs, viewerAccount } from './platformTools.js'
import { MORE_TOOLS, PLATFORM_GROUPS, TOOL_GROUPS, activeToolSpecs, activeToolSpecsWithRemote, executeTool, expandSlashCommand, initialToolGroups, needsPlatform, readerAllows, selectToolSpecs, toolGroupOf, toolOptionLabel } from './tools.js'
import type { WireMessage } from './gateway.js'
import { isEarlierToolTraffic } from './routes.js'

/**
 * TRANSITIONAL: the chat tools for the Go server in front. The Go server
 * runs POST /api/chat/stream (the agent loop, the gateway calls, the SSE
 * events, the conversation record); the tools a reply calls, the system
 * prompt they shape, and the calls held for approval still live here, in
 * tools.ts, context.ts and approvals.ts, until they are ported. Each op is
 * one of the calls the Node route made in-process, with the same inputs.
 * It goes away with the tools' port; nothing else belongs here.
 *
 * Only a request carrying STUDIO_BRIDGE_SECRET is answered, and the Go
 * server refuses /internal/go/ from clients.
 */

const HEADER = 'x-studio-bridge-secret'

function allowed(given: unknown): boolean {
  const secret = process.env.STUDIO_BRIDGE_SECRET
  if (!secret || typeof given !== 'string') return false
  const a = Buffer.from(given), b = Buffer.from(secret)
  return a.length === b.length && crypto.timingSafeEqual(a, b)
}

type Body = Record<string, any>

async function op(name: string, b: Body, req: any): Promise<unknown> {
  switch (name) {
    // Everything the route worked out before its first model turn.
    case 'begin': {
      const lastUserIdx = Number(b.lastUserIdx ?? -1)
      const used: WireMessage[] = b.used ?? []
      let listing: string | null = null
      const history: WireMessage[] = []
      for (let i = 0; i < used.length; i++) {
        const m = used[i] as WireMessage & { attachment_ids?: string[] }
        if (m.role === 'system') continue
        if (isEarlierToolTraffic(m)) continue
        let content = typeof m.content === 'string' ? m.content : m.content == null ? '' : JSON.stringify(m.content)
        if (i === lastUserIdx && content.trim().startsWith('/')) {
          const expanded = expandSlashCommand(content)
          if (expanded && typeof expanded === 'object') listing = expanded.listing
          else if (expanded) content = expanded
        }
        const ids = m.attachment_ids
        if (ids?.length) {
          const ctx = await attachmentContext(ids).catch(() => '')
          if (ctx) content = content ? `${content}\n\n${ctx}` : ctx
        }
        history.push({ role: m.role, content })
      }
      const viewer = viewerOf(req)
      const changes = changesAvailable({ viewer, key: b.changeKey ?? null, owner: b.owner ?? null, username: req.user?.username ?? null })
      const platform = platformAvailable(req.user?.id)
      const readerTools: Set<string> | null = Array.isArray(b.tools) ? new Set(b.tools.map(String)) : null
      const groups = initialToolGroups(String(b.recentUserText ?? ''), b.usedTools ?? [])
      if (readerTools) for (const g of [...groups]) if (!readerTools.has(g)) groups.delete(g)
      if (!platform) for (const g of PLATFORM_GROUPS) groups.delete(g)
      return {
        history, listing, viewer, changes, platform, groups: [...groups],
        persona: agentPrompt(b.agent) || null,
        system: await systemPrompt({ platform: groups.has('workflows') || groups.has('agents'), connected: platform }),
        meta: meta(),
      }
    }
    // The platform values the loop reads from the pw CLI's context, which
    // Node still owns (access.ts): the gateway, the CLI credential, and
    // whether a platform answers for this viewer.
    case 'access': {
      if (b.fresh) await ensureFreshAccess(Number(b.fresh) || undefined)
      return { gatewayBase: GATEWAY_BASE, deploymentKey: PW_API_KEY || cliCredential(), contextName: credentialContextName(), platform: platformAvailable(req.user?.id) }
    }
    case 'system':
      return { prompt: await systemPrompt({ platform: !!b.platform, connected: !!b.connected }) }
    // The tools offered this round, and the ones sent.
    case 'specs': {
      const readerTools: Set<string> | null = Array.isArray(b.tools) ? new Set(b.tools.map(String)) : null
      const platform = !!b.platform
      const changesOk = !!b.changesOk
      const offered = (n: string) => (platform || !needsPlatform(n)) && (!isPlatformTool(n) || (asksFirst(n) ? changesOk : platform))
      const available = (await activeToolSpecsWithRemote()).filter(t => readerAllows(t.function.name, readerTools) && offered(t.function.name))
      return { available, selected: selectToolSpecs(available, new Set((b.groups ?? []).map(String))) }
    }
    // What the round's calls are, by name: whether the reader's switches
    // allow each, the switch's label, and the tool's group.
    case 'names': {
      const readerTools: Set<string> | null = Array.isArray(b.tools) ? new Set(b.tools.map(String)) : null
      const names: string[] = (b.names ?? []).map(String)
      return {
        allowed: Object.fromEntries(names.map(n => [n, readerAllows(n, readerTools)])),
        labels: Object.fromEntries(names.map(n => [n, toolOptionLabel(n)])),
        groupOf: Object.fromEntries(names.map(n => [n, toolGroupOf(n)])),
        asksFirst: Object.fromEntries(names.map(n => [n, asksFirst(n)])),
      }
    }
    case 'execute':
      return executeTool(String(b.name), String(b.args ?? ''), b.ctx ?? {})
    case 'prepare': {
      const name = String(b.name)
      const needsAccount = !!platformTool(name)?.defaults?.length
      const account = needsAccount ? await viewerAccount(String(b.changeKey ?? '')) : null
      const prepared = prepareArgs(name, b.args, account?.username ?? null)
      if ('problem' in prepared) {
        return { problem: needsAccount && !account ? `Your platform account could not be read with your key, so ${name} could not be prepared. Nothing ran.` : `${prepared.problem} Nothing ran.` }
      }
      return prepared
    }
    case 'hold': {
      const viewer = viewerOf(req)
      if (!viewer) return { error: 'no viewer' }
      const rec = newHold({
        conversationId: String(b.conversationId), messageId: String(b.messageId), toolCallId: String(b.toolCallId), tool: String(b.tool), args: String(b.canonical),
        viewer: viewer.id, viewerName: viewer.name, turn: b.turn ?? [], groups: b.groups ?? [],
      })
      const approval = approvalPart(String(b.tool), b.args ?? {}, String(b.toolCallId), rec.expiresAt)
      holdCall(rec)
      return { approval }
    }
    case 'answer': {
      const viewer = viewerOf(req)
      const answered = await answerHeld(b.answer, viewer, b.changeKey ?? null, tool => activeToolSpecs().some(t => t.function.name === tool))
      return answered
    }
    case 'held':
      return { held: heldCallsIn(String(b.conversationId ?? '')).map(r => ({ toolCallId: r.toolCallId, viewer: r.viewer, viewerName: r.viewerName })) }
  }
  throw new Error(`unknown op ${name}`)
}

/** What the loop needs to know about tools by name. */
function meta() {
  const all = activeToolSpecs().map(t => t.function.name)
  const names = [...new Set([...all, ...Object.values(TOOL_GROUPS).flatMap(d => d.names)])]
  return {
    moreTools: MORE_TOOLS,
    platformGroups: PLATFORM_GROUPS,
    groupOrder: Object.keys(TOOL_GROUPS),
    groups: Object.fromEntries(Object.entries(TOOL_GROUPS).map(([g, d]) => [g, d.names])),
    groupOf: Object.fromEntries(names.map(n => [n, toolGroupOf(n)]).filter(([, g]) => g)),
    asksFirst: names.filter(n => asksFirst(n)),
    labels: Object.fromEntries(names.map(n => [n, toolOptionLabel(n)])),
  }
}

export async function goChatBridgeRoutes(app: FastifyInstance): Promise<void> {
  app.post('/internal/go/chat/:op', async (req, reply) => {
    if (!allowed(req.headers[HEADER])) return reply.status(404).send({ error: 'not found' })
    try {
      return await op(String((req.params as { op: string }).op), (req.body ?? {}) as Body, req)
    } catch (e) {
      return reply.status(500).send({ error: String((e as Error).message ?? e) })
    }
  })
}
