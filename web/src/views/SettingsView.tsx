import { LibrariesSection } from '../components/LibrariesSection'
import { ModelAccessSection } from '../components/ModelAccessSection'
import { IndexingSection } from '../components/IndexingSection'
import { WorkflowsSection } from '../components/WorkflowsSection'
import { useEffect, useRef, useState } from 'react'
import {
  ActionMenu, Autocomplete, Button, Callout, type CalloutTone, Checkbox, CopyButton, CopyCodeBlock,
  DescriptionList, DescriptionListItem, DisclosurePanel, EmptyState, Field, Input, Loader, NumberInput,
  RefreshButton, SegmentedControl, Select, type SelectItem, StatusDot, Switch, Table, TabPanel, Tabs, Textarea, cx, fieldBox,
} from '@parallelworks/ui'
import { ChevronDownIcon } from '@parallelworks/ui/icons'
import { SURFACES, applySurface } from '../accents'
import { AccentSwatches } from '../components/AccentSwatches'
import { BrandIcons, type Branding } from '../components/BrandIcons'
import { ServerSection } from '../components/ServerSection'
import { UpdatesSection } from '../components/UpdatesSection'
import { reloadAppConfig, saveAiEnabled, saveSections, saveSettings, useAppConfig } from '../config'
import { type ThemeChoice, currentThemeChoice, setThemeChoice } from '../theme'
import { useUpdateFlow } from '../updateFlow'
import { SidebarSwitches, THEME_OPTIONS, saveThemeChoice } from './SetupWizard'
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
  searchEngine: 'parallel' | 'gufi_vt'
  historyIntervalSec: number
  suggestedPrompts: string[]
  visionModel: string
  disabledTools: string[]
  customTools: { name: string; description: string; command: string }[]
  ragDefaultModel: string
  ragTopK: number
  ragAllowDeploymentKey: boolean
  ragAdvertiseRagModel: boolean
  ragAdvertiseAgentModel: boolean
  mcpEnabled: boolean
  mcpServers: { name: string; url: string; header?: string }[]
  delegationEnabled: boolean
  delegationMaxAgents: number
  maxToolRounds: number
  delegationMaxDepth: number
  agentExecution: 'runner' | 'sessions' | 'both'
  voiceEnabled: boolean
  voiceUrl: string
  ragProxyEnabled: boolean
  ragEndpointAutoStart: boolean
  ragEndpointName: string
  requirePersonalKey: boolean
  chatSharedHistory: boolean
  bannerText: string
  bannerColor: string
  bannerWhenEmbedded: boolean
  allowKeySharing: boolean
  kbMission: string
  sections: string[]
  aiEnabled: boolean
}

interface CatalogTool { name: string; description: string; builtin: boolean; enabled: boolean; parameters?: unknown; command?: string; implementation?: string; calls?: string; asksFirst?: boolean }
interface RagCall {
  at: string; model: string; mode: string; underlying: string; authSource: string
  durationMs: number | null; status: string; detail: string | null
  retrieval: { terms: string[]; blocks: number; chars: number } | null
}

interface ExtDoc { name: string; description: string; file: string; active?: boolean; command?: string }
interface Extensions { dir: string; tools: ExtDoc[]; skills: ExtDoc[]; agents: ExtDoc[] }

const TOOL_GROUPS: [string, string[]][] = [
  ['Knowledge base', ['search_kb', 'read_kb_file', 'write_kb_file', 'list_kb_dir', 'query_corpus', 'get_labels', 'suggest_labels', 'apply_labels']],
  ['Workflows', ['list_workflows', 'list_github_workflows', 'get_workflow', 'compose_workflow', 'serve_model', 'run_workflow', 'workflow_runs', 'workflow_run_detail', 'cancel_workflow_run', 'list_sessions', 'pw_help']],
  ['Clusters and storage', ['list_clusters', 'start_cluster', 'resume_cluster', 'stop_cluster', 'destroy_cluster', 'list_storage', 'hpc_status', 'cluster_command']],
  ['pw code agents', ['agent_sites', 'agent_sessions', 'start_agent', 'agent_status', 'agent_steer', 'agent_goal', 'agent_approve', 'agent_stop']],
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

const THEMES: SelectItem<Effective['theme']>[] = [
  { value: 'light', label: 'Light, or dark where the device prefers it' },
  { value: 'dark', label: 'Dark' },
]

const SEARCH_ENGINES: SelectItem<Effective['searchEngine']>[] = [
  { value: 'parallel', label: 'Each library in parallel, merged (faster)' },
  { value: 'gufi_vt', label: 'One gufi_vt statement over all libraries' },
]

const AGENT_EXECUTION: SelectItem<Effective['agentExecution']>[] = [
  { value: 'runner', label: 'Studio runner: one-shot pw code runs' },
  { value: 'sessions', label: 'pw code sessions' },
  { value: 'both', label: 'Both, chosen per task' },
]

type SectionId = 'general' | 'access' | 'tools' | 'rag' | 'previews' | 'ext' | 'libraries' | 'workflows'

/** The tabs of Settings, General, in order. Each has an address of its own
 *  (#view=settings:<tab>); #view=settings:general opens the first. */
export const GENERAL_TABS = [
  { value: 'appearance', label: 'Appearance' },
  { value: 'branding', label: 'Branding' },
  { value: 'sidebar', label: 'Sidebar' },
  { value: 'ai', label: 'AI features' },
  { value: 'indexing', label: 'Indexing' },
  { value: 'updates', label: 'Updates' },
  { value: 'server', label: 'Server' },
] as const
export type GeneralTab = typeof GENERAL_TABS[number]['value']

/** The section and General tab an address names: a section's own id, a
 *  General tab's id, or anything else for General's first tab. */
export function settingsTarget(id: string | null | undefined): { section: SectionId; tab: GeneralTab } {
  const tab = GENERAL_TABS.find(t => t.value === id)
  if (tab) return { section: 'general', tab: tab.value }
  const sections: SectionId[] = ['general', 'access', 'tools', 'rag', 'previews', 'ext', 'libraries', 'workflows']
  return { section: sections.includes(id as SectionId) ? id as SectionId : 'general', tab: 'appearance' }
}

/**
 * The settings rail, in order. Model access appears where the deployment
 * signs users in, and on a single-user install (`single`), where it holds
 * the one user's model endpoint. Feature previews is its own section rather
 * than a heading at the foot of another one: a capability nobody can
 * find is off for everyone regardless of the switch. Workflows appears
 * only when the Studio runs on the platform, since it lists the platform's.
 */
export function settingsSections(opts: { authEnabled: boolean; platform?: boolean; single?: boolean }): { id: SectionId; label: string; group: string | null }[] {
  return [
    { id: 'general', label: 'General', group: 'Studio' },
    { id: 'libraries', label: 'Libraries', group: 'Studio' },
    ...(opts.platform ? [{ id: 'workflows' as SectionId, label: 'Workflows', group: 'Studio' }] : []),
    ...(opts.authEnabled || opts.single ? [{ id: 'access' as SectionId, label: 'Model access', group: 'Assistant' }] : []),
    { id: 'tools', label: 'Assistant tools', group: 'Assistant' },
    { id: 'previews', label: 'Feature previews', group: 'Assistant' },
    { id: 'ext', label: 'Extensions', group: 'Assistant' },
    { id: 'rag', label: 'External access', group: null },
  ]
}

/** A model id, suggested from the live model catalog. Free text, so a saved
 *  value the catalog no longer lists keeps working, and a standalone
 *  deployment with no catalog (a local endpoint) can still name its model.
 *  Empty means none, which the placeholder spells out. */
function ModelInput({ value, models, emptyLabel, onChange }: {
  value: string
  models: string[]
  emptyLabel?: string
  onChange: (v: string) => void
}) {
  return (
    <Autocomplete
      items={models}
      value={value}
      placeholder={emptyLabel ?? 'model id, e.g. llama-3.3-70b'}
      onValueChange={v => onChange(v.trim())}
    />
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
  const [mcpStatus, setMcpStatus] = useState<{ name: string; url: string; tools: number; error: string | null }[]>([])
  const [ext, setExt] = useState<Extensions | null>(null)
  // Settings rail groups the visitor opened or closed this visit.
  const [railOpen, setRailOpen] = useState<Record<string, boolean>>({})
  const [runtime, setRuntime] = useState<{ local: { available: boolean; reason: string | null; version: string | null; remoteStart: boolean | null; remoteMaxPermissionMode: string | null } } | null>(null)
  const [specOpen, setSpecOpen] = useState<string | null>(null)
  const [me, setMe] = useState<{ authEnabled?: boolean; single?: boolean; verified: boolean; mode: 'stored' | 'session' | 'none'; last4?: string; addedAt?: string; sessionExpiresAt?: string; kind?: string; credExpiresAt?: string | null; credExpired?: boolean; baseUrl?: string | null; gatewayHost?: string
    shared?: { active: boolean; last4?: string; sharedBy?: string; sharedAt?: string; credExpired?: boolean; mine?: boolean }
    sharingAllowed?: boolean } | null>(null)
  // Setup runs only on a single-user install (sign-in off), which also
  // uploads its icons rather than naming them.
  const [setupAvailable, setSetupAvailable] = useState(false)
  const [branding, setBranding] = useState<Branding | null>(null)
  const [sectionsNote, setSectionsNote] = useState('')
  const [target, setTarget] = useState(() => settingsTarget(location.hash.match(/^#view=settings:([a-z-]+)/)?.[1]))
  const { section, tab } = target
  const setSection = (id: string) => setTarget(settingsTarget(id))

  const goSection = (id: SectionId | GeneralTab) => {
    setSection(id)
    history.replaceState(null, '', `${location.pathname}${location.search}#view=settings:${id}`)
  }
  const updatesFlow = useUpdateFlow()
  const tabs = GENERAL_TABS.filter(t => t.value !== 'updates' || !updatesFlow.none)
  const [theme, setTheme] = useState<ThemeChoice | null>(null)
  // #view=settings:setup (the macOS app's Run Setup…, or any link) opens the
  // setup wizard over General, where setup runs.
  const [setupAsked, setSetupAsked] = useState(() => /^#view=settings:setup$/.test(location.hash))
  const [setupKnown, setSetupKnown] = useState(false)
  const [appearanceNote, setAppearanceNote] = useState('')
  const accentTimer = useRef<number | undefined>(undefined)
  /** Appearance saves as it changes, like the setup wizard. */
  const saveLook = (patch: Partial<Pick<Effective, 'theme' | 'accent' | 'surface'>>, delay = 0) => {
    setForm(f => f && { ...f, ...patch })
    setAppearanceNote('')
    window.clearTimeout(accentTimer.current)
    accentTimer.current = window.setTimeout(() => {
      saveSettings(patch).catch(e => setAppearanceNote(String((e as Error).message ?? e)))
    }, delay)
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

  // The setup wizard saves some of the same settings; once it closes, the
  // form starts again from what is saved, so Save settings cannot put back
  // what the wizard changed.
  const loadForm = () => {
    setTheme(null)
    fetch('/api/settings').then(r => r.json())
      .then(d => { setForm(d.effective); setDeploymentCred(d.deploymentCredential ?? null) })
      .catch(() => setNote('Could not load settings.'))
    fetch('/api/setup/status').then(r => r.json()).then(d => { setSetupAvailable(!!d?.standalone); setBranding(d?.branding ?? null) })
      .catch(() => {}).finally(() => setSetupKnown(true))
    fetch('/api/me/model-key').then(r => r.json()).then(setMe).catch(() => {})
    refreshModels()
  }
  // The model ids the pickers below suggest; read again when model access
  // changes, which Model access announces.
  const refreshModels = () => {
    fetch('/api/chat/models?config=1').then(r => r.json())
      .then(d => setModels((d.models ?? []).map((m: { id: string }) => m.id)))
      .catch(() => {})
  }
  useEffect(() => {
    window.addEventListener('ade:model-access-changed', refreshModels)
    return () => window.removeEventListener('ade:model-access-changed', refreshModels)
  }, [])
  useEffect(() => {
    window.addEventListener('ade:setup-closed', loadForm)
    return () => window.removeEventListener('ade:setup-closed', loadForm)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    fetch('/api/settings/mcp-servers').then(r => r.json()).then(d => setMcpStatus(d.servers ?? [])).catch(() => {})
    loadForm()
    fetch('/api/chat/tools').then(r => r.json())
      .then(d => setCatalog(d.tools ?? []))
      .catch(() => {})
    fetch('/api/extensions').then(r => r.json()).then(setExt).catch(() => {})
    fetch('/api/tasks/runtime').then(r => r.json()).then(setRuntime).catch(() => {})
    refreshRagEp()
    refreshRagCalls()
    const onSection = (e: Event) => {
      const d = (e as CustomEvent).detail as { view?: string; section?: string }
      if (d?.view === 'settings' && d.section) setSection(d.section)
      if (d?.view === 'settings' && d.section === 'setup') setSetupAsked(true)
    }
    window.addEventListener('ade-view-section', onSection)
    return () => window.removeEventListener('ade-view-section', onSection)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (!setupAsked || !setupKnown) return
    setSetupAsked(false)
    history.replaceState(null, '', `${location.pathname}${location.search}#view=settings:general`)
    if (setupAvailable) window.dispatchEvent(new Event('ade:setup-open'))
  }, [setupAsked, setupKnown, setupAvailable])

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
        reloadAppConfig()
      }
    } catch (e) {
      setNote(String((e as Error).message ?? e))
      setBusy(false)
    }
  }

  if (!form) return (
    <div className="settings-view">
      <div className="card settings-card">
        {note ? <Callout tone="error">{note}</Callout> : <p className="muted pad"><Loader inline size={16} label="Loading…" /></p>}
      </div>
    </div>
  )

  const pickableModels = models.filter(m => !/studio-(agent|rag)/.test(m))

  const sections = settingsSections({ authEnabled: !!me?.authEnabled, platform: !!cfg.platform, single: !!me?.single })

  const mcpJson = JSON.stringify({ mcpServers: { 'studio-kb': { type: 'http', url: `${location.origin}/api/mcp`, ...(me?.authEnabled ? { headers: { Authorization: 'Bearer <platform token>' } } : {}) } } }, null, 2)
  const saveRow = (reload = true) => (
    <div className="query-actions">
      <Button variant="primary" onClick={() => void save(reload)} loading={busy}>{busy ? 'Saving…' : 'Save settings'}</Button>
      {note && <span className="muted">{note}</span>}
    </div>
  )

  const ragEpTone: CalloutTone = ragEp?.state === 'running' ? 'success' : ragEp?.state === 'error' ? 'error' : 'default'
  const surfaces: SelectItem<string>[] = Object.entries(SURFACES).map(([k, v]) => ({ value: k, label: v.label }))

  return (
    <div className="settings-view">
      <div className="help-docs card">
        <nav className="help-nav">
          <div className="help-nav-head"><span>Settings</span></div>
          {/* Grouped like the user guide's rail: a group opens when it holds
              the section being edited, and a header click opens or closes it. */}
          {sections.reduce<{ name: string | null; items: typeof sections }[]>((acc, sec) => {
            const last = acc[acc.length - 1]
            if (last && last.name === sec.group && sec.group !== null) last.items.push(sec); else acc.push({ name: sec.group, items: [sec] })
            return acc
          }, []).map((g, i) => {
            const rows = g.items.map(s => (
              <button key={s.id} className={section === s.id ? 'active' : ''} onClick={() => goSection(s.id)}>
                <span>{s.label}</span>
              </button>
            ))
            if (!g.name || g.items.length === 1) return <div key={`flat-${i}`} className="help-nav-group">{rows}</div>
            const open = railOpen[g.name] ?? g.items.some(s => s.id === section)
            return (
              <div key={g.name} className="help-nav-group">
                <button className="help-nav-group-head" aria-expanded={open} onClick={() => setRailOpen(o => ({ ...o, [g.name!]: !open }))}>
                  <span>{g.name}</span>
                  <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.6"><path d="m6 4 4 4-4 4"/></svg>
                </button>
                {open && <div className="help-nav-group-pages">{rows}</div>}
              </div>
            )
          })}
        </nav>
        <article className="help-content settings-content">

          {section === 'general' && (
            <>
              <div className="settings-head">
                <h1>General</h1>
                {setupAvailable && (
                  <Button variant="secondary" size="sm" title="The name and icon, appearance, AI features, the sidebar, the folder to index, and other libraries, a step at a time"
                    onClick={() => window.dispatchEvent(new Event('ade:setup-open'))}>Run setup again</Button>
                )}
              </div>
              {!setupAvailable && (
                <p className="muted view-sub">
                  Saved settings override the deployment's environment defaults and live beside the index, not in the
                  repository. Model credentials stay in the environment (see docs/CUSTOMIZATION.md).
                </p>
              )}
              <Tabs label="General settings" items={[...tabs]} value={tab} onValueChange={v => goSection(v)} className="settings-tabs">
                <TabPanel value="appearance">
                  <p className="muted view-sub">Saved as you change them.</p>
                  <Field group label={setupAvailable ? 'Theme' : 'Theme in this browser'}
                    description={setupAvailable
                      ? "System follows this computer's light or dark setting. The sidebar's theme button switches it too."
                      : "System follows the device's light or dark setting, unless the default below is Dark. Each browser keeps its own choice; the sidebar's theme button switches it too."}>
                    <SegmentedControl options={THEME_OPTIONS} value={theme ?? currentThemeChoice(form.theme)} onValueChange={t => {
                      setTheme(t)
                      if (setupAvailable) {
                        setForm(f => f && { ...f, theme: t === 'dark' ? 'dark' : 'light' })
                        saveThemeChoice(t).catch(e => setAppearanceNote(String((e as Error).message ?? e)))
                      } else setThemeChoice(t)
                    }} />
                  </Field>
                  <div className="settings-grid">
                    {!setupAvailable && (
                      <Field label="Default theme for new visitors">
                        <Select items={THEMES} value={form.theme} onValueChange={v => v && saveLook({ theme: v })} />
                      </Field>
                    )}
                    <Field label="Surface tone">
                      <Select items={surfaces} value={form.surface}
                        onValueChange={v => { if (v) { applySurface(v); saveLook({ surface: v }) } }} />
                    </Field>
                  </div>
                  <Field group label="Accent color">
                    <AccentSwatches value={form.accent} onChange={v => saveLook({ accent: v }, v.startsWith('custom:') ? 400 : 0)} />
                  </Field>
                  {appearanceNote && <Callout tone="error" live>{appearanceNote}</Callout>}
                </TabPanel>

                <TabPanel value="branding">
                  <div className="settings-grid">
                    <Input label="Product name" value={form.appName} onChange={e => setForm({ ...form, appName: e.target.value })} />
                    {setupAvailable ? (
                      <Field group label="Icon" description="Saved as it is chosen.">
                        <BrandIcons value={branding} onChange={b => {
                          setBranding(b)
                          setForm(f => f && { ...f, iconUrl: b.iconUrl ?? '', iconUrlDark: b.iconUrlDark ?? '' })
                        }} />
                      </Field>
                    ) : (
                      <>
                        <Input label="Brand icon (URL, or a path on the server)" value={form.iconUrl} placeholder="https://example.org/logo.png"
                          onChange={e => setForm({ ...form, iconUrl: e.target.value })} />
                        <Input label="Brand icon for dark mode (optional)" value={form.iconUrlDark} placeholder="leave empty to use the same image"
                          onChange={e => setForm({ ...form, iconUrlDark: e.target.value })} />
                      </>
                    )}
                    <div className="banner-field">
                      <Field label="Classification banner (empty for none)">
                        <div className="banner-row">
                          <ActionMenu label="Marking" align="start"
                            triggerClassName={cx(fieldBox(), 'banner-preset')}
                            items={BANNER_PRESETS.map(p => ({
                              label: p.label,
                              onClick: () => setForm({ ...form, bannerText: p.text, bannerColor: p.color }),
                            }))}>
                            Marking… <ChevronDownIcon aria-hidden="true" />
                          </ActionMenu>
                          <Input className="banner-text" value={form.bannerText ?? ''}
                            placeholder="Text across the top, or pick a marking"
                            onChange={e => setForm({ ...form, bannerText: e.target.value })} />
                          {/* Every library control in one field takes the field's
                              id, so the color, a second box, is a plain input in
                              the library's field look. */}
                          <input className={cx(fieldBox(), 'banner-hex')} aria-label="Banner color" value={form.bannerColor ?? ''} placeholder="#24612e"
                            onChange={e => setForm({ ...form, bannerColor: e.target.value })} />
                        </div>
                        {(form.bannerText ?? '').trim() && (
                          <div className="cls-banner banner-preview"
                            style={{ background: form.bannerColor, color: bannerInk(form.bannerColor) }}>
                            {form.bannerText}
                          </div>
                        )}
                      </Field>
                      <label className="key-persist">
                        <Checkbox checked={!!form.bannerWhenEmbedded}
                          onCheckedChange={v => setForm({ ...form, bannerWhenEmbedded: v })} />
                        <span>Show it inside the platform frame too, which already draws its own</span>
                      </label>
                    </div>
                  </div>
                  {saveRow()}
                </TabPanel>

                <TabPanel value="sidebar">
                  <p className="muted view-sub">Pages the sidebar shows; Settings and Help always show. Saved as you switch.</p>
                  <SidebarSwitches value={form.sections} platform={!!cfg.platform} aiEnabled={form.aiEnabled !== false} onChange={next => {
                    setForm({ ...form, sections: next })
                    setSectionsNote('')
                    saveSections(next).catch(e => setSectionsNote(String((e as Error).message ?? e)))
                  }} />
                  {sectionsNote && <Callout tone="error" live>{sectionsNote}</Callout>}
                </TabPanel>

                <TabPanel value="ai">
                  <p className="muted view-sub">
                    Off takes Chat and Agents out of the sidebar and sends nothing to a model; search, the library, query and
                    statistics work as before. They can be switched back on here at any time. Saved as you switch.
                    {(me?.single || me?.authEnabled) && <> The model endpoint is under <a href="#view=settings:access">Model access</a>.</>}
                  </p>
                  <div className="access-switch">
                    <Switch checked={form.aiEnabled !== false} aria-label="AI features" onLabel="On" offLabel="Off"
                      onCheckedChange={v => {
                        setSectionsNote('')
                        saveAiEnabled(v, form.sections)
                          .then(sections => setForm(f => f && { ...f, aiEnabled: v, sections }))
                          .catch(e => setSectionsNote(String((e as Error).message ?? e)))
                      }} />
                    <span>Chat, Agents, and other features that call a model</span>
                  </div>
                  {sectionsNote && <Callout tone="error" live>{sectionsNote}</Callout>}
                  {me?.authEnabled && (
                    <label className="key-persist">
                      <Checkbox checked={form.requirePersonalKey}
                        onCheckedChange={v => setForm({ ...form, requirePersonalKey: v })} />
                      Require each user to add their own model credential (chat and models refuse the deployment credential, though a key another user has shared here is accepted; browsing, search, and adding material stay open to everyone)
                    </label>
                  )}
                  {me?.authEnabled && (
                    <label className="key-persist">
                      <Checkbox checked={form.allowKeySharing}
                        onCheckedChange={v => setForm({ ...form, allowKeySharing: v })} />
                      Let a user share their own model credential with everyone here, standing in for the deployment credential (useful where the deployment has none; the sharer's quota covers the group)
                    </label>
                  )}
                  {me?.authEnabled && (
                    <label className="key-persist">
                      <Checkbox checked={form.chatSharedHistory}
                        onCheckedChange={v => setForm({ ...form, chatSharedHistory: v })} />
                      Allow users to see each other's chat sessions in the chat interface (the rail still defaults each user to their own, with a toggle to show all; off removes that option entirely; exported transcripts remain in the knowledge base tree either way)
                    </label>
                  )}
                  <Field className="settings-field" label="Mission and context"
                    description="The assistant treats this as authoritative about what this Studio is for, and it never invents expansions for acronyms the mission and corpus leave undefined.">
                    <Textarea
                      className="prompts-box"
                      rows={4}
                      value={form.kbMission ?? ''}
                      placeholder="What this Studio is for: the program or project it serves, what the corpus holds, terms the assistant should know. Injected into the assistant's system prompt."
                      onChange={e => setForm({ ...form, kbMission: e.target.value })}
                    />
                  </Field>
                  <Field className="settings-field" label="Suggested questions (one per line)"
                    description="Shown under an empty chat, to click and ask.">
                    <Textarea
                      className="prompts-box"
                      rows={6}
                      value={form.suggestedPrompts.join('\n')}
                      onChange={e => setForm({ ...form, suggestedPrompts: e.target.value.split('\n') })}
                    />
                  </Field>
                  <div className="settings-grid settings-field">
                    <Field label="Vision model for image captioning"
                      description={form.visionModel && deploymentCred === false ? <>
                        Captioning runs during background indexing, when no user is present, so it uses
                        the deployment credential or the host's pw CLI login rather than a personal key.
                        This deployment has neither, so images will be indexed by OCR and filename only.
                      </> : 'Captions images as they are indexed, so they can be found by what they show.'}>
                      <ModelInput
                        value={form.visionModel}
                        models={pickableModels}
                        emptyLabel="disabled (no image captioning)"
                        onChange={v => setForm({ ...form, visionModel: v })}
                      />
                    </Field>
                  </div>
                  {saveRow(false)}
                </TabPanel>

                <TabPanel value="indexing">
                  <div className="settings-grid">
                    <Field label="Search across libraries"
                      description="Both return the same matches. gufi_vt answers its libraries one after another; Query uses it either way.">
                      <Select items={SEARCH_ENGINES} value={form.searchEngine ?? 'parallel'} onValueChange={v => v && setForm({ ...form, searchEngine: v })} />
                    </Field>
                    <Field label="Background sync interval (seconds, 0 pauses)">
                      <NumberInput min={0} max={86400} value={form.sweepIntervalSec}
                        onValueChange={v => setForm({ ...form, sweepIntervalSec: v ?? 0 })} />
                    </Field>
                    <Field label="Corpus history snapshot (seconds between points)"
                      description={<>
                        86400 is one point a day, summarizing everything that changed since the previous point. 0 captures
                        every index pass, which is a churn log on a busy corpus. Days with no change are recorded as a
                        check either way.
                      </>}>
                      <NumberInput min={0} max={2592000} step={3600} value={form.historyIntervalSec}
                        onValueChange={v => setForm({ ...form, historyIntervalSec: v ?? 0 })} />
                    </Field>
                  </div>
                  <p className="muted view-sub">Libraries and their indexes are under <a href="#view=settings:libraries">Libraries</a>.</p>
                  {saveRow(false)}
                  {/* How the main folder's index is built: threads, readers,
                      embeddings, where it is kept (IndexingSection). */}
                  <IndexingSection />
                </TabPanel>

                <TabPanel value="updates">
                  <UpdatesSection onPolicySaved={updates => setForm(f => f && ({ ...f, updates } as Effective))} />
                </TabPanel>

                <TabPanel value="server">
                  <ServerSection />
                </TabPanel>
              </Tabs>
            </>
          )}

          {section === 'access' && <ModelAccessSection requirePersonalKey={form.requirePersonalKey} />}

          {section === 'tools' && (
            <>
              <h1>Assistant tools</h1>
              <p className="muted view-sub">
                Everything the chat assistant can call. Built-in tools are part of the server; unchecking one hides it from the model, and clicking a name shows what it actually calls plus its exact specification, so you can copy the shape into a custom tool or extension file with your own changes. Custom tools are commands you define here (saved with these settings, not as files); each appears to the assistant as a callable tool and runs on this server with its output returned to the conversation.
              </p>
              <div className="settings-grid">
                <Field label="Most tool rounds per reply"
                  description="Each round is one model call and the tools it asks for. At the limit the assistant answers from what it has and says it stopped.">
                  <NumberInput min={5} max={200} value={form.maxToolRounds}
                    onValueChange={v => setForm({ ...form, maxToolRounds: v ?? 0 })} />
                </Field>
              </div>
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
                          <Checkbox
                            aria-label={t.name}
                            checked={!form.disabledTools.includes(t.name)}
                            onCheckedChange={on => setForm({
                              ...form,
                              disabledTools: on
                                ? form.disabledTools.filter(n => n !== t.name)
                                : [...form.disabledTools, t.name],
                            })}
                          />
                          <button className="tool-name" title="Show the parameter schema this tool accepts"
                            aria-expanded={specOpen === t.name}
                            aria-controls={specOpen === t.name ? `tool-spec-${t.name}` : undefined}
                            onClick={() => setSpecOpen(o => o === t.name ? null : t.name)}>
                            <code>{t.name}</code>
                          </button>
                          <span className="tool-desc">
                            {t.asksFirst && <span className="tool-asks" title="Each call waits for the user's approval in the chat">Asks first</span>}
                            {t.description}
                          </span>
                        </div>
                        {/* What the tool actually runs is the useful part, so
                            it sits under every row rather than behind a click
                            with the wire schema on top of it. */}
                        {t.calls && <p className="tool-calls">{t.calls}</p>}
                        {t.command && <p className="tool-calls"><code>{t.command}</code></p>}
                        <DisclosurePanel open={specOpen === t.name} id={`tool-spec-${t.name}`}>
                          <pre className="tool-spec">{JSON.stringify({ name: t.name, description: t.description, parameters: t.parameters }, null, 2)}</pre>
                        </DisclosurePanel>
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
                    <Input placeholder="tool_name" aria-label="Tool name" value={t.name}
                      onChange={e => setForm({ ...form, customTools: form.customTools.map((x, j) => j === i ? { ...x, name: e.target.value } : x) })} />
                    <Input placeholder="What it does, for the model" aria-label="Tool description" value={t.description}
                      onChange={e => setForm({ ...form, customTools: form.customTools.map((x, j) => j === i ? { ...x, description: e.target.value } : x) })} />
                    <Input mono placeholder="command to run" aria-label="Command" value={t.command}
                      onChange={e => setForm({ ...form, customTools: form.customTools.map((x, j) => j === i ? { ...x, command: e.target.value } : x) })} />
                    <Button variant="danger" onClick={() => setForm({ ...form, customTools: form.customTools.filter((_, j) => j !== i) })}>Remove</Button>
                  </div>
                ))}
                <Button variant="secondary" onClick={() => setForm({ ...form, customTools: [...form.customTools, { name: '', description: '', command: '' }] })}>Add custom tool</Button>
              </div>
              {saveRow()}
            </>
          )}

          {section === 'rag' && (
            <>
              <h1>External access</h1>
              <p className="muted view-sub">Two ways to use this knowledge base from outside. They are independent, and a deployment can serve either or both.</p>
              <DescriptionList className="access-compare">
                <DescriptionListItem label="Knowledge tools (MCP)">
                  The client brings its own model and calls the corpus tools directly. Answers come from the caller's model, grounded by our search. Suits pw code, other agentic clients, and IDE assistants.
                </DescriptionListItem>
                <DescriptionListItem label="Grounded model (OpenAI-compatible)">
                  This deployment answers as a model, with retrieval built in. Suits any client that can only point at an OpenAI base URL, and it is what the platform model catalog publishes.
                </DescriptionListItem>
              </DescriptionList>
              <div className="tool-group">Knowledge tools over MCP (your model, our tools)</div>
              <p className="muted view-sub">
                <code>{location.origin}/api/mcp</code><CopyButton text={`${location.origin}/api/mcp`} label="MCP address" /> serves
                this knowledge base over the Model Context Protocol: the client brings its own model and calls the corpus tools
                directly (search, read, list, query, labels, docs). Read-only; adding material, labels, and platform actions stay
                with this assistant. {me?.authEnabled
                  ? 'This deployment requires sign-in, so clients send a platform token as the bearer.'
                  : 'This deployment is open, so no header is needed.'}
              </p>
              <div className="access-switch">
                <Switch checked={form.mcpEnabled} onCheckedChange={v => setForm({ ...form, mcpEnabled: v })} onLabel="On" offLabel="Off" aria-label="Serve MCP" />
                <span>Serve MCP at /api/mcp. Individual tools can be disabled in the tool catalog above.</span>
              </div>
              <p className="field-label">pw code</p>
              <CopyCodeBlock className="mcp-snippet" text={`pw code mcp add --transport http studio-kb ${location.origin}/api/mcp`} label="pw code command">
                pw code mcp add --transport http studio-kb {location.origin}/api/mcp
              </CopyCodeBlock>
              <p className="muted key-note">pw code signs its platform requests with your existing CLI login, so no token goes in the command.</p>
              <div className="tool-group">Delegation (the assistant working in parallel)</div>
              <p className="muted view-sub">
                With this on, the assistant may split a request into subtasks and run them concurrently as headless
                agents, rather than doing everything in one conversation. It decides when, which is how these systems
                are built; you decide whether, and how far it may go. Progress appears on the Agents tab and results
                land in the knowledge base under <code>tasks/</code>.
              </p>
              <p className="muted view-sub">
                Worth knowing before enabling: parallel agents are the right tool for work that splits into
                independent strands, and the wrong one for tightly interdependent work. Published systems of this
                kind use roughly fifteen times the tokens of a single conversation, so the ceilings below are a cost
                control as much as a safety one.
              </p>
              <div className="access-switch">
                <Switch checked={form.delegationEnabled} onCheckedChange={v => setForm({ ...form, delegationEnabled: v })} onLabel="On" offLabel="Off" aria-label="Delegate to concurrent agents" />
                <span>Let the assistant delegate subtasks to concurrent agents</span>
              </div>
              <div className="settings-grid">
                <Field label="Most agents per task" description="Counting subtasks the agents themselves start.">
                  <NumberInput min={1} max={24} value={form.delegationMaxAgents}
                    onValueChange={v => setForm({ ...form, delegationMaxAgents: v ?? 0 })} />
                </Field>
                <Field label="How deep agents may delegate" description="0 stops agents starting their own subtasks; 1 lets them go one level.">
                  <NumberInput min={0} max={3} value={form.delegationMaxDepth}
                    onValueChange={v => setForm({ ...form, delegationMaxDepth: v ?? 0 })} />
                </Field>
                <div>
                  <Field label="Agent execution"
                    description={<>
                      Sessions run in this host's pw code daemon: their progress is live on the Agents tab, a person
                      approves what an agent asks to do, and a working agent can be steered. With remote control on,
                      they can also be watched from ACTIVATE's agents page. Campaigns on other systems use the runner.
                    </>}>
                    <Select items={AGENT_EXECUTION} value={form.agentExecution} onValueChange={v => v && setForm({ ...form, agentExecution: v })} />
                  </Field>
                  {runtime && (
                    <p className="muted key-note">
                      {runtime.local.version
                        ? `pw code daemon ${runtime.local.version}${runtime.local.available ? '' : `: ${runtime.local.reason}`}; remote session start ${runtime.local.remoteStart ? `on, at most ${runtime.local.remoteMaxPermissionMode}` : 'off'}.`
                        : runtime.local.reason}
                    </p>
                  )}
                </div>
              </div>
              {saveRow(false)}

              <div className="tool-group">Attached MCP servers (tools from elsewhere)</div>
              <p className="muted view-sub">
                Servers this deployment connects to as a client, so their tools join the assistant's own and one
                question can cross both. A server's tools appear namespaced under its name. Two things to weigh
                before attaching one: the assistant will send tool arguments to that third party, and a server's
                tool descriptions are text the model reads while choosing what to call, so attach only servers you
                trust.
              </p>
              {(form.mcpServers ?? []).map((srv, i) => {
                const st = mcpStatus.find(x => x.name === srv.name)
                return (
                  <div className="mcp-server-row" key={i}>
                    <Input placeholder="name (hubspot)" aria-label="Server name" value={srv.name}
                      onChange={e => { const next = [...form.mcpServers]; next[i] = { ...srv, name: e.target.value }; setForm({ ...form, mcpServers: next }) }} />
                    <Input placeholder="https://host/mcp" aria-label="Server URL" value={srv.url}
                      onChange={e => { const next = [...form.mcpServers]; next[i] = { ...srv, url: e.target.value }; setForm({ ...form, mcpServers: next }) }} />
                    <Input placeholder="Authorization: Bearer <token>" aria-label="Authorization header" value={srv.header ?? ''}
                      onChange={e => { const next = [...form.mcpServers]; next[i] = { ...srv, header: e.target.value }; setForm({ ...form, mcpServers: next }) }} />
                    <Button variant="secondary" onClick={() => setForm({ ...form, mcpServers: form.mcpServers.filter((_, j) => j !== i) })}>Remove</Button>
                    <span className="mcp-server-state">
                      {st ? (st.error
                        ? <><StatusDot variant="warning" /> {st.error}</>
                        : <><StatusDot variant="success" /> {st.tools} tools</>)
                        : <span className="muted">save to connect</span>}
                    </span>
                  </div>
                )
              })}
              <div className="query-actions">
                <Button variant="secondary" onClick={() => setForm({ ...form, mcpServers: [...(form.mcpServers ?? []), { name: '', url: '', header: '' }] })}>Attach a server</Button>
              </div>
              {saveRow(false)}

              <p className="field-label">Other MCP clients (settings JSON)</p>
              <CopyCodeBlock className="mcp-snippet" text={mcpJson} label="MCP settings">{mcpJson}</CopyCodeBlock>
              {saveRow(false)}


              <div className="tool-group">Grounded model (OpenAI-compatible /v1)</div>
              <div className="access-switch">
                <Switch checked={form.ragProxyEnabled} onCheckedChange={v => setForm({ ...form, ragProxyEnabled: v })} onLabel="On" offLabel="Off" aria-label="Serve the grounded model" />
                <span>Serve the grounded model at /v1.</span>
              </div>
              {!form.ragProxyEnabled && (
                <p className="muted view-sub">
                  Off: /v1 refuses calls, and any platform registration is taken down with it. Turn it on to configure the models, keys, and platform registration.
                </p>
              )}
              {!form.ragProxyEnabled && saveRow(false)}
              {form.ragProxyEnabled && (<>
              <p className="muted view-sub">
                The endpoint at{' '}
                <code>{location.origin}/v1</code><CopyButton text={`${location.origin}/v1`} label="endpoint address" /> lets
                any OpenAI-speaking client (pw code, SDKs, other agents) use the knowledge base as a grounded model.
                Callers authenticate with their own gateway API key as the bearer token; the endpoint holds no credentials of its own.
              </p>
              <p className="muted view-sub">
                Pick the underlying model per request as <code>studio-agent/&lt;model-id&gt;</code> (any other model id behaves like studio-rag with that model), or set the default below. Headers <code>X-RAG-Top-K</code>, <code>X-RAG-Tags</code>, and <code>X-RAG-Off: 1</code> tune retrieval per request.
              </p>
              <div className="settings-grid">
                <Field label="Default underlying model (for bare studio-agent / studio-rag)">
                  <ModelInput
                    value={form.ragDefaultModel}
                    models={pickableModels}
                    emptyLabel="none (callers must pass studio-agent/<model-id>)"
                    onChange={v => setForm({ ...form, ragDefaultModel: v })}
                  />
                </Field>
                <Field label="Retrieved context blocks (top K)">
                  <NumberInput min={1} max={20} value={form.ragTopK}
                    onValueChange={v => setForm({ ...form, ragTopK: v ?? 0 })} />
                </Field>
              </div>
              <label className="key-persist">
                <Checkbox checked={form.ragAllowDeploymentKey}
                  onCheckedChange={v => setForm({ ...form, ragAllowDeploymentKey: v })} />
                Allow callers without a bearer key to use the deployment credential (off means every caller must present their own key)
              </label>
              <label className="key-persist">
                <Checkbox checked={form.ragAdvertiseAgentModel}
                  onCheckedChange={v => setForm({ ...form, ragAdvertiseAgentModel: v })} />
                Advertise <code>studio-agent</code> in model listings
              </label>
              <p className="muted key-note toggle-note">
                Runs the full assistant pipeline on the server (system prompt, search, read, query, labels) and returns a finished cited answer. Slower, several model calls per question. The right choice inside tool-calling harnesses such as pw code.
              </p>
              <label className="key-persist">
                <Checkbox checked={form.ragAdvertiseRagModel}
                  onCheckedChange={v => setForm({ ...form, ragAdvertiseRagModel: v })} />
                Advertise <code>studio-rag</code> in model listings
              </label>
              <p className="muted key-note toggle-note">
                Injects retrieved context blocks into a single model call, no tool loop. Fast, a few seconds. Suits plain chat interfaces; inside code agents the host's own tools take over and the grounding is lost.
              </p>
              <p className="muted key-note">Both stay callable by name whether advertised or not.</p>
              <div className="settings-grid">
                <Input label="Platform registration name (how the model appears in catalogs and dropdowns)" value={form.ragEndpointName}
                  placeholder="defaults to <product-name>-rag"
                  onChange={e => setForm({ ...form, ragEndpointName: e.target.value })} />
              </div>
              <label className="key-persist">
                <Checkbox checked={form.ragEndpointAutoStart}
                  onCheckedChange={v => setForm({ ...form, ragEndpointAutoStart: v })} />
                Register on the platform automatically at startup
              </label>
              {saveRow(false)}
              <div className="tool-group">Platform registration</div>
              <p className="muted view-sub">
                Publishes this /v1 surface into the platform chat and AI provider catalog (via <code>pw endpoints run --openai</code> around a local forwarder), so users pick the corpus-grounded models like any other. Platform callers reach it with their own credentials; the deployment credential covers only keyless clients such as pw code. Requires an authenticated pw CLI on the host.
              </p>
              <Callout className="rag-banner" tone={ragEpTone}
                title={ragEp?.state === 'running' ? 'Connected: published in the platform model catalog' :
                  ragEp?.state === 'starting' ? 'Registering…' :
                  ragEp?.state === 'error' ? 'Registration failed' : 'Not registered on the platform'}
                action={<>
                  {ragEp?.state === 'running' || ragEp?.state === 'starting'
                    ? <Button variant="secondary" size="sm" onClick={() => void ragEpAction('stop')}>Stop</Button>
                    : <Button variant="primary" size="sm" onClick={() => void ragEpAction('start')}>Register now</Button>}
                  <RefreshButton size="sm" onRefresh={refreshRagEp} />
                </>}>
                {ragEp?.state === 'running' && ragEp.name && (
                  <div className="rag-banner-models">
                    Users can now select {form.ragAdvertiseAgentModel && form.ragAdvertiseRagModel ? 'these models' : 'this model'} anywhere on the platform:
                    <div className="rag-chip-row">
                      {form.ragAdvertiseAgentModel && (
                        <span><code>session:{cfg.user.username}:{ragEp.name}/studio-agent</code><CopyButton text={`session:${cfg.user.username}:${ragEp.name}/studio-agent`} label="model name" /></span>
                      )}
                      {form.ragAdvertiseRagModel && (
                        <span><code>session:{cfg.user.username}:{ragEp.name}/studio-rag</code><CopyButton text={`session:${cfg.user.username}:${ragEp.name}/studio-rag`} label="model name" /></span>
                      )}
                      {!form.ragAdvertiseAgentModel && !form.ragAdvertiseRagModel && <span className="muted">No models advertised; enable a toggle above.</span>}
                    </div>
                  </div>
                )}
                {ragEp?.detail && ragEp.state !== 'running' && <p className="muted key-note">{ragEp.detail}</p>}
              </Callout>
              </>)}
              {form.ragProxyEnabled && (<>
              <div className="tool-group">Recent grounded-model calls</div>
              <p className="muted view-sub">
                The last 50 calls to this /v1 surface, most recent first, held in memory and cleared on restart. Question text is not recorded; the distilled retrieval terms are.
              </p>
              <RefreshButton onRefresh={refreshRagCalls} />
              {!ragCalls.length && <p className="muted key-note">No calls since the server started; they appear here as clients use the endpoint.</p>}
              {ragCalls.length > 0 && (
                <Table density="compact" className="rag-calls-log" wrapperClassName="rag-calls-wrap">
                  <Table.Header>Time</Table.Header>
                  <Table.Header>Model</Table.Header>
                  <Table.Header>Underlying</Table.Header>
                  <Table.Header>Credential</Table.Header>
                  <Table.Header>Duration</Table.Header>
                  <Table.Header>Retrieval</Table.Header>
                  <Table.Header>Status</Table.Header>
                  {ragCalls.map((c, i) => (
                    <tr key={i}>
                      <Table.Item>{new Date(c.at).toLocaleTimeString()}</Table.Item>
                      <Table.Item><code>{c.model}</code></Table.Item>
                      <Table.Item><code>{c.underlying || '-'}</code></Table.Item>
                      <Table.Item>{c.authSource.includes('injected') ? 'deployment (keyless caller)'
                        : c.authSource.includes('stored personal') ? 'personal key'
                        : c.authSource.includes('deployment') ? 'deployment'
                        : c.authSource === 'none' ? 'none' : 'caller key'}</Table.Item>
                      <Table.Item>{c.durationMs == null ? 'running' : `${(c.durationMs / 1000).toFixed(1)}s`}</Table.Item>
                      <Table.Item wrap>{c.retrieval ? `${c.retrieval.blocks} blocks (${c.retrieval.terms.join(', ')})` : c.mode === 'agent' ? 'agent tool loop' : '-'}</Table.Item>
                      <Table.Item wrap>{c.status}{c.detail ? `; ${c.detail}` : ''}</Table.Item>
                    </tr>
                  ))}
                </Table>
              )}
              </>)}
            </>
          )}

          {section === 'previews' && (
            <>
              <h1>Feature previews</h1>
              <p className="muted view-sub">
                Capabilities that work end to end but are still being shaped. Each is off until switched on here, and each
                shows its control in the interface only when it is on and configured.
              </p>
              <div className="access-switch">
                <Switch checked={form.voiceEnabled} onCheckedChange={v => setForm({ ...form, voiceEnabled: v })} onLabel="On" offLabel="Off" aria-label="Voice conversations" />
                <span>Voice conversations: talk with the assistant out loud, through a speech service you deploy</span>
              </div>
              {form.voiceEnabled && (
                <>
                  <p className="muted view-sub">
                    Voice runs on an Unmute deployment (Kyutai, MIT), which adds speech recognition, turn-taking and speech
                    synthesis around a text model; only the speech services need a GPU. Deploy it with
                    the <code>unmute</code> workflow pointed at this Studio and the <code>studio-voice</code> model, which is
                    the assistant with its tools and knowledge base answering in spoken sentences
                    (<code>studio-voice/&lt;gateway-model-id&gt;</code> picks the model behind it). Then put the deployment's
                    session URL here, and a Voice button appears above the chat. The browser you use has to reach the
                    deployment, so a closed network needs its own.
                  </p>
                  <Input label="Unmute session URL" value={form.voiceUrl} placeholder="https://unmute.example" onChange={e => setForm({ ...form, voiceUrl: e.target.value })} />
                </>
              )}
              {saveRow()}
            </>
          )}
          {section === 'libraries' && <LibrariesSection />}
          {section === 'workflows' && <WorkflowsSection />}
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
                    ? <DescriptionList>{ext.tools.map(t => (
                      <DescriptionListItem key={t.file} label={<code>{t.name}</code>}>
                        {t.description}{t.command ? <> (<code>{t.command}</code>)</> : null}
                      </DescriptionListItem>
                    ))}</DescriptionList>
                    : <EmptyState variant="bare" title="None yet. Drop a JSON file in tools/." />}
                </div>
                <div>
                  <div className="tool-group">Skills ({ext?.skills.length ?? 0})</div>
                  {ext?.skills.length
                    ? <DescriptionList>{ext.skills.map(t => (
                      <DescriptionListItem key={t.file} label={<code>{t.name}</code>}>{t.description}</DescriptionListItem>
                    ))}</DescriptionList>
                    : <EmptyState variant="bare" title="None yet. Drop a markdown file in skills/." />}
                </div>
                <div>
                  <div className="tool-group">Agents ({ext?.agents.length ?? 0})</div>
                  {ext?.agents.length
                    ? <DescriptionList>{ext.agents.map(t => (
                      <DescriptionListItem key={t.file} label={<code>{t.file}</code>}>
                        {t.active ? 'active (appended to the system prompt)' : 'inactive; rename to default.md to activate'}
                      </DescriptionListItem>
                    ))}</DescriptionList>
                    : <EmptyState variant="bare" title="None yet. Drop default.md in agents/." />}
                </div>
              </div>
            </>
          )}

        </article>
      </div>
    </div>
  )
}
