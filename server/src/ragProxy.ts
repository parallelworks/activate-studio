import type { FastifyInstance, FastifyRequest } from 'fastify'
import { GATEWAY_BASE, MAX_TOOL_ITERATIONS, gufiAvailable } from './config.js'
import { blendHits, searchFts, searchNames, searchVector } from './gufi.js'
import { annotateHits } from './tags.js'
import { readFileContent } from './kb.js'
import { gatewayKey, streamTurn, WireMessage } from './chat/gateway.js'
import { resolveUserKey } from './credentials.js'
import { activeToolSpecs, executeTool } from './chat/tools.js'
import { systemPrompt } from './chat/context.js'
import { effectiveSettings } from './settings.js'

/**
 * OpenAI-compatible surface at /v1 exposing the knowledge base to any
 * OpenAI-speaking client (pw code, SDKs, or the platform itself via
 * `pw endpoints run --openai`). Two virtual model families:
 *
 *   studio-agent[/<gateway-model>] runs the Studio's full assistant
 *     pipeline (system prompt, tool loop: search, read, query, labels,
 *     docs) on the underlying model and streams the final grounded answer.
 *   studio-rag[/<gateway-model>] injects retrieved context blocks into the
 *     request and passes it through, one model call, citations directed
 *     inline.
 *
 * Any other model id passes through with context injection (studio-rag
 * behavior), so pointing an existing client here and keeping its model id
 * simply grounds it. Requests are made with the CALLER'S bearer key; the
 * proxy holds no credentials (a deployment can opt callers without keys
 * onto its own credential in Settings). Per-request headers: X-RAG-Top-K,
 * X-RAG-Tags (label filter), X-RAG-Off: 1 (plain passthrough).
 */

const FAIL_OPEN = (process.env.RAG_FAIL_OPEN ?? 'true').toLowerCase() !== 'false'
const CONTEXT_CHAR_CAP = 14_000
const PER_HIT_CHAR_CAP = 2_500

const DIRECTIVE =
  'Context blocks retrieved from the knowledge base follow, numbered [1], [2], … with their source file paths. ' +
  'Ground your answer in them and cite blocks inline as [n]. End with a "References:" section listing one "[n] file_path" per line for every block you used. ' +
  'Do not invent citations. If the context does not cover the question, say so briefly.'

function bearerOf(req: FastifyRequest): string | null {
  const h = String(req.headers.authorization ?? '')
  if (h.toLowerCase().startsWith('bearer ')) return h.slice(7).trim() || null
  return null
}

function callerKey(req: FastifyRequest): { key: string; source: string } | null {
  const bearer = bearerOf(req)
  if (bearer) return { key: bearer, source: 'caller bearer' }
  // A session-proxied caller with a verified identity uses their stored
  // personal key, tying keyless in-platform access to their JWT identity.
  const stored = resolveUserKey((req as FastifyRequest & { user?: { id: string } }).user?.id)
  if (stored) return { key: stored, source: 'stored personal key (session identity)' }
  if (effectiveSettings().ragAllowDeploymentKey && gatewayKey()) return { key: gatewayKey(), source: 'deployment (keyless fallback)' }
  return null
}

/** The deployment's default underlying model may not be accessible to
 *  every caller's key (personal providers are per-account); resolve the
 *  default PER CALLER, preferring the deployment setting when their key
 *  can use it, else their first available model. Cached briefly per key. */
const modelListCache = new Map<string, { ids: string[]; at: number }>()

async function defaultModelFor(key: string): Promise<string> {
  const preferred = effectiveSettings().ragDefaultModel
  const cacheId = key.slice(-16)
  const cached = modelListCache.get(cacheId)
  let ids = cached && Date.now() - cached.at < 300_000 ? cached.ids : null
  if (!ids) {
    try {
      const res = await fetch(`${GATEWAY_BASE}/models`, { headers: { Authorization: `Bearer ${key}` }, signal: AbortSignal.timeout(8000) })
      const data = res.ok ? await res.json() as { data?: { id: string }[]; models?: { id: string }[] } : {}
      ids = (data.data ?? data.models ?? []).map(m => m.id)
    } catch { ids = [] }
    modelListCache.set(cacheId, { ids, at: Date.now() })
  }
  if (preferred && ids.includes(preferred)) return preferred
  const own = ids.find(id => !id.startsWith('org:') && !/studio-(agent|rag)/.test(id))
  return own ?? ids.find(id => !/studio-(agent|rag)/.test(id)) ?? preferred
}

function lastUserText(messages: unknown[]): string | null {
  for (let i = messages.length - 1; i >= 0; i--) {
    const m = messages[i] as { role?: string; content?: unknown }
    if (m?.role !== 'user') continue
    if (typeof m.content === 'string') return m.content
    if (Array.isArray(m.content)) {
      const text = m.content
        .map(p => (p && typeof p === 'object' && (p as { type?: string }).type === 'text' ? String((p as { text?: string }).text ?? '') : ''))
        .join(' ')
        .trim()
      return text || null
    }
    return null
  }
  return null
}

const STOPWORDS = new Set(['the', 'a', 'an', 'and', 'or', 'but', 'not', 'only', 'what', 'which', 'who', 'whom', 'whose', 'why', 'how', 'when', 'where', 'was', 'were', 'is', 'are', 'be', 'been', 'do', 'does', 'did', 'can', 'could', 'should', 'would', 'will', 'shall', 'may', 'might', 'for', 'from', 'with', 'without', 'about', 'into', 'onto', 'that', 'this', 'these', 'those', 'there', 'their', 'they', 'them', 'its', 'our', 'your', 'you', 'me', 'my', 'we', 'us', 'one', 'two', 'three', 'please', 'tell', 'give', 'show', 'short', 'brief', 'paragraph', 'paragraphs', 'sentence', 'sentences', 'word', 'words', 'line', 'lines', 'answer', 'question', 'explain', 'describe', 'summarize', 'summary', 'list'])

/** Raw questions make poor AND-joined term queries; keep the informative
 *  words for the exact-match legs while the vector leg embeds the full
 *  question. */
function distill(query: string): string[] {
  const terms = query.toLowerCase().replace(/[^a-z0-9\s_]/g, ' ').split(/\s+/)
    .filter(t => t.length >= 3 && !STOPWORDS.has(t))
  return [...new Set(terms)].slice(0, 8)
}

async function buildContext(query: string, topK: number, tags?: string[]): Promise<{ text: string; terms: string[]; blocks: number; chars: number } | null> {
  const q = query.slice(0, 300)
  const terms = distill(q)
  const termQuery = terms.join(' ') || q.slice(0, 100)
  let [fts, names, vec] = await Promise.all([
    searchFts(termQuery, topK),
    searchNames(termQuery, Math.min(topK, 5)),
    searchVector(q, Math.min(topK, 8)).catch(() => []),
  ])
  // An over-specified AND query finds nothing; retry on the strongest terms.
  if (!fts.length && terms.length > 3) {
    const strongest = [...terms].sort((a, b) => b.length - a.length).slice(0, 3).join(' ')
    fts = await searchFts(strongest, topK).catch(() => [])
  }
  const hits = await annotateHits(blendHits(fts, names, vec, topK), tags?.length ? tags : undefined)
  if (!hits.length) return null
  const blocks: string[] = []
  let total = 0
  for (const h of hits) {
    let text = h.snippet ?? ''
    try {
      const fc = await readFileContent(h.path)
      if (fc.content) text = fc.content.slice(0, PER_HIT_CHAR_CAP)
    } catch { /* snippet fallback */ }
    text = text.trim()
    if (!text) continue
    if (total + text.length > CONTEXT_CHAR_CAP) break
    total += text.length
    const labels = h.tags?.length ? ` (labels: ${h.tags.join(', ')})` : ''
    blocks.push(`[${blocks.length + 1}] ${h.path}${labels}\n${text}`)
  }
  return blocks.length ? { text: blocks.join('\n\n'), terms, blocks: blocks.length, chars: total } : null
}

/** Resolve a virtual model id to { mode, underlying }. */
function resolveModel(requested: string): { mode: 'agent' | 'rag'; underlying: string } {
  const eff = effectiveSettings()
  const fallback = eff.ragDefaultModel
  if (requested === 'studio-agent' || requested.startsWith('studio-agent/')) {
    return { mode: 'agent', underlying: requested.slice('studio-agent/'.length) || fallback }
  }
  if (requested === 'studio-rag' || requested.startsWith('studio-rag/')) {
    return { mode: 'rag', underlying: requested.slice('studio-rag/'.length) || fallback }
  }
  return { mode: 'rag', underlying: requested }
}

/* ---- OpenAI wire helpers ---- */

function chunkOf(id: string, model: string, delta: Record<string, unknown>, finish: string | null): string {
  return `data: ${JSON.stringify({
    id, object: 'chat.completion.chunk', created: Math.floor(Date.now() / 1000), model,
    choices: [{ index: 0, delta, finish_reason: finish }],
  })}\n\n`
}

/** The Studio's full assistant pipeline behind an OpenAI response. */
async function agenticCompletion(
  underlying: string,
  clientMessages: unknown[],
  key: string,
  onDelta: (text: string) => void,
  signal?: AbortSignal,
): Promise<{ content: string; finish: string }> {
  const sys = await systemPrompt()
  const today = new Date().toISOString().slice(0, 10)
  const messages: WireMessage[] = [
    { role: 'system', content: `${sys}\n\nToday's date is ${today}.` },
    ...(clientMessages as WireMessage[]).filter(m => m.role !== 'system').map(m => ({
      role: m.role,
      content: typeof m.content === 'string' ? m.content : m.content == null ? '' : JSON.stringify(m.content),
    })),
  ]
  let finalContent = ''
  const cb = { onContent: (t: string) => { finalContent += t; onDelta(t) } }
  const toolCache = new Map<string, string>()
  for (let iter = 0; iter < MAX_TOOL_ITERATIONS; iter++) {
    const turn = await streamTurn(
      { model: underlying, messages, tools: activeToolSpecs(), tool_choice: 'auto' },
      null, cb, signal, key,
    )
    if (turn.finishReason === 'tool_calls' && turn.toolCalls.length > 0) {
      messages.push({ role: 'assistant', content: turn.content || null, tool_calls: turn.toolCalls })
      for (const tc of turn.toolCalls) {
        const cacheKey = `${tc.function.name}:${tc.function.arguments}`
        let result = toolCache.get(cacheKey)
        if (result === undefined) {
          const out = await executeTool(tc.function.name, tc.function.arguments, { userKey: key })
            .catch(e => ({ result: `tool failed: ${String((e as Error).message ?? e)}`, summary: 'failed' }))
          result = out.result
          toolCache.set(cacheKey, result)
        }
        messages.push({ role: 'tool', tool_call_id: tc.id ?? `${tc.function.name}-${iter}`, content: result })
      }
      continue
    }
    return { content: finalContent, finish: turn.finishReason ?? 'stop' }
  }
  messages.push({ role: 'user', content: 'Tool budget exhausted; answer now from what you gathered.' })
  const finalTurn = await streamTurn({ model: underlying, messages }, null, cb, signal, key)
  return { content: finalContent, finish: finalTurn.finishReason ?? 'stop' }
}

/** Ring buffer of recent /v1 calls so the Settings page can show what the
 *  endpoint is actually doing. Question text is never recorded; the
 *  distilled retrieval terms are, which the page discloses. */
export interface RagCallRecord {
  at: string
  model: string
  mode: string
  underlying: string
  authSource: string
  stream: boolean
  durationMs: number | null
  status: 'running' | 'ok' | 'error'
  detail: string | null
  retrieval: { terms: string[]; blocks: number; chars: number } | null
}
const CALL_LOG: RagCallRecord[] = []
function logCall(rec: RagCallRecord): RagCallRecord {
  CALL_LOG.unshift(rec)
  if (CALL_LOG.length > 50) CALL_LOG.pop()
  return rec
}
export function ragCallLog(): RagCallRecord[] { return CALL_LOG }

export async function ragProxyRoutes(app: FastifyInstance): Promise<void> {
  app.get('/api/rag/calls', async () => ({ calls: ragCallLog() }))

  // Advertises only studio-agent by default: as a host agent's driver
  // model, studio-rag passes the host's tool specs through and the model
  // works the host's tools instead of the injected context, so it is
  // unlisted (still callable by name; ?all=1 shows it plus the upstream
  // ids for clients that want one flat list). The gateway's own catalog
  // already lists the underlying models.
  app.get('/v1/models', async (req, reply) => {
    const auth = callerKey(req)
    if (!auth) return reply.status(401).send({ error: { message: 'bearer API key required' } })
    const key = auth.key
    const created = Math.floor(Date.now() / 1000)
    const entry = (id: string) => ({ id, object: 'model', created, owned_by: 'studio' })
    const eff0 = effectiveSettings()
    const data: { id: string; object: string; created: number; owned_by: string }[] = []
    // Advertise the bare name (stable alias) and, when the caller's key
    // resolves a default, the pinned form showing the underlying model.
    // Computed per caller from their own credential, so the listing is
    // truthful for whoever is looking at it, and picking the pinned id
    // guarantees that base model.
    const resolved = await defaultModelFor(key).catch(() => '')
    const advertise = (name: string) => {
      data.push(entry(name))
      if (resolved) data.push(entry(`${name}/${resolved}`))
    }
    if (eff0.ragAdvertiseAgentModel) advertise('studio-agent')
    if (eff0.ragAdvertiseRagModel) advertise('studio-rag')
    if (String((req.query as { all?: string }).all ?? '') === '1') {
      for (const id of ['studio-agent', 'studio-rag']) {
        if (!data.some(d => d.id === id)) data.push(entry(id))
      }
      try {
        const res = await fetch(`${GATEWAY_BASE}/models`, { headers: { Authorization: `Bearer ${key}` } })
        if (res.ok) {
          const wire = await res.json() as { data?: { id: string }[]; models?: { id: string }[] }
          for (const m of wire.data ?? wire.models ?? []) data.push({ id: m.id, object: 'model', created, owned_by: 'gateway' })
        }
      } catch { /* studio entries still listed */ }
    }
    return { object: 'list', data }
  })

  app.post('/v1/chat/completions', async (req, reply) => {
    const auth = callerKey(req)
    if (!auth) {
      logCall({
        at: new Date().toISOString(), model: String((req.body as { model?: string })?.model ?? '?'),
        mode: '?', underlying: '', authSource: 'none', stream: false, durationMs: 0,
        status: 'error', detail: 'rejected: no credential (bearer API key required)', retrieval: null,
      })
      return reply.status(401).send({ error: { message: 'bearer API key required' } })
    }
    const key = auth.key
    const eff = effectiveSettings()
    const body = { ...(req.body as Record<string, unknown>) }
    const requested = String(body.model ?? 'studio-rag')
    let { mode, underlying } = resolveModel(requested)
    const bare = requested === 'studio-agent' || requested === 'studio-rag'
    if (bare) underlying = await defaultModelFor(key)
    if (!underlying) {
      return reply.status(400).send({ error: { message: `No underlying model available to this key: pass ${requested}/<gateway-model-id> or set a default model on the Settings RAG endpoint section.` } })
    }
    const messages = Array.isArray(body.messages) ? [...(body.messages as unknown[])] : []
    const wantStream = body.stream === true
    // Observability for the credential path: whose key is this call on?
    const authSource = String(req.headers['x-studio-injected-key'] ?? '') === '1'
      ? 'deployment (injected by registration forwarder)'
      : auth.source
    req.log.info({ model: requested, underlying, mode, authSource }, 'rag proxy call')
    const started = Date.now()
    const rec = logCall({
      at: new Date().toISOString(), model: requested, mode, underlying, authSource,
      stream: wantStream, durationMs: null, status: 'running', detail: null, retrieval: null,
    })
    const id = `studio-${Date.now().toString(36)}`

    if (mode === 'agent') {
      if (wantStream) {
        reply.hijack()
        reply.raw.writeHead(200, { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-cache' })
        reply.raw.write(chunkOf(id, requested, { role: 'assistant' }, null))
        // The tool loop can run tens of seconds without emitting a byte,
        // and idle HTTP/2 streams get killed by intermediaries (seen as
        // INTERNAL_ERROR in pw code through the platform tunnel): send
        // empty-delta keepalives during silent phases, and stop the loop
        // when the peer goes away instead of finishing for nobody.
        const abort = new AbortController()
        reply.raw.on('close', () => { if (!reply.raw.writableEnded) abort.abort() })
        const keepalive = setInterval(() => {
          if (!reply.raw.writableEnded) reply.raw.write(chunkOf(id, requested, {}, null))
        }, 3_000)
        try {
          const r = await agenticCompletion(underlying, messages, key, t => {
            reply.raw.write(chunkOf(id, requested, { content: t }, null))
          }, abort.signal)
          reply.raw.write(chunkOf(id, requested, {}, r.finish))
          rec.status = 'ok'
        } catch (err) {
          rec.status = 'error'
          rec.detail = String((err as Error).message ?? err).slice(0, 200)
          if (!abort.signal.aborted) {
            reply.raw.write(chunkOf(id, requested, { content: `\n[studio-agent error: ${String((err as Error).message ?? err).slice(0, 300)}]` }, 'stop'))
          }
        } finally {
          clearInterval(keepalive)
          rec.durationMs = Date.now() - started
          if (abort.signal.aborted && rec.status === 'error') rec.detail = 'caller disconnected'
        }
        reply.raw.write('data: [DONE]\n\n')
        // Give the tunnel a beat to flush the tail frames; an immediate
        // close races the final SSE lines off the wire (seen as missing
        // [DONE] and client-side stream errors through QUIC).
        await new Promise(r => setTimeout(r, 300))
        reply.raw.end()
        return reply
      }
      try {
        const r = await agenticCompletion(underlying, messages, key, () => {})
        rec.status = 'ok'
        rec.durationMs = Date.now() - started
        return {
          id, object: 'chat.completion', created: Math.floor(Date.now() / 1000), model: requested,
          choices: [{ index: 0, message: { role: 'assistant', content: r.content }, finish_reason: r.finish }],
          usage: null,
        }
      } catch (err) {
        rec.status = 'error'
        rec.detail = String((err as Error).message ?? err).slice(0, 200)
        rec.durationMs = Date.now() - started
        throw err
      }
    }

    // studio-rag: context injection, then byte-for-byte passthrough.
    const ragOff = String(req.headers['x-rag-off'] ?? '') === '1'
    const topK = Math.min(Math.max(Number(req.headers['x-rag-top-k']) || eff.ragTopK, 1), 20)
    const tags = String(req.headers['x-rag-tags'] ?? '').split(',').map(t => t.trim()).filter(Boolean)
    if (ragOff) rec.detail = 'retrieval disabled by X-RAG-Off header'
    if (!ragOff && gufiAvailable()) {
      const query = lastUserText(messages)
      if (query?.trim()) {
        try {
          const context = await buildContext(query, topK, tags)
          if (context) {
            rec.retrieval = { terms: context.terms, blocks: context.blocks, chars: context.chars }
            let insertAt = 0
            while (insertAt < messages.length && (messages[insertAt] as { role?: string })?.role === 'system') insertAt++
            messages.splice(insertAt, 0, { role: 'system', content: `${DIRECTIVE}\n\n${context.text}` })
          } else {
            rec.detail = 'no retrieval hits'
          }
        } catch (err) {
          if (!FAIL_OPEN) {
            return reply.status(502).send({ error: { message: `retrieval failed: ${String((err as Error).message ?? err)}` } })
          }
          req.log.warn({ err }, 'rag retrieval failed; passing through without context')
          rec.detail = 'retrieval failed; passed through without context'
        }
      }
    }
    const headers: Record<string, string> = { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json' }
    const allocation = String(req.headers['x-allocation'] ?? '')
    if (allocation) headers['X-Allocation'] = allocation
    const upstream = await fetch(`${GATEWAY_BASE}/chat/completions`, {
      method: 'POST', headers, body: JSON.stringify({ ...body, model: underlying, messages }),
    })
    rec.status = upstream.ok ? 'ok' : 'error'
    if (!upstream.ok) rec.detail = `upstream ${upstream.status}`
    rec.durationMs = Date.now() - started
    reply.hijack()
    reply.raw.writeHead(upstream.status, {
      'Content-Type': upstream.headers.get('content-type') ?? 'application/json',
      'Cache-Control': 'no-cache',
    })
    if (upstream.body) {
      for await (const chunk of upstream.body as unknown as AsyncIterable<Uint8Array>) reply.raw.write(chunk)
    }
    await new Promise(r => setTimeout(r, 300))
    reply.raw.end()
    return reply
  })
}
