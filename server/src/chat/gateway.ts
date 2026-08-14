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

export async function listModels(): Promise<unknown> {
  const res = await fetch(`${GATEWAY_BASE}/models`, {
    headers: { Authorization: `Bearer ${gatewayKey()}` },
  })
  if (!res.ok) throw new Error(`gateway /models ${res.status}: ${await res.text()}`)
  return res.json()
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
): Promise<TurnResult> {
  const headers: Record<string, string> = {
    Authorization: `Bearer ${gatewayKey()}`,
    'Content-Type': 'application/json',
  }
  if (allocation) headers['X-Allocation'] = allocation
  const res = await fetch(`${GATEWAY_BASE}/chat/completions`, {
    method: 'POST',
    headers,
    body: JSON.stringify({ ...body, stream: true }),
    signal,
  })
  if (!res.ok || !res.body) throw new Error(`gateway chat ${res.status}: ${await res.text()}`)

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
