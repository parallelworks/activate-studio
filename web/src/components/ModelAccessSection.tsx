import { useEffect, useState } from 'react'
import { Button, Callout, type CalloutTone, ExternalLink, Field, Loader, RadioGroup, SettingModeOption } from '@parallelworks/ui'
import './ModelAccessSection.css'
import { NeedsActivate } from './NeedsActivate'
import { type AccessStatus, type Health, type KeyStatus, announceChange, api, describeSource, errText } from './accessShared'
import { ACCOUNTS_HREF, accountsHref, openAccounts } from '../accounts'

export { endpointBase, readPlatformHost, testNote, type AccessStatus, type KeyTestResult } from './accessShared'

/**
 * Settings, Model access: where the Studio's models come from, and whether
 * they answer. The accounts behind them (ACTIVATE, an OpenAI-compatible
 * endpoint) are set up under Settings, Accounts; this page chooses between
 * them and links there.
 *
 * The server takes models from a stored endpoint whenever there is one,
 * and from the ACTIVATE account otherwise, so choosing ACTIVATE while an
 * endpoint is stored removes the endpoint, after saying so.
 */

type From = 'activate' | 'endpoint' | 'sidecar' | 'admin-endpoint' | 'none'

/** Where models come from now, as the choice names it. */
export function modelsFrom(a: AccessStatus): From {
  switch (a.models.source) {
    case 'endpoint': return 'endpoint'
    case 'sidecar': return 'sidecar'
    case 'admin-endpoint': return 'admin-endpoint'
    case 'none': return 'none'
    default: return 'activate'
  }
}

export function ModelAccessSection({ requirePersonalKey = false }: { requirePersonalKey?: boolean }) {
  const [access, setAccess] = useState<AccessStatus | null>(null)
  const [me, setMe] = useState<KeyStatus | null>(null)
  const [health, setHealth] = useState<Health | null>(null)
  const [models, setModels] = useState<string[] | null>(null)
  const [modelsError, setModelsError] = useState('')
  const [checking, setChecking] = useState(false)
  const [loadError, setLoadError] = useState('')
  // A choice that needs a step first: an account to set up, or the
  // endpoint to remove.
  const [pending, setPending] = useState<From | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const load = async (fresh = false) => {
    setChecking(true)
    try {
      const [a, k] = await Promise.all([api<AccessStatus>(`/api/access${fresh ? '?fresh=1' : ''}`), api<KeyStatus>('/api/me/model-key')])
      setAccess(a)
      setMe(k)
      setLoadError('')
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
  useEffect(() => {
    void load()
    // Accounts changed in another tab of the page, or by the setup wizard.
    const again = () => { void load(true) }
    window.addEventListener('ade:model-access-changed', again)
    return () => window.removeEventListener('ade:model-access-changed', again)
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

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
        Your own model access attaches to your platform-verified identity, which this connection does not carry. Open the
        app through the platform session to add it.
      </p>
    </>
  )

  const single = access.single
  const from = modelsFrom(access)
  const connected = access.platform.source !== 'none'
  const own = access.platform.source === 'signin' || access.platform.source === 'key'
  const offline = health?.status === 'offline'
  const keyRequired = health?.status === 'key-required'
  const title = from === 'none'
    ? 'No model access yet'
    : keyRequired ? 'This deployment requires your own model credential'
    : `Models: ${describeSource(access.models, single)}`
  const tone: CalloutTone = from === 'none' || keyRequired
    ? (requirePersonalKey || single ? 'warning' : 'default')
    : !health ? 'default'
    : health.status === 'ok' ? 'success'
    : offline ? 'warning'
    : 'error'
  const modelCount = models?.length ?? health?.models ?? 0

  const choices: { value: From; label: string; description: string }[] = [
    {
      value: 'activate', label: 'ACTIVATE',
      description: connected
        ? `The models of your ACTIVATE account, with ${describeSource(access.platform, single)}.`
        : 'The models of your ACTIVATE account. Sign in with ACTIVATE under Accounts first.',
    },
    {
      value: 'endpoint', label: 'OpenAI-compatible endpoint',
      description: from === 'endpoint'
        ? `${access.models.baseUrl ?? 'Your endpoint'}, set up under Accounts.`
        : 'A site AI portal, a provider, or a model server on this computer. Add one under Accounts first.',
    },
    ...(from === 'sidecar' ? [{ value: 'sidecar' as From, label: 'The model served beside this Studio', description: 'Set by the deployment, for anyone without an account of their own.' }] : []),
    ...(from === 'admin-endpoint' ? [{ value: 'admin-endpoint' as From, label: 'The administrator\'s endpoint', description: `${access.models.baseUrl ?? 'An OpenAI-compatible endpoint'}, set by the administrator for anyone without an account of their own.` }] : []),
  ]
  const shown = pending ?? from

  const useActivate = async () => {
    setBusy(true); setError('')
    try {
      // Only the endpoint: an ACTIVATE key kept beside it stays.
      await api('/api/me/model-key/clear', { body: { which: 'endpoint' } })
      announceChange()
      setPending(null)
      await load(true)
    } catch (e) { setError(errText(e)) } finally { setBusy(false) }
  }
  const choose = (v: From) => {
    setError('')
    if (v === from) { setPending(null); return }
    setPending(v)
  }

  return (
    <div className="model-access">
      <h1>Model access</h1>
      <p className="muted view-sub">
        Where the Studio's models come from, for chat and the assistant. The accounts behind them are under{' '}
        <a href={ACCOUNTS_HREF}>Accounts</a>.
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
        {from === 'none' ? (
          <p className="muted key-note">
            Sign in with ACTIVATE, or add an OpenAI-compatible endpoint, under <a href={ACCOUNTS_HREF}>Accounts</a>.
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
        {me.mode !== 'none' && me.credExpired && me.credExpiresAt && (
          <p className="identity-note">Your token expired {new Date(me.credExpiresAt).toLocaleString()}. Add a current one under <a href={accountsHref('activate')}>Accounts</a>.</p>
        )}
      </Callout>

      <Field group label="Where models come from" className="access-source">
        <RadioGroup className="access-choices" aria-label="Where models come from" value={shown === 'none' ? '' : shown}
          onValueChange={v => choose(v as From)}>
          {choices.map(c => <SettingModeOption key={c.value} value={c.value} label={c.label} description={c.description} />)}
        </RadioGroup>
      </Field>

      {pending === 'activate' && (!connected
        ? <NeedsActivate>The models of your ACTIVATE account come once you sign in under Accounts.</NeedsActivate>
        : from === 'endpoint' && (
          <Callout tone="warning" className="rag-banner" title="Models come from your endpoint while it is stored"
            action={<Button variant="secondary" size="sm" onClick={() => setPending(null)}>Keep the endpoint</Button>}>
            <p className="identity-note">
              Using ACTIVATE removes the endpoint at {access.models.baseUrl ?? 'its address'}, with its key and options.
            </p>
            <div className="key-row">
              <Button variant="primary" size="sm" loading={busy} onClick={() => void useActivate()}>Remove the endpoint and use ACTIVATE</Button>
            </div>
          </Callout>
        ))}
      {pending === 'endpoint' && (
        <Callout tone="info" className="rag-banner" title="Add an endpoint under Accounts"
          action={<Button variant="secondary" size="sm" onClick={() => openAccounts('endpoint')}>Open Accounts</Button>}>
          Under OpenAI-compatible endpoint, enter its base URL and key, then Save and use. Models then come from it.
        </Callout>
      )}
      {error && <Callout tone="error" live>{error}</Callout>}
    </div>
  )
}
