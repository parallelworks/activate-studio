import { type ReactNode, useEffect, useRef, useState } from 'react'
import {
  Button, Callout, Checkbox, Disclosure, ExternalLink, Field, IconButton, Input, Loader, RadioGroup, Select,
  type SelectItem, SettingModeOption, StatusBadge, StatusLabel, Table, Textarea,
} from '@parallelworks/ui'
import { CloseIcon } from '@parallelworks/ui/icons'
import './ModelAccessSection.css'
import { IndexGroup } from './IndexGroup'
import { PlatformSignInPanel } from './PlatformSignInPanel'
import { ClaudeAccountSlot } from './ClaudeAccountSlot'
import {
  type AccessStatus, AUTH_ITEMS, type CliSource, DeviceCode, type Device, type EndpointAuth, type EndpointSummary, type Health,
  type HeaderRow, KIND_LABEL, type KeyStatus, type KeyTestResult, type LoginState, NO_KEY, type Note, PRESETS, type Subscriptions,
  announceChange, api, chatModel, describeSource, errText, isLocalUrl, platformCredential, presetOf, readPlatformHost, testNote,
} from './accessShared'
import { useAppConfig } from '../config'

/**
 * Settings, Accounts: the connections the Studio makes for the person, in
 * one place. ACTIVATE first: signing in (with ACTIVATE on a Studio several
 * people use, with the pw CLI on a Studio of one), or an ACTIVATE API key,
 * set up once, with who is signed in and what it turns on here, each part
 * with its state. Then an OpenAI-compatible endpoint, for models from any
 * other service, and a place for the Claude account pw code uses.
 *
 * The Studio works without any of them: chat with a model of the person's
 * own, local libraries, and remote computers over ssh. Settings, Model
 * access says which of these the models come from.
 *
 * On a Studio several people use, every account here is the person's own:
 * the deployment's ACTIVATE credential is its administrator's and is never
 * used for a person. Only an OpenAI-compatible endpoint the administrator
 * set may serve everyone's models.
 */

type Method = 'cli' | 'signin' | 'key'
type NoteAt = Note & { at: 'activate' | 'endpoint' }

/** A part of the Studio that ACTIVATE turns on, and how it stands. */
export interface ActivatePart { part: string; state: 'on' | 'sign-in' | 'cli' | 'endpoint' | 'admin-endpoint' | 'sidecar'; detail?: string }

/**
 * What ACTIVATE turns on in this Studio, each part with its state. Every
 * part runs with the person's own ACTIVATE credential (on a Studio of one,
 * the pw CLI on the computer counts as theirs): without one, each asks to
 * sign in; nothing falls back to another's. Models may also come from an
 * endpoint: the person's own, or one the administrator set for everyone.
 */
export function activateParts(access: AccessStatus, aiEnabled: boolean): ActivatePart[] {
  const single = access.single
  const any = access.platform.source !== 'none'
  const cliMissing = single && !!access.cli && !access.cli.available && !access.cli.stopped
  const part = (name: string, cli = false): ActivatePart => ({ part: name, state: !any ? 'sign-in' : cli && cliMissing ? 'cli' : 'on' })
  const m = access.models.source
  const models: ActivatePart[] = !aiEnabled ? [] : [
    m === 'endpoint' ? { part: 'Models', state: 'endpoint', detail: access.models.baseUrl ?? undefined }
      : m === 'admin-endpoint' ? { part: 'Models', state: 'admin-endpoint', detail: access.models.baseUrl ?? undefined }
      : m === 'sidecar' ? { part: 'Models', state: 'sidecar' }
      : { part: 'Models', state: m === 'none' ? 'sign-in' : 'on' },
  ]
  return [
    part('pw code and the Code page', true),
    part('Libraries on ACTIVATE systems (pw ssh)', true),
    part('Workflows', true),
    part('Desktops'),
    ...(aiEnabled ? [part('The assistant\'s platform tools')] : []),
    ...models,
  ]
}

const PART_STATE: Record<ActivatePart['state'], ReactNode> = {
  on: <StatusLabel variant="success">On</StatusLabel>,
  'sign-in': <StatusLabel variant="muted">Sign in to use</StatusLabel>,
  cli: <StatusLabel variant="warning">pw CLI not found</StatusLabel>,
  endpoint: <StatusLabel variant="muted">From your endpoint</StatusLabel>,
  'admin-endpoint': <StatusLabel variant="muted">From the administrator's endpoint</StatusLabel>,
  sidecar: <StatusLabel variant="muted">From the model served beside this Studio</StatusLabel>,
}

/** With AI features off (`aiEnabled` false) only ACTIVATE is here: no
 *  endpoint, no subscriptions, and models are not listed among its parts. */
export function AccountsSection({ requirePersonalKey = false, aiEnabled = true }: { requirePersonalKey?: boolean; aiEnabled?: boolean }) {
  const cfg = useAppConfig()
  const [access, setAccess] = useState<AccessStatus | null>(null)
  const [me, setMe] = useState<KeyStatus | null>(null)
  const [health, setHealth] = useState<Health | null>(null)
  const [loadError, setLoadError] = useState('')
  const [method, setMethod] = useState<Method | null>(null)
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState<NoteAt | null>(null)

  // The ACTIVATE API key form.
  const [keyInput, setKeyInput] = useState('')
  const [keyHost, setKeyHost] = useState('')
  const [persistKey, setPersistKey] = useState(true)
  // The endpoint form, with its own key.
  const [preset, setPreset] = useState('ollama')
  const [baseUrl, setBaseUrl] = useState(PRESETS[0].url)
  const [endpointInput, setEndpointInput] = useState('')
  // The endpoint's Advanced options. Stored header values never come back
  // from the server: a stored header shows its name, and an empty value
  // keeps what is stored.
  const [auth, setAuth] = useState<EndpointAuth>('bearer')
  const [headerRows, setHeaderRows] = useState<HeaderRow[]>([])
  const [modelNames, setModelNames] = useState('')
  const [query, setQuery] = useState('')
  const [advanced, setAdvanced] = useState(false)
  const filled = useRef(false)
  const endpointCard = useRef<HTMLDivElement | null>(null)
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
    try {
      const [a, k] = await Promise.all([api<AccessStatus>(`/api/access${fresh ? '?fresh=1' : ''}`), api<KeyStatus>('/api/me/model-key')])
      setAccess(a)
      setMe(k)
      setLoadError('')
      setLogin(l => a.login ?? l)
      // The way in use; on a shared Studio with none of the person's own
      // yet, signing in with ACTIVATE.
      setMethod(m => m ?? (a.platform.source === 'signin' ? 'signin'
        : a.platform.source === 'key' ? 'key'
        : a.single ? 'cli'
        : a.signInOffered ? 'signin' : 'key'))
      if (!filled.current && a.models.source === 'endpoint' && a.models.baseUrl) {
        setPreset(presetOf(a.models.baseUrl)); setBaseUrl(a.models.baseUrl); fillEndpoint(k.endpoint)
      }
      filled.current = true
      setLoginHost(h => h || a.login?.host || (a.cli?.contexts.find(c => c.name === a.cli?.active)?.server ?? ''))
      // Whether the platform takes a key that models take: asked after the
      // page shows, since it can take a moment.
      if (aiEnabled && a.models.source === 'key') void api<Health>(`/api/ai/health${fresh ? '?fresh=1' : ''}`).then(setHealth, () => setHealth(null))
      else setHealth(null)
    } catch (e) {
      setLoadError(errText(e))
    }
  }
  useEffect(() => { void load() }, [aiEnabled]) // eslint-disable-line react-hooks/exhaustive-deps

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

  const run = async (at: NoteAt['at'], what: () => Promise<unknown>, done?: string) => {
    setBusy(true)
    setNote(null)
    try {
      await what()
      announceChange()
      if (done) setNote({ at, tone: 'success', text: done })
      await load(true)
      return true
    } catch (e) {
      setNote({ at, tone: 'error', text: errText(e) })
      return false
    } finally {
      setBusy(false)
    }
  }

  const pickContext = (name: string | null) => {
    const clearKey = access?.platform.source === 'key' || access?.models.source === 'key'
    return run('activate',
      () => api('/api/access/source', { method: 'PUT', body: { context: name, clearKey } }),
      (name ? `The Studio now uses the pw CLI context ${name}.` : 'The Studio now follows the pw CLI\'s current context.')
        + (clearKey ? ' Your key was removed.' : ''),
    )
  }
  // Stop using the pw CLI, and Use it again. Nothing is signed out: the
  // CLI keeps its sign-ins for the other tools on this computer.
  const useCli = (use: boolean) => run('activate',
    () => api('/api/access/cli/use', { method: 'PUT', body: { use } }),
    use ? 'The Studio uses the pw CLI again.'
      : 'The Studio no longer uses the pw CLI. Each ACTIVATE part now asks to sign in; the pw CLI is still signed in for other tools.',
  )
  /** The Advanced options as the server takes them: a stored header left
   *  empty is sent by name alone, which keeps its stored value. */
  const endpointBody = () => ({
    auth,
    headers: headerRows.filter(h => h.name.trim() || h.value.trim()).map(h => ({ name: h.name.trim(), ...(h.value.trim() ? { value: h.value.trim() } : {}) })),
    models: modelNames.split(/[\n,]/).map(m => m.trim()).filter(Boolean),
    query: query.trim(),
  })
  const saveKey = (at: NoteAt['at'], key: string, url: string, endpoint?: ReturnType<typeof endpointBody>) => run(at,
    async () => {
      const st = await api<KeyStatus>('/api/me/model-key', { body: { key, persist: persistKey, baseUrl: url, ...(endpoint ? { endpoint } : {}) } })
      if (endpoint) fillEndpoint(st.endpoint)
    },
    'Saved.',
  )
  const removeKey = (at: NoteAt['at']) => run(at, async () => { await api('/api/me/model-key/clear', { body: {} }); fillEndpoint(null) }, 'Removed.')
  const test = (body: Record<string, unknown>) =>
    api<KeyTestResult>('/api/me/model-key/test', { body: { ...body, ...(chatModel() ? { model: chatModel() } : {}) } })
  const testEndpoint = async (key: string, url: string, endpoint: ReturnType<typeof endpointBody>) => {
    setBusy(true)
    setNote(null)
    try {
      setNote({ at: 'endpoint', ...testNote(await test({ key, baseUrl: url || undefined, endpoint })) })
    } catch (e) {
      setNote({ at: 'endpoint', tone: 'error', text: errText(e) })
    } finally {
      setBusy(false)
    }
  }
  /** An endpoint address typed as the ACTIVATE host, moved to the endpoint
   *  card with the key that came with it. */
  const moveToEndpoint = (url: string) => {
    // A stored endpoint's options belong to its own address.
    if (access?.models.source !== 'endpoint' || access.models.baseUrl !== url) fillEndpoint(null)
    setBaseUrl(url)
    setPreset(presetOf(url))
    setEndpointInput(keyInput)
    setKeyInput('')
    setKeyHost('')
    setNote({ at: 'endpoint', tone: 'info', text: `Moved here, with ${url} as the base URL and the key you entered. Test it, then Save and use.` })
    endpointCard.current?.scrollIntoView?.({ block: 'start', behavior: 'smooth' })
  }
  /** Test of an ACTIVATE API key. An address with a path is tested as the
   *  endpoint it is. A host whose platform path does not exist is asked
   *  once more at /v1, so the note can say what the host is. */
  const testKey = async () => {
    const typed = readPlatformHost(keyHost)
    const key = keyInput.trim()
    setBusy(true)
    setNote(null)
    try {
      if (typed.endpointUrl) {
        const url = typed.endpointUrl
        const h = await test({ key, baseUrl: url })
        setNote({ at: 'activate', ...testNote(h, `Tested as an OpenAI-compatible endpoint at ${url}, since the address has a path. `), action: aiEnabled ? { label: 'Use as an endpoint', run: () => moveToEndpoint(url) } : undefined })
        return
      }
      const h = await test({ key, baseUrl: typed.platformUrl || undefined })
      if (!h.notFound || !typed.host) { setNote({ at: 'activate', ...testNote(h) }); return }
      const v1 = `${typed.origin}/v1`
      const alt = await test({ key, baseUrl: v1, tools: false }).catch(() => null)
      const answers = alt && (alt.status === 'ok' || alt.status === 'auth')
      setNote({
        at: 'activate',
        tone: 'warning',
        text: `${typed.host} is not an ACTIVATE platform, since ${h.notFound} does not exist (404).`,
        detail: answers
          ? `${v1} answers as an OpenAI-compatible endpoint${alt!.status === 'ok' ? ` with ${alt!.models} model${alt!.models === 1 ? '' : 's'}` : ', and refused this key'}. Use it under OpenAI-compatible endpoint.`
          : 'A site AI portal or another OpenAI-compatible service goes under OpenAI-compatible endpoint, with its base URL.',
        action: aiEnabled ? { label: answers ? 'Use as an endpoint' : 'Open OpenAI-compatible endpoint', run: () => moveToEndpoint(answers ? v1 : typed.origin) } : undefined,
      })
    } catch (e) {
      setNote({ at: 'activate', tone: 'error', text: errText(e) })
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
      setNote({ at: 'activate', tone: 'error', text: errText(e) })
    } finally {
      setBusy(false)
    }
  }
  const cancelLogin = () => api<{ login: LoginState | null }>('/api/access/cli/login/cancel', { body: {} }).then(r => setLogin(r.login)).catch(() => {})
  const chooseCliPath = (p: string | null) => run('activate',
    () => api('/api/access/cli/path', { method: 'PUT', body: { path: p } }),
    p ? `The Studio now runs the pw CLI at ${p}.` : 'The Studio looks for the pw CLI by itself again.',
  )

  if (loadError && !access) return (
    <>
      <h1>Accounts</h1>
      <Callout tone="error" title="Could not read the account settings">{loadError}</Callout>
    </>
  )
  if (!access || !me) return (
    <>
      <h1>Accounts</h1>
      <p className="muted pad"><Loader inline size={16} label="Loading…" /></p>
    </>
  )
  if (!access.single && !me.verified) return (
    <>
      <h1>Accounts</h1>
      <p className="muted view-sub">
        Your accounts attach to your platform-verified identity, which this connection does not carry. Open the app
        through the platform session to add one.
      </p>
    </>
  )

  const single = access.single
  const stored = me.mode !== 'none'
  const keyStored = stored && access.models.source !== 'endpoint'
  const endpointStored = access.models.source === 'endpoint'
  const signInOffered = !single && !!access.signInOffered
  const signedIn = access.platform.source === 'signin'
  const p = access.platform.source
  const connected = p !== 'none'
  const own = p === 'signin' || p === 'key'

  // The ways in, each named for what it is and said in one line. On a
  // shared Studio, signing in with ACTIVATE comes first: it is the
  // person's own and renews itself.
  const methods: { value: Method; label: string; description: string }[] = [
    ...(single
      ? [{ value: 'cli' as Method, label: 'pw CLI on this computer', description: 'The ACTIVATE platforms the pw CLI here is signed in to. Sign it in once, with a code you approve in a browser.' }]
      : signInOffered
        ? [{ value: 'signin' as Method, label: 'Sign in with ACTIVATE', description: 'Recommended. Your own ACTIVATE account, approved once on the platform and renewed by the Studio.' }]
        : []),
    { value: 'key', label: 'ACTIVATE API key', description: 'A token or API key from your ACTIVATE account (Account, API keys), until it expires or is revoked.' },
  ]
  const shownMethod = method && methods.some(m => m.value === method) ? method : methods[0].value
  const presets: SelectItem<string>[] = PRESETS.map(x => ({ value: x.value, label: x.label }))
  const local = isLocalUrl(baseUrl)
  // With no key typed: the stored endpoint keeps its stored key, which lets
  // its options change without the key; a server on this computer, or an
  // endpoint whose key header is None, takes the no-key marker; anything
  // else needs a key.
  const sameEndpoint = endpointStored && (access.models.baseUrl ?? '') === baseUrl.trim().replace(/\/+$/, '')
  const endpointKey = endpointInput.trim() || (sameEndpoint ? '' : local || auth === 'none' ? NO_KEY : null)
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
  const noteHere = (at: NoteAt['at']) => note && note.at === at && (
    <Callout className="rag-banner access-note" tone={note.tone} live onDismiss={() => setNote(null)}
      title={note.detail ? note.text : undefined}
      action={note.action ? <Button variant="secondary" size="sm" onClick={note.action.run}>{note.action.label}</Button> : undefined}>
      {note.detail ?? note.text}
    </Callout>
  )

  // Who the Studio is signed in to ACTIVATE as, for the card's heading.
  const who = access.cli?.stopped && !own ? 'Not using the pw CLI. The Studio works as for someone without ACTIVATE; sign in to use the parts below.'
    : !connected ? 'Not signed in. The Studio works without ACTIVATE; sign in to use the parts below.'
    : `Signed in: ${describeSource(access.platform, single)}.`

  return (
    <div className="model-access accounts">
      <h1>Accounts</h1>
      <p className="muted view-sub">
        The accounts this Studio uses for you, set up once. The Studio works without any of them: chat with a model of
        your own{aiEnabled ? '' : ' (with AI features on)'}, libraries on this computer, and remote computers over ssh.
        {aiEnabled && <> Which of them your models come from is under <a href="#view=settings:access">Model access</a>.</>}
      </p>

      <div className="ix-cards">
        <IndexGroup title="ACTIVATE" help={who}
          actions={connected
            ? <StatusLabel variant="success">Signed in</StatusLabel>
            : <StatusLabel variant="muted">Not signed in</StatusLabel>}>
          <ActivatePartsTable parts={activateParts(access, aiEnabled)} />

          <div>
            <Field group label="How you sign in" className="access-source">
              <RadioGroup className="access-choices" aria-label="How you sign in" value={shownMethod}
                onValueChange={v => { setMethod(v as Method); setNote(null) }}>
                {methods.map(m => <SettingModeOption key={m.value} value={m.value} label={m.label} description={m.description} />)}
              </RadioGroup>
            </Field>

            {shownMethod === 'cli' && access.cli && (
              <CliPanel access={access} busy={busy} login={login} loginHost={loginHost} setLoginHost={setLoginHost}
                onUse={pickContext} onStartLogin={() => void startLogin()} onCancelLogin={() => void cancelLogin()} onDismissLogin={() => setLogin(null)}
                onChoosePath={x => void chooseCliPath(x)} onUseCli={x => void useCli(x)} />
            )}

            {shownMethod === 'signin' && (
              <div className="access-panel">
                <p className="muted view-sub">
                  The pw CLI on this server shows a code, which you approve on the platform. Nobody else here can use the
                  sign-in, and it lasts until you sign out. It covers {cfg.signIn ? 'My library, ' : ''}pw code, desktops,
                  your folders on ACTIVATE systems{aiEnabled ? ', the platform and its models' : ' and the platform'}.
                </p>
                <PlatformSignInPanel onChanged={() => { announceChange(); void load(true) }} />
                {signedIn && keyStored && (
                  <p className="identity-note access-override">
                    The key you added under ACTIVATE API key is kept, and used only while you are signed out.{' '}
                    <Button variant="secondary" size="sm" disabled={busy} onClick={() => void removeKey('activate')}>Remove the key</Button>
                  </p>
                )}
              </div>
            )}

            {shownMethod === 'key' && (
              <div className="access-panel">
                <p className="muted view-sub">
                  A token or API key from your ACTIVATE account (Account, API keys) covers the platform{aiEnabled ? ' and its models' : ''}.
                  The host is the platform's own, such as platform.example.com.
                  {endpointStored && ' Saving one replaces your OpenAI-compatible endpoint, which is kept in the same place.'}
                </p>
                {signedIn && (
                  <p className="identity-note access-override">
                    You are signed in with ACTIVATE, which the Studio uses before a key. A key saved here is used after you sign out.
                  </p>
                )}
                {access.cli && access.platform.source === 'cli' && (
                  <p className="identity-note access-override">A key saved here is used before the pw CLI's sign-in.</p>
                )}
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
                {typedHost.endpointUrl && aiEnabled && (
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
                    onClick={async () => { if (await saveKey('activate', keyInput.trim(), typedHost.platformUrl)) setKeyInput('') }}>Save and use</Button>
                  <Button variant="secondary" disabled={busy || !keyInput.trim()} onClick={() => void testKey()}>Test</Button>
                  {keyStored && <Button variant="danger" disabled={busy} onClick={() => void removeKey('activate')}>Remove</Button>}
                </div>
                {keyStored && (
                  <p className="muted key-note">
                    {me.mode === 'stored' && `Added ${me.addedAt?.slice(0, 10)}, ${single ? 'stored on this computer' : 'remembered in this browser'}.`}
                    {me.mode === 'session' && `Held in memory until ${me.sessionExpiresAt ? new Date(me.sessionExpiresAt).toLocaleTimeString() : 'later today'}.`}
                    {me.credExpiresAt && !me.credExpired && ` The token expires ${new Date(me.credExpiresAt).toLocaleString()}; a platform API key does not.`}
                    {me.credExpired && me.credExpiresAt && ` The token expired ${new Date(me.credExpiresAt).toLocaleString()}.`}
                  </p>
                )}
                {health?.status === 'ok' && health.platformAccess === false && access.models.source === 'key' && (
                  <p className="identity-note">
                    Models accept this key, but the platform API refuses it, so the parts above that use the platform do not
                    work with it. Sign in{single ? ' the pw CLI' : ' with ACTIVATE'}, or add a key from your ACTIVATE account (Account, API keys).
                  </p>
                )}
              </div>
            )}
            {noteHere('activate')}
          </div>

          {/* Offline or signed out, there is nothing to connect through. */}
          {aiEnabled && (single ? connected : own) && (
            <SubscriptionsPanel platformOrigin={access.platformOrigin} host={access.platform.host}
              credential={platformCredential(access.platform)} onChanged={() => { announceChange(); void load(true) }} />
          )}
        </IndexGroup>

        {aiEnabled && (
          <div ref={endpointCard}>
            <IndexGroup title="OpenAI-compatible endpoint"
              help="Models from any service that speaks the OpenAI API: a site AI portal, a provider, or a model server on this computer. It covers models only."
              actions={endpointStored ? <StatusLabel variant="success">In use for models</StatusLabel> : <StatusLabel variant="muted">None</StatusLabel>}>
              <div>
                <p className="muted view-sub">
                  The base URL usually ends in /v1. A model server on this computer keeps working with no network.
                  {keyStored && ' Saving one replaces your ACTIVATE API key, which is kept in the same place.'}
                </p>
                <div className="key-row">
                  <Field layout="inline" compact label="Endpoint" className="provider-field">
                    <Select className="provider-select" items={presets} value={preset}
                      onValueChange={v => { if (!v) return; setPreset(v); const x = PRESETS.find(y => y.value === v); if (x?.url) setBaseUrl(x.url) }} />
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
                    <Input type="password" aria-label="Endpoint key" value={endpointInput} autoComplete="off"
                      placeholder={auth === 'none' ? 'No key is sent (Advanced, Key header)' : sameEndpoint ? 'Stored; type a new key to replace it' : local ? 'API key, if the server asks for one' : 'API key'}
                      onChange={e => setEndpointInput(e.target.value)} />
                  </div>
                </div>
                <Disclosure className="endpoint-advanced" summaryClassName="endpoint-advanced-summary" panelClassName="endpoint-advanced-panel"
                  open={advanced} onOpenChange={setAdvanced} keepMounted
                  summary={`Advanced${advancedParts.length ? `: ${advancedParts.join(', ')}` : ', for portals that differ'}`}>
                  <EndpointAdvanced auth={auth} setAuth={setAuth} rows={headerRows} setRows={setHeaderRows}
                    modelNames={modelNames} setModelNames={setModelNames} query={query} setQuery={setQuery} keepStored={sameEndpoint} />
                </Disclosure>
                {!single && (
                  <label className="key-persist">
                    <Checkbox checked={persistKey} onCheckedChange={setPersistKey} />
                    Remember in this browser (a sealed cookie; the server holds the key in memory only); unchecked keeps it for about 12 hours
                  </label>
                )}
                <div className="key-row access-actions">
                  <Button variant="primary" disabled={busy || !canSaveEndpoint}
                    onClick={async () => { if (await saveKey('endpoint', endpointKey ?? '', baseUrl.trim(), endpointBody())) setEndpointInput('') }}>Save and use</Button>
                  <Button variant="secondary" disabled={busy || !baseUrl.trim()} onClick={() => void testEndpoint(endpointKey ?? NO_KEY, baseUrl.trim(), endpointBody())}>Test</Button>
                  {endpointStored && <Button variant="danger" disabled={busy} onClick={() => void removeKey('endpoint')}>Remove</Button>}
                </div>
                {preset === 'github' && (
                  <p className="muted key-note">
                    Create a fine-grained personal access token with the Models (read) permission on GitHub, and paste it as the key.
                  </p>
                )}
                {noteHere('endpoint')}
              </div>
            </IndexGroup>
          </div>
        )}

        <ClaudeAccountSlot />
      </div>

      <p className="muted key-trust">
        {single
          ? 'A key or endpoint added here, with its header values, is stored on this computer, encrypted, beside the index, and header values are never shown again. The pw CLI keeps its own sign-ins; the Studio reads them and asks the CLI to renew them, and never sends them to the browser.'
          : `${signInOffered ? 'Your ACTIVATE sign-in is kept on this server in a folder of yours and renewed by the pw CLI there; its tokens never reach the browser. ' : ''}A key added here is available to this server and its operator; encryption at rest protects the stored file, not the running process. Use a key you can revoke. A key bound to an endpoint is never passed to platform commands.`}
      </p>
    </div>
  )
}

/** What ACTIVATE turns on in this Studio, each part with its state. */
function ActivatePartsTable({ parts }: { parts: ActivatePart[] }) {
  return (
    <Field group label="What it turns on in this Studio" className="access-uses">
      <Table density="compact" className="access-uses-table" wrapperClassName="access-uses-wrap">
        <Table.Header>Part</Table.Header>
        <Table.Header>State</Table.Header>
        {parts.map(r => (
          <tr key={r.part}>
            <Table.Item wrap>{r.part}</Table.Item>
            <Table.Item wrap>{PART_STATE[r.state]}{r.state === 'endpoint' && r.detail ? <span className="muted"> {r.detail}</span> : null}</Table.Item>
          </tr>
        ))}
      </Table>
    </Field>
  )
}

function CliPanel({ access, busy, login, loginHost, setLoginHost, onUse, onStartLogin, onCancelLogin, onDismissLogin, onChoosePath, onUseCli, aiEnabled = true }: {
  access: AccessStatus
  /** Off: the panel speaks of the platform only (Workflows and libraries on
   *  ACTIVATE systems), since no models are in use. */
  aiEnabled?: boolean
  busy: boolean
  login: LoginState | null
  loginHost: string
  setLoginHost: (h: string) => void
  onUse: (name: string | null) => void
  onStartLogin: () => void
  onCancelLogin: () => void
  onDismissLogin: () => void
  onChoosePath: (path: string | null) => void
  /** Use it again (true) or Stop using the pw CLI (false). */
  onUseCli: (use: boolean) => void
}) {
  const cli = access.cli!
  if (cli.stopped) return (
    <div className="access-panel">
      <Callout tone="info" className="rag-banner" title="The Studio is not using the pw CLI"
        action={<Button variant="primary" size="sm" disabled={busy} onClick={() => onUseCli(true)}>Use it again</Button>}>
        It reads none of this computer's ACTIVATE sign-ins{cli.envKey ? ', nor PW_API_KEY' : ''}, so it works as for someone
        without ACTIVATE: every ACTIVATE part asks to sign in. The pw CLI is still signed in, for the other tools on this computer.
      </Callout>
    </div>
  )
  // A key of the person's own is used before a context; an endpoint
  // (models only, chosen under Model access) never displaces one.
  const overridden = access.platform.source === 'key'
  const following = cli.chosen === null
  const waiting = login?.state === 'waiting' || login?.state === 'starting'
  if (!cli.available && !cli.contexts.length) return (
    <div className="access-panel">
      <CliLocation cli={cli} busy={busy} onChoose={onChoosePath} />
    </div>
  )
  return (
    <div className="access-panel">
      <p className="muted view-sub">
        {aiEnabled
          ? 'The context in use covers pw code, the Workflows page, clusters, runs, libraries on ACTIVATE systems, and the platform\'s models.'
          : 'The context in use covers the Workflows page, clusters, runs, and libraries on ACTIVATE systems.'}
      </p>
      <CliLocation cli={cli} busy={busy} onChoose={onChoosePath} />
      {overridden && (
        <p className="identity-note access-override">Your ACTIVATE API key is used now. Use on a context removes it.</p>
      )}
      {cli.envKey && <p className="identity-note">PW_API_KEY is set in the Studio's environment, and it is used before the pw CLI.</p>}
      <div className="key-row cli-stop">
        <Button variant="secondary" size="sm" disabled={busy} onClick={() => onUseCli(false)}>Stop using the pw CLI</Button>
        <span className="muted key-note">The Studio then works as for someone without ACTIVATE. The pw CLI stays signed in.</span>
      </div>
      {aiEnabled && cli.gatewayFromEnv && <p className="identity-note">The model gateway is fixed by PW_GATEWAY_URL; a context on another platform covers the platform only.</p>}
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
                    {c.expired && <StatusLabel variant="error" className="ctx-badge">Expired</StatusLabel>}
                    {c.renewError && <div className="muted ctx-error">{c.renewError}</div>}
                  </Table.Item>
                  <Table.Item>
                    {inUse
                      ? <StatusLabel variant="success">In use</StatusLabel>
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

/** Subscriptions connected through ACTIVATE: the platform holds them and
 *  serves their models through its gateway, so the Studio lists them with
 *  the rest once connected. They are asked for again whenever the platform
 *  credential changes, so an answer to the previous one does not stay. */
export function SubscriptionsPanel({ platformOrigin, host, credential, onChanged }: {
  platformOrigin: string | null; host: string | null; credential: string; onChanged: () => void
}) {
  const [subs, setSubs] = useState<Subscriptions | null>(null)
  const [error, setError] = useState('')
  const [device, setDevice] = useState<(Device & { id: string; intervalSec: number; expiresAt: string | null }) | null>(null)
  const [busy, setBusy] = useState(false)
  const [done, setDone] = useState('')
  const timer = useRef<number | null>(null)
  const asked = useRef(0)

  // Only the latest request's answer is shown.
  const load = () => {
    const n = ++asked.current
    return api<Subscriptions>('/api/access/subscriptions')
      .then(s => { if (n === asked.current) { setSubs(s); setError('') } })
      .catch(e => { if (n === asked.current) setError(errText(e)) })
  }
  useEffect(() => {
    setSubs(null); setError(''); setDone('')
    void load()
  }, [credential])
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
