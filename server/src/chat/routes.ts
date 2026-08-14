import type { FastifyInstance } from 'fastify'
import { MAX_TOOL_ITERATIONS } from '../config.js'
import { gatewayConfigured, listModels, streamTurn, WireMessage, WireToolCall } from './gateway.js'
import { TOOL_SPECS, activeToolSpecs, customToolSpecs, executeTool, skillToolSpec } from './tools.js'
import { systemPrompt } from './context.js'
import { attachmentContext } from '../attachments.js'
import { effectiveSettings } from '../settings.js'
import { agentPrompt } from '../extensions.js'

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

  // Tool catalog for the Settings page: built-ins plus custom, with state.
  app.get('/api/chat/tools', async () => {
    const eff = effectiveSettings()
    const disabled = new Set(eff.disabledTools)
    return {
      tools: [
        ...TOOL_SPECS.map(t => ({
          name: t.function.name, description: t.function.description, builtin: true,
          enabled: !disabled.has(t.function.name), parameters: t.function.parameters,
        })),
        ...customToolSpecs().map(t => ({
          name: t.function.name, description: t.function.description, builtin: false,
          enabled: !disabled.has(t.function.name), parameters: t.function.parameters,
        })),
        ...(skillToolSpec() ? [{
          name: 'use_skill', description: skillToolSpec()!.function.description, builtin: true,
          enabled: !disabled.has('use_skill'), parameters: skillToolSpec()!.function.parameters,
        }] : []),
      ],
    }
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

    // Client history arrives in wire shape; keep only fields the gateway
    // needs, expanding attachment references into their extracted text.
    const history: WireMessage[] = []
    for (const m of body.messages ?? []) {
      if (m.role === 'system') continue
      let content = typeof m.content === 'string' ? m.content : m.content == null ? '' : JSON.stringify(m.content)
      const ids = (m as { attachment_ids?: string[] }).attachment_ids
      if (ids?.length) {
        const ctx = await attachmentContext(ids).catch(() => '')
        if (ctx) content = content ? `${content}

${ctx}` : ctx
      }
      history.push({ role: m.role, content })
    }

    const today = new Date().toISOString().slice(0, 10)
    // The persona file is read per request so edits apply immediately.
    const persona = agentPrompt()
    const messages: WireMessage[] = [
      { role: 'system', content: `${await systemPrompt()}\n\nToday's date is ${today}. Treat earlier dates as past; call out deadlines that have already passed.${persona ? `\n\n--- DEPLOYMENT AGENT INSTRUCTIONS (extensions/agents/default.md) ---\n${persona}` : ''}` },
      ...history,
    ]

    let finalContent = ''
    let finalModel: string | null = null
    const allocation = body.allocation ?? process.env.PW_ALLOCATION ?? null
    const callbacks = {
      onContent: (t: string) => { finalContent += t; sse(res, 'content', { text: t }) },
      onReasoning: (t: string) => sse(res, 'reasoning', { text: t }),
    }
    const finish = (finishReason: string | null) => {
      sse(res, 'done', {
        content: finalContent,
        finishReason: finishReason ?? 'stop',
        model: finalModel,
        messageId: `srv-${Date.now()}`,
      })
      res.end()
    }
    try {
      // Identical repeated calls get the cached result with a nudge, so a
      // model stuck re-issuing one call burns no time and gets told to move on.
      const toolCache = new Map<string, string>()
      for (let iter = 0; iter < MAX_TOOL_ITERATIONS; iter++) {
        const turn = await streamTurn(
          { model: body.model, messages, tools: activeToolSpecs(), tool_choice: 'auto' },
          allocation,
          callbacks,
          abort.signal,
        )
        finalModel = turn.model ?? finalModel
        if (turn.finishReason === 'tool_calls' && turn.toolCalls.length > 0) {
          messages.push({ role: 'assistant', content: turn.content || null, tool_calls: turn.toolCalls })
          for (const tc of turn.toolCalls as WireToolCall[]) {
            const key = `${tc.function.name}:${tc.function.arguments}`
            req.log.info({ tool: tc.function.name, args: tc.function.arguments.slice(0, 300), iter }, 'tool call')
            sse(res, 'tool', { phase: 'call', name: tc.function.name, args: tc.function.arguments })
            let result: string
            let summary: string
            if (toolCache.has(key)) {
              result = `[You already made this exact call in this reply; identical result repeated below. Do not repeat it again.]\n${toolCache.get(key)}`
              summary = 'repeated call (cached)'
            } else {
              const out = await executeTool(tc.function.name, tc.function.arguments)
              result = out.result
              summary = out.summary
              toolCache.set(key, result)
            }
            sse(res, 'tool', { phase: 'result', name: tc.function.name, summary })
            messages.push({ role: 'tool', tool_call_id: tc.id ?? `${tc.function.name}-${iter}`, content: result })
          }
          continue
        }
        finish(turn.finishReason)
        return reply
      }
      // Tool budget exhausted: one final turn with tools disabled so the user
      // gets an answer built from what was gathered instead of an error.
      req.log.warn({ iterations: MAX_TOOL_ITERATIONS }, 'tool budget exhausted; forcing final answer')
      messages.push({
        role: 'system',
        content: 'Tool budget for this reply is exhausted. Answer the user now using only the information gathered above. State plainly anything you could not verify.',
      })
      const finalTurn = await streamTurn(
        { model: body.model, messages },
        allocation,
        callbacks,
        abort.signal,
      )
      finalModel = finalTurn.model ?? finalModel
      finish(finalTurn.finishReason)
      return reply
    } catch (err: any) {
      app.log.error({ err }, 'chat stream failed')
      if (!abort.signal.aborted) sse(res, 'error', { message: String(err?.message ?? err) })
    }
    res.end()
    return reply
  })
}
