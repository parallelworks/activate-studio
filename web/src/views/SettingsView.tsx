import { useEffect, useState } from 'react'
import { CopyToClipboard } from '@parallelworks/ui'
import { ACCENTS, SURFACES, applyAccent, applySurface } from '../accents'
import { useAppConfig } from '../config'
import { bannerInk } from '../components/ClassificationBanner'

interface Effective {
  appName: string
  kbLabel: string
  theme: 'light' | 'dark'
  accent: string
  surface: string
  iconUrl: string
  iconUrlDark: string
  sweepIntervalSec: number
  suggestedPrompts: string[]
  visionModel: string
  disabledTools: string[]
  customTools: { name: string; description: string; command: string }[]
  ragDefaultModel: string
  ragTopK: number
  ragAllowDeploymentKey: boolean
  ragAdvertiseRagModel: boolean
  ragAdvertiseAgentModel: boolean
  ragEndpointAutoStart: boolean
  ragEndpointName: string
  requirePersonalKey: boolean
  chatSharedHistory: boolean
  bannerText: string
  bannerColor: string
  bannerWhenEmbedded: boolean
  allowKeySharing: boolean
}

interface CatalogTool { name: string; description: string; builtin: boolean; enabled: boolean; parameters?: unknown; command?: string; implementation?: string; calls?: string }
interface RagCall {
  at: string; model: string; mode: string; underlying: string; authSource: string
  durationMs: number | null; status: string; detail: string | null
  retrieval: { terms: string[]; blocks: number; chars: number } | null
}

interface ExtDoc { name: string; description: string; file: string; active?: boolean; command?: string }
interface Extensions { dir: string; tools: ExtDoc[]; skills: ExtDoc[]; agents: ExtDoc[] }

const TOOL_GROUPS: [string, string[]][] = [
  ['Knowledge base', ['search_kb', 'read_kb_file', 'list_kb_dir', 'query_corpus', 'get_labels', 'apply_labels']],
  ['Workflows', ['list_workflows', 'get_workflow', 'compose_workflow', 'run_workflow', 'workflow_runs', 'workflow_run_detail', 'pw_help']],
  ['Clusters', ['list_clusters', 'cluster_command']],
  ['Display and skills', ['show_in_viewer', 'use_skill', 'studio_docs']],
]

/** Markings and their colors, following the platform's banners. Any of
 *  them can be edited after picking one; the list is a starting point. */
const BANNER_PRESETS: { label: string; text: string; color: string }[] = [
  { label: 'Unclassified', text: '***** UNCLASSIFIED *****', color: '#1c5180' },
  { label: 'CUI', text: '***** CONTROLLED UNCLASSIFIED INFORMATION (CUI) *****', color: '#24612e' },
  { label: 'CUI at IL5 High', text: '***** APPROVED FOR IL5 HIGH - CONTROLLED UNCLASSIFIED INFORMATION (CUI) *****', color: '#24612e' },
  { label: 'Secret', text: '***** SECRET *****', color: '#c8102e' },
  { label: 'Top Secret', text: '***** TOP SECRET *****', color: '#ff8c00' },
  { label: 'Top Secret / SCI', text: '***** TOP SECRET // SCI *****', color: '#fce83a' },
]

type SectionId = 'general' | 'access' | 'tools' | 'rag' | 'ext'

/** Dropdown over the live model catalog; a saved value not in the catalog
 *  stays selectable so settings never silently break. */
function ModelSelect({ value, models, allowEmpty, emptyLabel, onChange }: {
  value: string
  models: string[]
  allowEmpty?: boolean
  emptyLabel?: string
  onChange: (v: string) => void
}) {
  // Standalone deployments may list no models (local endpoint, no catalog);
  // the picker always offers typing an id directly.
  const [typing, setTyping] = useState<string | null>(null)
  const opts = [...new Set([...(value && !models.includes(value) ? [value] : []), ...models])]
  if (typing !== null) {
    return (
      <input className="field" autoFocus value={typing}
        placeholder="model id, e.g. llama-3.3-70b"
        onChange={e => setTyping(e.target.value)}
        onBlur={() => { const v = typing.trim(); setTyping(null); if (v) onChange(v) }}
        onKeyDown={e => {
          if (e.key === 'Enter') (e.target as HTMLInputElement).blur()
          if (e.key === 'Escape') setTyping(null)
        }}
      />
    )
  }
  return (
    <select className="field" value={value} onChange={e => {
      if (e.target.value === '__type__') setTyping(value)
      else onChange(e.target.value)
    }}>
      {allowEmpty && <option value="">{emptyLabel ?? '(none)'}</option>}
      {opts.map(m => <option key={m} value={m}>{m}</option>)}
      <option value="__type__">Type a model id…</option>
    </select>
  )
}

export function SettingsView() {
  const cfg = useAppConfig()
  const [form, setForm] = useState<Effective | null>(null)
  const [deploymentCred, setDeploymentCred] = useState<boolean | null>(null)
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [models, setModels] = useState<string[]>([])
  const [catalog, setCatalog] = useState<CatalogTool[]>([])
  const [ext, setExt] = useState<Extensions | null>(null)
  const [specOpen, setSpecOpen] = useState<string | null>(null)
  const [me, setMe] = useState<{ authEnabled?: boolean; verified: boolean; mode: 'stored' | 'session' | 'none'; last4?: string; addedAt?: string; sessionExpiresAt?: string; kind?: string; credExpiresAt?: string | null; credExpired?: boolean; baseUrl?: string | null; gatewayHost?: string
    shared?: { active: boolean; last4?: string; sharedBy?: string; sharedAt?: string; credExpired?: boolean; mine?: boolean }
    sharingAllowed?: boolean } | null>(null)
  const [keyInput, setKeyInput] = useState('')
  const [providerMode, setProviderMode] = useState<'platform' | 'activate' | 'custom'>('platform')
  const [providerHost, setProviderHost] = useState('')
  const [baseUrlInput, setBaseUrlInput] = useState('')

  const effectiveBaseUrl = () => {
    if (providerMode === 'platform') return ''
    if (providerMode === 'activate') {
      const host = providerHost.trim().replace(/^https?:\/\//, '').replace(/\/.*$/, '')
      return host ? `https://${host}/api/openai/v1` : ''
    }
    return baseUrlInput.trim()
  }
  const [persistKey, setPersistKey] = useState(true)
  const [keyBusy, setKeyBusy] = useState(false)
  const [keyNote, setKeyNote] = useState('')
  const [section, setSection] = useState<SectionId>(() => {
    const m = location.hash.match(/^#view=settings:([a-z-]+)/)
    return (m?.[1] as SectionId) || 'general'
  })

  const goSection = (id: SectionId) => {
    setSection(id)
    history.replaceState(null, '', `${location.pathname}${location.search}#view=settings:${id}`)
  }
  const [ragEp, setRagEp] = useState<{ state: string; name: string | null; url: string | null; detail: string | null } | null>(null)
  const [ragCalls, setRagCalls] = useState<RagCall[]>([])
  const refreshRagCalls = () => fetch('/api/rag/calls').then(r => r.json()).then(d => setRagCalls(d.calls ?? [])).catch(() => {})

  const refreshRagEp = () => fetch('/api/rag/endpoint').then(r => r.json()).then(setRagEp).catch(() => {})
  const ragEpAction = async (verb: 'start' | 'stop') => {
    await fetch(`/api/rag/endpoint/${verb}`, { method: 'POST' }).then(r => r.json()).then(setRagEp).catch(() => {})
    setTimeout(refreshRagEp, 2500)
    setTimeout(refreshRagEp, 8000)
  }

  useEffect(() => {
    fetch('/api/settings').then(r => r.json())
      .then(d => { setForm(d.effective); setDeploymentCred(d.deploymentCredential ?? null) })
      .catch(() => setNote('Could not load settings.'))
    fetch('/api/chat/tools').then(r => r.json())
      .then(d => setCatalog(d.tools ?? []))
      .catch(() => {})
    fetch('/api/extensions').then(r => r.json()).then(setExt).catch(() => {})
    fetch('/api/chat/models?config=1').then(r => r.json())
      .then(d => setModels((d.models ?? []).map((m: { id: string }) => m.id)))
      .catch(() => {})
    fetch('/api/me/model-key').then(r => r.json()).then(d => { setMe(d); if (d?.verified && d.mode !== 'none') refreshAccess() }).catch(() => {})
    refreshRagEp()
    refreshRagCalls()
    const onSection = (e: Event) => {
      const d = (e as CustomEvent).detail as { view?: string; section?: string }
      if (d?.view === 'settings' && d.section) setSection(d.section as SectionId)
    }
    window.addEventListener('ade-view-section', onSection)
    return () => window.removeEventListener('ade-view-section', onSection)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const keyAction = async (action: 'set' | 'clear' | 'share' | 'unshare', body?: unknown) => {
    setKeyBusy(true)
    setKeyNote('')
    try {
      const res = await fetch(action === 'set' ? '/api/me/model-key' : `/api/me/model-key/${action}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: body === undefined ? '{}' : JSON.stringify(body),
      })
      const data = await res.json()
      if (!res.ok) throw new Error(data.error ?? `${res.status}`)
      setMe(data)
      if (data.mode && data.mode !== 'none') refreshAccess()
      else setAccessHealth(null)
      return true
    } catch (e) {
      setKeyNote(String((e as Error).message ?? e))
      return false
    } finally {
      setKeyBusy(false)
    }
  }

  const [accessHealth, setAccessHealth] = useState<{ status: string; models: number; gatewayHost?: string; message?: string | null } | null>(null)

  const refreshAccess = () => {
    fetch('/api/me/model-key/test', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' })
      .then(r => r.json()).then(h => setAccessHealth(h.error ? null : h)).catch(() => setAccessHealth(null))
    fetch('/api/chat/models?config=1').then(r => r.json())
      .then(d => setModels((d.models ?? []).map((m: { id: string }) => m.id)))
      .catch(() => {})
  }

  const testKey = async (candidate?: string) => {
    setKeyBusy(true)
    setKeyNote('')
    try {
      const res = await fetch('/api/me/model-key/test', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(candidate ? { key: candidate, baseUrl: effectiveBaseUrl() || undefined } : {}),
      })
      const h = await res.json()
      if (!res.ok) throw new Error(h.error ?? `${res.status}`)
      setKeyNote(h.status === 'ok' ? `Key works: ${h.models} models available.` : `Key check failed (${h.status}): ${h.message ?? ''}`)
    } catch (e) {
      setKeyNote(String((e as Error).message ?? e))
    } finally {
      setKeyBusy(false)
    }
  }

  const save = async (reload = true) => {
    if (!form) return
    setBusy(true)
    setNote('')
    try {
      const res = await fetch('/api/settings', {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(form),
      })
      const data = await res.json()
      if (!res.ok) throw new Error(data.error ?? `${res.status}`)
      if (reload) {
        setNote('Saved. Reloading…')
        setTimeout(() => location.reload(), 600)
      } else {
        setNote('Saved.')
        setBusy(false)
      }
    } catch (e) {
      setNote(String((e as Error).message ?? e))
      setBusy(false)
    }
  }

  if (!form) return <div className="settings-view"><div className="card settings-card"><p className="muted pad">{note || 'Loading…'}</p></div></div>

  const pickableModels = models.filter(m => !/studio-(agent|rag)/.test(m))

  const sections: { id: SectionId; label: string }[] = [
    { id: 'general', label: 'General' },
    ...(me?.authEnabled ? [{ id: 'access' as SectionId, label: 'Model access' }] : []),
    { id: 'rag', label: 'RAG endpoint' },
    { id: 'tools', label: 'Assistant tools' },
    { id: 'ext', label: 'Extensions' },
  ]

  const saveRow = (reload = true) => (
    <div className="query-actions">
      <button className="btn-primary" onClick={() => void save(reload)} disabled={busy}>{busy ? 'Saving…' : 'Save settings'}</button>
      {note && <span className="muted">{note}</span>}
    </div>
  )

  return (
    <div className="settings-view">
      <div className="help-docs card">
        <nav className="help-nav">
          <div className="help-nav-head"><span>Settings</span></div>
          {sections.map(s => (
            <button key={s.id} className={section === s.id ? 'active' : ''} onClick={() => goSection(s.id)}>
              <span>{s.label}</span>
            </button>
          ))}
        </nav>
        <article className="help-content settings-content">

          {section === 'general' && (
            <>
              <h1>General</h1>
              <p className="muted view-sub">
                Saved settings apply immediately and override the deployment's environment defaults; they live beside the index, not in the repository. Brand icon and model credentials stay in the environment (see docs/CUSTOMIZATION.md).
              </p>
              <div className="settings-grid">
                <div>
                  <label className="field-label">Product name</label>
                  <input className="field" value={form.appName} onChange={e => setForm({ ...form, appName: e.target.value })} />
                </div>
                <div>
                  <label className="field-label">Default theme</label>
                  <select className="field" value={form.theme} onChange={e => setForm({ ...form, theme: e.target.value as 'light' | 'dark' })}>
                    <option value="light">light</option>
                    <option value="dark">dark</option>
                  </select>
                </div>
                <div>
                  <label className="field-label">Accent color</label>
                  <div className="accent-row">
                    {Object.entries(ACCENTS).map(([k, a]) => (
                      <button
                        key={k}
                        type="button"
                        className={`accent-swatch ${form.accent === k ? 'active' : ''}`}
                        title={a.label}
                        style={{ background: a.light[0] }}
                        onClick={() => { setForm({ ...form, accent: k }); applyAccent(k) }}
                      />
                    ))}
                    <input
                      type="color"
                      className={`accent-swatch accent-custom ${form.accent.startsWith('custom:') ? 'active' : ''}`}
                      title="Custom accent"
                      value={form.accent.startsWith('custom:') ? form.accent.slice(7) : '#06354f'}
                      onChange={e => { const v = `custom:${e.target.value}`; setForm({ ...form, accent: v }); applyAccent(v) }}
                    />
                  </div>
                </div>
                <div>
                  <label className="field-label">Surface tone</label>
                  <select className="field" value={form.surface}
                    onChange={e => { setForm({ ...form, surface: e.target.value }); applySurface(e.target.value) }}>
                    {Object.entries(SURFACES).map(([k, v]) => <option key={k} value={k}>{v.label}</option>)}
                  </select>
                </div>
                <div>
                  <label className="field-label">Brand icon (URL, or a path on the server)</label>
                  <input className="field" value={form.iconUrl} placeholder="https://example.org/logo.png"
                    onChange={e => setForm({ ...form, iconUrl: e.target.value })} />
                </div>
                <div>
                  <label className="field-label">Brand icon for dark mode (optional)</label>
                  <input className="field" value={form.iconUrlDark} placeholder="leave empty to use the same image"
                    onChange={e => setForm({ ...form, iconUrlDark: e.target.value })} />
                </div>
                <div className="banner-field">
                  <label className="field-label">Classification banner (empty for none)</label>
                  <div className="banner-row">
                    <select className="field banner-preset" value=""
                      onChange={e => {
                        const p = BANNER_PRESETS.find(x => x.label === e.target.value)
                        if (p) setForm({ ...form, bannerText: p.text, bannerColor: p.color })
                      }}>
                      <option value="">Marking…</option>
                      {BANNER_PRESETS.map(p => <option key={p.label} value={p.label}>{p.label}</option>)}
                    </select>
                    <input className="field banner-text" value={form.bannerText ?? ''}
                      placeholder="Text across the top, or pick a marking"
                      onChange={e => setForm({ ...form, bannerText: e.target.value })} />
                    <input className="field banner-hex" value={form.bannerColor ?? ''} placeholder="#24612e"
                      onChange={e => setForm({ ...form, bannerColor: e.target.value })} />
                  </div>
                  {(form.bannerText ?? '').trim() && (
                    <div className="cls-banner banner-preview"
                      style={{ background: form.bannerColor, color: bannerInk(form.bannerColor) }}>
                      {form.bannerText}
                    </div>
                  )}
                  <label className="key-persist">
                    <input type="checkbox" checked={!!form.bannerWhenEmbedded}
                      onChange={e => setForm({ ...form, bannerWhenEmbedded: e.target.checked })} />
                    <span>Show it inside the platform frame too, which already draws its own</span>
                  </label>
                </div>
                <div>
                  <label className="field-label">Background sync interval (seconds, 0 pauses)</label>
                  <input className="field" type="number" min={0} max={86400} value={form.sweepIntervalSec}
                    onChange={e => setForm({ ...form, sweepIntervalSec: Number(e.target.value) })} />
                </div>
                <div>
                  <label className="field-label">Vision model for image captioning</label>
                  <ModelSelect
                    value={form.visionModel}
                    models={pickableModels}
                    allowEmpty
                    emptyLabel="disabled (no image captioning)"
                    onChange={v => setForm({ ...form, visionModel: v })}
                  />
                  {form.visionModel && deploymentCred === false && (
                    <p className="muted key-note">
                      Captioning runs during background indexing, when no user is present, so it uses
                      the deployment credential or the host's pw CLI login rather than a personal key.
                      This deployment has neither, so images will be indexed by OCR and filename only.
                    </p>
                  )}
                </div>
              </div>
              {me?.authEnabled && (
                <label className="key-persist">
                  <input type="checkbox" checked={form.requirePersonalKey}
                    onChange={e => setForm({ ...form, requirePersonalKey: e.target.checked })} />
                  Require each user to add their own model credential (chat and models refuse the deployment credential; browsing, search, and adding material stay open to everyone)
                </label>
              )}
              {me?.authEnabled && (
                <label className="key-persist">
                  <input type="checkbox" checked={form.allowKeySharing}
                    onChange={e => setForm({ ...form, allowKeySharing: e.target.checked })} />
                  Let a user share their own model credential with everyone here, standing in for the deployment credential (useful where the deployment has none; the sharer's quota covers the group)
                </label>
              )}
              {me?.authEnabled && (
                <label className="key-persist">
                  <input type="checkbox" checked={form.chatSharedHistory}
                    onChange={e => setForm({ ...form, chatSharedHistory: e.target.checked })} />
                  Allow users to see each other's chat sessions in the chat interface (the rail still defaults each user to their own, with a toggle to show all; off removes that option entirely; exported transcripts remain in the knowledge base tree either way)
                </label>
              )}
              <label className="field-label">Chat starter prompts (one per line)</label>
              <textarea
                className="field prompts-box"
                rows={10}
                value={form.suggestedPrompts.join('\n')}
                onChange={e => setForm({ ...form, suggestedPrompts: e.target.value.split('\n') })}
              />
              {saveRow()}
            </>
          )}

          {section === 'access' && (
            <>
              <h1>Model access</h1>
              {!me?.verified ? (
                <p className="muted view-sub">
                  Personal model keys attach to your platform-verified identity, which this connection does not carry. Open the app through the platform session to add one; until then, requests use the deployment's credential.
                </p>
              ) : (
                <>
                  <div className={`rag-banner ${me.mode !== 'none' && accessHealth?.status === 'ok' ? 'connected' : me.mode !== 'none' && accessHealth && accessHealth.status !== 'ok' ? 'errored' : ''}`}>
                    <div className="rag-banner-head">
                      <span className={`status-dot ${me.mode === 'none' ? (form.requirePersonalKey ? 'warn' : 'off') : accessHealth?.status === 'ok' ? 'ok' : accessHealth ? 'warn' : 'off'}`} />
                      <strong>
                        {me.mode === 'none' && (form.requirePersonalKey
                          ? 'Not connected; this deployment requires your own model credential'
                          : 'No personal key loaded; requests use the deployment credential')}
                        {me.mode !== 'none' && accessHealth?.status === 'ok' && `Connected with your ${me.kind === 'token' ? 'token' : 'key'} ending ${me.last4}`}
                        {me.mode !== 'none' && accessHealth && accessHealth.status !== 'ok' && `Your ${me.kind === 'token' ? 'token' : 'key'} ending ${me.last4} is not working (${accessHealth.status})`}
                        {me.mode !== 'none' && !accessHealth && `Your ${me.kind === 'token' ? 'token' : 'key'} ending ${me.last4} is loaded`}
                      </strong>
                      {me.mode !== 'none' && (
                        <span className="rag-banner-actions">
                          <button className="btn-secondary" onClick={refreshAccess}>Re-check</button>
                        </span>
                      )}
                    </div>
                    {me.mode !== 'none' && accessHealth?.status === 'ok' && (
                      <div className="rag-banner-models">
                        {models.length || accessHealth.models} models available to you{accessHealth.gatewayHost ? ` via ${accessHealth.gatewayHost}` : ''}:
                        <div className="rag-chip-row">
                          {models.slice(0, 12).map(m => <code key={m}>{m}</code>)}
                          {models.length > 12 && <span className="muted">and {models.length - 12} more</span>}
                        </div>
                      </div>
                    )}
                    {me.mode !== 'none' && accessHealth && accessHealth.status !== 'ok' && accessHealth.message && (
                      <p className="muted key-note">{accessHealth.message}</p>
                    )}
                    {me.mode !== 'none' && (
                      <p className="muted key-note">
                        {me.mode === 'stored' && `Added ${me.addedAt?.slice(0, 10)}, stored encrypted on this server.`}
                        {me.mode === 'session' && `Held in memory for this session only (until ${me.sessionExpiresAt ? new Date(me.sessionExpiresAt).toLocaleTimeString() : 'soon'}).`}
                        {me.credExpiresAt && !me.credExpired && ` The ${me.kind === 'token' ? 'token itself expires' : 'credential expires'} ${new Date(me.credExpiresAt).toLocaleString()}.`}
                        {me.baseUrl && ` Provider: ${me.baseUrl}.`}
                        {me.credExpired && ' THIS TOKEN HAS EXPIRED; paste a fresh one.'}
                      </p>
                    )}
                  </div>
                  {me.mode === 'none' && (
                    <p className="muted view-sub">
                      {form.requirePersonalKey
                        ? 'Add your own API key or a platform token below to use chat and models on this deployment. Browsing, search, and adding material work without one.'
                        : 'Add your own API key or a platform token to call the gateway (and platform tools) as yourself.'}
                    </p>
                  )}
                  <div className="key-row">
                    <input
                      className="field grow"
                      type="password"
                      value={keyInput}
                      placeholder="Paste your PW API key, a platform token, or another provider's key"
                      autoComplete="off"
                      onChange={e => setKeyInput(e.target.value)}
                    />
                    <button className="btn-primary" disabled={keyBusy || !keyInput.trim()}
                      onClick={async () => { if (await keyAction('set', { key: keyInput.trim(), persist: persistKey, baseUrl: effectiveBaseUrl() })) { setKeyInput(''); void testKey() } }}>
                      Save and test
                    </button>
                    <button className="btn-secondary" disabled={keyBusy || (!keyInput.trim() && me.mode === 'none')}
                      onClick={() => void testKey(keyInput.trim() || undefined)}>Test</button>
                    {me.mode !== 'none' && (
                      <button className="btn-danger-outline" disabled={keyBusy} onClick={() => void keyAction('clear')}>Remove</button>
                    )}
                  </div>
                  <div className="key-row provider-row">
                    <label className="field-label provider-label">Credential provider</label>
                    <select className="field" value={providerMode} onChange={e => setProviderMode(e.target.value as typeof providerMode)}>
                      <option value="platform">{me?.gatewayHost ? `This deployment's gateway (${me.gatewayHost})` : "This deployment's gateway (default)"}</option>
                      <option value="activate">Another ACTIVATE platform…</option>
                      <option value="custom">OpenAI-compatible endpoint…</option>
                    </select>
                    {providerMode === 'activate' && (
                      <input className="field grow" value={providerHost} autoComplete="off"
                        placeholder="platform host, e.g. activate.parallel.works"
                        onChange={e => setProviderHost(e.target.value)} />
                    )}
                    {providerMode === 'custom' && (
                      <input className="field grow" value={baseUrlInput} autoComplete="off"
                        placeholder="base URL, e.g. https://host/v1"
                        onChange={e => setBaseUrlInput(e.target.value)} />
                    )}
                  </div>
                  <label className="key-persist">
                    <input type="checkbox" checked={persistKey} onChange={e => setPersistKey(e.target.checked)} />
                    Remember on this server (encrypted at rest); unchecked keeps it in memory for about 12 hours only
                  </label>
                  {me.shared?.active ? (
                    <div className="rag-banner connected shared-key">
                      <div className="rag-banner-head">
                        <span className={`status-dot ${me.shared.credExpired ? 'warn' : 'ok'}`} />
                        <strong>
                          Key ending {me.shared.last4} is shared with everyone on this deployment
                          {me.shared.sharedBy ? `, by ${me.shared.mine ? 'you' : me.shared.sharedBy}` : ''}
                          {me.shared.sharedAt ? ` on ${me.shared.sharedAt.slice(0, 10)}` : ''}
                          {me.shared.credExpired ? '; it has expired and is no longer used' : ''}
                        </strong>
                        <span className="rag-banner-actions">
                          <button className="btn-danger-outline" disabled={keyBusy}
                            onClick={() => void keyAction('unshare')}>Stop sharing</button>
                        </span>
                      </div>
                      <p className="muted key-note">
                        Anyone here without a key of their own calls the gateway with this one, so its owner's
                        quota and usage cover the deployment. Their own keys still take precedence. Anyone can
                        stop the sharing, since the person who started it may not be around when it needs to end.
                      </p>
                    </div>
                  ) : me.mode !== 'none' && me.sharingAllowed && (
                    <div className="key-row share-row">
                      <button className="btn-secondary" disabled={keyBusy}
                        onClick={() => void keyAction('share')}>Share this key with everyone</button>
                      <span className="muted key-note">
                        Lets users here who have no key of their own work with yours, which spends your quota
                        and runs as you. You can stop it at any time.
                      </span>
                    </div>
                  )}
                  <p className="muted key-trust">
                    A key added here is available to this server and its operator; encryption at rest protects the stored file, not the running process. Use a key you can revoke. If your provider locks keys on a schedule, Test tells you the key’s current state. With a custom provider, chat and models use that provider while platform tools (clusters, workflows) fall back to the deployment credential; a non-platform key is never passed to platform commands.
                  </p>
                  {keyNote && <p className="muted key-note">{keyNote}</p>}
                </>
              )}
            </>
          )}

          {section === 'tools' && (
            <>
              <h1>Assistant tools</h1>
              <p className="muted view-sub">
                Everything the chat assistant can call. Built-in tools are part of the server; unchecking one hides it from the model, and clicking a name shows what it actually calls plus its exact specification, so you can copy the shape into a custom tool or extension file with your own changes. Custom tools are commands you define here (saved with these settings, not as files); each appears to the assistant as a callable tool and runs on this server with its output returned to the conversation.
              </p>
              <div className="tools-grid">
              {TOOL_GROUPS.map(([group, names]) => {
                const rows = catalog.filter(t => t.builtin && names.includes(t.name))
                if (!rows.length) return null
                return (
                  <div key={group}>
                    <div className="tool-group">{group}</div>
                    {rows.map(t => (
                      <div key={t.name}>
                        <div className="tool-row">
                          <input
                            type="checkbox"
                            checked={!form.disabledTools.includes(t.name)}
                            onChange={e => setForm({
                              ...form,
                              disabledTools: e.target.checked
                                ? form.disabledTools.filter(n => n !== t.name)
                                : [...form.disabledTools, t.name],
                            })}
                          />
                          <button className="tool-name" title="Show the parameter schema this tool accepts"
                            onClick={() => setSpecOpen(o => o === t.name ? null : t.name)}>
                            <code>{t.name}</code>
                          </button>
                          <span className="tool-desc">{t.description}</span>
                        </div>
                        {/* What the tool actually runs is the useful part, so
                            it sits under every row rather than behind a click
                            with the wire schema on top of it. */}
                        {t.calls && <p className="tool-calls">{t.calls}</p>}
                        {t.command && <p className="tool-calls"><code>{t.command}</code></p>}
                        {specOpen === t.name && (
                          <pre className="tool-spec">{JSON.stringify({ name: t.name, description: t.description, parameters: t.parameters }, null, 2)}</pre>
                        )}
                      </div>
                    ))}
                  </div>
                )
              })}
              </div>
              <div className="tool-group">Custom tools</div>
              <div className="custom-tools">
                {form.customTools.map((t, i) => (
                  <div className="custom-tool-row" key={i}>
                    <input className="field" placeholder="tool_name" value={t.name}
                      onChange={e => setForm({ ...form, customTools: form.customTools.map((x, j) => j === i ? { ...x, name: e.target.value } : x) })} />
                    <input className="field" placeholder="What it does, for the model" value={t.description}
                      onChange={e => setForm({ ...form, customTools: form.customTools.map((x, j) => j === i ? { ...x, description: e.target.value } : x) })} />
                    <input className="field mono" placeholder="command to run" value={t.command}
                      onChange={e => setForm({ ...form, customTools: form.customTools.map((x, j) => j === i ? { ...x, command: e.target.value } : x) })} />
                    <button className="btn-danger-outline" onClick={() => setForm({ ...form, customTools: form.customTools.filter((_, j) => j !== i) })}>Remove</button>
                  </div>
                ))}
                <button className="btn-secondary" onClick={() => setForm({ ...form, customTools: [...form.customTools, { name: '', description: '', command: '' }] })}>Add custom tool</button>
              </div>
              {saveRow()}
            </>
          )}

          {section === 'rag' && (
            <>
              <h1>RAG endpoint</h1>
              <p className="muted view-sub">
                This deployment serves an OpenAI-compatible endpoint at{' '}
                <CopyToClipboard text={`${location.origin}/v1`}><code>{location.origin}/v1</code></CopyToClipboard>, so any OpenAI-speaking client (pw code, SDKs, other agents) can use the knowledge base as a grounded model. Callers authenticate with their own gateway API key as the bearer token; the endpoint holds no credentials of its own.
              </p>
              <p className="muted view-sub">
                Pick the underlying model per request as <code>studio-agent/&lt;model-id&gt;</code> (any other model id behaves like studio-rag with that model), or set the default below. Headers <code>X-RAG-Top-K</code>, <code>X-RAG-Tags</code>, and <code>X-RAG-Off: 1</code> tune retrieval per request.
              </p>
              <div className="settings-grid">
                <div>
                  <label className="field-label">Default underlying model (for bare studio-agent / studio-rag)</label>
                  <ModelSelect
                    value={form.ragDefaultModel}
                    models={pickableModels}
                    allowEmpty
                    emptyLabel="none (callers must pass studio-agent/<model-id>)"
                    onChange={v => setForm({ ...form, ragDefaultModel: v })}
                  />
                </div>
                <div>
                  <label className="field-label">Retrieved context blocks (top K)</label>
                  <input className="field" type="number" min={1} max={20} value={form.ragTopK}
                    onChange={e => setForm({ ...form, ragTopK: Number(e.target.value) })} />
                </div>
              </div>
              <label className="key-persist">
                <input type="checkbox" checked={form.ragAllowDeploymentKey}
                  onChange={e => setForm({ ...form, ragAllowDeploymentKey: e.target.checked })} />
                Allow callers without a bearer key to use the deployment credential (off means every caller must present their own key)
              </label>
              <label className="key-persist">
                <input type="checkbox" checked={form.ragAdvertiseAgentModel}
                  onChange={e => setForm({ ...form, ragAdvertiseAgentModel: e.target.checked })} />
                Advertise <code>studio-agent</code> in model listings
              </label>
              <p className="muted key-note toggle-note">
                Runs the full assistant pipeline on the server (system prompt, search, read, query, labels) and returns a finished cited answer. Slower, several model calls per question. The right choice inside tool-calling harnesses such as pw code.
              </p>
              <label className="key-persist">
                <input type="checkbox" checked={form.ragAdvertiseRagModel}
                  onChange={e => setForm({ ...form, ragAdvertiseRagModel: e.target.checked })} />
                Advertise <code>studio-rag</code> in model listings
              </label>
              <p className="muted key-note toggle-note">
                Injects retrieved context blocks into a single model call, no tool loop. Fast, a few seconds. Suits plain chat interfaces; inside code agents the host's own tools take over and the grounding is lost.
              </p>
              <p className="muted key-note">Both stay callable by name whether advertised or not.</p>
              <div className="settings-grid">
                <div>
                  <label className="field-label">Platform registration name (how the model appears in catalogs and dropdowns)</label>
                  <input className="field" value={form.ragEndpointName}
                    placeholder="defaults to <product-name>-rag"
                    onChange={e => setForm({ ...form, ragEndpointName: e.target.value })} />
                </div>
              </div>
              <label className="key-persist">
                <input type="checkbox" checked={form.ragEndpointAutoStart}
                  onChange={e => setForm({ ...form, ragEndpointAutoStart: e.target.checked })} />
                Register on the platform automatically at startup
              </label>
              {saveRow(false)}
              <div className="tool-group">Platform registration</div>
              <p className="muted view-sub">
                Publishes this /v1 surface into the platform chat and AI provider catalog (via <code>pw endpoints run --openai</code> around a local forwarder), so users pick the corpus-grounded models like any other. Platform callers reach it with their own credentials; the deployment credential covers only keyless clients such as pw code. Requires an authenticated pw CLI on the host.
              </p>
              <div className={`rag-banner ${ragEp?.state === 'running' ? 'connected' : ragEp?.state === 'error' ? 'errored' : ''}`}>
                <div className="rag-banner-head">
                  <span className={`status-dot ${ragEp?.state === 'running' ? 'ok' : ragEp?.state === 'error' ? 'warn' : 'off'}`} />
                  <strong>
                    {ragEp?.state === 'running' ? 'Connected: published in the platform model catalog' :
                     ragEp?.state === 'starting' ? 'Registering…' :
                     ragEp?.state === 'error' ? 'Registration failed' : 'Not registered on the platform'}
                  </strong>
                  <span className="rag-banner-actions">
                    {ragEp?.state === 'running' || ragEp?.state === 'starting'
                      ? <button className="btn-secondary" onClick={() => void ragEpAction('stop')}>Stop</button>
                      : <button className="btn-primary" onClick={() => void ragEpAction('start')}>Register now</button>}
                    <button className="btn-secondary" onClick={refreshRagEp}>Refresh</button>
                  </span>
                </div>
                {ragEp?.state === 'running' && ragEp.name && (
                  <div className="rag-banner-models">
                    Users can now select {form.ragAdvertiseAgentModel && form.ragAdvertiseRagModel ? 'these models' : 'this model'} anywhere on the platform:
                    <div className="rag-chip-row">
                      {form.ragAdvertiseAgentModel && (
                        <CopyToClipboard text={`session:${cfg.user.username}:${ragEp.name}/studio-agent`}>
                          <code>session:{cfg.user.username}:{ragEp.name}/studio-agent</code>
                        </CopyToClipboard>
                      )}
                      {form.ragAdvertiseRagModel && (
                        <CopyToClipboard text={`session:${cfg.user.username}:${ragEp.name}/studio-rag`}>
                          <code>session:{cfg.user.username}:{ragEp.name}/studio-rag</code>
                        </CopyToClipboard>
                      )}
                      {!form.ragAdvertiseAgentModel && !form.ragAdvertiseRagModel && <span className="muted">No models advertised; enable a toggle above.</span>}
                    </div>
                  </div>
                )}
                {ragEp?.detail && ragEp.state !== 'running' && <p className="muted key-note">{ragEp.detail}</p>}
              </div>
              <div className="tool-group">Recent endpoint calls</div>
              <p className="muted view-sub">
                The last 50 calls to this /v1 surface, most recent first, held in memory and cleared on restart. Question text is not recorded; the distilled retrieval terms are.
              </p>
              <button className="btn-secondary" onClick={refreshRagCalls}>Refresh</button>
              {!ragCalls.length && <p className="muted key-note">No calls since the server started; they appear here as clients use the endpoint.</p>}
              {ragCalls.length > 0 && (
                <div className="rag-calls-wrap">
                  <table className="rag-calls-table">
                    <thead><tr><th>Time</th><th>Model</th><th>Underlying</th><th>Credential</th><th>Duration</th><th>Retrieval</th><th>Status</th></tr></thead>
                    <tbody>
                      {ragCalls.map((c, i) => (
                        <tr key={i}>
                          <td>{new Date(c.at).toLocaleTimeString()}</td>
                          <td><code>{c.model}</code></td>
                          <td><code>{c.underlying || '-'}</code></td>
                          <td>{c.authSource.includes('injected') ? 'deployment (keyless caller)'
                            : c.authSource.includes('stored personal') ? 'personal key'
                            : c.authSource.includes('deployment') ? 'deployment'
                            : c.authSource === 'none' ? 'none' : 'caller key'}</td>
                          <td>{c.durationMs == null ? 'running' : `${(c.durationMs / 1000).toFixed(1)}s`}</td>
                          <td>{c.retrieval ? `${c.retrieval.blocks} blocks (${c.retrieval.terms.join(', ')})` : c.mode === 'agent' ? 'agent tool loop' : '-'}</td>
                          <td>{c.status}{c.detail ? `; ${c.detail}` : ''}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}

          {section === 'ext' && (
            <>
              <h1>Extensions</h1>
              <p className="muted view-sub">
                File-based extensions load from <code>{ext?.dir ?? 'the extensions directory beside the index'}</code> and take effect without a restart. <code>tools/*.json</code> files (name, description, command) become chat tools like the custom tools above. <code>skills/*.md</code> files are instruction sets the assistant loads on demand through the use_skill tool, by name or when a task matches their description. <code>agents/default.md</code> is appended to the system prompt as this deployment's standing instructions.
              </p>
              <div className="ext-cols">
                <div>
                  <div className="tool-group">Tool files ({ext?.tools.length ?? 0})</div>
                  {ext?.tools.length
                    ? ext.tools.map(t => <div className="ext-row" key={t.file}><code>{t.name}</code><span className="tool-desc">{t.description}{t.command ? <> (<code>{t.command}</code>)</> : null}</span></div>)
                    : <p className="muted ext-empty">None yet. Drop a JSON file in tools/.</p>}
                </div>
                <div>
                  <div className="tool-group">Skills ({ext?.skills.length ?? 0})</div>
                  {ext?.skills.length
                    ? ext.skills.map(t => <div className="ext-row" key={t.file}><code>{t.name}</code><span className="tool-desc">{t.description}</span></div>)
                    : <p className="muted ext-empty">None yet. Drop a markdown file in skills/.</p>}
                </div>
                <div>
                  <div className="tool-group">Agents ({ext?.agents.length ?? 0})</div>
                  {ext?.agents.length
                    ? ext.agents.map(t => <div className="ext-row" key={t.file}><code>{t.file}</code><span className="tool-desc">{t.active ? 'active (appended to the system prompt)' : 'inactive; rename to default.md to activate'}</span></div>)
                    : <p className="muted ext-empty">None yet. Drop default.md in agents/.</p>}
                </div>
              </div>
            </>
          )}

        </article>
      </div>
    </div>
  )
}
