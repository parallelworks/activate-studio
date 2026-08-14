import type { FastifyInstance } from 'fastify'
import { MAX_TOOL_ITERATIONS } from '../config.js'
import { aiHealth, gatewayConfigured, listModels, streamTurn, WireMessage, WireToolCall } from './gateway.js'
import { TOOL_CALLS, TOOL_SPECS, activeToolSpecs, commandFor, customToolSpecs, executeTool, expandSlashCommand, skillToolSpec } from './tools.js'
import { systemPrompt } from './context.js'
import { attachmentContext } from '../attachments.js'
import { effectiveSettings } from '../settings.js'
import { agentPrompt } from '../extensions.js'
import { recordAssistantTurn, recordUserTurn } from '../conversations.js'
import { clearUserKey, getUserKeyStatus, resolveUserKey, setUserKey } from '../credentials.js'
import { authEnabled } from '../auth.js'

interface StreamBody {
  model: string
  messages: WireMessage[]
  conversationId?: string
  userMessageId?: string
  parentMessageId?: string | null
  allocation?: string | null
  labelScope?: string[]
}

function sse(res: NodeJS.WritableStream, event: string, data: unknown): void {
  res.write(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
}

/** Human-readable one-liner for a tool call, shown in the thinking stream
 *  (live and persisted). Keeps the raw JSON args out of the reader's face. */
function prettyToolArgs(argsJson: string): string {
  try {
    const a = JSON.parse(argsJson || '{}') as Record<string, unknown>
    const parts = Object.entries(a).slice(0, 3).map(([k, v]) => {
      let s = typeof v === 'string' ? v : JSON.stringify(v)
      if (s.length > 48) s = `${s.slice(0, 45)}…`
      return `${k}: ${typeof v === 'string' ? `"${s}"` : s}`
    })
    return parts.join(' · ')
  } catch {
    return argsJson.slice(0, 60)
  }
}

export async function chatRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/chat/models', async (req, reply) => {
    if (!gatewayConfigured() && !resolveUserKey(req.user?.id)) {
      return reply.send({ models: [], unreachableSessions: [], error: 'no gateway credential: set PW_API_KEY or authenticate the pw CLI' })
    }
    const eff0 = effectiveSettings()
    const personalKey = resolveUserKey(req.user?.id)
    if (eff0.requirePersonalKey && authEnabled() && !personalKey) {
      return reply.send({ models: [], unreachableSessions: [], error: 'This deployment requires your own model credential: add your API key or platform token in Settings, Your model access.' })
    }
    const wire: any = await listModels(personalKey)
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

  // Live model-callability check for the footer status line.
  app.get('/api/ai/health', async req => {
    const personal = resolveUserKey(req.user?.id)
    if (effectiveSettings().requirePersonalKey && authEnabled() && !personal) {
      return {
        ok: false, status: 'key-required', models: 0, gatewayHost: '', message:
          'This deployment requires your own model credential; add it in Settings, Your model access.',
        lastCallFailure: null, checkedAt: new Date().toISOString(),
      }
    }
    return aiHealth(personal)
  })

  // Personal model key: verified identity required; only status metadata
  // (mode, last4, timestamps) ever goes to the browser.
  app.get('/api/me/model-key', async req => {
    // Standalone deployments (no platform identity configured) run single
    // user on the deployment credential; the personal-key surface is an
    // ACTIVATE-session feature and stays hidden there.
    if (!authEnabled()) return { authEnabled: false, verified: false, mode: 'none' }
    if (!req.user) return { authEnabled: true, verified: false, mode: 'none' }
    return { authEnabled: true, verified: true, ...getUserKeyStatus(req.user.id) }
  })

  // POST mirrors of set/clear: some proxies in front of sessions are
  // unfriendly to PUT and DELETE, and the embed saw "failed to fetch".
  app.post('/api/me/model-key', async (req, reply) => {
    if (!req.user) return reply.status(403).send({ error: 'Personal keys need a platform-verified identity; open the app through the platform.' })
    const body = req.body as { key?: string; persist?: boolean }
    const key = String(body.key ?? '').trim()
    if (!key) return reply.status(400).send({ error: 'key required' })
    setUserKey(req.user.id, key, body.persist !== false)
    return { authEnabled: true, verified: true, ...getUserKeyStatus(req.user.id) }
  })

  app.post('/api/me/model-key/clear', async (req, reply) => {
    if (!req.user) return reply.status(403).send({ error: 'not verified' })
    clearUserKey(req.user.id)
    return { authEnabled: true, verified: true, mode: 'none' }
  })

  app.put('/api/me/model-key', async (req, reply) => {
    if (!req.user) return reply.status(403).send({ error: 'Personal keys need a platform-verified identity; open the app through the platform.' })
    const body = req.body as { key?: string; persist?: boolean }
    const key = String(body.key ?? '').trim()
    if (!key) return reply.status(400).send({ error: 'key required' })
    setUserKey(req.user.id, key, body.persist !== false)
    return { verified: true, ...getUserKeyStatus(req.user.id) }
  })

  app.delete('/api/me/model-key', async (req, reply) => {
    if (!req.user) return reply.status(403).send({ error: 'not verified' })
    clearUserKey(req.user.id)
    return { verified: true, mode: 'none' }
  })

  // Test a candidate key (or the stored one) against the gateway without
  // exposing anything about it beyond the health classification.
  app.post('/api/me/model-key/test', async (req, reply) => {
    if (!req.user) return reply.status(403).send({ error: 'not verified' })
    const body = req.body as { key?: string } | null
    const candidate = String(body?.key ?? '').trim() || resolveUserKey(req.user.id)
    if (!candidate) return reply.status(400).send({ error: 'no key to test' })
    return aiHealth(candidate)
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
          implementation: 'built-in (server/src/chat/tools.ts)',
          calls: TOOL_CALLS[t.function.name],
        })),
        ...customToolSpecs().map(t => ({
          name: t.function.name, description: t.function.description, builtin: false,
          enabled: !disabled.has(t.function.name), parameters: t.function.parameters,
          command: commandFor(t.function.name),
        })),
        ...(skillToolSpec() ? [{
          name: 'use_skill', description: skillToolSpec()!.function.description, builtin: true,
          enabled: !disabled.has('use_skill'), parameters: skillToolSpec()!.function.parameters,
          calls: TOOL_CALLS.use_skill,
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

    const userKeyEarly = resolveUserKey(req.user?.id)
    if (effectiveSettings().requirePersonalKey && authEnabled() && !userKeyEarly) {
      sse(res, 'error', {
        message: 'This deployment requires your own model credential. Add your API key or platform token in Settings, Your model access, then retry. Browsing, search, and adding material work without one.',
        kind: 'credential',
      })
      res.end()
      return reply
    }
    if (!gatewayConfigured() && !userKeyEarly) {
      sse(res, 'error', { message: 'No gateway credential (set PW_API_KEY, authenticate the pw CLI, or add your key in Settings); chat is unavailable.' })
      res.end()
      return reply
    }

    // Client history arrives in wire shape; keep only fields the gateway
    // needs, expanding attachment references into their extracted text and
    // resolving a /command on the newest user message.
    const raw = body.messages ?? []
    const lastUserIdx = raw.map(m => m.role).lastIndexOf('user')
    let listing: string | null = null
    const history: WireMessage[] = []
    for (let i = 0; i < raw.length; i++) {
      const m = raw[i]
      if (m.role === 'system') continue
      let content = typeof m.content === 'string' ? m.content : m.content == null ? '' : JSON.stringify(m.content)
      if (i === lastUserIdx && content.trim().startsWith('/')) {
        const expanded = expandSlashCommand(content)
        if (expanded && typeof expanded === 'object') listing = expanded.listing
        else if (expanded) content = expanded
      }
      const ids = (m as { attachment_ids?: string[] }).attachment_ids
      if (ids?.length) {
        const ctx = await attachmentContext(ids).catch(() => '')
        if (ctx) content = content ? `${content}

${ctx}` : ctx
      }
      history.push({ role: m.role, content })
    }

    // Persist the turn server-side; the raw text is what the user typed,
    // not the slash or attachment expansion.
    const lastUser = lastUserIdx >= 0 ? raw[lastUserIdx] : null
    recordUserTurn(
      body.conversationId,
      body.userMessageId,
      typeof lastUser?.content === 'string' ? lastUser.content : '',
      body.parentMessageId ?? null,
    )

    const today = new Date().toISOString().slice(0, 10)
    // The persona file is read per request so edits apply immediately.
    const persona = agentPrompt()
    const userKey = resolveUserKey(req.user?.id)
    const labelScope = Array.isArray(body.labelScope)
      ? body.labelScope.map(String).filter(Boolean).slice(0, 16)
      : []
    const scopeNote = labelScope.length
      ? `\n\nThe user has scoped this conversation to knowledge base material labeled: ${labelScope.join(', ')}. search_kb is restricted server-side to those labels for this conversation. Ground answers in that material, and say so when the scoped material has nothing relevant rather than answering from outside the scope.`
      : ''
    const messages: WireMessage[] = [
      { role: 'system', content: `${await systemPrompt()}\n\nToday's date is ${today}. Treat earlier dates as past; call out deadlines that have already passed.${scopeNote}${persona ? `\n\n--- DEPLOYMENT AGENT INSTRUCTIONS (extensions/agents/default.md) ---\n${persona}` : ''}` },
      ...history,
    ]

    let finalContent = ''
    let finalModel: string | null = null
    let finalReasoning = ''
    let reasoningStart = 0
    let reasoningDuration = 0
    const markThinking = () => { if (!reasoningStart) reasoningStart = Date.now() }
    const allocation = body.allocation ?? process.env.PW_ALLOCATION ?? null
    const callbacks = {
      onContent: (t: string) => {
        if (reasoningStart && !reasoningDuration) reasoningDuration = Date.now() - reasoningStart
        finalContent += t
        sse(res, 'content', { text: t })
      },
      onReasoning: (t: string) => { markThinking(); finalReasoning += t; sse(res, 'reasoning', { text: t }) },
    }
    let recorded = false
    const finish = (finishReason: string | null) => {
      const messageId = `srv-${Date.now()}`
      recorded = true
      recordAssistantTurn(body.conversationId, {
        id: messageId, role: 'assistant', content: finalContent,
        parentId: body.userMessageId ?? null, timestamp: new Date().toISOString(),
        model: finalModel,
        reasoning: finalReasoning || undefined,
        reasoningDuration: reasoningDuration || (reasoningStart ? Date.now() - reasoningStart : undefined),
      })
      sse(res, 'done', {
        content: finalContent,
        finishReason: finishReason ?? 'stop',
        model: finalModel,
        messageId,
      })
      res.end()
    }
    // Meta commands (/help) answer deterministically, no model call.
    if (listing) {
      callbacks.onContent(listing)
      finish('stop')
      return reply
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
          userKey,
        )
        finalModel = turn.model ?? finalModel
        if (turn.finishReason === 'tool_calls' && turn.toolCalls.length > 0) {
          messages.push({ role: 'assistant', content: turn.content || null, tool_calls: turn.toolCalls })
          for (const tc of turn.toolCalls as WireToolCall[]) {
            const key = `${tc.function.name}:${tc.function.arguments}`
            req.log.info({ tool: tc.function.name, args: tc.function.arguments.slice(0, 300), iter }, 'tool call')
            markThinking()
            const callLine = `\n→ \`${tc.function.name}\` ${prettyToolArgs(tc.function.arguments)}\n`
            finalReasoning += callLine
            sse(res, 'tool', { phase: 'call', name: tc.function.name, args: tc.function.arguments, pretty: callLine })
            let result: string
            let summary: string
            if (toolCache.has(key)) {
              result = `[You already made this exact call in this reply; identical result repeated below. Do not repeat it again.]\n${toolCache.get(key)}`
              summary = 'repeated call (cached)'
            } else {
              const out = await executeTool(tc.function.name, tc.function.arguments, { labelScope, userKey })
              result = out.result
              summary = out.summary
              toolCache.set(key, result)
            }
            const resultLine = `\n↳ ${summary}\n`
            finalReasoning += resultLine
            sse(res, 'tool', { phase: 'result', name: tc.function.name, summary, pretty: resultLine })
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
        userKey,
      )
      finalModel = finalTurn.model ?? finalModel
      finish(finalTurn.finishReason)
      return reply
    } catch (err: any) {
      app.log.error({ err }, 'chat stream failed')
      if (!abort.signal.aborted) {
        sse(res, 'error', {
          message: String(err?.message ?? err),
          kind: err?.name === 'GatewayAuthError' ? 'credential' : 'error',
        })
      }
    }
    // An aborted or failed stream still keeps whatever was said.
    if (!recorded && finalContent) {
      recordAssistantTurn(body.conversationId, {
        id: `srv-${Date.now()}`, role: 'assistant', content: finalContent,
        parentId: body.userMessageId ?? null, timestamp: new Date().toISOString(),
        model: finalModel,
        reasoning: finalReasoning || undefined,
        reasoningDuration: reasoningDuration || (reasoningStart ? Date.now() - reasoningStart : undefined),
      })
    }
    res.end()
    return reply
  })
}
