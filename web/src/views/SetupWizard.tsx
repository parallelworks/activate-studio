import { type CSSProperties, useEffect, useRef, useState } from 'react'
import {
  type BadgeVariant, Button, Callout, EmptyState, ExternalLink, Field, Input, Loader, SegmentedControl, Select, StatusDot, Switch,
} from '@parallelworks/ui'
import { ACCENTS } from '../accents'
import { api } from '../api'
import { AccentSwatches } from '../components/AccentSwatches'
import { BrandIcons } from '../components/BrandIcons'
import { KINDS } from '../components/LibrariesSection'
import { LocalLibraryForm } from '../components/LocalLibraryForm'
import { MainFolderDepth } from '../components/IndexDepth'
import { FolderPicker, IndexBuildProgress, json, post, errText, useIndexBuild, type IndexJob, type SetupStatus } from '../components/PrimaryFolder'
import { SshLibraryForm } from '../components/SshLibraryForm'
import {
  AI_SECTIONS, SECTION_SWITCHES, reloadAppConfig, saveAiEnabled, saveSections, saveSettings, sectionAllowed, sectionsSetting, useAppConfig,
} from '../config'
import { type ThemeChoice, currentThemeChoice, setThemeChoice } from '../theme'

/**
 * First-run setup for a single-user install: the Studio's name and icon,
 * its theme and accent, AI features and the model endpoint, which pages the
 * sidebar shows, the folder to index, and other libraries. Shown over the
 * app on a first run, and from Settings, General afterwards; everything
 * here is in Settings too. Each step can be skipped, and Finish later
 * closes setup at any step. The folder step is the same picker and index
 * build as Settings, Libraries (PrimaryFolder); the build runs on the
 * server, so no step waits for it.
 */

export type { IndexJob, SetupStatus } from '../components/PrimaryFolder'

const STEPS = ['Welcome', 'Your Studio', 'Appearance', 'AI features', 'Sidebar', 'Your folder', 'Other libraries', 'Done']
const STEP = { welcome: 0, studio: 1, appearance: 2, ai: 3, sidebar: 4, folder: 5, libraries: 6, done: 7 }

/** Switches for the sidebar's pages, saved as the sections setting. With
 *  AI features off, Chat and Agents stay off. */
export function SidebarSwitches({ value, platform, aiEnabled = true, onChange }: {
  value: string[] | undefined
  platform: boolean
  aiEnabled?: boolean
  onChange: (sections: string[]) => void
}) {
  const fromValue = () => SECTION_SWITCHES.map(s => s.id).filter(id => sectionAllowed(id, { sections: value, platform: true }))
  const [shown, setShown] = useState(fromValue)
  const key = (value ?? []).join(',')
  // eslint-disable-next-line react-hooks/exhaustive-deps
  useEffect(() => setShown(fromValue()), [key])
  const toggle = (id: string, on: boolean) => {
    const next = on ? [...shown, id] : shown.filter(x => x !== id)
    setShown(next)
    onChange(sectionsSetting(next))
  }
  return (
    <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(180px, 1fr))', gap: '10px 16px' }}>
      {SECTION_SWITCHES.map(s => {
        const aiOff = !aiEnabled && AI_SECTIONS.includes(s.id)
        return (
          <label key={s.id} className="key-persist" style={{ margin: 0, color: 'var(--pw-text)' }}>
            <Switch checked={shown.includes(s.id) && !aiOff} disabled={aiOff} onCheckedChange={v => toggle(s.id, v)} aria-label={s.label} />
            <span>
              {s.label}
              {s.id === 'workflows' && !platform && <span className="muted"> (needs a platform login)</span>}
              {aiOff && <span className="muted"> (AI features off)</span>}
            </span>
          </label>
        )
      })}
    </div>
  )
}

/** The model key as /api/me/model-key reports it. */
interface KeyStatus { mode: 'stored' | 'session' | 'none'; last4?: string; baseUrl?: string | null; single?: boolean }
interface KeyTest { ok: boolean; status: string; models: number; gatewayHost?: string; message?: string | null; ids?: string[] }

const hostOf = (url: string | null | undefined) => { try { return new URL(String(url)).host } catch { return '' } }

/** Stored for an endpoint that takes no key (a local vLLM or Ollama), which
 *  ignores the bearer token the Studio sends. */
export const NO_KEY = 'none'
const sameUrl = (a: string | null | undefined, b: string) => String(a ?? '').trim().replace(/\/+$/, '') === b.trim().replace(/\/+$/, '')

/** What a failed key test means, in plain words. */
export function keyTestProblem(t: KeyTest, baseUrl: string): string | null {
  const host = t.gatewayHost || hostOf(baseUrl) || 'The endpoint'
  if (t.status === 'auth') return `Unauthorized: ${host} refused this key. Check the key, and that it is for this endpoint.`
  if (t.status !== 'ok') {
    const m = String(t.message ?? '')
    const why = /abort|timeout/i.test(m) ? 'did not answer within 8 seconds.'
      : /fetch failed|ECONNREFUSED|ENOTFOUND|EAI_AGAIN|getaddrinfo/i.test(m) ? 'could not be reached. Check the address, and that the server is running.'
      : /JSON|Unexpected token/i.test(m) ? 'answered, but not with a list of models. The base URL is the API itself, usually ending in /v1.'
      : m || 'did not answer.'
    return `Unreachable: ${host} ${why}`
  }
  if (!t.models) return `No models: ${host} answered but offers no models.`
  return null
}

/** AI features on or off, and the OpenAI-compatible endpoint the Studio's
 *  model calls go to (the personal model key routes, with its base URL). */
function AiStep({ enabled, sections, me, onSaved, onOpenAccess }: {
  enabled: boolean
  sections: string[] | undefined
  me: KeyStatus | null
  onSaved: (saved: { host: string; model: string; status: KeyStatus }) => void
  /** Closes setup and opens Settings, Model access. */
  onOpenAccess: () => void
}) {
  const [baseUrl, setBaseUrl] = useState(me?.baseUrl ?? '')
  useEffect(() => { if (me?.baseUrl) setBaseUrl(b => b || me.baseUrl || '') }, [me?.baseUrl])
  const [key, setKey] = useState('')
  const [test, setTest] = useState<{ fields: string; result: KeyTest } | null>(null)
  const [model, setModel] = useState('')
  const [busy, setBusy] = useState<'switch' | 'test' | 'save' | null>(null)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState('')

  // An empty key field keeps the stored key for the same endpoint, and
  // otherwise means the endpoint takes none.
  const keep = !key.trim() && !!me && me.mode !== 'none' && sameUrl(me.baseUrl, baseUrl)
  const sent = key.trim() || (keep ? '' : NO_KEY)
  const fields = JSON.stringify([baseUrl.trim(), sent])
  const result = test?.fields === fields ? test.result : null
  const problem = result ? keyTestProblem(result, baseUrl) : null
  const ids = result && !problem ? result.ids ?? [] : []

  const toggle = async (on: boolean) => {
    setBusy('switch'); setError('')
    try { await saveAiEnabled(on, sections) } catch (e) { setError(errText(e)) } finally { setBusy(null) }
  }
  const runTest = async () => {
    setBusy('test'); setError(''); setSaved('')
    try {
      const r = await post<KeyTest>('/api/me/model-key/test', { key: sent, baseUrl: baseUrl.trim() })
      setTest({ fields, result: r })
      setModel(m => (r.ids?.includes(m) ? m : r.ids?.[0] ?? ''))
    } catch (e) { setError(errText(e)) } finally { setBusy(null) }
  }
  const save = async () => {
    setBusy('save'); setError('')
    try {
      const status = keep ? me! : await post<KeyStatus>('/api/me/model-key', { key: sent, baseUrl: baseUrl.trim(), persist: true })
      // The default for label suggestions and the grounded model, and the
      // chat's own remembered pick.
      await saveSettings({ ragDefaultModel: model })
      try { localStorage.setItem('aiChat.model', JSON.stringify(model)) } catch { /* the chat picks the first model */ }
      window.dispatchEvent(new CustomEvent('ade:model-access-changed'))
      const host = hostOf(baseUrl.trim())
      setKey(''); setTest(null)
      setSaved(`Saved. The Studio uses ${model} through ${host}.`)
      onSaved({ host, model, status })
    } catch (e) { setError(errText(e)) } finally { setBusy(null) }
  }

  const access = (
    <a href="#view=settings:access" onClick={e => { e.preventDefault(); onOpenAccess() }}>Settings, Model access</a>
  )
  return (
    <>
      <p style={{ margin: 0 }}>
        This step is optional. Skip it to set up a model later in {access}; search, the library and query work without one.
      </p>
      <label className="key-persist" style={{ margin: 0, color: 'var(--pw-text)' }}>
        <Switch checked={enabled} disabled={busy === 'switch'} onCheckedChange={v => void toggle(v)} aria-label="AI features" onLabel="On" offLabel="Off" />
        <span>Chat, Agents, and other features that call a model</span>
      </label>
      {!enabled && (
        <p className="muted" style={{ margin: 0 }}>
          Off: Chat and Agents leave the sidebar and nothing is sent to a model. Search, the library, query and statistics work as before.
          AI features can be turned back on at any time in Settings, General, AI features.
        </p>
      )}
      {enabled && (
        <>
          <p className="muted" style={{ margin: 0 }}>
            An OpenAI-compatible endpoint: a provider, a gateway, or a model server such as vLLM or Ollama. The key is stored
            on this computer, encrypted.
          </p>
          <p className="muted" style={{ margin: 0 }}>
            An ACTIVATE account works too: set it up in {access}, or enter it here with the base
            URL <code>https://&lt;platform host&gt;/api/openai/v1</code> and an API key from the account.
          </p>
          {me && me.mode !== 'none' && me.baseUrl && !saved && (
            <Callout tone="success">
              {me.last4 === NO_KEY
                ? `In use: ${hostOf(me.baseUrl)}, with no key.`
                : `In use: the key ending ${me.last4} for ${hostOf(me.baseUrl)}. Enter a key below to replace it.`}
            </Callout>
          )}
          <div className="settings-grid" style={{ marginBottom: 0 }}>
            <Input label="Base URL" placeholder="https://host/v1" autoComplete="off" spellCheck={false} value={baseUrl}
              onChange={e => { setBaseUrl(e.target.value); setSaved('') }} />
            <Input label="API key" type="password" autoComplete="off" placeholder={keep ? 'the saved key' : 'empty if the endpoint needs none'} value={key}
              onChange={e => { setKey(e.target.value); setSaved('') }} />
          </div>
          <div style={row}>
            <Button variant="secondary" loading={busy === 'test'} disabled={!baseUrl.trim() || busy !== null} onClick={() => void runTest()}>Test</Button>
          </div>
          {problem && <Callout tone="error" live>{problem}</Callout>}
          {result && !problem && (
            <>
              <Callout tone="success" live title={`${result.models} model${result.models === 1 ? '' : 's'} offered by ${result.gatewayHost || hostOf(baseUrl)}`}>
                {ids.slice(0, 8).join(', ')}{ids.length > 8 ? `, and ${ids.length - 8} more` : ''}
              </Callout>
              <Field label="Default model">
                <Select items={ids.map(id => ({ value: id, label: id }))} value={model || null} onValueChange={v => v && setModel(v)} />
              </Field>
              <div style={row}>
                <Button variant="primary" loading={busy === 'save'} disabled={!model || busy !== null} onClick={() => void save()}>Save</Button>
              </div>
            </>
          )}
          {saved && <Callout tone="success" live>{saved}</Callout>}
        </>
      )}
      {error && <Callout tone="error" live>{error}</Callout>}
    </>
  )
}

const overlay: CSSProperties = {
  position: 'fixed', inset: 0, zIndex: 60, overflowY: 'auto', background: 'var(--pw-bg)',
  display: 'flex', justifyContent: 'center', alignItems: 'flex-start', padding: '6vh 16px 24px',
}
const panel: CSSProperties = { width: '100%', maxWidth: 680, padding: 24, display: 'flex', flexDirection: 'column', gap: 16 }
const row: CSSProperties = { display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 8 }
const label: CSSProperties = { margin: '0 0 6px', fontSize: 13, fontWeight: 500 }

type OtherKind = 'local' | 'ssh' | 'pw'
const KIND_TAB: Record<OtherKind, string> = { local: 'On this machine', ssh: 'Over SSH', pw: 'On ACTIVATE (pw ssh)' }

export const THEME_OPTIONS: { value: ThemeChoice; label: string }[] = [
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
  { value: 'system', label: 'System' },
]

/** The open-source workflow engine, for workflow YAML files of one's own. */
export const MERIDIAN_URL = 'https://github.com/parallelworks/meridian'

/** A theme chosen in setup or Settings: painted in this window at once,
 *  and saved as the Studio's default so a new window opens in it. System
 *  saves light, the default that follows the device's dark setting. */
export function saveThemeChoice(choice: ThemeChoice): Promise<void> {
  setThemeChoice(choice)
  return saveSettings({ theme: choice === 'dark' ? 'dark' : 'light' })
}

/** The setup index build in one line, for the steps after the folder. */
export function buildSummary(job: IndexJob | null, error: string): { text: string; tone: BadgeVariant; pulse: boolean } | null {
  if (error) return { text: error, tone: 'error', pulse: false }
  if (!job) return null
  const p = job.progress
  if (job.state === 'queued' || job.state === 'running') {
    const where = !p ? '' : p.phase === 'directories' ? `: ${p.current ?? ''} (${p.done} of ${p.total})` : p.phase === 'files' ? ': files at the top of the folder' : ': finishing'
    return { text: `Building the index in the background${where}`, tone: 'info', pulse: true }
  }
  if (job.state === 'error') return { text: `The index build failed: ${job.error ?? 'unknown error'}`, tone: 'error', pulse: false }
  return p?.failed.length
    ? { text: `Index built; ${p.failed.length} folder${p.failed.length === 1 ? '' : 's'} could not be indexed`, tone: 'warning', pulse: false }
    : { text: 'Index built', tone: 'success', pulse: false }
}

export function SetupWizard({ onClose, pollMs = 1000 }: {
  /** `folderChanged` says the Studio restarted on another folder. */
  onClose: (folderChanged: boolean) => void
  pollMs?: number
}) {
  const cfg = useAppConfig()
  const [step, setStep] = useState(0)
  const [status, setStatus] = useState<SetupStatus | null>(null)
  const [statusError, setStatusError] = useState('')
  const [restarting, setRestarting] = useState(false)
  const [folderChanged, setFolderChanged] = useState(false)
  const [added, setAdded] = useState<{ id: string; label: string }[]>([])
  const [stepError, setStepError] = useState('')
  const [finishing, setFinishing] = useState(false)
  const [name, setName] = useState<string | null>(null)
  const [accent, setAccent] = useState<string | null>(null)
  const [theme, setTheme] = useState<ThemeChoice | null>(null)
  const [me, setMe] = useState<KeyStatus | null>(null)
  const [aiSaved, setAiSaved] = useState<{ host: string; model: string } | null>(null)
  const [kind, setKind] = useState<OtherKind>('local')
  // The libraries step leads with the common case, another folder here; the
  // other kinds (an existing index, ssh, ACTIVATE) are a click away.
  const [otherKinds, setOtherKinds] = useState(false)
  const [pwOffered, setPwOffered] = useState(false)
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    return () => { alive.current = false }
  }, [])
  // Each step starts with focus on its heading, so a screen reader reads it
  // and Tab moves through that step.
  const heading = useRef<HTMLHeadingElement>(null)
  useEffect(() => { heading.current?.focus() }, [step])

  const loadStatus = async (): Promise<SetupStatus | null> => {
    try {
      const s = await json<SetupStatus>('/api/setup/status?files=1')
      if (alive.current) { setStatus(s); setStatusError('') }
      return s
    } catch (e) {
      if (alive.current) setStatusError(errText(e))
      return null
    }
  }

  // The build runs on the server, so moving on (or closing setup) never
  // waits for it; this only follows it while setup is open.
  const index = useIndexBuild({ pollMs, onDone: async () => { await loadStatus(); reloadAppConfig() } })

  useEffect(() => {
    void loadStatus().then(s => {
      // A build started before the wizard was reopened.
      if (s?.job) void index.follow(s.job)
    })
    json<KeyStatus>('/api/me/model-key').then(k => { if (alive.current) setMe(k) }).catch(() => {})
    // Whether an index on a cluster is offered; the form says why when pw ssh cannot be used yet.
    api.userLibraries().then(r => { if (alive.current) setPwOffered(!!(r.pwOffered ?? r.pw)) }).catch(() => {})
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  // The name, theme and accent show what is in effect, so setup run again
  // starts from the current values: the configuration is read afresh as
  // the wizard opens, and each follows it until it is changed here.
  const touched = useRef(new Set<'name' | 'accent' | 'theme'>())
  useEffect(() => { reloadAppConfig() }, [])
  useEffect(() => {
    if (!cfg.loaded) return
    if (!touched.current.has('name')) setName(cfg.appName)
    if (!touched.current.has('accent')) setAccent(cfg.accent)
    if (!touched.current.has('theme')) setTheme(currentThemeChoice(cfg.theme))
  }, [cfg.loaded, cfg.appName, cfg.accent, cfg.theme])

  const save = (patch: Record<string, unknown>) => {
    setStepError('')
    return saveSettings(patch).catch(e => { if (alive.current) setStepError(errText(e)) })
  }
  const saveName = () => {
    const n = (name ?? '').trim()
    if (n && n !== cfg.appName) void save({ appName: n })
  }
  // A custom color reports every step of a drag; the last one is saved.
  const accentTimer = useRef<number | undefined>(undefined)
  const pickAccent = (a: string) => {
    touched.current.add('accent')
    setAccent(a)
    window.clearTimeout(accentTimer.current)
    accentTimer.current = window.setTimeout(() => void save({ accent: a }), a.startsWith('custom:') ? 400 : 0)
  }
  const pickTheme = (t: ThemeChoice) => {
    touched.current.add('theme')
    setTheme(t)
    setStepError('')
    saveThemeChoice(t).catch(e => { if (alive.current) setStepError(errText(e)) })
  }

  const folderChangedTo = async () => {
    setFolderChanged(true)
    index.reset()
    const s = await loadStatus()
    reloadAppConfig()
    return s?.primary?.path
  }

  const go = (to: number) => {
    if (step === STEP.studio) saveName()
    setStepError('')
    setStep(to)
  }
  const finish = async () => {
    if (step === STEP.studio) saveName()
    setFinishing(true)
    try { await post('/api/setup/complete') } catch { /* shown again next time; nothing lost */ }
    window.dispatchEvent(new Event('ade:setup-closed'))
    onClose(folderChanged)
  }
  const openAccess = async () => {
    await finish()
    location.hash = '#view=settings:access'
  }

  const changeSections = (next: string[]) => {
    setStepError('')
    saveSections(next).catch(e => setStepError(errText(e)))
  }
  const libraryAdded = (lib: { id: string; label: string }) => {
    setAdded(a => [...a, lib])
    reloadAppConfig()
  }

  const aiOn = cfg.aiEnabled !== false
  const primary = status?.primary
  const building = index.building
  const others = [
    ...(status?.libraries ?? []).filter(l => !l.primary).map(l => ({ id: l.id, label: l.label })),
    ...added,
  ].filter((l, i, all) => all.findIndex(x => x.id === l.id) === i)
  const shownLabels = SECTION_SWITCHES.filter(s => sectionAllowed(s.id, cfg)).map(s => s.label)
  const kinds: OtherKind[] = ['local', 'ssh', ...(pwOffered ? ['pw' as const] : [])]

  const indexState = !primary ? '' : !primary.exists ? 'This folder does not exist yet.'
    : building ? 'The index is building in the background.'
    : primary.indexed ? (primary.files !== undefined ? `Indexed: ${primary.files.toLocaleString()} files.` : 'Indexed.')
    : 'Not indexed yet.'
  const endpoint = aiSaved ?? (me && me.mode !== 'none' && me.baseUrl ? { host: hostOf(me.baseUrl), model: '' } : null)
  const aiSummary = !aiOn ? 'off'
    : endpoint ? `on, through ${endpoint.host}${endpoint.model ? `, default model ${endpoint.model}` : ''}`
    : 'on, no model set up yet'
  const accentName = (a: string) => (a.startsWith('custom:') ? `custom (${a.slice(7)})` : ACCENTS[a]?.label ?? a)
  const build = buildSummary(index.job, index.error)
  // A folder just chosen and not indexed: offer the build at once.
  const offerBuild = folderChanged && !!primary?.exists && !primary.indexed && !index.job && !restarting
  // Steps with something to set up that has not been, where moving on skips it.
  const skippable = (step === STEP.ai && aiOn && !endpoint)
    || (step === STEP.folder && !primary?.indexed && !index.job)
    || (step === STEP.libraries && !others.length)

  const last = STEPS.length - 1
  return (
    <div style={overlay} role="dialog" aria-modal="true" aria-labelledby="setup-title">
      <div className="card" style={panel}>
        <div>
          <p className="muted" style={{ margin: 0, fontSize: 12 }}>Step {step + 1} of {STEPS.length}</p>
          <div className="setup-progress" aria-hidden="true">
            {STEPS.map((s, i) => <span key={s} className={i <= step ? 'on' : ''} />)}
          </div>
          <h1 id="setup-title" ref={heading} tabIndex={-1} style={{ margin: '4px 0 0', fontSize: 20, outline: 'none' }}>{STEPS[step]}</h1>
        </div>

        {step === STEP.welcome && (
          <>
            <p style={{ margin: 0 }}>
              {cfg.appName} indexes a folder on this computer so you can browse, search and query its files, and it can search
              GUFI indexes on other machines and clusters.
            </p>
            <p className="muted" style={{ margin: 0 }}>
              The next steps set its name and icon, its theme and color, AI features, the sidebar, the folder, and other
              libraries. Each step shows what is set now and changes nothing until you change it; any step can be skipped,
              and Finish later closes setup. Everything here is also in Settings, and Run setup again, in Settings, General,
              comes back here.
            </p>
          </>
        )}

        {step === STEP.studio && (
          <>
            <Input label="Name" value={name ?? ''} maxLength={60} placeholder="Studio"
              description="Shown at the top of the sidebar and in the browser tab."
              onChange={e => { touched.current.add('name'); setName(e.target.value) }} onBlur={saveName}
              onKeyDown={e => { if (e.key === 'Enter') saveName() }} />
            <div>
              <p style={label}>Icon</p>
              <BrandIcons value={status?.branding} />
            </div>
          </>
        )}

        {step === STEP.appearance && (
          <>
            <Field group label="Theme" description="System follows this computer's light or dark setting. The sidebar's theme button switches it later.">
              <SegmentedControl options={THEME_OPTIONS} value={theme ?? currentThemeChoice(cfg.theme)} onValueChange={pickTheme} />
            </Field>
            <Field group label="Accent color">
              <AccentSwatches value={accent ?? cfg.accent} onChange={pickAccent} />
            </Field>
            <p className="muted" style={{ margin: 0 }}>Both apply as you pick them. Settings, General, Appearance has these and the surface tone.</p>
          </>
        )}

        {step === STEP.ai && (
          <AiStep enabled={aiOn} sections={cfg.sections} me={me} onOpenAccess={() => void openAccess()}
            onSaved={s => { setAiSaved({ host: s.host, model: s.model }); setMe(s.status) }} />
        )}

        {step === STEP.sidebar && (
          <>
            <p className="muted" style={{ margin: 0 }}>Pages to show in the sidebar. Settings and Help always show.</p>
            <SidebarSwitches value={cfg.sections} platform={!!cfg.platform} aiEnabled={aiOn} onChange={changeSections} />
            <p className="muted" style={{ margin: 0 }}>
              Workflows runs ACTIVATE workflows, so it needs an ACTIVATE credential (Settings, Model access). For workflow
              YAML files of your own, without ACTIVATE, use <ExternalLink href={MERIDIAN_URL}>Meridian</ExternalLink>, the
              open-source workflow engine.
            </p>
          </>
        )}

        {step === STEP.folder && (
          <>
            {statusError && <Callout tone="error">{statusError}</Callout>}
            {!status && !statusError && <Loader inline size={16} label="Loading…" />}
            {primary && (
              <div>
                <p style={{ margin: 0 }}>Folder in use: <b style={{ wordBreak: 'break-all' }}>{primary.path}</b></p>
                <p className="muted" style={{ margin: '2px 0 0' }}>{indexState}</p>
              </div>
            )}
            {status && (
              <FolderPicker current={primary?.path} start={primary?.exists ? primary.path : '~'} disabled={building}
                pollMs={pollMs} onRestarting={setRestarting} onChanged={folderChangedTo} />
            )}
            {status && !restarting && (
              <div style={{ display: 'flex', flexDirection: 'column', gap: 8 }}>
                {status.gufi && !status.gufi.found && (
                  <Callout tone="warning">GUFI was not found in {status.gufi.bin}, so the index cannot be built.</Callout>
                )}
                <MainFolderDepth disabled={building} />
                {offerBuild && status.gufi?.found ? (
                  <Callout tone="info" title="Build the index for this folder now?"
                    action={<Button variant="primary" size="sm" onClick={() => void index.start()}>Build the index</Button>}>
                    It builds in the background, so setup can go on while it runs.
                  </Callout>
                ) : (
                  <div style={row}>
                    <Button variant="secondary" loading={building} disabled={building || !status.gufi?.found || !primary?.exists}
                      onClick={() => void index.start()}>
                      {primary?.indexed ? 'Rebuild the index' : 'Build the index'}
                    </Button>
                  </div>
                )}
                <IndexBuildProgress job={index.job} error={index.error} />
                <p className="muted" style={{ margin: 0 }}>
                  A large folder can take a long time to index. The build runs in the background: go on to the next step, or
                  finish setup, while it runs. Indexes can be added or changed at any time in Settings, Libraries.
                </p>
              </div>
            )}
          </>
        )}

        {step === STEP.libraries && (
          <>
            <p style={{ margin: 0 }}>
              Optional: another folder on this computer to search beside your main one. It is indexed here, in the background,
              and the Studio only reads it. Libraries can be added or removed at any time in Settings, Libraries.
            </p>
            {!otherKinds ? (
              status?.gufi?.found === false
                ? <Callout tone="warning">GUFI was not found in {status.gufi.bin}, so another folder cannot be indexed here.</Callout>
                : <LocalLibraryForm mode="build" browse compact onAdded={lib => libraryAdded({ id: lib.id, label: lib.label })} />
            ) : (
              <>
                <SegmentedControl label="Where the index is" value={kind} onValueChange={setKind}
                  options={kinds.map(k => ({ value: k, label: KIND_TAB[k] }))} />
                <p className="muted" style={{ margin: 0 }}>{KINDS[kind].description}</p>
                {kind === 'local' && <LocalLibraryForm browse compact onAdded={lib => libraryAdded({ id: lib.id, label: lib.label })} />}
                {kind === 'ssh' && <SshLibraryForm compact onAdded={libraryAdded} />}
                {kind === 'pw' && <SshLibraryForm compact transport="pw" onAdded={libraryAdded} onOpenAccess={() => void openAccess()} />}
              </>
            )}
            <div>
              <Button variant="ghost" size="sm" aria-expanded={otherKinds} onClick={() => setOtherKinds(o => !o)}>
                {otherKinds ? 'Back to a folder on this computer' : 'Other kinds: an existing GUFI index, or one on another machine'}
              </Button>
            </div>
            {others.length > 0 && (
              <div>
                <p style={{ margin: '0 0 4px' }}>Added:</p>
                <ul style={{ margin: 0, paddingLeft: 20 }}>{others.map(l => <li key={l.id}>{l.label}</li>)}</ul>
              </div>
            )}
          </>
        )}

        {step === STEP.done && (
          <>
            <ul style={{ margin: 0, paddingLeft: 20, display: 'flex', flexDirection: 'column', gap: 4 }}>
              <li>Name: {cfg.appName}</li>
              <li>Theme: {THEME_OPTIONS.find(o => o.value === (theme ?? currentThemeChoice(cfg.theme)))?.label}, accent {accentName(accent ?? cfg.accent)}</li>
              <li>AI features: {aiSummary}</li>
              <li>Sidebar: {shownLabels.length ? shownLabels.join(', ') : 'Settings and Help only'}</li>
              <li>Folder: {primary ? `${primary.path}. ${indexState}` : 'unknown'}</li>
              <li>Other libraries: {others.length ? others.map(l => l.label).join(', ') : 'none'}</li>
            </ul>
            {aiOn && !endpoint && (
              <p className="muted" style={{ margin: 0 }}>
                Chat needs a model: set one up in <a href="#view=settings:access" onClick={e => { e.preventDefault(); void openAccess() }}>Settings, Model access</a>.
              </p>
            )}
          </>
        )}

        {stepError && <Callout tone="error" live>{stepError}</Callout>}

        {build && step !== STEP.folder && (
          <p className="setup-build muted" role="status">
            <StatusDot variant={build.tone} pulse={build.pulse} size={7} />
            <span>{build.text}</span>
          </p>
        )}

        <div style={{ ...row, justifyContent: 'space-between', marginTop: 8 }}>
          {step < last
            ? <Button variant="ghost" disabled={finishing || restarting} onClick={() => void finish()}>Finish later</Button>
            : <span />}
          <div style={{ ...row, marginLeft: 'auto' }}>
            {step > 0 && <Button variant="secondary" disabled={restarting} onClick={() => go(step - 1)}>Back</Button>}
            {skippable && <Button variant="ghost" disabled={restarting} onClick={() => go(step + 1)}>Skip this step</Button>}
            {step < last
              ? <Button variant="primary" disabled={restarting} onClick={() => go(step + 1)}>Next</Button>
              : <Button variant="primary" loading={finishing} onClick={() => void finish()}>Finish</Button>}
          </div>
        </div>
      </div>
    </div>
  )
}
