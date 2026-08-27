import type { FastifyInstance } from 'fastify'
import { TOOL_SPECS, executeTool } from './chat/tools.js'
import { effectiveSettings } from './settings.js'

/**
 * The knowledge base as an MCP server, at POST /api/mcp.
 *
 * The RAG proxy publishes this corpus as a model: callers get our serving
 * loop, our prompt, and whatever model this deployment runs. MCP inverts
 * that. A client (pw code, Claude Code, any MCP-capable tool) brings its
 * own model and calls the corpus tools directly, so the reasoning happens
 * wherever the user already works and this server only answers questions
 * about the corpus. The tool layer is the chat assistant's own specs and
 * executor; this file is protocol, not capability.
 *
 * Streamable-HTTP MCP, stateless: each POST carries one JSON-RPC message
 * and gets a JSON reply, which is the shape `claude mcp add --transport
 * http` expects. Living under /api/ puts it behind the same bearer auth
 * as every other route when auth is enabled.
 *
 * Only corpus-reading tools are exposed. Writes (files, labels) and
 * platform actions (running workflows) stay with the chat assistant,
 * where the deployment's own settings and audit trail govern them; a
 * remote client with a corpus question does not need them, and the
 * exposure would otherwise be governed by the loosest client config
 * anywhere rather than by this deployment.
 */
const EXPOSED = new Set([
  'search_kb', 'read_kb_file', 'list_kb_dir', 'query_corpus', 'get_labels', 'studio_docs',
])

const PROTOCOL_VERSION = '2025-06-18'

interface RpcMessage { jsonrpc?: string; id?: number | string | null; method?: string; params?: Record<string, unknown> }

function rpcResult(id: RpcMessage['id'], result: unknown): unknown {
  return { jsonrpc: '2.0', id: id ?? null, result }
}

function rpcError(id: RpcMessage['id'], code: number, message: string): unknown {
  return { jsonrpc: '2.0', id: id ?? null, error: { code, message } }
}

function exposedTools(): { name: string; description: string; inputSchema: Record<string, unknown> }[] {
  const disabled = new Set(effectiveSettings().disabledTools ?? [])
  return TOOL_SPECS
    .filter(t => EXPOSED.has(t.function.name) && !disabled.has(t.function.name))
    .map(t => ({
      name: t.function.name,
      description: t.function.description,
      inputSchema: t.function.parameters,
    }))
}

export async function mcpRoutes(app: FastifyInstance): Promise<void> {
  app.post('/api/mcp', async (req, reply) => {
    if (!effectiveSettings().mcpEnabled) {
      return reply.status(403).send({ error: 'MCP access is disabled for this deployment (Settings, MCP access)' })
    }
    const msg = (req.body ?? {}) as RpcMessage

    // Notifications carry no id and expect no body.
    if (msg.method?.startsWith('notifications/')) return reply.status(202).send()

    switch (msg.method) {
      case 'initialize':
        return rpcResult(msg.id, {
          protocolVersion: typeof msg.params?.protocolVersion === 'string' ? msg.params.protocolVersion : PROTOCOL_VERSION,
          capabilities: { tools: {} },
          serverInfo: { name: 'activate-studio', version: '0.1.0' },
        })
      case 'ping':
        return rpcResult(msg.id, {})
      case 'tools/list':
        return rpcResult(msg.id, { tools: exposedTools() })
      case 'tools/call': {
        const name = String((msg.params as { name?: string })?.name ?? '')
        if (!EXPOSED.has(name)) return rpcError(msg.id, -32602, `unknown tool: ${name}`)
        const args = (msg.params as { arguments?: unknown })?.arguments ?? {}
        try {
          const out = await executeTool(name, JSON.stringify(args))
          return rpcResult(msg.id, { content: [{ type: 'text', text: out.result }], isError: false })
        } catch (e) {
          // Tool failures are results, not protocol errors: the calling
          // model is meant to read them and adjust.
          return rpcResult(msg.id, { content: [{ type: 'text', text: String((e as Error).message ?? e) }], isError: true })
        }
      }
      default:
        return rpcError(msg.id, -32601, `method not supported: ${msg.method ?? '(none)'}`)
    }
  })

  // GET is the SSE half of the streamable transport; this server has no
  // server-initiated messages, and saying so is the spec's answer.
  app.get('/api/mcp', async (_req, reply) => reply.status(405).send({ error: 'POST JSON-RPC messages to this endpoint' }))
}
