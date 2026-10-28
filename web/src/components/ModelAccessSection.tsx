import { type ReactNode, useEffect, useRef, useState } from 'react'
import {
  Button, Callout, type CalloutTone, Checkbox, CopyButton, Disclosure, ExternalLink, Field, IconButton, Input, Loader, SegmentedControl, Select,
  type SelectItem, StatusBadge, Table, Textarea,
} from '@parallelworks/ui'
import { CloseIcon } from '@parallelworks/ui/icons'
import './ModelAccessSection.css'

/**
 * Settings, Model access: where the Studio's model calls and platform calls
 * get their credential, and the controls that change it.
 *
 * Three sources, in the order the server applies them: a key or endpoint
 * stored here, a deployment credential (a key someone shared, PW_API_KEY),
 * and the pw CLI's sign-in on the server's computer. On a Studio with
 * sign-in off the CLI is the one user's own, so its contexts are listed and
 * one can be chosen, or the CLI signed in to another platform with its
 * device code. Platform subscriptions (a ChatGPT plan, GitHub Copilot) are
 * connected through ACTIVATE, which then serves their models with the rest.
 * An endpoint can carry options for a portal that differs from the OpenAI
 * shape (EndpointAdvanced), and Test also says whether its model calls
 * tools, which chat needs to search the libraries.
 */

type Source = 'endpoint' | 'key' | 'shared' | 'env' | 'cli' | 'sidecar' | 'none'
type CliSource = 'env' | 'chosen' | 'path' | 'common' | 'none'
interface SourceInfo { source: Source; host: string | null; context: string | null; baseUrl?: string | null; last4?: string | null; kind?: string | null }
interface CliContext { name: string; server: string; organization: string | null; kind: 'sign-in' | 'api-key' | 'token' | null; expiresAt: string | null; expired: boolean; current: boolean; renewError: string | null }
interface LoginState { host: string; state: 'starting' | 'waiting' | 'done' | 'failed' | 'canceled'; url: string | null; code: string | null; context: string | null; message: string | null }
export interface AccessStatus {
  single: boolean
  authEnabled: boolean
  models: SourceInfo
  platform: SourceInfo
  /** What requests fall back to without a key of the person's own. */
  deployment: SourceInfo
  gatewayHost: string | null
  platformOrigin: string | null
  cli: {
    available: boolean; version: string | null
    /** Where the Studio found the CLI, and how. */
    path: string | null; source: CliSource; chosenPath: string | null; commonDirs: string[]
    contexts: CliContext[]; chosen: string | null; active: string | null; used: string | null; gatewayFromEnv: boolean; envKey: boolean
  } | null
  login: LoginState | null
}
/** The header an endpoint takes its key in. */
export type EndpointAuth = 'bearer' | 'api-key' | 'x-api-key' | 'none'
/** A stored endpoint's options as the server reports them: header names,
 *  never their values. */
interface EndpointSummary { auth: EndpointAuth; headers: string[]; models: string[]; query: string }
interface KeyStatus {
  authEnabled?: boolean; single?: boolean; verified: boolean; mode: 'stored' | 'session' | 'none'
  last4?: string; addedAt?: string; sessionExpiresAt?: string; kind?: string; credExpiresAt?: string | null; credExpired?: boolean
  baseUrl?: string | null; gatewayHost?: string; endpoint?: EndpointSummary | null
  shared?: { active: boolean; last4?: string; sharedBy?: string; sharedAt?: string; credExpired?: boolean; mine?: boolean }
  sharingAllowed?: boolean
}
interface Health { status: string; models: number; gatewayHost?: string; message?: string | null; platformAccess?: boolean | null; unlockUrl?: string | null }
/** Whether chat can use tools with the model a test used. */
export interface ToolCheck { model: string; result: 'calls' | 'no-call' | 'refused' | 'failed' | 'emulated'; message: string }
export interface KeyTestResult extends Health { ids?: string[]; checked?: 'models' | 'completion'; notFound?: string; toolCheck?: ToolCheck }
type Note = { tone: CalloutTone; text: string; detail?: string; action?: { label: string; run: () => void } }
interface Device { url: string; code: string }
interface Subscriptions {
  available: boolean; reason?: string
  codex?: { offered: boolean; providers: { name: string; displayName: string | null; account: string | null }[] }
  copilot?: { offered: boolean; providers: { name: string; displayName: string | null; account: string | null }[]; github: { linked: boolean; username: string | null; appConfigured: boolean; tokensAllowed: boolean } | null }
}

/** A key for an endpoint that takes none (a local Ollama or vLLM), which
 *  ignores the bearer token the Studio sends. The setup wizard stores the same. */
const NO_KEY = 'none'

type Tab = 'cli' | 'deployment' | 'key' | 'endpoint'

const PRESETS: { value: string; label: string; url: string }[] = [
  { value: 'ollama', label: 'Ollama on this computer', url: 'http://127.0.0.1:11434/v1' },
  { value: 'lmstudio', label: 'LM Studio on this computer', url: 'http://127.0.0.1:1234/v1' },
  { value: 'vllm', label: 'vLLM on this computer', url: 'http://127.0.0.1:8000/v1' },
  { value: 'openai', label: 'OpenAI API (API key)', url: 'https://api.openai.com/v1' },
  { value: 'github', label: 'GitHub Models (token with models:read)', url: 'https://models.github.ai/inference' },
  { value: 'other', label: 'Other, such as a site AI portal', url: '' },
]
/** A server on this computer takes no key; anything else needs one. */
export const isLocalUrl = (url: string) => /^https?:\/\/(localhost|127\.0\.0\.1|\[::1\])(:\d+)?(\/|$)/i.test(url.trim())
const presetOf = (url: string | null | undefined) => PRESETS.find(p => p.url && p.url === String(url ?? '').replace(/\/+$/, ''))?.value ?? 'other'

const AUTH_ITEMS: SelectItem<EndpointAuth>[] = [
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
function chatModel(): string | undefined {
  try { const v = JSON.parse(localStorage.getItem('aiChat.model') ?? 'null'); return typeof v === 'string' && v ? v : undefined } catch { return undefined }
}

interface HeaderRow { name: string; value: string; stored: boolean }

async function api<T>(url: string, init?: { method?: string; body?: unknown }): Promise<T> {
  const res = await fetch(url, init ? {
    method: init.method ?? 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(init.body ?? {}),
  } : undefined)
  const data = await res.json().catch(() => ({}))
  if (!res.ok) throw new Error((data as { error?: string }).error ?? `${res.status}`)
  return data as T
}

const errText = (e: unknown) => String((e as Error)?.message ?? e)

/** Tell the rest of the app: the chat drops its cached model list, and the
 *  app's configuration (whether the Workflows page shows) is read again. */
function announceChange(): void {
  window.dispatchEvent(new CustomEvent('ade:model-access-changed'))
}

/** One source in words, for the banner. */
export function describeSource(s: SourceInfo, single: boolean): string {
  const at = s.host ? ` (${s.host})` : ''
  switch (s.source) {
    case 'cli': return s.context ? `pw CLI context ${s.context}${at}` : `the pw CLI sign-in on the server${at}`
    case 'key': return `your ${s.kind === 'token' ? 'token' : 'key'}${s.last4 ? ` ending ${s.last4}` : ''}${at}`
    case 'endpoint': return `the endpoint ${s.baseUrl ?? s.host ?? ''}`
    case 'shared': return `the key${s.last4 ? ` ending ${s.last4}` : ''} shared with everyone here${at}`
    case 'env': return `${single ? 'PW_API_KEY in the Studio\'s environment' : 'the deployment credential'}${at}`
    case 'sidecar': return 'the model served beside this Studio'
    default: return 'none'
  }
}

const KIND_LABEL: Record<string, string> = { 'sign-in': 'Sign-in', 'api-key': 'API key', token: 'Platform token' }

export function ModelAccessSection({ requirePersonalKey = false }: { requirePersonalKey?: boolean }) {
  const [access, setAccess] = useState<AccessStatus | null>(null)
  const [me, setMe] = useState<KeyStatus | null>(null)
  const [health, setHealth] = useState<Health | null>(null)
  const [models, setModels] = useState<string[] | null>(null)
  const [modelsError, setModelsError] = useState('')
  const [checking, setChecking] = useState(false)
  const [loadError, setLoadError] = useState('')
  const [tab, setTab] = useState<Tab | null>(null)
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState<Note | null>(null)

  // Key and endpoint forms.
  const [keyInput, setKeyInput] = useState('')
  const [keyHost, setKeyHost] = useState('')
  const [persistKey, setPersistKey] = useState(true)
  const [preset, setPreset] = useState('ollama')
  const [baseUrl, setBaseUrl] = useState(PRESETS[0].url)
  // The endpoint's Advanced options. Stored header values never come back
  // from the server: a stored header shows its name, and an empty value
  // keeps what is stored.
  const [auth, setAuth] = useState<EndpointAuth>('bearer')
  const [headerRows, setHeaderRows] = useState<HeaderRow[]>([])
  const [modelNames, setModelNames] = useState('')
  const [query, setQuery] = useState('')
  const [advanced, setAdvanced] = useState(false)
  const filled = useRef(false)
  const fillEndpoint = (e: EndpointSummary | null | undefined) => {
    setAuth(e?.auth ?? 'bearer')
    setHeaderRows((e?.headers ?? []).map(name => ({ name, value: '', stored: true })))
    setModelNames((e?.models ?? []).join('\n'))
    setQuery(e?.query ?? '')
  }

  // Signing the CLI in.
  const [loginHost, setLoginHost] = useState('')
  const [login, setLogin] = useState<LoginState | null>(null)

  const load = async (fresh = false) => {
    setChecking(true)
    try {
      const [a, k] = await Promise.all([api<AccessStatus>(`/api/access${fresh ? '?fresh=1' : ''}`), api<KeyStatus>('/api/me/model-key')])
      setAccess(a)
      setMe(k)
      setLoadError('')
      setLogin(l => a.login ?? l)
      setTab(t => t ?? (a.models.source === 'endpoint' ? 'endpoint' : a.models.source === 'key' ? 'key' : a.single ? 'cli' : 'deployment'))
      if (!filled.current && a.models.source === 'endpoint' && a.models.baseUrl) {
        setPreset(presetOf(a.models.baseUrl)); setBaseUrl(a.models.baseUrl); fillEndpoint(k.endpoint)
      }
      filled.current = true
      setLoginHost(h => h || a.login?.host || (a.cli?.contexts.find(c => c.name === a.cli?.active)?.server ?? ''))
      const [h, m] = await Promise.all([
        api<Health>(`/api/ai/health${fresh ? '?fresh=1' : ''}`).catch(() => null),
        api<{ models?: { id: string }[]; error?: string }>(`/api/chat/models${fresh ? '?refresh=1' : ''}`).catch(e => ({ models: [], error: errText(e) })),
      ])
      setHealth(h)
      setModels((m.models ?? []).map(x => x.id).filter(id => !/studio-(agent|rag)/.test(id)))
      setModelsError(m.error ?? '')
    } catch (e) {
      setLoadError(errText(e))
    } finally {
      setChecking(false)
    }
  }
  useEffect(() => { void load() }, [])

  // A device sign-in under way: ask how it stands until it settles.
  useEffect(() => {
    if (login?.state !== 'waiting' && login?.state !== 'starting') return
    const id = window.setInterval(() => {
      api<{ login: LoginState | null }>('/api/access/cli/login').then(r => {
        setLogin(r.login)
        if (r.login?.state === 'done') { announceChange(); void load(true) }
      }).catch(() => {})
    }, 1500)
    return () => window.clearInterval(id)
  }, [login?.state])

  const run = async (what: () => Promise<unknown>, done?: string) => {
    setBusy(true)
    setNote(null)
    try {
      await what()
      announceChange()
      if (done) setNote({ tone: 'success', text: done })
      await load(true)
      return true
    } catch (e) {
      setNote({ tone: 'error', text: errText(e) })
      return false
    } finally {
      setBusy(false)
    }
  }

  const pickContext = (name: string | null) => {
    const clearKey = access?.models.source === 'key' || access?.models.source === 'endpoint'
    return run(
      () => api('/api/access/source', { method: 'PUT', body: { context: name, clearKey } }),
      (name ? `The Studio now uses the pw CLI context ${name}.` : 'The Studio now follows the pw CLI\'s current context.')
        + (clearKey ? ` Your ${access?.models.source === 'endpoint' ? 'endpoint' : 'key'} was removed.` : ''),
    )
  }
  /** The Advanced options as the server takes them: a stored header left
   *  empty is sent by name alone, which keeps its stored value. */
  const endpointBody = () => ({
    auth,
    headers: headerRows.filter(h => h.name.trim() || h.value.trim()).map(h => ({ name: h.name.trim(), ...(h.value.trim() ? { value: h.value.trim() } : {}) })),
    models: modelNames.split(/[\n,]/).map(m => m.trim()).filter(Boolean),
    query: query.trim(),
  })
  const saveKey = (key: string, url: string, endpoint?: ReturnType<typeof endpointBody>) => run(
    async () => {
      const st = await api<KeyStatus>('/api/me/model-key', { body: { key, persist: persistKey, baseUrl: url, ...(endpoint ? { endpoint } : {}) } })
      if (endpoint) fillEndpoint(st.endpoint)
    },
    'Saved.',
  )
  const removeKey = () => run(async () => { await api('/api/me/model-key/clear', { body: {} }); fillEndpoint(null) }, 'Removed.')
  const test = (body: Record<string, unknown>) =>
    api<KeyTestResult>('/api/me/model-key/test', { body: { ...body, ...(chatModel() ? { model: chatModel() } : {}) } })
  const testCandidate = async (key: string, url: string, endpoint?: ReturnType<typeof endpointBody>) => {
    setBusy(true)
    setNote(null)
    try {
      setNote(testNote(await test({ key, baseUrl: url || undefined, ...(endpoint ? { endpoint } : {}) })))
    } catch (e) {
      setNote({ tone: 'error', text: errText(e) })
    } finally {
      setBusy(false)
    }
  }
  /** An endpoint address typed on the ACTIVATE API key tab, moved to the
   *  OpenAI-compatible endpoint tab with the key that came with it. */
  const moveToEndpoint = (url: string) => {
    // A stored endpoint's options belong to its own address.
    if (access?.models.source !== 'endpoint' || access.models.baseUrl !== url) fillEndpoint(null)
    setBaseUrl(url)
    setPreset(presetOf(url))
    setKeyHost('')
    setTab('endpoint')
    setNote({ tone: 'info', text: `Moved to OpenAI-compatible endpoint, with ${url} as the base URL and the key you entered. Test it here, then Save and use.` })
  }
  /** Test on the ACTIVATE API key tab. An address with a path is tested as
   *  the endpoint it is. A host whose platform path does not exist is asked
   *  once more at /v1, so the note can say what the host is. */
  const testKeyTab = async () => {
    const typed = readPlatformHost(keyHost)
    const key = keyInput.trim()
    setBusy(true)
    setNote(null)
    try {
      if (typed.endpointUrl) {
        const url = typed.endpointUrl
        const h = await test({ key, baseUrl: url })
        setNote({ ...testNote(h, `Tested as an OpenAI-compatible endpoint at ${url}, since the address has a path. `), action: { label: 'Use as an endpoint', run: () => moveToEndpoint(url) } })
        return
      }
      const h = await test({ key, baseUrl: typed.platformUrl || undefined })
      if (!h.notFound || !typed.host) { setNote(testNote(h)); return }
      const v1 = `${typed.origin}/v1`
      const alt = await test({ key, baseUrl: v1, tools: false }).catch(() => null)
      const answers = alt && (alt.status === 'ok' || alt.status === 'auth')
      setNote({
        tone: 'warning',
        text: `${typed.host} is not an ACTIVATE platform, since ${h.notFound} does not exist (404).`,
        detail: answers
          ? `${v1} answers as an OpenAI-compatible endpoint${alt!.status === 'ok' ? ` with ${alt!.models} model${alt!.models === 1 ? '' : 's'}` : ', and refused this key'}. Use it under OpenAI-compatible endpoint.`
          : 'A site AI portal or another OpenAI-compatible service goes under OpenAI-compatible endpoint, with its base URL.',
        action: { label: answers ? 'Use as an endpoint' : 'Open OpenAI-compatible endpoint', run: () => moveToEndpoint(answers ? v1 : typed.origin) },
      })
    } catch (e) {
      setNote({ tone: 'error', text: errText(e) })
    } finally {
      setBusy(false)
    }
  }
  const startLogin = async () => {
    setNote(null)
    setBusy(true)
    try {
      const r = await api<{ login: LoginState }>('/api/access/cli/login', { body: { host: loginHost } })
      setLogin(r.login)
    } catch (e) {
      setNote({ tone: 'error', text: errText(e) })
    } finally {
      setBusy(false)
    }
  }
  const cancelLogin = () => api<{ login: LoginState | null }>('/api/access/cli/login/cancel', { body: {} }).then(r => setLogin(r.login)).catch(() => {})
  const chooseCliPath = (p: string | null) => run(
    () => api('/api/access/cli/path', { method: 'PUT', body: { path: p } }),
    p ? `The Studio now runs the pw CLI at ${p}.` : 'The Studio looks for the pw CLI by itself again.',
  )

  if (loadError && !access) return (
    <>
      <h1>Model access</h1>
      <Callout tone="error" title="Could not read the model access settings">{loadError}</Callout>
    </>
  )
  if (!access || !me) return (
    <>
      <h1>Model access</h1>
      <p className="muted pad"><Loader inline size={16} label="Loading…" /></p>
    </>
  )
  if (!access.single && !me.verified) return (
    <>
      <h1>Model access</h1>
      <p className="muted view-sub">
        Personal model keys attach to your platform-verified identity, which this connection does not carry. Open the
        app through the platform session to add one; until then, requests use the deployment's credential.
      </p>
    </>
  )

  const single = access.single
  const stored = me.mode !== 'none'
  const offline = health?.status === 'offline'
  const keyRequired = health?.status === 'key-required'
  const modelsLine = describeSource(access.models, single)
  const title = access.models.source === 'none' && access.platform.source === 'none'
    ? 'No model access yet'
    : keyRequired ? 'This deployment requires your own model credential'
    : `Models: ${modelsLine}`
  const tone: CalloutTone = access.models.source === 'none' || keyRequired
    ? (requirePersonalKey || single ? 'warning' : 'default')
    : !health ? 'default'
    : health.status === 'ok' ? 'success'
    : offline ? 'warning'
    : 'error'
  const modelCount = models?.length ?? health?.models ?? 0

  // Named for what each is, since "API key" and "Endpoint" alone did not
  // say which kind of key or endpoint; short enough to fit a phone's width.
  const tabs: { value: Tab; label: string }[] = [
    single ? { value: 'cli', label: 'ACTIVATE (pw CLI)' } : { value: 'deployment', label: 'ACTIVATE (deployment)' },
    { value: 'key', label: 'ACTIVATE API key' },
    { value: 'endpoint', label: 'OpenAI-compatible endpoint' },
  ]
  const presets: SelectItem<string>[] = PRESETS.map(p => ({ value: p.value, label: p.label }))
  const local = isLocalUrl(baseUrl)
  // With no key typed: the stored endpoint keeps its stored key, which lets
  // its options change without the key; a server on this computer, or an
  // endpoint whose key header is None, takes the no-key marker; anything
  // else needs a key.
  const sameEndpoint = access.models.source === 'endpoint' && (access.models.baseUrl ?? '') === baseUrl.trim().replace(/\/+$/, '')
  const endpointKey = keyInput.trim() || (sameEndpoint ? '' : local || auth === 'none' ? NO_KEY : null)
  const canSaveEndpoint = !!baseUrl.trim() && endpointKey !== null
  const typedHost = readPlatformHost(keyHost)
  // What Advanced holds, on its folded line, so a stored endpoint's
  // options show without opening it.
  const namedHeaders = headerRows.filter(h => h.name.trim()).length
  const advancedParts = [
    auth !== 'bearer' ? (auth === 'none' ? 'no key header' : `key in ${auth}`) : '',
    namedHeaders ? `${namedHeaders} header${namedHeaders === 1 ? '' : 's'}` : '',
    modelNames.trim() ? 'model names' : '',
    query.trim() ? 'query string' : '',
  ].filter(Boolean)

  return (
    <div className="model-access">
      <h1>Model access</h1>
      <p className="muted view-sub">
        Where the Studio gets its models, and the credential the Workflows page and the assistant's platform tools use.
      </p>

      <Callout className="rag-banner" tone={tone} title={title}
        action={<Button variant="secondary" size="sm" loading={checking} onClick={() => void load(true)}>Re-check</Button>}>
        {health?.status === 'ok' && (
          <div className="rag-banner-models">
            {modelCount} model{modelCount === 1 ? '' : 's'} available{models && models.length ? ':' : '.'}
            {models && models.length > 0 && (
              <div className="rag-chip-row">
                {models.slice(0, 12).map(m => <code key={m}>{m}</code>)}
                {models.length > 12 && <span className="muted">and {models.length - 12} more</span>}
              </div>
            )}
          </div>
        )}
        {access.models.source === 'none' ? (
          <p className="muted key-note">
            {single
              ? 'Sign the pw CLI in to an ACTIVATE platform below, paste an ACTIVATE API key, or connect an OpenAI-compatible endpoint, such as a site AI portal or a model server on this computer.'
              : 'Add an ACTIVATE API key or an OpenAI-compatible endpoint below.'}
          </p>
        ) : health && health.status !== 'ok' && (health.message || modelsError) && (
          <p className="muted key-note">{health.message || modelsError}</p>
        )}
        {health?.unlockUrl && (
          <p className="identity-note">
            The provider reports this key is locked, which it does on a schedule. <ExternalLink href={health.unlockUrl}>Unlock
            your key</ExternalLink>, then Re-check.
          </p>
        )}
        {health?.status === 'ok' && health.platformAccess === false && access.models.source === 'key' && (
          <p className="identity-note">
            This key reaches models only. Workflows, run monitoring, and the DAG viewer use the platform API, which needs a
            platform token or a platform API key; an AI gateway key does not grant that access.
          </p>
        )}
        {stored && me.credExpired && me.credExpiresAt && (
          <p className="identity-note">Your token expired {new Date(me.credExpiresAt).toLocaleString()}.</p>
        )}
      </Callout>

      <UsesTable access={access} />

      <Field group label="Where access comes from" className="access-source">
        <SegmentedControl options={tabs} value={tab ?? tabs[0].value} onValueChange={v => { setTab(v); setNote(null) }} />
      </Field>

      {tab === 'cli' && access.cli && (
        <CliPanel access={access} busy={busy} login={login} loginHost={loginHost} setLoginHost={setLoginHost}
          onUse={pickContext} onStartLogin={() => void startLogin()} onCancelLogin={() => void cancelLogin()} onDismissLogin={() => setLogin(null)}
          onChoosePath={p => void chooseCliPath(p)} />
      )}

      {tab === 'deployment' && (
        <div className="access-panel">
          <p className="access-lead">The ACTIVATE credential this deployment runs with, for anyone without a key of their own.</p>
          <p className="muted view-sub">
            {access.deployment.source === 'none'
              ? 'This deployment has no credential of its own, so each person adds a key or an endpoint.'
              : `Without a key of your own, requests use ${describeSource(access.deployment, false)}.`}
            {requirePersonalKey && ' This deployment requires each person to add their own, under ACTIVATE API key or OpenAI-compatible endpoint.'}
          </p>
          {stored && (
            <div className="key-row">
              <Button variant="secondary" disabled={busy} onClick={() => void removeKey()}>Remove my {access.models.source === 'endpoint' ? 'endpoint' : 'key'} and use the deployment's</Button>
            </div>
          )}
        </div>
      )}

      {tab === 'key' && (
        <div className="access-panel">
          <p className="access-lead">A token or API key from an ACTIVATE account, for its models and its platform.</p>
          <p className="muted view-sub">
            A platform token or platform API key (Account, API keys) covers models and the platform; an AI gateway key covers
            models only. The host is the platform's own, such as platform.example.com. A site AI portal or another API goes
            under OpenAI-compatible endpoint.{stored && access.models.source !== 'key' ? ' Saving one replaces your endpoint.' : ''}
          </p>
          <div className="key-row">
            <div className="grow">
              <Input type="password" aria-label="Platform key or token" value={keyInput} autoComplete="off"
                placeholder="Paste a platform token or API key"
                onChange={e => setKeyInput(e.target.value)} />
            </div>
          </div>
          <div className="key-row">
            <div className="grow">
              <Input aria-label="Platform host" value={keyHost} autoComplete="off"
                placeholder={`Platform host; empty for ${access.gatewayHost ?? 'this Studio\'s platform'}`}
                onChange={e => setKeyHost(e.target.value)} />
            </div>
          </div>
          {typedHost.endpointUrl && (
            <Callout tone="info" className="rag-banner key-as-endpoint" title="This is an endpoint address, not a platform host"
              action={<Button variant="secondary" size="sm" onClick={() => moveToEndpoint(typedHost.endpointUrl!)}>Use as an endpoint</Button>}>
              <p className="identity-note">
                {keyHost.trim()} has a path, so it is the address of an API. A site AI portal or any other OpenAI-compatible
                service goes under OpenAI-compatible endpoint, with <code>{typedHost.endpointUrl}</code> as its base URL.
              </p>
            </Callout>
          )}
          {!single && (
            <label className="key-persist">
              <Checkbox checked={persistKey} onCheckedChange={setPersistKey} />
              Remember in this browser (a sealed cookie; the server holds the key in memory only); unchecked keeps it for about 12 hours
            </label>
          )}
          <div className="key-row access-actions">
            <Button variant="primary" disabled={busy || !keyInput.trim() || !!typedHost.endpointUrl}
              onClick={async () => { if (await saveKey(keyInput.trim(), typedHost.platformUrl)) setKeyInput('') }}>Save and use</Button>
            <Button variant="secondary" disabled={busy || !keyInput.trim()} onClick={() => void testKeyTab()}>Test</Button>
            {access.models.source === 'key' && <Button variant="danger" disabled={busy} onClick={() => void removeKey()}>Remove</Button>}
          </div>
          {me.mode !== 'none' && access.models.source === 'key' && (
            <p className="muted key-note">
              {me.mode === 'stored' && `Added ${me.addedAt?.slice(0, 10)}, ${single ? 'stored on this computer' : 'remembered in this browser'}.`}
              {me.mode === 'session' && `Held in memory until ${me.sessionExpiresAt ? new Date(me.sessionExpiresAt).toLocaleTimeString() : 'later today'}.`}
              {me.credExpiresAt && !me.credExpired && ` The token expires ${new Date(me.credExpiresAt).toLocaleString()}; a platform API key does not.`}
            </p>
          )}
          {!single && <SharePanel me={me} busy={busy} run={run} />}
        </div>
      )}

      {tab === 'endpoint' && (
        <div className="access-panel">
          <p className="access-lead">Any service that speaks the OpenAI API, such as a site AI portal, a provider, or a model server on this computer.</p>
          <p className="muted view-sub">
            The base URL usually ends in /v1. An endpoint covers models only; platform tools keep using {single ? 'the pw CLI' : 'the deployment credential'}.
            A model server on this computer keeps working with no network.
          </p>
          <div className="key-row">
            <Field layout="inline" compact label="Endpoint" className="provider-field">
              <Select className="provider-select" items={presets} value={preset}
                onValueChange={v => { if (!v) return; setPreset(v); const p = PRESETS.find(x => x.value === v); if (p?.url) setBaseUrl(p.url) }} />
            </Field>
          </div>
          <div className="key-row">
            <div className="grow">
              <Input aria-label="Base URL" value={baseUrl} autoComplete="off" placeholder="Base URL, such as https://host/v1"
                onChange={e => { setBaseUrl(e.target.value); setPreset(presetOf(e.target.value)) }} />
            </div>
          </div>
          <div className="key-row">
            <div className="grow">
              <Input type="password" aria-label="Endpoint key" value={keyInput} autoComplete="off"
                placeholder={auth === 'none' ? 'No key is sent (Advanced, Key header)' : sameEndpoint ? 'Stored; type a new key to replace it' : local ? 'API key, if the server asks for one' : 'API key'}
                onChange={e => setKeyInput(e.target.value)} />
            </div>
          </div>
          <Disclosure className="endpoint-advanced" summaryClassName="endpoint-advanced-summary" panelClassName="endpoint-advanced-panel"
            open={advanced} onOpenChange={setAdvanced} keepMounted
            summary={`Advanced${advancedParts.length ? `: ${advancedParts.join(', ')}` : ', for portals that differ'}`}>
            <EndpointAdvanced auth={auth} setAuth={setAuth} rows={headerRows} setRows={setHeaderRows}
              modelNames={modelNames} setModelNames={setModelNames} query={query} setQuery={setQuery} keepStored={sameEndpoint} />
          </Disclosure>
          <div className="key-row access-actions">
            <Button variant="primary" disabled={busy || !canSaveEndpoint}
              onClick={async () => { if (await saveKey(endpointKey ?? '', baseUrl.trim(), endpointBody())) setKeyInput('') }}>Save and use</Button>
            <Button variant="secondary" disabled={busy || !baseUrl.trim()} onClick={() => void testCandidate(endpointKey ?? NO_KEY, baseUrl.trim(), endpointBody())}>Test</Button>
            {access.models.source === 'endpoint' && <Button variant="danger" disabled={busy} onClick={() => void removeKey()}>Remove</Button>}
          </div>
          {preset === 'github' && (
            <p className="muted key-note">
              Create a fine-grained personal access token with the Models (read) permission on GitHub, and paste it as the key.
            </p>
          )}
        </div>
      )}

      {note && (
        <Callout className="rag-banner access-note" tone={note.tone} live onDismiss={() => setNote(null)}
          title={note.detail ? note.text : undefined}
          action={note.action ? <Button variant="secondary" size="sm" onClick={note.action.run}>{note.action.label}</Button> : undefined}>
          {note.detail ?? note.text}
        </Callout>
      )}

      {/* Offline, the banner already says the platform cannot be reached. */}
      {!offline && (single ? access.platform.source !== 'none' : access.platform.source === 'key') && (
        <SubscriptionsPanel platformOrigin={access.platformOrigin} host={access.platform.host} onChanged={() => { announceChange(); void load(true) }} />
      )}

      <p className="muted key-trust">
        {single
          ? 'A key or endpoint added here, with its header values, is stored on this computer, encrypted, beside the index, and header values are never shown again. The pw CLI keeps its own sign-ins; the Studio reads them and asks the CLI to renew them, and never sends them to the browser.'
          : 'A key added here is available to this server and its operator; encryption at rest protects the stored file, not the running process. Use a key you can revoke. A key bound to an endpoint is never passed to platform commands.'}
      </p>
    </div>
  )
}

function CliPanel({ access, busy, login, loginHost, setLoginHost, onUse, onStartLogin, onCancelLogin, onDismissLogin, onChoosePath }: {
  access: AccessStatus
  busy: boolean
  login: LoginState | null
  loginHost: string
  setLoginHost: (h: string) => void
  onUse: (name: string | null) => void
  onStartLogin: () => void
  onCancelLogin: () => void
  onDismissLogin: () => void
  onChoosePath: (path: string | null) => void
}) {
  const cli = access.cli!
  const overridden = access.models.source === 'key' || access.models.source === 'endpoint'
  const following = cli.chosen === null
  const waiting = login?.state === 'waiting' || login?.state === 'starting'
  if (!cli.available && !cli.contexts.length) return (
    <div className="access-panel">
      <CliLocation cli={cli} busy={busy} onChoose={onChoosePath} />
    </div>
  )
  return (
    <div className="access-panel">
      <p className="access-lead">The ACTIVATE platforms the pw CLI on this computer is signed in to, for their models and the platform.</p>
      <p className="muted view-sub">The context in use covers models, the Workflows page, clusters, and runs.</p>
      <CliLocation cli={cli} busy={busy} onChoose={onChoosePath} />
      {overridden && (
        <p className="identity-note access-override">
          Your {access.models.source === 'endpoint' ? 'endpoint' : 'key'} is used for models now. Use on a context removes it.
        </p>
      )}
      {cli.envKey && <p className="identity-note">PW_API_KEY is set in the Studio's environment, and it is used before the pw CLI.</p>}
      {cli.gatewayFromEnv && <p className="identity-note">The model gateway is fixed by PW_GATEWAY_URL; a context on another platform covers the platform only.</p>}
      {cli.contexts.length > 0 ? (
        <>
          <Table density="compact" className="cli-contexts" wrapperClassName="cli-contexts-wrap">
            <Table.Header>Context</Table.Header>
            <Table.Header>Platform</Table.Header>
            <Table.Header>Kind</Table.Header>
            <Table.Header />
            {cli.contexts.map(c => {
              const inUse = !overridden && cli.used === c.name
              return (
                <tr key={c.name} className={inUse ? 'in-use' : undefined}>
                  <Table.Item wrap>
                    <code>{c.name}</code>
                    {c.current && <StatusBadge variant="muted" size="sm" className="ctx-badge">CLI current</StatusBadge>}
                  </Table.Item>
                  <Table.Item wrap>{c.server}{c.organization ? <span className="muted">, {c.organization}</span> : null}</Table.Item>
                  <Table.Item wrap>
                    {c.kind ? KIND_LABEL[c.kind] : <span className="muted">Kept by the CLI</span>}
                    {c.expired && <StatusBadge variant="error" size="sm" className="ctx-badge">Expired</StatusBadge>}
                    {c.renewError && <div className="muted ctx-error">{c.renewError}</div>}
                  </Table.Item>
                  <Table.Item>
                    {inUse
                      ? <StatusBadge variant="success" dot>In use</StatusBadge>
                      : <Button variant="secondary" size="sm" disabled={busy} onClick={() => onUse(c.name)}>Use</Button>}
                  </Table.Item>
                </tr>
              )
            })}
          </Table>
          <label className="key-persist">
            <Checkbox checked={following} disabled={busy}
              onCheckedChange={v => onUse(v ? null : (cli.active ?? cli.contexts[0]?.name ?? null))} />
            <span>Follow the pw CLI's current context, as <code>pw context use</code> sets it</span>
          </label>
        </>
      ) : (
        <p className="muted key-note">The pw CLI has no sign-ins yet. Sign in to a platform below.</p>
      )}

      <div className="tool-group">Sign in to a platform</div>
      <p className="muted view-sub">
        Signs the pw CLI in with a code you approve in a browser on any device, as <code>pw auth login --device</code> does.
        The new sign-in becomes the CLI's current context, and the Studio uses it.
      </p>
      <div className="key-row">
        <div className="grow">
          <Input aria-label="Platform host to sign in to" value={loginHost} autoComplete="off" placeholder="Platform host, such as platform.example.com"
            onChange={e => setLoginHost(e.target.value)} onKeyDown={e => { if (e.key === 'Enter' && loginHost.trim() && !waiting) onStartLogin() }} />
        </div>
        <Button variant="primary" disabled={busy || waiting || !loginHost.trim() || !cli.available} onClick={onStartLogin}>Sign in</Button>
      </div>
      {login && waiting && login.code && login.url && (
        <DeviceCode device={{ url: login.url, code: login.code }} what={`Sign in to ${login.host}`} onCancel={onCancelLogin} />
      )}
      {login && login.state === 'starting' && <p className="key-note"><Loader inline size={14} label="Starting the sign-in…" /></p>}
      {login && login.state === 'done' && <Callout className="rag-banner" tone="success" live onDismiss={onDismissLogin}>{login.message}</Callout>}
      {login && login.state === 'failed' && <Callout className="rag-banner" tone="error" live onDismiss={onDismissLogin}>{login.message}</Callout>}
    </div>
  )
}

/** An endpoint's options for portals that differ from the OpenAI shape:
 *  the header the key goes in, headers of their own, model names for an
 *  endpoint that lists none, and a query string for every request. */
function EndpointAdvanced({ auth, setAuth, rows, setRows, modelNames, setModelNames, query, setQuery, keepStored }: {
  auth: EndpointAuth
  setAuth: (a: EndpointAuth) => void
  rows: HeaderRow[]
  setRows: (r: HeaderRow[]) => void
  modelNames: string
  setModelNames: (m: string) => void
  query: string
  setQuery: (q: string) => void
  /** The stored endpoint is the one in the form, so an empty stored value keeps it. */
  keepStored: boolean
}) {
  const set = (i: number, patch: Partial<HeaderRow>) => setRows(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)))
  return (
    <div className="endpoint-advanced-body">
      <Field layout="inline" compact label="Key header" className="provider-field">
        <Select className="provider-select" items={AUTH_ITEMS} value={auth} onValueChange={v => { if (v) setAuth(v) }} />
      </Field>
      <div className="advanced-label">Extra headers, sent with every request</div>
      {rows.map((r, i) => (
        <div className="key-row header-row" key={i}>
          <div className="header-name">
            <Input aria-label={`Header ${i + 1} name`} value={r.name} autoComplete="off" spellCheck={false}
              placeholder="Name, such as X-Org" onChange={e => set(i, { name: e.target.value, stored: false })} />
          </div>
          <div className="grow">
            <Input type="password" aria-label={`Header ${i + 1} value`} value={r.value} autoComplete="off"
              placeholder={r.stored && keepStored ? 'Stored; type to replace' : 'Value'} onChange={e => set(i, { value: e.target.value })} />
          </div>
          <IconButton variant="outline" size="md" icon={<CloseIcon />} label={`Remove header ${r.name || i + 1}`}
            onClick={() => setRows(rows.filter((_, j) => j !== i))} />
        </div>
      ))}
      <Button variant="ghost" size="sm" onClick={() => setRows([...rows, { name: '', value: '', stored: false }])}>Add a header</Button>
      <Textarea label="Model names" rows={3} value={modelNames} spellCheck={false} placeholder="One per line"
        description="For an endpoint that does not list its models: these are offered instead, and Test tries a chat completion with the first."
        onChange={e => setModelNames(e.target.value)} />
      <Input label="Query string" value={query} autoComplete="off" spellCheck={false} placeholder="api-version=2024-10-21"
        description="Added to every request. It is shown here, so put a secret in a header instead."
        onChange={e => setQuery(e.target.value)} />
      <p className="muted key-note">Header values are stored like the key, encrypted, and never shown again.</p>
    </div>
  )
}

const CLI_SOURCE: Record<CliSource, string> = {
  env: 'set by PW_CLI', chosen: 'chosen here', path: 'found on the PATH', common: 'found where it is usually installed', none: '',
}

/** Where the Studio runs the pw CLI from, or, when it cannot find it, a
 *  place to say where it is. The macOS app's server does not start from a
 *  terminal, so a CLI installed in a folder only the person's shell knows
 *  about can be missed. */
function CliLocation({ cli, busy, onChoose }: { cli: NonNullable<AccessStatus['cli']>; busy: boolean; onChoose: (path: string | null) => void }) {
  const [input, setInput] = useState(cli.chosenPath ?? '')
  if (cli.available && cli.path) return (
    <p className="muted key-note cli-location">
      pw{cli.version ? ` ${cli.version}` : ''} at <code>{cli.path}</code>, {CLI_SOURCE[cli.source]}.
      {cli.source === 'chosen' && <> <Button variant="ghost" size="sm" disabled={busy} onClick={() => onChoose(null)}>Look for it by itself</Button></>}
    </p>
  )
  return (
    <Callout tone="warning" className="rag-banner cli-location" title="The Studio cannot find the pw CLI">
      <p className="identity-note">
        It looked on the PATH and in {cli.commonDirs.map((d, i) => <span key={d}>{i ? ', ' : ''}<code>{d}</code></span>)}.
        Without it the Studio cannot renew pw CLI sign-ins, show the Workflows page, or reach ACTIVATE systems over
        pw ssh. If it is installed, enter where it is; <code>command -v pw</code> in Terminal prints it.
      </p>
      <div className="key-row">
        <div className="grow">
          <Input aria-label="Where the pw CLI is" value={input} autoComplete="off" placeholder="/Users/me/bin/pw"
            onChange={e => setInput(e.target.value)} onKeyDown={e => { if (e.key === 'Enter' && input.trim()) onChoose(input.trim()) }} />
        </div>
        <Button variant="primary" disabled={busy || !input.trim()} onClick={() => onChoose(input.trim())}>Use this location</Button>
      </div>
    </Callout>
  )
}

/** Which credential each part of the Studio uses, in one place: models
 *  and the platform can come from different sources, and the platform's
 *  parts run the pw CLI, which has to be found on this computer. */
function UsesTable({ access }: { access: AccessStatus }) {
  const single = access.single
  const platform = access.platform.source === 'none'
    ? 'none: an endpoint covers models only; sign the pw CLI in, or add a platform key'
    : describeSource(access.platform, single)
  const cliMissing = single && !!access.cli && !access.cli.available
  const needsCli = cliMissing ? <StatusBadge variant="warning" size="sm" className="ctx-badge">pw CLI not found</StatusBadge> : null
  const rows: { part: string; uses: string; extra?: ReactNode }[] = [
    { part: 'Models in chat and for the assistant', uses: describeSource(access.models, single) },
    { part: 'Workflows page and the assistant\'s platform tools', uses: platform, extra: needsCli },
    { part: 'Libraries on ACTIVATE systems (pw ssh)', uses: platform, extra: needsCli },
    { part: 'ChatGPT and Copilot subscriptions', uses: platform },
  ]
  return (
    <Field group label="What each part uses" className="access-uses">
      <Table density="compact" className="access-uses-table" wrapperClassName="access-uses-wrap">
        <Table.Header>Part</Table.Header>
        <Table.Header>Credential</Table.Header>
        {rows.map(r => (
          <tr key={r.part}>
            <Table.Item wrap>{r.part}</Table.Item>
            <Table.Item wrap>{r.uses}{r.extra}</Table.Item>
          </tr>
        ))}
      </Table>
    </Field>
  )
}

/** A code to enter at an address, while something waits for it. */
function DeviceCode({ device, what, onCancel }: { device: Device; what: string; onCancel: () => void }) {
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

function SharePanel({ me, busy, run }: { me: KeyStatus; busy: boolean; run: (what: () => Promise<unknown>, done?: string) => Promise<boolean> }) {
  if (me.shared?.active) return (
    <Callout className="rag-banner shared-key" tone={me.shared.credExpired ? 'warning' : 'success'}
      title={<>
        Key ending {me.shared.last4} is shared with everyone on this deployment
        {me.shared.sharedBy ? `, by ${me.shared.mine ? 'you' : me.shared.sharedBy}` : ''}
        {me.shared.sharedAt ? ` on ${me.shared.sharedAt.slice(0, 10)}` : ''}
        {me.shared.credExpired ? '; it has expired and is no longer used' : ''}
      </>}
      action={<Button variant="danger" size="sm" disabled={busy} onClick={() => void run(() => api('/api/me/model-key/unshare', { body: {} }))}>Stop sharing</Button>}>
      <p className="muted key-note">
        Anyone here without a key of their own calls the gateway with this one, so its owner's quota and usage cover the
        deployment. Their own keys still take precedence. Anyone can stop the sharing.
      </p>
    </Callout>
  )
  if (!me.sharingAllowed) return null
  return (
    <div className="key-row share-row">
      <Button variant="secondary" disabled={busy || me.mode === 'none' || !!me.baseUrl}
        onClick={() => void run(() => api('/api/me/model-key/share', { body: {} }), 'Shared with everyone here.')}>Share this key with everyone</Button>
      <span className="muted key-note">
        {me.mode === 'none'
          ? 'Add your own key above first; you can then share it with everyone here.'
          : 'Lets people here with no key of their own work with yours, which spends your quota and runs as you. You can stop it at any time.'}
      </span>
    </div>
  )
}

/** Subscriptions connected through ACTIVATE: the platform holds them and
 *  serves their models through its gateway, so the Studio lists them with
 *  the rest once connected. */
function SubscriptionsPanel({ platformOrigin, host, onChanged }: { platformOrigin: string | null; host: string | null; onChanged: () => void }) {
  const [subs, setSubs] = useState<Subscriptions | null>(null)
  const [error, setError] = useState('')
  const [device, setDevice] = useState<(Device & { id: string; intervalSec: number; expiresAt: string | null }) | null>(null)
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState('')
  const timer = useRef<number | null>(null)

  const load = () => api<Subscriptions>('/api/access/subscriptions')
    .then(s => { setSubs(s); setError('') })
    .catch(e => setError(errText(e)))
  useEffect(() => { void load() }, [host])
  useEffect(() => () => { if (timer.current) window.clearTimeout(timer.current) }, [])

  const poll = (d: { id: string; intervalSec: number; expiresAt: string | null }) => {
    timer.current = window.setTimeout(async () => {
      if (d.expiresAt && Date.parse(d.expiresAt) < Date.now()) { setDevice(null); setError('The code expired before it was approved. Start again.'); return }
      try {
        const r = await api<{ status: string; provider: { name: string; account: string | null } | null }>(`/api/access/codex/${encodeURIComponent(d.id)}`, { body: {} })
        if (r.status === 'connected') {
          setDevice(null)
          setDone(`Connected${r.provider?.account ? ` as ${r.provider.account}` : ''}. Its models are listed as me:${r.provider?.name ?? 'chatgpt'}/<model>.`)
          void load()
          onChanged()
          return
        }
      } catch (e) {
        setDevice(null)
        setError(errText(e))
        return
      }
      poll(d)
    }, d.intervalSec * 1000)
  }
  const connectCodex = async () => {
    setBusy(true); setError(''); setDone('')
    try {
      const d = await api<{ id: string; url: string; code: string; intervalSec: number; expiresAt: string | null }>('/api/access/codex', { body: { name: 'chatgpt' } })
      setDevice(d)
      poll(d)
    } catch (e) { setError(errText(e)) } finally { setBusy(false) }
  }
  const cancel = () => { if (timer.current) window.clearTimeout(timer.current); setDevice(null) }
  const addCopilot = async () => {
    setBusy(true); setError(''); setDone('')
    try {
      await api('/api/access/copilot', { body: {} })
      setDone('GitHub Copilot added. Its models are listed as me:copilot/<model>.')
      void load()
      onChanged()
    } catch (e) { setError(errText(e)) } finally { setBusy(false) }
  }

  const providers = (list: { name: string; displayName: string | null; account: string | null }[]) =>
    list.map(p => `${p.displayName || p.name}${p.account ? ` (${p.account})` : ''}`).join(', ')

  return (
    <div className="access-subs">
      <div className="tool-group">Subscriptions through ACTIVATE</div>
      <p className="muted view-sub">
        A ChatGPT plan or GitHub Copilot, connected to your account on {host ?? 'the platform'}. The platform holds the
        connection and serves the models with the rest; usage draws on the subscription.
      </p>
      {!subs && !error && <p className="muted"><Loader inline size={14} label="Asking the platform…" /></p>}
      {subs && !subs.available && <p className="muted key-note">{subs.reason}</p>}
      {subs?.available && (
        <Table density="compact" className="access-subs-table" wrapperClassName="cli-contexts-wrap">
          <Table.Header>Subscription</Table.Header>
          <Table.Header>State</Table.Header>
          <Table.Header />
          <tr>
            <Table.Item>ChatGPT (Codex)</Table.Item>
            <Table.Item wrap>
              {!subs.codex?.offered ? <span className="muted">This platform does not offer it.</span>
                : subs.codex.providers.length ? <>Connected: {providers(subs.codex.providers)}</>
                : <span className="muted">Not connected</span>}
            </Table.Item>
            <Table.Item>
              {subs.codex?.offered && (
                <Button variant="secondary" size="sm" disabled={busy || !!device} onClick={() => void connectCodex()}>
                  {subs.codex.providers.length ? 'Connect another' : 'Connect'}
                </Button>
              )}
            </Table.Item>
          </tr>
          <tr>
            <Table.Item>GitHub Copilot</Table.Item>
            <Table.Item wrap>
              {!subs.copilot?.offered ? <span className="muted">This platform does not offer it.</span>
                : subs.copilot.providers.length ? <>Connected: {providers(subs.copilot.providers)}</>
                : subs.copilot.github?.linked ? <>GitHub account {subs.copilot.github.username} is linked; not added yet.</>
                : <span className="muted">
                    Link your GitHub account in ACTIVATE's account settings first{platformOrigin ? <> (<ExternalLink href={platformOrigin}>{host}</ExternalLink>)</> : ''}.
                  </span>}
            </Table.Item>
            <Table.Item>
              {subs.copilot?.offered && !subs.copilot.providers.length && subs.copilot.github?.linked && (
                <Button variant="secondary" size="sm" disabled={busy} onClick={() => void addCopilot()}>Add</Button>
              )}
            </Table.Item>
          </tr>
        </Table>
      )}
      {device && <DeviceCode device={device} what="Connect a ChatGPT plan" onCancel={cancel} />}
      {done && <Callout className="rag-banner" tone="success" live onDismiss={() => setDone('')}>{done}</Callout>}
      {error && <Callout className="rag-banner" tone={/Could not reach/.test(error) ? 'warning' : 'error'} live>{error}</Callout>}
    </div>
  )
}
