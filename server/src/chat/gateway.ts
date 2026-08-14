import fs from 'node:fs'
import os from 'node:os'
import path from 'node:path'
import { GATEWAY_BASE, PW_API_KEY } from '../config.js'

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
    for (const ident of Object.values<any>(data.identities ?? {})) {
      if (ident?.server === host) { key = ident.token || ident.apikey || ''; break }
    }
    credCache = { key, mtimeMs: st.mtimeMs }
    return key
  } catch {
    return ''
  }
}

export function gatewayKey(): string {
  return PW_API_KEY || credentialFromCliStore()
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

/** Credential rejections get their own error type and an actionable
 *  message, so the UI can say "unlock your key" instead of a raw 401. */
export class GatewayAuthError extends Error {
  constructor(message: string) { super(message); this.name = 'GatewayAuthError' }
}

let lastCallFailure: { at: string; message: string; auth: boolean } | null = null

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
}

/** Live callability check: exercises the gateway with the current
 *  credential (via the model listing) and reports the last real call
 *  failure, which catches keys that list fine but fail at invoke time. */
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
      return { ...base, ok: false, status: auth ? 'auth' : 'unreachable', message: `${res.status}: ${text.slice(0, 200)}` }
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
  const res = await fetch(`${baseUrl || GATEWAY_BASE}/chat/completions`, {
    method: 'POST',
    headers,
    body: JSON.stringify({ ...body, stream: true }),
    signal,
  })
  if (!res.ok || !res.body) throw gatewayError(res.status, await res.text(), 'chat')

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
