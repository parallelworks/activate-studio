import { createHash } from 'node:crypto'
import { GATEWAY_BASE, PW_API_KEY, SIDECAR_BASE, SIDECAR_KEY } from '../config.js'
import { resolveSharedKey, type EndpointOptions } from '../credentials.js'
import { cliCredential } from '../access.js'
import { connectionProblem, isLoopbackHost, networkFailure, unreachableMessage } from '../net.js'

/**
 * Gateway credential: PW_API_KEY env when set, otherwise the pw CLI's own
 * credential for the gateway host, from the context chosen in Model access
 * or the CLI's current one (access.ts; a sign-in the CLI renews is asked of
 * the CLI). Never sent to the browser.
 */
export function gatewayKey(): string {
  // A key a user has promoted for everyone stands in for the deployment
  // credential, and wins over the ambient one: it was chosen deliberately,
  // in the app, by someone who wanted the group to use it.
  return resolveSharedKey() || PW_API_KEY || cliCredential()
}

const hostOf = (url: string) => { try { return new URL(url).host } catch { return url } }

/**
 * The headers of a request to a model endpoint: its own extra headers, and
 * the key in the header the endpoint takes it in (Authorization: Bearer
 * unless the endpoint's options say otherwise). Every call to an endpoint
 * goes through here and withQuery, so a portal's requirements apply to the
 * listing, the health check, chat, label suggestions and the /v1 proxy
 * alike. The result carries secrets: never log it.
 */
export function modelHeaders(key: string, endpoint?: EndpointOptions | null): Record<string, string> {
  const h: Record<string, string> = {}
  for (const x of endpoint?.headers ?? []) h[x.name] = x.value
  switch (endpoint?.auth ?? 'bearer') {
    case 'bearer': h.Authorization = `Bearer ${key}`; break
    case 'api-key': h['api-key'] = key; break
    case 'x-api-key': h['x-api-key'] = key; break
    case 'none': break
  }
  return h
}

/** A request URL with the endpoint's query string (api-version=..., say). */
export function withQuery(url: string, endpoint?: EndpointOptions | null): string {
  const q = endpoint?.query
  return q ? `${url}${url.includes('?') ? '&' : '?'}${q}` : url
}

/** A fetch whose network failure reads as a sentence about the host. Other
 *  failures (a stop the person asked for, an AbortError) pass through. */
async function reach(url: string, init: RequestInit): Promise<Response> {
  try {
    return await fetch(url, init)
  } catch (e) {
    const conn = connectionProblem(e, hostOf(url))
    if (conn) throw new GatewayUnreachable(conn)
    const f = networkFailure(e)
    if (!f) throw e
    throw new GatewayUnreachable(unreachableMessage(hostOf(url), f))
  }
}

/** The gateway or provider could not be reached at all. */
export class GatewayUnreachable extends Error {
  constructor(message: string) { super(message); this.name = 'GatewayUnreachable' }
}

export interface WireToolCall {
  id?: string
  index?: number
  type?: string
  function: { name: string; arguments: string }
}

export interface WireMessage {
  role: string
  content?: string | null
  tool_call_id?: string
  tool_calls?: WireToolCall[]
  name?: string
}

export interface StreamCallbacks {
  onContent: (text: string) => void
  onReasoning?: (text: string) => void
}

export interface TokenUsage { prompt_tokens?: number; completion_tokens?: number; total_tokens?: number }
export interface TurnResult {
  content: string
  reasoning: string
  toolCalls: WireToolCall[]
  finishReason: string | null
  model: string | null
  /** Token counts when the stream carried a usage chunk; absent otherwise. */
  usage?: TokenUsage
}

export function gatewayConfigured(): boolean {
  return gatewayKey().length > 0
}

/* ---- the model served beside this deployment ---- */

/**
 * Ids seen from the sidecar, recorded when it is listed so a later chat
 * turn can tell which target a chosen model belongs to. A model name is
 * the only thing the browser sends back, and the two catalogs are
 * otherwise indistinguishable.
 */
const sidecarIds = new Set<string>()

export function sidecarConfigured(): boolean {
  return SIDECAR_BASE.length > 0
}

export function isSidecarModel(id: string): boolean {
  return sidecarIds.has(id)
}

export function sidecarTarget(): { key: string; baseUrl: string } {
  return { key: SIDECAR_KEY, baseUrl: SIDECAR_BASE }
}

/**
 * The sidecar's own catalog. A serve that is still loading, or has gone
 * away with its job, must not empty the picker of everything else, so a
 * failure here is an empty list rather than an error.
 */
export async function listSidecarModels(): Promise<any[]> {
  if (!sidecarConfigured()) return []
  try {
    const res = await fetch(`${SIDECAR_BASE}/models`, {
      headers: { Authorization: `Bearer ${SIDECAR_KEY}` },
      signal: AbortSignal.timeout(5000),
    })
    if (!res.ok) return []
    const data = await res.json() as { data?: any[]; models?: any[] }
    const models = (data.data ?? data.models ?? []).filter(Boolean)
    for (const m of models) if (m?.id) sidecarIds.add(String(m.id))
    return models
  } catch {
    return []
  }
}

/** Credential rejections get their own error type and an actionable
 *  message, so the UI can say "unlock your key" instead of a raw 401. */
export class GatewayAuthError extends Error {
  constructor(message: string) { super(message); this.name = 'GatewayAuthError' }
}

let lastCallFailure: { at: string; message: string; auth: boolean } | null = null

/**
 * Which models have recently failed at invoke time, keyed by model id.
 * A provider's catalog listing succeeds on the registration alone, so a
 * model whose credential has expired still lists as available; the only
 * evidence of callability is an actual call. Failures are recorded here
 * and cleared by the next success, so the picker can say "last call
 * failed" about exactly the models where that is true, without probing
 * anything.
 */
const modelFailures = new Map<string, { at: string; message: string; auth: boolean; status: number }>()

export function modelFailure(id: string): { at: string; message: string; auth: boolean; status: number } | undefined {
  return modelFailures.get(id)
}

export function recordModelOutcome(id: string | null | undefined, failure: { message: string; auth: boolean; status: number } | null): void {
  if (!id) return
  if (failure) modelFailures.set(String(id), { at: new Date().toISOString(), ...failure })
  else modelFailures.delete(String(id))
}

function gatewayError(status: number, bodyText: string, ctx: string): Error {
  const auth = status === 401 || status === 403
    || /unauthoriz|invalid[ _-]?(api[ _-]?)?key|credential|locked|expired token/i.test(bodyText)
  const err = auth
    ? new GatewayAuthError(
        `The model gateway rejected the credential (${status}). The API key may be locked or expired; some providers require a periodic unlock. Unlock or update the key, then retry. Gateway said: ${bodyText.slice(0, 200)}`)
    : new Error(`gateway ${ctx} ${status}: ${bodyText.slice(0, 300)}`)
  lastCallFailure = { at: new Date().toISOString(), message: err.message.slice(0, 300), auth }
  return err
}

/**
 * Where a base URL lists its models. GitHub Models serves its catalog apart
 * from its inference endpoint, as a plain array.
 */
export function modelsUrl(base: string): string {
  try {
    const u = new URL(base)
    if (u.host === 'models.github.ai') return `${u.origin}/catalog/models`
  } catch { /* not a URL: used as given */ }
  return `${base}/models`
}

/** A listing as { data: [...] }, whichever shape the server answered with. */
function asListing(body: unknown): { data?: unknown[]; models?: unknown[] } {
  return Array.isArray(body) ? { data: body } : (body as { data?: unknown[]; models?: unknown[] }) ?? {}
}

/** Model names typed under Advanced, as a listing: the endpoint lists none. */
function typedListing(models: string[]): { data: { id: string; object: string }[] } {
  return { data: models.map(id => ({ id, object: 'model' })) }
}

export async function listModels(key?: string | null, baseUrl?: string | null, endpoint?: EndpointOptions | null): Promise<unknown> {
  if (baseUrl && endpoint?.models?.length) return typedListing(endpoint.models)
  const res = await reach(withQuery(modelsUrl(baseUrl || GATEWAY_BASE), baseUrl ? endpoint : null), {
    headers: modelHeaders(key || gatewayKey(), baseUrl ? endpoint : null),
    signal: AbortSignal.timeout(10_000),
  })
  if (!res.ok) throw gatewayError(res.status, await res.text(), '/models')
  return asListing(await res.json())
}

/**
 * What a 404 from an endpoint means, naming the URL that was tried. A base
 * URL that already ends in a version (/v1) is not told to add one: the
 * address is likely right and the endpoint lists no models, which typed
 * model names answer. The ACTIVATE gateway path 404s on a host that is not
 * an ACTIVATE platform, the usual sign that an endpoint was entered as a
 * platform host.
 */
export function notFoundMessage(tried: string, base: string): string {
  let path = ''
  let origin = base
  try { const u = new URL(base); path = u.pathname.replace(/\/+$/, ''); origin = u.origin } catch { /* named as given */ }
  if (/\/api\/openai\/v1$/.test(path)) {
    return `${tried} does not exist (404), so ${hostOf(base)} is not an ACTIVATE platform. For a site AI portal or another OpenAI-compatible service, use OpenAI-compatible endpoint with the service's base URL.`
  }
  const trimmed = path.replace(/\/(chat\/completions|completions|models|embeddings)$/, '')
  if (trimmed !== path) return `${tried} does not exist (404). The base URL ends before /chat/completions: try ${origin}${trimmed}.`
  if (/\/v\d+[a-z0-9]*$/i.test(path)) {
    return `${tried} does not exist (404). If ${base} is the right address, the endpoint does not list its models: type their names under Advanced (Settings, Model access, OpenAI-compatible endpoint), and Test then tries a chat completion with the first.`
  }
  return `${tried} does not exist (404). An OpenAI-compatible base URL usually ends in a version, such as ${origin}${path}/v1.`
}

export interface AiHealth {
  ok: boolean
  /** offline: the host could not be reached at all, as with no network. */
  status: 'ok' | 'auth' | 'unreachable' | 'offline' | 'unconfigured'
  models: number
  gatewayHost: string
  message: string | null
  lastCallFailure: { at: string; message: string; auth: boolean } | null
  checkedAt: string
  /** Present when the provider reports a locked key with its unlock link. */
  unlockUrl?: string | null
  /** The model ids, when asked for (a key being tested). */
  ids?: string[]
  /** How the endpoint was asked: its model list, or, with model names typed
   *  for it, a chat completion with the first. */
  checked?: 'models' | 'completion'
  /** The URL that answered 404, when one did. */
  notFound?: string
}

/** Live callability check: exercises the gateway with the current
 *  credential (via the model listing) and reports the last real call
 *  failure, which catches keys that list fine but fail at invoke time. */
/**
 * Provider liveness at listing time. The gateway lists a registered
 * provider's models from its registry without asking the provider, so a
 * locked provider key produced a picker full of selectable models that could
 * only fail. The probe asks the provider itself: for a personal custom
 * provider that is a GET of its /models (free), and for gateway-registered
 * providers a one-token completion on one model of that provider, which is
 * the only request shape the gateway forwards. Cached per provider and
 * key, since a lock lasts hours and the picker refreshes often; probing is
 * limited to providers matching STUDIO_PROBE_PROVIDERS (default genai),
 * because a one-token call per provider is a real request and the locking
 * behaviour this exists for belongs to those providers.
 */
export interface ProviderVerdict { ok: boolean; kind: 'locked' | 'unavailable' | null; unlockUrl: string | null; message: string }
const providerProbes = new Map<string, { at: number; v: ProviderVerdict }>()
const PROBE_TTL_MS = 5 * 60_000
const PROBE_PATTERN = new RegExp(process.env.STUDIO_PROBE_PROVIDERS || 'genai', 'i')

/** Cleared on an explicit re-check, so an unlock takes effect at once
 *  instead of after the cache ages out. */
export function invalidateProviderProbes(): void { providerProbes.clear(); completionPings.clear() }

export function probeableProvider(prefix: string): boolean { return PROBE_PATTERN.test(prefix) }

/** The provider's own sentence from an error body, for showing a person. */
export function providerReason(text: string): string {
  let t = text
  for (let i = 0; i < 3; i++) {
    // A complete string first; failing that, a string cut off by an
    // upstream truncation, taken to the end of what arrived.
    const m = /"message"\s*:\s*"((?:[^"\\]|\\.)*)"/.exec(t) ?? /"message"\s*:\s*"((?:[^"\\]|\\.)*)$/.exec(t)
    if (!m) break
    t = m[1].replace(/\\"/g, '"').replace(/\\\\/g, '\\')
  }
  t = t.replace(/^received error while streaming:\s*/i, '').replace(/\s+/g, ' ').trim()
  return t.length > 180 ? t.slice(0, 177) + '...' : t
}

export function extractUnlockUrl(text: string): string | null {
  return /unlock_url[\\":\s]*(https?:\/\/[^"\\\s]+)/.exec(text)?.[1] ?? null
}

/** A body that names a request parameter as the problem: the provider is up. */
function rejectedParameter(text: string): boolean {
  return /unsupported|unknown|unrecognized|invalid|not supported/i.test(text)
    && /parameter|max_tokens|max_output_tokens|max_completion_tokens|temperature|top_p/i.test(text)
}

/**
 * The response up to its first SSE frame, or the whole body when it is
 * not a stream. An error body is one JSON object and arrives complete; a
 * healthy stream is abandoned after the first data frame proves the
 * provider is answering, so an uncapped ping costs the provider a few
 * tokens rather than a paragraph. Bodies without a readable stream (the
 * test double, a proxy that buffers) fall back to text().
 */
async function firstFrame(res: Response, ctl: AbortController): Promise<string> {
  if (!res.ok || !res.body || typeof (res.body as { getReader?: unknown }).getReader !== 'function') return res.text()
  const reader = (res.body as ReadableStream<Uint8Array>).getReader()
  const dec = new TextDecoder()
  let buf = ''
  try {
    for (;;) {
      const { value, done } = await reader.read()
      if (done) break
      buf += dec.decode(value, { stream: true })
      if (/^data: /m.test(buf) || /^\s*\{"error"/.test(buf)) break
      if (buf.length > 16_384) break
    }
  } finally {
    try { ctl.abort() } catch { /* already closed */ }
    try { reader.releaseLock() } catch { /* already released */ }
  }
  return buf
}

export async function probeProvider(prefix: string, sampleModelId: string, key?: string | null): Promise<ProviderVerdict> {
  const cacheKey = `${prefix}:${(key ?? '').slice(-6)}`
  const hit = providerProbes.get(cacheKey)
  if (hit && Date.now() - hit.at < PROBE_TTL_MS) return hit.v
  const ping = async (): Promise<{ ok: boolean; status: number; text: string }> => {
    // No token cap, deliberately. Some provider families reject every
    // spelling of one (max_tokens, max_completion_tokens, max_output_tokens),
    // and a probe that carries one then fails on its own parameter and
    // reports a healthy provider as down; the mark then sits on every
    // model of that family. The cost of an uncapped ping is bounded
    // another way: the response is read only until its first frame.
    const ctl = new AbortController()
    const timer = setTimeout(() => ctl.abort(), 6000)
    try {
      const res = await fetch(`${GATEWAY_BASE}/chat/completions`, {
        method: 'POST',
        headers: { Authorization: `Bearer ${key || gatewayKey()}`, 'Content-Type': 'application/json' },
        // stream:true, deliberately: the gateway masks provider errors on the
        // non-streaming path and passes them through on the streaming one
        // (measured for both the locked-key 401 and parameter rejections),
        // so only a streaming probe can see "API key locked". A healthy
        // model answers with SSE frames, which the error check ignores.
        body: JSON.stringify({ model: sampleModelId, messages: [{ role: 'user', content: 'ping' }], stream: true }),
        signal: ctl.signal,
      })
      const text = await firstFrame(res, ctl)
      // A streaming success arrives as SSE data frames; an error arrives as
      // one JSON error body whatever the transport asked for.
      return { ok: res.ok && !/^\s*\{"error"/.test(text), status: res.status, text: text.slice(0, 4000) }
    } finally {
      clearTimeout(timer)
    }
  }
  let v: ProviderVerdict = { ok: true, kind: null, unlockUrl: null, message: '' }
  try {
    let r = await ping()
    if (!r.ok && rejectedParameter(r.text)) {
      // The provider answered, in detail, about the request's shape. That
      // is a reachable, unlocked provider; whatever it disliked is ours.
      r = { ...r, ok: true }
    }
    if (!r.ok) {
      const unlockUrl = extractUnlockUrl(r.text)
      const credentialish = unlockUrl !== null || r.status === 401 || r.status === 403 || /key locked|locked|unauthorized|api key/i.test(r.text)
      if (credentialish) {
        v = { ok: false, kind: 'locked', unlockUrl, message: r.text.slice(0, 2000) }
      } else {
        // The gateway masks a provider's own 401 into a generic 400
        // (parallelworks/core#19405), so the lock this probe exists for
        // arrives looking like any transient failure. A one-token ping
        // failing twice in a row is treated as the provider being down
        // for real, whatever the wording; a healthy serve essentially
        // never fails it once, let alone twice.
        r = await ping()
        if (!r.ok) v = { ok: false, kind: 'unavailable', unlockUrl: extractUnlockUrl(r.text), message: r.text.slice(0, 2000) }
      }
    }
  } catch { /* unreachable is not proof of a lock; leave ok */ }
  providerProbes.set(cacheKey, { at: Date.now(), v })
  return v
}

/**
 * With model names typed for an endpoint, a chat completion with the first
 * stands in for the model list as the health check. It costs a model call,
 * so the answer is kept for a few minutes (the footer asks every minute)
 * and dropped on an explicit re-check, like the provider probes.
 */
const completionPings = new Map<string, { at: number; status: number; text: string }>()

async function pingCompletion(url: string, headers: Record<string, string>, model: string, cacheKey: string): Promise<{ status: number; text: string }> {
  const hit = completionPings.get(cacheKey)
  if (hit && Date.now() - hit.at < PROBE_TTL_MS) return hit
  const ctl = new AbortController()
  const timer = setTimeout(() => ctl.abort(), 8000)
  try {
    // Streamed and read to its first frame, uncapped, as probeProvider does.
    const res = await fetch(url, {
      method: 'POST',
      headers: { ...headers, 'Content-Type': 'application/json' },
      body: JSON.stringify({ model, messages: [{ role: 'user', content: 'ping' }], stream: true }),
      signal: ctl.signal,
    })
    const text = await firstFrame(res, ctl)
    const v = { at: Date.now(), status: res.ok && /^\s*\{"error"/.test(text) ? 500 : res.status, text: text.slice(0, 4000) }
    completionPings.set(cacheKey, v)
    return v
  } finally {
    clearTimeout(timer)
  }
}

export async function aiHealth(key?: string | null, baseUrl?: string | null, opts: { ids?: boolean; endpoint?: EndpointOptions | null } = {}): Promise<AiHealth> {
  const checkedAt = new Date().toISOString()
  const apiBase = baseUrl || GATEWAY_BASE
  const gatewayHost = (() => { try { return new URL(apiBase).host } catch { return apiBase } })()
  const base = { models: 0, gatewayHost, lastCallFailure, checkedAt }
  if (!key && !gatewayConfigured()) {
    return { ...base, ok: false, status: 'unconfigured', message: 'No gateway credential configured (PW_API_KEY or pw CLI login).' }
  }
  // An endpoint's options apply only to an endpoint, never to the gateway.
  const endpoint = baseUrl ? opts.endpoint ?? null : null
  const typed = endpoint?.models ?? []
  const headers = modelHeaders(key || gatewayKey(), endpoint)
  const tried = withQuery(typed.length ? `${apiBase}/chat/completions` : modelsUrl(apiBase), endpoint)
  try {
    let status: number
    let text: string
    if (typed.length) {
      // Keyed by a digest, so the cache holds no key or header value.
      const cacheKey = createHash('sha256').update(JSON.stringify([apiBase, key ?? '', endpoint])).digest('hex')
      const ping = await pingCompletion(tried, headers, typed[0], cacheKey)
      status = ping.status
      text = ping.text
    } else {
      const res = await fetch(tried, { headers, signal: AbortSignal.timeout(8000) })
      status = res.status
      text = await res.text()
    }
    const checked = typed.length ? 'completion' as const : 'models' as const
    if (status < 200 || status >= 300) {
      const auth = status === 401 || status === 403
      // Some providers lock keys on a schedule, and their 401 body carries
      // the unlock URL. That link is the remedy, so it is extracted before the
      // body is truncated for display and carried as its own field.
      const unlockUrl = /unlock_url[\\":\s]*(https?:\/\/[^"\\\s]+)/.exec(text)?.[1] ?? null
      return {
        ...base, ok: false, status: auth ? 'auth' : 'unreachable', checked,
        message: unlockUrl
          ? 'The provider locked this API key (it does so on a schedule); unlock it and re-check.'
          : auth
            ? (baseUrl
                ? `${gatewayHost} rejected this credential (${status}). Check the key, and under Advanced the header the endpoint expects it in.`
                : `${gatewayHost} rejected this credential (${status}). A platform token lasts 24 hours from login; an API key from Account, API keys does not expire.`)
            : status === 404 && !typed.length
              ? notFoundMessage(tried, apiBase)
              : typed.length
                ? `A chat completion with ${typed[0]} at ${tried} failed (${status}): ${providerReason(text) || 'no reason given'}`
                : `${status}: ${text.slice(0, 200)}`,
        unlockUrl,
        ...(status === 404 ? { notFound: tried } : {}),
      }
    }
    if (typed.length) {
      return { ...base, ok: true, status: 'ok', models: typed.length, message: null, checked, ...(opts.ids ? { ids: typed.slice(0, 500) } : {}) }
    }
    const data = asListing(JSON.parse(text))
    const list = Array.isArray(data.data) ? data.data : Array.isArray(data.models) ? data.models : []
    const ids = opts.ids ? list.map(m => String((m as { id?: unknown })?.id ?? '')).filter(Boolean).slice(0, 500) : undefined
    return { ...base, ok: true, status: 'ok', models: list.length, message: null, checked, ...(ids ? { ids } : {}) }
  } catch (e) {
    // A certificate or proxy refusal is not being offline.
    const conn = connectionProblem(e, gatewayHost)
    if (conn) return { ...base, ok: false, status: 'unreachable', message: conn }
    const f = networkFailure(e)
    // A server on this computer that does not answer is not running; the
    // network has nothing to do with it.
    if (f) return { ...base, ok: false, status: isLoopbackHost(gatewayHost) ? 'unreachable' : 'offline', message: unreachableMessage(gatewayHost, f) }
    return { ...base, ok: false, status: 'unreachable', message: String((e as Error).message ?? e).slice(0, 200) }
  }
}

/** What one request offering a tool showed about a model. */
export interface ToolProbe {
  /** calls: it called the tool. no-call: it answered without calling it.
   *  refused: the endpoint turned the request down because of the tool.
   *  failed: the check itself did not complete. */
  kind: 'calls' | 'no-call' | 'refused' | 'failed'
  detail: string
}

const TOOL_CHECK_MS = 15_000
const TIME_TOOL = {
  type: 'function',
  function: {
    name: 'get_utc_time',
    description: 'Returns the current date and time in UTC.',
    parameters: { type: 'object', properties: {}, required: [] },
  },
}

/**
 * Whether a model calls tools. Chat searches the libraries through tool
 * calls, and a model that cannot make them lists and answers like any
 * other, so a check of the model list passes and chat then produces text
 * with nothing behind it. This sends one chat completion offering a single
 * trivial tool with a prompt that needs it, capped and with a short timeout;
 * a stream is read only until it shows a tool call or ends. `stream` and
 * `extra` follow what chat sends the same model, so the answer holds for it.
 */
export async function probeToolCalling(model: string, o: {
  key?: string | null; baseUrl?: string | null; endpoint?: EndpointOptions | null
  stream?: boolean; extra?: Record<string, unknown>
} = {}): Promise<ToolProbe> {
  const target = o.baseUrl ? o.endpoint ?? null : null
  const url = withQuery(`${o.baseUrl || GATEWAY_BASE}/chat/completions`, target)
  const headers = { ...modelHeaders(o.key || gatewayKey(), target), 'Content-Type': 'application/json' }
  const stream = o.stream !== false
  const send = async (cap: boolean): Promise<{ status: number; text: string; called: boolean; content: string; finish?: string }> => {
    const ctl = new AbortController()
    const timer = setTimeout(() => ctl.abort(), TOOL_CHECK_MS)
    try {
      const res = await fetch(url, {
        method: 'POST', headers, signal: ctl.signal,
        body: JSON.stringify({
          model, stream, ...(cap ? { max_tokens: 256 } : {}), ...o.extra,
          messages: [{ role: 'user', content: 'What time is it in UTC right now? Call the get_utc_time tool to find out.' }],
          tools: [TIME_TOOL], tool_choice: 'auto',
        }),
      })
      if (!res.ok || !res.body) return { status: res.status, text: (await res.text()).slice(0, 4000), called: false, content: '' }
      if (!stream || !/event-stream/.test(res.headers.get('content-type') ?? 'text/event-stream')) {
        const full = await res.json() as { error?: { message?: string }; choices?: { finish_reason?: string; message?: { content?: string | null; tool_calls?: unknown[] } }[] }
        if (full.error) return { status: 500, text: JSON.stringify(full.error).slice(0, 4000), called: false, content: '' }
        const msg = full.choices?.[0]?.message ?? {}
        return { status: res.status, text: '', called: Array.isArray(msg.tool_calls) && msg.tool_calls.length > 0, content: String(msg.content ?? ''), finish: full.choices?.[0]?.finish_reason }
      }
      const reader = (res.body as ReadableStream<Uint8Array>).getReader()
      const dec = new TextDecoder()
      let buf = ''
      let content = ''
      try {
        for (;;) {
          const { value, done } = await reader.read()
          if (done) break
          buf += dec.decode(value, { stream: true })
          let nl
          while ((nl = buf.indexOf('\n')) >= 0) {
            const line = buf.slice(0, nl).trim()
            buf = buf.slice(nl + 1)
            if (!line.startsWith('data:')) continue
            const data = line.slice(5).trim()
            if (data === '[DONE]') return { status: res.status, text: '', called: false, content }
            let chunk: any
            try { chunk = JSON.parse(data) } catch { continue }
            if (chunk.error) return { status: 500, text: JSON.stringify(chunk.error).slice(0, 4000), called: false, content }
            const choice = chunk.choices?.[0]
            if ((choice?.delta?.tool_calls ?? []).some((t: any) => t?.function?.name)) return { status: res.status, text: '', called: true, content }
            if (typeof choice?.delta?.content === 'string') content += choice.delta.content
            if (choice?.finish_reason || content.length > 4000) return { status: res.status, text: '', called: false, content, finish: choice?.finish_reason }
          }
        }
      } finally {
        try { ctl.abort() } catch { /* already closed */ }
        try { reader.releaseLock() } catch { /* already released */ }
      }
      return { status: res.status, text: '', called: false, content }
    } finally {
      clearTimeout(timer)
    }
  }
  try {
    let r = await send(true)
    // A provider that refuses any token cap: ask again without one.
    if (r.status === 400 && rejectedParameter(r.text) && /max_tokens|max_output_tokens|max_completion_tokens/.test(r.text)) r = await send(false)
    if (r.status >= 200 && r.status < 300) {
      if (r.called) return { kind: 'calls', detail: '' }
      // A model that reasons first can spend the cap before it acts; with no
      // text either, that says nothing about tool calling.
      if (r.finish === 'length' && !r.content.trim()) return { kind: 'failed', detail: 'the model used its whole allowance for the check before calling the tool or answering' }
      // A server that does not parse tool calls for this model passes the
      // model's attempt through as text.
      return { kind: 'no-call', detail: /get_utc_time/.test(r.content) ? 'it wrote the call as text instead of making it' : '' }
    }
    const reason = providerReason(r.text) || `status ${r.status}`
    if ((r.status === 400 || r.status === 422 || r.status === 500) && /tool|function/i.test(r.text)) return { kind: 'refused', detail: reason }
    return { kind: 'failed', detail: `${r.status}: ${reason}` }
  } catch (e) {
    const name = (e as Error)?.name
    if (name === 'AbortError' || name === 'TimeoutError') return { kind: 'failed', detail: `no answer within ${TOOL_CHECK_MS / 1000} seconds` }
    return { kind: 'failed', detail: String((e as Error)?.message ?? e).slice(0, 200) }
  }
}

/**
 * One streamed chat.completions turn. Tool-call deltas are accumulated and
 * returned whole; content/reasoning deltas are forwarded as they arrive.
 */
/** An error the upstream serve delivered inside an SSE stream after the 200
 *  was already committed (e.g. a tool-call parse failure mid-generation).
 *  These are sampling-dependent, so a retry of the same turn is reasonable. */
export class StreamedTurnError extends Error {}

export async function streamTurn(
  body: Record<string, unknown>,
  allocation: string | null,
  cb: StreamCallbacks,
  signal?: AbortSignal,
  key?: string | null,
  baseUrl?: string | null,
  endpoint?: EndpointOptions | null,
): Promise<TurnResult> {
  const target = baseUrl ? endpoint ?? null : null
  const headers: Record<string, string> = {
    ...modelHeaders(key || gatewayKey(), target),
    'Content-Type': 'application/json',
  }
  if (allocation) headers['X-Allocation'] = allocation
  const streaming = body.stream !== false
  const res = await reach(withQuery(`${baseUrl || GATEWAY_BASE}/chat/completions`, target), {
    method: 'POST',
    headers,
    body: JSON.stringify({ ...body, stream: streaming }),
    signal,
  })
  if (!res.ok || !res.body) {
    const text = await res.text()
    const err = gatewayError(res.status, text, 'chat')
    recordModelOutcome(String(body.model ?? ''), {
      message: text.slice(0, 200), auth: err.name === 'GatewayAuthError', status: res.status,
    })
    throw err
  }
  // The provider answered; whatever this model's history was, it is
  // callable now.
  recordModelOutcome(String(body.model ?? ''), null)

  // The non-streaming path exists as a fallback: vLLM's incremental output
  // parser can fail on token sequences the whole-message parser handles
  // (seen with gpt-oss tool-call headers), so a broken streamed turn is
  // retried without streaming.
  if (!streaming) {
    const full: any = await res.json()
    if (full.error) throw new StreamedTurnError(String(full.error.message ?? JSON.stringify(full.error)))
    const choice = full.choices?.[0] ?? {}
    const msg = choice.message ?? {}
    // A malformed generation can surface raw harmony control tokens as the
    // content ("<|channel|>… <|call|>", " to=functions.x…<|call|"); that is
    // not text for a user.
    if (typeof msg.content === 'string' && (/<\|(call|channel|message|constrain|end)\|/.test(msg.content) || /^\s*to=/.test(msg.content))) msg.content = ''
    if (typeof msg.content === 'string' && msg.content) cb.onContent(msg.content)
    if (typeof msg.reasoning_content === 'string' && msg.reasoning_content) cb.onReasoning?.(msg.reasoning_content)
    return {
      content: typeof msg.content === 'string' ? msg.content : '',
      reasoning: typeof msg.reasoning_content === 'string' ? msg.reasoning_content : '',
      toolCalls: (msg.tool_calls ?? []).map((tc: any, i: number) => ({
        index: i, id: tc.id, type: tc.type ?? 'function',
        function: { name: tc.function?.name ?? '', arguments: tc.function?.arguments ?? '' },
      })),
      finishReason: choice.finish_reason ?? null,
      model: full.model ?? null,
      ...(full.usage && typeof full.usage === 'object' ? { usage: full.usage as TokenUsage } : {}),
    }
  }

  let content = ''
  let reasoning = ''
  let finishReason: string | null = null
  let usage: TokenUsage | undefined
  let model: string | null = null
  const toolCalls = new Map<number, WireToolCall>()

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  // A provider can accept a request, open the stream, and send nothing.
  // Only the client's own abort ended that, so a reply could sit on its
  // spinner indefinitely. Each read now has a limit, reset by every chunk
  // and generous enough for providers that buffer a whole answer (about a
  // minute is common) or a model that thinks before its first token.
  const idleMs = Number(process.env.STUDIO_STREAM_IDLE_MS) || 300_000
  const readWithin = async () => {
    let timer: NodeJS.Timeout | undefined
    const idle = new Promise<never>((_, reject) => {
      timer = setTimeout(() => {
        reader.cancel().catch(() => {})
        reject(new Error(`The model sent nothing for ${Math.round(idleMs / 1000)} seconds, so the reply was stopped. Try again, or pick another model.`))
      }, idleMs)
    })
    try { return await Promise.race([reader.read(), idle]) } finally { clearTimeout(timer) }
  }
  for (;;) {
    const { done, value } = await readWithin()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    let nl
    while ((nl = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, nl).trim()
      buf = buf.slice(nl + 1)
      if (!line.startsWith('data:')) continue
      const data = line.slice(5).trim()
      if (data === '[DONE]') continue
      let chunk: any
      try { chunk = JSON.parse(data) } catch { continue }
      if (chunk.error) {
        const msg = typeof chunk.error === 'object' ? String(chunk.error.message ?? JSON.stringify(chunk.error)) : String(chunk.error)
        throw new StreamedTurnError(msg)
      }
      model = chunk.model ?? model
      // Some servers send a final chunk with usage and no choices; read
      // it before the choice check would skip that chunk.
      if (chunk.usage && typeof chunk.usage === 'object') usage = chunk.usage as TokenUsage
      const choice = chunk.choices?.[0]
      if (!choice) continue
      const delta = choice.delta ?? {}
      if (typeof delta.content === 'string' && delta.content) {
        content += delta.content
        cb.onContent(delta.content)
      }
      if (typeof delta.reasoning_content === 'string' && delta.reasoning_content) {
        reasoning += delta.reasoning_content
        cb.onReasoning?.(delta.reasoning_content)
      }
      for (const tc of delta.tool_calls ?? []) {
        const idx = tc.index ?? 0
        const cur = toolCalls.get(idx) ?? { index: idx, id: tc.id, type: tc.type ?? 'function', function: { name: '', arguments: '' } }
        if (tc.id) cur.id = tc.id
        if (tc.function?.name) cur.function.name += tc.function.name
        if (tc.function?.arguments) cur.function.arguments += tc.function.arguments
        toolCalls.set(idx, cur)
      }
      if (choice.finish_reason) finishReason = choice.finish_reason
    }
  }
  return { content, reasoning, toolCalls: [...toolCalls.values()], finishReason, model, ...(usage ? { usage } : {}) }
}
