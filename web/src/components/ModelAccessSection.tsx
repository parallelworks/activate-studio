import { useEffect, useRef, useState } from 'react'
import {
  Button, Callout, type CalloutTone, Checkbox, CopyButton, ExternalLink, Field, Input, Loader, SegmentedControl, Select,
  type SelectItem, StatusBadge, Table,
} from '@parallelworks/ui'
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
 */

type Source = 'endpoint' | 'key' | 'shared' | 'env' | 'cli' | 'sidecar' | 'none'
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
  cli: { available: boolean; version: string | null; contexts: CliContext[]; chosen: string | null; active: string | null; used: string | null; gatewayFromEnv: boolean; envKey: boolean } | null
  login: LoginState | null
}
interface KeyStatus {
  authEnabled?: boolean; single?: boolean; verified: boolean; mode: 'stored' | 'session' | 'none'
  last4?: string; addedAt?: string; sessionExpiresAt?: string; kind?: string; credExpiresAt?: string | null; credExpired?: boolean
  baseUrl?: string | null; gatewayHost?: string
  shared?: { active: boolean; last4?: string; sharedBy?: string; sharedAt?: string; credExpired?: boolean; mine?: boolean }
  sharingAllowed?: boolean
}
interface Health { status: string; models: number; gatewayHost?: string; message?: string | null; platformAccess?: boolean | null; unlockUrl?: string | null }
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
  { value: 'other', label: 'Other OpenAI-compatible endpoint', url: '' },
]
/** A server on this computer takes no key; anything else needs one. */
export const isLocalUrl = (url: string) => /^https?:\/\/(localhost|127\.0\.0\.1|\[::1\])(:\d+)?(\/|$)/i.test(url.trim())
const presetOf = (url: string | null | undefined) => PRESETS.find(p => p.url && p.url === String(url ?? '').replace(/\/+$/, ''))?.value ?? 'other'

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

const sameSource = (a: SourceInfo, b: SourceInfo) => a.source === b.source && a.context === b.context && a.host === b.host && (a.baseUrl ?? null) === (b.baseUrl ?? null)

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
  const [note, setNote] = useState<{ tone: CalloutTone; text: string } | null>(null)

  // Key and endpoint forms.
  const [keyInput, setKeyInput] = useState('')
  const [keyHost, setKeyHost] = useState('')
  const [persistKey, setPersistKey] = useState(true)
  const [preset, setPreset] = useState('ollama')
  const [baseUrl, setBaseUrl] = useState(PRESETS[0].url)

  // Signing the CLI in.
  const [loginHost, setLoginHost] = useState('')
  const [login, setLogin] = useState<LoginState | null>(null)

  const load = async (fresh = false) => {
    setChecking(true)
    try {
      const [a, k] = await Promise.all([api<AccessStatus>('/api/access'), api<KeyStatus>('/api/me/model-key')])
      setAccess(a)
      setMe(k)
      setLoadError('')
      setLogin(l => a.login ?? l)
      setTab(t => t ?? (a.models.source === 'endpoint' ? 'endpoint' : a.models.source === 'key' ? 'key' : a.single ? 'cli' : 'deployment'))
      if (a.models.source === 'endpoint' && a.models.baseUrl) { setPreset(presetOf(a.models.baseUrl)); setBaseUrl(a.models.baseUrl) }
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
  const saveKey = (key: string, url: string) => run(
    () => api('/api/me/model-key', { body: { key, persist: persistKey, baseUrl: url } }),
    'Saved.',
  )
  const removeKey = () => run(() => api('/api/me/model-key/clear', { body: {} }), 'Removed.')
  const testCandidate = async (key: string, url: string) => {
    setBusy(true)
    setNote(null)
    try {
      const h = await api<Health & { ids?: string[] }>('/api/me/model-key/test', { body: { key, baseUrl: url || undefined } })
      setNote(h.status === 'ok'
        ? { tone: 'success', text: `It works: ${h.models} model${h.models === 1 ? '' : 's'} available.` }
        : { tone: h.status === 'offline' ? 'warning' : 'error', text: h.message ?? `The check failed (${h.status}).` })
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
  const together = sameSource(access.models, access.platform)
  const modelsLine = describeSource(access.models, single)
  const platformLine = describeSource(access.platform, single)
  const title = access.models.source === 'none' && access.platform.source === 'none'
    ? 'No model access yet'
    : keyRequired ? 'This deployment requires your own model credential'
    : together ? `Models and platform tools: ${modelsLine}` : `Models: ${modelsLine}`
  const tone: CalloutTone = access.models.source === 'none' || keyRequired
    ? (requirePersonalKey || single ? 'warning' : 'default')
    : !health ? 'default'
    : health.status === 'ok' ? 'success'
    : offline ? 'warning'
    : 'error'
  const modelCount = models?.length ?? health?.models ?? 0

  const tabs: { value: Tab; label: string }[] = [
    single ? { value: 'cli', label: 'pw CLI' } : { value: 'deployment', label: 'Deployment' },
    { value: 'key', label: 'API key' },
    { value: 'endpoint', label: 'Endpoint' },
  ]
  const presets: SelectItem<string>[] = PRESETS.map(p => ({ value: p.value, label: p.label }))
  const local = isLocalUrl(baseUrl)
  // Saving with no key stores the no-key marker, which is right for a server
  // on this computer and wrong for anything else; and saving the stored
  // endpoint again with no key would replace its key with that marker.
  const sameEndpoint = access.models.source === 'endpoint' && (access.models.baseUrl ?? '') === baseUrl.trim().replace(/\/+$/, '')
  const canSaveEndpoint = !!baseUrl.trim() && (keyInput.trim() ? true : local && !sameEndpoint)
  const keyHostUrl = (() => {
    const h = keyHost.trim().replace(/^https?:\/\//, '').replace(/\/.*$/, '')
    return h ? `https://${h}/api/openai/v1` : ''
  })()

  return (
    <div className="model-access">
      <h1>Model access</h1>
      <p className="muted view-sub">
        Where the Studio gets its models, and the credential the Workflows page and the assistant's platform tools use.
      </p>

      <Callout className="rag-banner" tone={tone} title={title}
        action={<Button variant="secondary" size="sm" loading={checking} onClick={() => void load(true)}>Re-check</Button>}>
        {!together && (
          <p className="identity-note">
            Platform tools (Workflows, clusters, runs): {access.platform.source === 'none'
              ? 'none. An endpoint covers models only; sign the pw CLI in to a platform, or add a platform key, for these.'
              : platformLine}
          </p>
        )}
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
            {single ? 'Sign the pw CLI in to a platform below, paste an API key, or connect an endpoint such as a model server on this computer.' : 'Add an API key or an endpoint below.'}
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

      <Field group label="Where access comes from" className="access-source">
        <SegmentedControl options={tabs} value={tab ?? tabs[0].value} onValueChange={v => { setTab(v); setNote(null) }} />
      </Field>

      {tab === 'cli' && access.cli && (
        <CliPanel access={access} busy={busy} login={login} loginHost={loginHost} setLoginHost={setLoginHost}
          onUse={pickContext} onStartLogin={() => void startLogin()} onCancelLogin={() => void cancelLogin()} onDismissLogin={() => setLogin(null)} />
      )}

      {tab === 'deployment' && (
        <div className="access-panel">
          <p className="muted view-sub">
            {access.deployment.source === 'none'
              ? 'This deployment has no credential of its own, so each person adds a key or an endpoint.'
              : `Without a key of your own, requests use ${describeSource(access.deployment, false)}.`}
            {requirePersonalKey && ' This deployment requires each person to add their own, under API key or Endpoint.'}
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
          <p className="muted view-sub">
            A platform token or platform API key (from your ACTIVATE account, Account, API keys) covers models and the
            platform. An AI gateway key covers models only.{stored && access.models.source !== 'key' ? ' Saving one replaces your endpoint.' : ''}
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
          {!single && (
            <label className="key-persist">
              <Checkbox checked={persistKey} onCheckedChange={setPersistKey} />
              Remember in this browser (a sealed cookie; the server holds the key in memory only); unchecked keeps it for about 12 hours
            </label>
          )}
          <div className="key-row access-actions">
            <Button variant="primary" disabled={busy || !keyInput.trim()}
              onClick={async () => { if (await saveKey(keyInput.trim(), keyHostUrl)) setKeyInput('') }}>Save and use</Button>
            <Button variant="secondary" disabled={busy || !keyInput.trim()} onClick={() => void testCandidate(keyInput.trim(), keyHostUrl)}>Test</Button>
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
          <p className="muted view-sub">
            Any OpenAI-compatible endpoint: a provider, or a model server on this computer, which keeps working with no
            network. It covers models only; platform tools keep using {single ? 'the pw CLI' : 'the deployment credential'}.
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
                placeholder={local ? 'API key, if the server asks for one' : sameEndpoint ? 'A new API key, to replace the stored one' : 'API key'}
                onChange={e => setKeyInput(e.target.value)} />
            </div>
          </div>
          <div className="key-row access-actions">
            <Button variant="primary" disabled={busy || !canSaveEndpoint}
              onClick={async () => { if (await saveKey(keyInput.trim() || NO_KEY, baseUrl.trim())) setKeyInput('') }}>Save and use</Button>
            <Button variant="secondary" disabled={busy || !baseUrl.trim()} onClick={() => void testCandidate(keyInput.trim() || NO_KEY, baseUrl.trim())}>Test</Button>
            {access.models.source === 'endpoint' && <Button variant="danger" disabled={busy} onClick={() => void removeKey()}>Remove</Button>}
          </div>
          {preset === 'github' && (
            <p className="muted key-note">
              Create a fine-grained personal access token with the Models (read) permission on GitHub, and paste it as the key.
            </p>
          )}
        </div>
      )}

      {note && <Callout className="rag-banner access-note" tone={note.tone} live onDismiss={() => setNote(null)}>{note.text}</Callout>}

      {/* Offline, the banner already says the platform cannot be reached. */}
      {!offline && (single ? access.platform.source !== 'none' : access.platform.source === 'key') && (
        <SubscriptionsPanel platformOrigin={access.platformOrigin} host={access.platform.host} onChanged={() => { announceChange(); void load(true) }} />
      )}

      <p className="muted key-trust">
        {single
          ? 'A key or endpoint added here is stored on this computer, encrypted, beside the index. The pw CLI keeps its own sign-ins; the Studio reads them and asks the CLI to renew them, and never sends them to the browser.'
          : 'A key added here is available to this server and its operator; encryption at rest protects the stored file, not the running process. Use a key you can revoke. A key bound to an endpoint is never passed to platform commands.'}
      </p>
    </div>
  )
}

function CliPanel({ access, busy, login, loginHost, setLoginHost, onUse, onStartLogin, onCancelLogin, onDismissLogin }: {
  access: AccessStatus
  busy: boolean
  login: LoginState | null
  loginHost: string
  setLoginHost: (h: string) => void
  onUse: (name: string | null) => void
  onStartLogin: () => void
  onCancelLogin: () => void
  onDismissLogin: () => void
}) {
  const cli = access.cli!
  const overridden = access.models.source === 'key' || access.models.source === 'endpoint'
  const following = cli.chosen === null
  const waiting = login?.state === 'waiting' || login?.state === 'starting'
  if (!cli.available && !cli.contexts.length) return (
    <div className="access-panel">
      <Callout tone="default" title="The pw CLI is not installed on this computer">
        Install it and sign in, or sign in here once it is installed; until then, use an API key or an endpoint.
      </Callout>
    </div>
  )
  return (
    <div className="access-panel">
      <p className="muted view-sub">
        The platforms the pw CLI on this computer is signed in to. The one in use covers models and the platform: the
        Workflows page, clusters, and runs.{cli.version ? ` pw ${cli.version}.` : ''}
      </p>
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
