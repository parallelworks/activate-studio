import { Button, Callout, type CalloutTone, CopyButton, ExternalLink, Loader, type SelectItem } from '@parallelworks/ui'

/**
 * What Settings, Accounts and Settings, Model access share: the access
 * status the server reports (GET /api/access, GET /api/me/model-key), the
 * requests that change it, and the words for each source.
 *
 * Three sources, in the order the server applies them: the person's own
 * (their ACTIVATE sign-in, made with the pw CLI's device code, before a key
 * or endpoint stored here; an endpoint still serves models), a deployment
 * credential (a key someone shared, PW_API_KEY), and the pw CLI's sign-in
 * on the server's computer. Accounts holds the credentials; Model access
 * says where models come from.
 */

export type Source = 'endpoint' | 'key' | 'signin' | 'shared' | 'env' | 'cli' | 'sidecar' | 'admin-endpoint' | 'none'
export type CliSource = 'env' | 'chosen' | 'path' | 'common' | 'none'
export interface SourceInfo { source: Source; host: string | null; context: string | null; baseUrl?: string | null; last4?: string | null; kind?: string | null }
export interface CliContext { name: string; server: string; organization: string | null; kind: 'sign-in' | 'api-key' | 'token' | null; expiresAt: string | null; expired: boolean; current: boolean; renewError: string | null }
export interface LoginState { host: string; state: 'starting' | 'waiting' | 'done' | 'failed' | 'canceled'; url: string | null; code: string | null; context: string | null; message: string | null }
export interface AccessStatus {
  single: boolean
  authEnabled: boolean
  models: SourceInfo
  platform: SourceInfo
  /** What requests fall back to without a credential of the person's own:
   *  on a Studio of one the owner's; on a Studio several people use only an
   *  OpenAI-compatible endpoint the administrator set (models), or none. */
  deployment: SourceInfo
  gatewayHost: string | null
  platformOrigin: string | null
  /** Each person can sign in with ACTIVATE (a Studio several people use,
   *  keeping personal credentials). */
  signInOffered?: boolean
  cli: {
    available: boolean; version: string | null
    /** Where the Studio found the CLI, and how. */
    path: string | null; source: CliSource; chosenPath: string | null; commonDirs: string[]
    contexts: CliContext[]; chosen: string | null; active: string | null; used: string | null; gatewayFromEnv: boolean; envKey: boolean
    /** Stop using the pw CLI: the Studio reads none of the computer's ACTIVATE credentials. */
    stopped?: boolean
  } | null
  login: LoginState | null
}
/** The header an endpoint takes its key in. */
export type EndpointAuth = 'bearer' | 'api-key' | 'x-api-key' | 'none'
/** A stored endpoint's options as the server reports them: header names,
 *  never their values. */
export interface EndpointSummary { auth: EndpointAuth; headers: string[]; models: string[]; query: string }
export interface KeyStatus {
  authEnabled?: boolean; single?: boolean; verified: boolean; mode: 'stored' | 'session' | 'none'
  last4?: string; addedAt?: string; sessionExpiresAt?: string; kind?: string; credExpiresAt?: string | null; credExpired?: boolean
  baseUrl?: string | null; gatewayHost?: string; endpoint?: EndpointSummary | null
  shared?: { active: boolean; last4?: string; sharedBy?: string; sharedAt?: string; credExpired?: boolean; mine?: boolean }
  sharingAllowed?: boolean
}
export interface Health { status: string; models: number; gatewayHost?: string; message?: string | null; platformAccess?: boolean | null; unlockUrl?: string | null }
/** Whether chat can use tools with the model a test used. */
export interface ToolCheck { model: string; result: 'calls' | 'no-call' | 'refused' | 'failed' | 'emulated'; message: string }
export interface KeyTestResult extends Health { ids?: string[]; checked?: 'models' | 'completion'; notFound?: string; toolCheck?: ToolCheck }
export type Note = { tone: CalloutTone; text: string; detail?: string; action?: { label: string; run: () => void } }
export interface Device { url: string; code: string }
export interface Subscriptions {
  available: boolean; reason?: string
  codex?: { offered: boolean; providers: { name: string; displayName: string | null; account: string | null }[] }
  copilot?: { offered: boolean; providers: { name: string; displayName: string | null; account: string | null }[]; github: { linked: boolean; username: string | null; appConfigured: boolean; tokensAllowed: boolean } | null }
}

/** A key for an endpoint that takes none (a local Ollama or vLLM), which
 *  ignores the bearer token the Studio sends. The setup wizard stores the same. */
export const NO_KEY = 'none'

export const PRESETS: { value: string; label: string; url: string }[] = [
  { value: 'ollama', label: 'Ollama on this computer', url: 'http://127.0.0.1:11434/v1' },
  { value: 'lmstudio', label: 'LM Studio on this computer', url: 'http://127.0.0.1:1234/v1' },
  { value: 'vllm', label: 'vLLM on this computer', url: 'http://127.0.0.1:8000/v1' },
  { value: 'openai', label: 'OpenAI API (API key)', url: 'https://api.openai.com/v1' },
  { value: 'github', label: 'GitHub Models (token with models:read)', url: 'https://models.github.ai/inference' },
  { value: 'other', label: 'Other, such as a site AI portal', url: '' },
]
/** A server on this computer takes no key; anything else needs one. */
export const isLocalUrl = (url: string) => /^https?:\/\/(localhost|127\.0\.0\.1|\[::1\])(:\d+)?(\/|$)/i.test(url.trim())
export const presetOf = (url: string | null | undefined) => PRESETS.find(p => p.url && p.url === String(url ?? '').replace(/\/+$/, ''))?.value ?? 'other'

export const AUTH_ITEMS: SelectItem<EndpointAuth>[] = [
  { value: 'bearer', label: 'Authorization: Bearer <key>' },
  { value: 'api-key', label: 'api-key: <key>' },
  { value: 'x-api-key', label: 'x-api-key: <key>' },
  { value: 'none', label: 'None: no key is sent' },
]

/** An endpoint's base URL from an address that names one of its paths, as
 *  a portal's documentation often gives it (…/v1/chat/completions). */
export function endpointBase(url: string): string {
  return url.trim().replace(/\/+$/, '').replace(/\/(chat\/completions|completions|models|embeddings|responses)$/, '')
}

/**
 * What was typed as the host on the ACTIVATE API key tab. A platform host
 * becomes its model gateway, https://<host>/api/openai/v1 (http only when
 * typed so). An address with
 * any other path (…/v1, …/v1/chat/completions) is an API, so it is read as
 * the base URL of an OpenAI-compatible endpoint instead: a site's AI portal
 * entered on the wrong tab, which the platform path can only answer with 404.
 */
export function readPlatformHost(typed: string): { host: string; origin: string; platformUrl: string; endpointUrl: string | null } {
  const t = typed.trim()
  if (!t) return { host: '', origin: '', platformUrl: '', endpointUrl: null }
  let u: URL
  try { u = new URL(/^[a-z][a-z0-9+.-]*:\/\//i.test(t) ? t : `https://${t}`) } catch {
    const host = t.replace(/^[a-z]+:\/\//i, '').replace(/\/.*$/, '')
    return { host, origin: `https://${host}`, platformUrl: `https://${host}/api/openai/v1`, endpointUrl: null }
  }
  const origin = `${u.protocol === 'http:' ? 'http:' : 'https:'}//${u.host}`
  const path = u.pathname.replace(/\/+$/, '')
  const api = !!path && !/^\/api\/openai(\/v1)?$/.test(path)
  return { host: u.host, origin, platformUrl: `${origin}/api/openai/v1`, endpointUrl: api ? endpointBase(`${origin}${path}`) : null }
}

/** A key test's outcome as the note under the form: whether the endpoint
 *  answered, and, when it did, whether chat can use tools with its model. */
export function testNote(h: KeyTestResult, prefix = ''): Note {
  if (h.status !== 'ok') return { tone: h.status === 'offline' ? 'warning' : 'error', text: `${prefix}${h.message ?? `The check failed (${h.status}).`}` }
  const what = h.checked === 'completion'
    ? `a chat completion with ${h.ids?.[0] ?? 'the first model'} answered`
    : `${h.models} model${h.models === 1 ? '' : 's'} available`
  const text = `${prefix}Connected: ${what}.`
  if (!h.toolCheck) return { tone: 'success', text }
  const r = h.toolCheck.result
  return { tone: r === 'calls' ? 'success' : r === 'no-call' || r === 'refused' ? 'warning' : 'info', text, detail: h.toolCheck.message }
}

/** The chat's own remembered model, which a test checks tool calling with
 *  when the endpoint lists it. */
export function chatModel(): string | undefined {
  try { const v = JSON.parse(localStorage.getItem('aiChat.model') ?? 'null'); return typeof v === 'string' && v ? v : undefined } catch { return undefined }
}

export interface HeaderRow { name: string; value: string; stored: boolean }

export async function api<T>(url: string, init?: { method?: string; body?: unknown }): Promise<T> {
  const res = await fetch(url, init ? {
    method: init.method ?? 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(init.body ?? {}),
  } : undefined)
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((data as { error?: string }).error ?? `${res.status}`)
  return data as T
}

export const errText = (e: unknown) => String((e as Error)?.message ?? e)

/** Tell the rest of the app: the chat drops its cached model list, and the
 *  app's configuration (whether the Workflows page shows) is read again. */
export function announceChange(): void {
  window.dispatchEvent(new CustomEvent('ade:model-access-changed'))
}

/** One source in words, for the banner. */
export function describeSource(s: SourceInfo, single: boolean): string {
  const at = s.host ? ` (${s.host})` : ''
  switch (s.source) {
    case 'cli': return s.context ? `pw CLI context ${s.context}${at}` : `the pw CLI sign-in on the server${at}`
    case 'signin': return `your ACTIVATE sign-in${at}`
    case 'key': return `your ${s.kind === 'token' ? 'token' : 'key'}${s.last4 ? ` ending ${s.last4}` : ''}${at}`
    case 'endpoint': return `the endpoint ${s.baseUrl ?? s.host ?? ''}`
    case 'shared': return `the key${s.last4 ? ` ending ${s.last4}` : ''} shared with everyone here${at}`
    case 'env': return `${single ? 'PW_API_KEY in the Studio\'s environment' : 'the deployment credential'}${at}`
    case 'sidecar': return 'the model served beside this Studio'
    case 'admin-endpoint': return `the endpoint the administrator set${s.baseUrl ? ` (${s.baseUrl})` : at}`
    default: return 'none'
  }
}

export const KIND_LABEL: Record<string, string> = { 'sign-in': 'Sign-in', 'api-key': 'API key', token: 'Platform token' }

/** Which platform credential the Studio uses for the viewer: signing in,
 *  signing out, or changing the key changes it, the host alone may not. */
export const platformCredential = (s: SourceInfo) => [s.source, s.host ?? '', s.last4 ?? '', s.kind ?? ''].join(':')

/** A code to enter at an address, while something waits for it. */
export function DeviceCode({ device, what, onCancel }: { device: Device; what: string; onCancel: () => void }) {
  return (
    <Callout className="rag-banner device-code" tone="info" live title={what}
      action={<Button variant="secondary" size="sm" onClick={onCancel}>Cancel</Button>}>
      <p className="identity-note">Open <ExternalLink href={device.url}>{device.url}</ExternalLink> on any device and enter this code:</p>
      <div className="device-code-row">
        <code className="device-code-value">{device.code}</code>
        <CopyButton text={device.code} label="code" />
      </div>
      <p className="key-note"><Loader inline size={14} label="Waiting for approval…" /></p>
    </Callout>
  )
}
