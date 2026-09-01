import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { GATEWAY_BASE, PW_API_KEY, SIDECAR_BASE, SIDECAR_KEY } from '../config.js'
import { resolveSharedKey } from '../credentials.js'

/**
 * Gateway credential: PW_API_KEY env when set, otherwise the pw CLI's own
 * credential for the gateway host from ~/.config/pw/credentials (the CLI
 * refreshes it; we re-read when the file changes). Never sent to the browser.
 */
let credCache: { key: string; mtimeMs: number } | null = null

function credentialFromCliStore(): string {
  const file = path.join(os.homedir(), '.config', 'pw', 'credentials')
  try {
    const st = fs.statSync(file)
    if (credCache && credCache.mtimeMs === st.mtimeMs) return credCache.key
    const data = JSON.parse(fs.readFileSync(file, 'utf8'))
    const host = new URL(GATEWAY_BASE).host
    let key = ''
    // The active context (PW_CONTEXT, then currentIdentity) wins when it is
    // for this host; several identities can share a server, and the current
    // one is the credential the CLI itself would use.
    const current = data.identities?.[process.env.PW_CONTEXT || data.currentIdentity]
    if (current?.server === host) {
      key = current.token || current.apikey || ''
    } else {
      for (const ident of Object.values<any>(data.identities ?? {})) {
        if (ident?.server === host) { key = ident.token || ident.apikey || ''; break }
      }
    }
    credCache = { key, mtimeMs: st.mtimeMs }
    return key
  } catch {
    return ''
  }
}

export function gatewayKey(): string {
  // A key a user has promoted for everyone stands in for the deployment
  // credential, and wins over the ambient one: it was chosen deliberately,
  // in the app, by someone who wanted the group to use it.
  return resolveSharedKey() || PW_API_KEY || credentialFromCliStore()
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

export interface TurnResult {
  content: string
  reasoning: string
  toolCalls: WireToolCall[]
  finishReason: string | null
  model: string | null
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

export async function listModels(key?: string | null, baseUrl?: string | null): Promise<unknown> {
  const res = await fetch(`${baseUrl || GATEWAY_BASE}/models`, {
    headers: { Authorization: `Bearer ${key || gatewayKey()}` },
  })
  if (!res.ok) throw gatewayError(res.status, await res.text(), '/models')
  return res.json()
}

export interface AiHealth {
  ok: boolean
  status: 'ok' | 'auth' | 'unreachable' | 'unconfigured'
  models: number
  gatewayHost: string
  message: string | null
  lastCallFailure: { at: string; message: string; auth: boolean } | null
  checkedAt: string
  /** Present when the provider reports a locked key with its unlock link. */
  unlockUrl?: string | null
}

/** Live callability check: exercises the gateway with the current
 *  credential (via the model listing) and reports the last real call
 *  failure, which catches keys that list fine but fail at invoke time. */
/**
 * Provider liveness at listing time. The gateway lists a registered
 * provider's models from its registry without asking the provider, so a
 * locked GenAI key produced a picker full of selectable models that could
 * only fail. The probe asks the provider itself: for a personal custom
 * provider that is a GET of its /models (free), and for gateway-registered
 * providers a one-token completion on one model of that provider, which is
 * the only request shape the gateway forwards. Cached per provider and
 * key, since a lock lasts hours and the picker refreshes often; probing is
 * limited to providers matching STUDIO_PROBE_PROVIDERS (default genai),
 * because a one-token call per provider is a real request and the locking
 * behaviour this exists for is GenAI's.
 */
export interface ProviderVerdict { ok: boolean; unlockUrl: string | null; message: string }
const providerProbes = new Map<string, { at: number; v: ProviderVerdict }>()
const PROBE_TTL_MS = 5 * 60_000
const PROBE_PATTERN = new RegExp(process.env.STUDIO_PROBE_PROVIDERS || 'genai', 'i')

export function probeableProvider(prefix: string): boolean { return PROBE_PATTERN.test(prefix) }

export function extractUnlockUrl(text: string): string | null {
  return /unlock_url[\\":\s]*(https?:\/\/[^"\\\s]+)/.exec(text)?.[1] ?? null
}

export async function probeProvider(prefix: string, sampleModelId: string, key?: string | null): Promise<ProviderVerdict> {
  const cacheKey = `${prefix}:${(key ?? '').slice(-6)}`
  const hit = providerProbes.get(cacheKey)
  if (hit && Date.now() - hit.at < PROBE_TTL_MS) return hit.v
  let v: ProviderVerdict = { ok: true, unlockUrl: null, message: '' }
  try {
    const res = await fetch(`${GATEWAY_BASE}/chat/completions`, {
      method: 'POST',
      headers: { Authorization: `Bearer ${key || gatewayKey()}`, 'Content-Type': 'application/json' },
      body: JSON.stringify({ model: sampleModelId, messages: [{ role: 'user', content: 'ping' }], max_tokens: 1, stream: false }),
      signal: AbortSignal.timeout(6000),
    })
    const text = await res.text()
    if (!res.ok || /"error"/.test(text.slice(0, 40))) {
      const unlockUrl = extractUnlockUrl(text)
      // Only a recognizable credential failure marks the provider: a
      // transient generation error must not grey out a whole provider.
      const credentialish = unlockUrl !== null || res.status === 401 || res.status === 403 || /locked|unauthorized|api key/i.test(text)
      if (credentialish) v = { ok: false, unlockUrl, message: text.slice(0, 200) }
    }
  } catch { /* unreachable is not proof of a lock; leave ok */ }
  providerProbes.set(cacheKey, { at: Date.now(), v })
  return v
}

export async function aiHealth(key?: string | null, baseUrl?: string | null): Promise<AiHealth> {
  const checkedAt = new Date().toISOString()
  const apiBase = baseUrl || GATEWAY_BASE
  const gatewayHost = (() => { try { return new URL(apiBase).host } catch { return apiBase } })()
  const base = { models: 0, gatewayHost, lastCallFailure, checkedAt }
  if (!key && !gatewayConfigured()) {
    return { ...base, ok: false, status: 'unconfigured', message: 'No gateway credential configured (PW_API_KEY or pw CLI login).' }
  }
  try {
    const res = await fetch(`${baseUrl || GATEWAY_BASE}/models`, {
      headers: { Authorization: `Bearer ${key || gatewayKey()}` },
      signal: AbortSignal.timeout(8000),
    })
    const text = await res.text()
    if (!res.ok) {
      const auth = res.status === 401 || res.status === 403
      // GenAI.mil locks keys every 8 hours and its 401 body carries the
      // unlock URL. That link is the remedy, so it is extracted before the
      // body is truncated for display and carried as its own field.
      const unlockUrl = /unlock_url[\\":\s]*(https?:\/\/[^"\\\s]+)/.exec(text)?.[1] ?? null
      return {
        ...base, ok: false, status: auth ? 'auth' : 'unreachable',
        message: unlockUrl
          ? 'The provider locked this API key (it does so on a schedule); unlock it and re-check.'
          : `${res.status}: ${text.slice(0, 200)}`,
        unlockUrl,
      }
    }
    const data = JSON.parse(text) as { data?: unknown[]; models?: unknown[] }
    const n = Array.isArray(data.data) ? data.data.length : Array.isArray(data.models) ? data.models.length : 0
    return { ...base, ok: true, status: 'ok', models: n, message: null }
  } catch (e) {
    return { ...base, ok: false, status: 'unreachable', message: String((e as Error).message ?? e).slice(0, 200) }
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
): Promise<TurnResult> {
  const headers: Record<string, string> = {
    Authorization: `Bearer ${key || gatewayKey()}`,
    'Content-Type': 'application/json',
  }
  if (allocation) headers['X-Allocation'] = allocation
  const streaming = body.stream !== false
  const res = await fetch(`${baseUrl || GATEWAY_BASE}/chat/completions`, {
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
    }
  }

  let content = ''
  let reasoning = ''
  let finishReason: string | null = null
  let model: string | null = null
  const toolCalls = new Map<number, WireToolCall>()

  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  for (;;) {
    const { done, value } = await reader.read()
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
  return { content, reasoning, toolCalls: [...toolCalls.values()], finishReason, model }
}
