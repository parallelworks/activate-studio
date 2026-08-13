import type { FastifyInstance } from 'fastify'
import { MAX_TOOL_ITERATIONS } from '../config.js'
import { gatewayConfigured, listModels, streamTurn, WireMessage, WireToolCall } from './gateway.js'
import { TOOL_SPECS, executeTool } from './tools.js'
import { systemPrompt } from './context.js'

interface StreamBody {
  model: string
  messages: WireMessage[]
  conversationId?: string
  userMessageId?: string
  allocation?: string | null
}

function sse(res: NodeJS.WritableStream, event: string, data: unknown): void {
  res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
}

export async function chatRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/chat/models', async (_req, reply) => {
    if (!gatewayConfigured()) {
      return reply.send({ models: [], unreachableSessions: [], error: 'no gateway credential: set PW_API_KEY or authenticate the pw CLI' })
    }
    const wire: any = await listModels()
    let models = (wire.data ?? wire.models ?? []).filter((m: any) => m)
    // Org-provider models require an X-Allocation header; without a configured
    // allocation they can only fail, so hide them. Personal providers sort first
    // so the default selection is a model that works.
    if (!process.env.PW_ALLOCATION) {
      models = models.filter((m: any) => !String(m.id).startsWith('org:'))
    }
    models.sort((a: any, b: any) =>
      Number(String(a.id).startsWith('org:')) - Number(String(b.id).startsWith('org:')))
    return reply.send({ models, unreachableSessions: wire.unreachable_sessions ?? [] })
  })

  app.post('/api/chat/stream', async (req, reply) => {
    const body = req.body as StreamBody
    reply.hijack()
    reply.raw.writeHead(200, {
      'Content-Type': 'text/event-stream',
      'Cache-Control': 'no-cache',
      Connection: 'keep-alive',
      'X-Accel-Buffering': 'no',
    })
    const res = reply.raw
    const abort = new AbortController()
    // Response 'close' fires on client disconnect (request 'close' fires as
    // soon as the body is consumed, which would abort immediately).
    res.on('close', () => { if (!res.writableEnded) abort.abort() })

    if (!gatewayConfigured()) {
      sse(res, 'error', { message: 'No gateway credential (set PW_API_KEY or authenticate the pw CLI); chat is unavailable.' })
      res.end()
      return reply
    }

    // Client history arrives in wire shape; keep only fields the gateway needs.
    const history: WireMessage[] = (body.messages ?? []).map(m => ({
      role: m.role,
      content: typeof m.content === 'string' ? m.content : m.content == null ? '' : JSON.stringify(m.content),
    })).filter(m => m.role !== 'system')

    const messages: WireMessage[] = [
      { role: 'system', content: await systemPrompt() },
      ...history,
    ]

    let finalContent = ''
    let finalModel: string | null = null
    try {
      for (let iter = 0; iter < MAX_TOOL_ITERATIONS; iter++) {
        const allocation = body.allocation ?? process.env.PW_ALLOCATION ?? null
        const turn = await streamTurn(
          { model: body.model, messages, tools: TOOL_SPECS, tool_choice: 'auto' },
          allocation,
          {
            onContent: t => { finalContent += t; sse(res, 'content', { text: t }) },
            onReasoning: t => sse(res, 'reasoning', { text: t }),
          },
          abort.signal,
        )
        finalModel = turn.model ?? finalModel
        if (turn.finishReason === 'tool_calls' && turn.toolCalls.length > 0) {
          messages.push({ role: 'assistant', content: turn.content || null, tool_calls: turn.toolCalls })
          for (const tc of turn.toolCalls as WireToolCall[]) {
            sse(res, 'tool', { phase: 'call', name: tc.function.name, args: tc.function.arguments })
            const { result, summary } = await executeTool(tc.function.name, tc.function.arguments)
            sse(res, 'tool', { phase: 'result', name: tc.function.name, summary })
            messages.push({ role: 'tool', tool_call_id: tc.id ?? `${tc.function.name}-${iter}`, content: result })
          }
          continue
        }
        sse(res, 'done', {
          content: finalContent,
          finishReason: turn.finishReason ?? 'stop',
          model: finalModel,
          messageId: `srv-${Date.now()}`,
        })
        res.end()
        return reply
      }
      sse(res, 'error', { message: `Stopped after ${MAX_TOOL_ITERATIONS} tool iterations.` })
    } catch (err: any) {
      app.log.error({ err }, 'chat stream failed')
      if (!abort.signal.aborted) sse(res, 'error', { message: String(err?.message ?? err) })
    }
    res.end()
    return reply
  })
}
