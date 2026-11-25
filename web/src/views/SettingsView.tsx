import { LibrariesSection } from '../components/LibrariesSection'
import { ModelAccessSection } from '../components/ModelAccessSection'
import { AccountsSection } from '../components/AccountsSection'
import { IndexBuildSettings, IndexStorageSettings } from '../components/IndexingSection'
import { IndexGroup } from '../components/IndexGroup'
import { WorkflowsSection } from '../components/WorkflowsSection'
import { TransfersSettingsSection } from '../components/TransfersSettingsSection'
import { transfersPresent } from '../transferApi'
import { SectionStrip } from '../components/SectionStrip'
import { useEffect, useRef, useState, type ReactNode } from 'react'
import { okJSON } from '../api'
import {
  ActionMenu, Autocomplete, Button, Callout, type CalloutTone, Checkbox, CopyButton, CopyCodeBlock,
  DescriptionList, DescriptionListItem, DisclosurePanel, EmptyState, Field, Input, Loader, NumberInput,
  RefreshButton, SegmentedControl, Select, type SelectItem, StatusLabel, Switch, Table, TabPanel, Tabs, Textarea, cx, fieldBox, formatBytes,
} from '@parallelworks/ui'
import { ChevronDownIcon } from '@parallelworks/ui/icons'
import { SURFACES, applySurface } from '../accents'
import { AccentSwatches } from '../components/AccentSwatches'
import { BrandIcons, type Branding } from '../components/BrandIcons'
import { ServerSection } from '../components/ServerSection'
import { UpdatesSection } from '../components/UpdatesSection'
import { NAV_WIDTHS, aiOn as aiOnIn, reloadAppConfig, saveNavWidth, saveAiEnabled, saveSections, saveSettings, useAppConfig } from '../config'
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
  navWidth: number
  iconUrl: string
  iconUrlDark: string
  sweepIntervalSec: number
  /** Seconds each library's search runs before it is stopped; 0 is no limit. */
  searchTimeLimitSec: number
  searchEngine: 'parallel' | 'gufi_vt'
  gufiLog: boolean
  historyIntervalSec: number
  suggestedPrompts: string[]
  visionModel: string
  disabledTools: string[]
  customTools: { name: string; description: string; command: string }[]
  ragDefaultModel: string
  ragTopK: number
  ragAdvertiseRagModel: boolean
  ragAdvertiseAgentModel: boolean
  mcpEnabled: boolean
  mcpServers: { name: string; url: string; header?: string }[]
  delegationEnabled: boolean
  delegationMaxAgents: number
  maxToolRounds: number
  delegationMaxDepth: number
  agentExecution: 'runner' | 'sessions' | 'both'
  chatEngine: 'auto' | 'pwcode' | 'studio'
  pwcodeModel: string
  pwcodePermissionMode: '' | 'read-only' | 'accept-edits' | 'auto' | 'plan'
  /** Whether pw code may run on Claude Code accounts here; auto is on for a
   *  Studio of one and off where people sign in or ACTIVATE deployed it,
   *  except for someone already using them. */
  claudeCodeAccounts?: 'auto' | 'on' | 'off'
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
  ['Knowledge base', ['search_kb', 'read_kb_file', 'read_original', 'write_kb_file', 'edit_kb_file', 'move_kb_files', 'rename_kb_file', 'copy_kb_files', 'delete_kb_files', 'list_kb_dir', 'query_corpus', 'get_labels', 'suggest_labels', 'apply_labels']],
  ['Transfers', ['transfer_locations', 'browse_location', 'transfer_files', 'transfer_status', 'cancel_transfer', 'save_transfer_location']],
  ['Workflows', ['list_workflows', 'list_github_workflows', 'get_workflow', 'compose_workflow', 'serve_model', 'run_workflow', 'workflow_runs', 'workflow_run_detail', 'cancel_workflow_run', 'list_sessions', 'pw_help']],
  ['Clusters and storage', ['list_clusters', 'start_cluster', 'resume_cluster', 'stop_cluster', 'destroy_cluster', 'list_storage', 'hpc_status', 'launch_status_monitor', 'cluster_command']],
  ['pw code agents', ['agent_sites', 'start_agent_daemon', 'agent_sessions', 'start_agent', 'agent_status', 'agent_steer', 'agent_goal', 'agent_approve', 'agent_stop']],
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

/** The search time limits offered, in seconds (the server takes 0 to 86400). */
export const SEARCH_TIME_LIMITS: SelectItem<number>[] = [
  { value: 10, label: '10 seconds' },
  { value: 20, label: '20 seconds' },
  { value: 30, label: '30 seconds' },
  { value: 60, label: '1 minute' },
  { value: 120, label: '2 minutes' },
  { value: 300, label: '5 minutes' },
  { value: 600, label: '10 minutes' },
  { value: 1800, label: '30 minutes' },
  { value: 3600, label: '1 hour' },
  { value: 0, label: 'No limit' },
]

/** The choices, with a limit set some other way (the deployment's
 *  STUDIO_SEARCH_DEADLINE_MS) among them as it is. */
export function searchTimeLimits(current: number | undefined): SelectItem<number>[] {
  if (current === undefined || SEARCH_TIME_LIMITS.some(i => i.value === current)) return SEARCH_TIME_LIMITS
  return [{ value: current, label: `${current} seconds` }, ...SEARCH_TIME_LIMITS]
}

const CHAT_ENGINES: SelectItem<Effective['chatEngine']>[] = [
  { value: 'auto', label: 'Automatic: pw code under ACTIVATE, else the Studio' },
  { value: 'pwcode', label: 'pw code' },
  { value: 'studio', label: 'The Studio' },
]

const PWCODE_MODES: SelectItem<Effective['pwcodePermissionMode']>[] = [
  { value: '', label: "pw code's default (asks before commands)" },
  { value: 'read-only', label: 'Read only: no edits or commands' },
  { value: 'accept-edits', label: 'Accept edits, ask before commands' },
  { value: 'auto', label: 'Auto: pw code decides what to ask' },
  { value: 'plan', label: 'Plan first' },
]

const CLAUDE_CODE: SelectItem<NonNullable<Effective['claudeCodeAccounts']>>[] = [
  { value: 'auto', label: 'Automatic' },
  { value: 'on', label: 'On' },
  { value: 'off', label: 'Off' },
]

/** Claude Code accounts as they stand for the viewer (GET /api/settings). */
interface ClaudeCodeState { on: boolean; personal: boolean; locked?: string }

/** The Studio's own settings, read only for a person who may not change
 *  them, with why. The server refuses their changes either way. */
function Locked({ why, quiet, children }: { why?: string; quiet?: boolean; children: ReactNode }) {
  if (!why) return <>{children}</>
  return (
    <>
      {!quiet && <Callout tone="info">{why} They show here as they are set; ask one of them for a change.</Callout>}
      <fieldset disabled className="settings-locked">{children}</fieldset>
    </>
  )
}

type SectionId = 'general' | 'indexing' | 'transfers' | 'accounts' | 'access' | 'tools' | 'rag' | 'previews' | 'ext' | 'workflows'

/** The tabs of Settings, General, in order. Each has an address of its own
 *  (#view=settings:<tab>); #view=settings:general opens the first. Branding
 *  is a group of Appearance; its old address opens Appearance there. */
export const GENERAL_TABS = [
  { value: 'appearance', label: 'Appearance' },
  { value: 'sidebar', label: 'Sidebar' },
  { value: 'ai', label: 'AI features' },
  { value: 'updates', label: 'Updates' },
  { value: 'server', label: 'Server' },
] as const
export type GeneralTab = typeof GENERAL_TABS[number]['value']

/** The tabs of Settings, Indexing, in order, each with an address of its
 *  own. Libraries keeps the address it had as a section of its own
 *  (#view=settings:libraries, with ?add=1 for the Add a library form). */
export const INDEXING_TABS = [
  { value: 'libraries', label: 'Libraries' },
  { value: 'sync-search', label: 'Sync and search' },
  { value: 'building', label: 'Building' },
  { value: 'storage', label: 'Storage and logs' },
] as const
export type IndexingTab = typeof INDEXING_TABS[number]['value']
export type SettingsTab = GeneralTab | IndexingTab

/** The section and tab an address names: a tab's id, a section's own id, or
 *  anything else for General's first tab. A section opens on its first tab,
 *  by the rail or by its address: #view=settings:indexing opens Libraries. */
export function settingsTarget(id: string | null | undefined): { section: SectionId; tab: SettingsTab; anchor?: 'branding' } {
  const general = GENERAL_TABS.find(t => t.value === id)
  if (general) return { section: 'general', tab: general.value }
  if (id === 'branding') return { section: 'general', tab: 'appearance', anchor: 'branding' }
  if (id === 'indexing') return { section: 'indexing', tab: INDEXING_TABS[0].value }
  const indexing = INDEXING_TABS.find(t => t.value === id)
  if (indexing) return { section: 'indexing', tab: indexing.value }
  const sections: SectionId[] = ['general', 'transfers', 'accounts', 'access', 'tools', 'rag', 'previews', 'ext', 'workflows']
  return { section: sections.includes(id as SectionId) ? id as SectionId : 'general', tab: 'appearance' }
}

/** Sections with nothing to set while AI features are off: every setting in
 *  them is the assistant's. Their addresses open General, AI features, where
 *  the switch is. External access stays (MCP serves outside clients with no
 *  model here), and so does Accounts, which holds the ACTIVATE account the
 *  Workflows page and ACTIVATE libraries use. Model access, which says
 *  where models come from, opens Accounts. */
export const AI_ONLY_SECTIONS: readonly SectionId[] = ['tools', 'previews', 'ext']

/** The section and tab to show for a target: an AI-only section, while AI
 *  features are off, is General, AI features; Model access is Accounts. */
export function shownSettingsTarget<T extends { section: SectionId; tab: SettingsTab }>(t: T, aiEnabled: boolean): T | { section: SectionId; tab: SettingsTab } {
  if (!aiEnabled && t.section === 'access') return { section: 'accounts', tab: t.tab }
  return !aiEnabled && AI_ONLY_SECTIONS.includes(t.section) ? { section: 'general', tab: 'ai' } : t
}

/**
 * The settings rail, in order. Accounts, the ACTIVATE account and model
 * endpoints, appears where the deployment signs users in, and on a
 * single-user install (`single`); Model access, where models come from,
 * beside it with the assistant's sections. Feature previews is its own
 * section rather than a heading at the foot of another one: a capability
 * nobody can find is off for everyone regardless of the switch. Workflows
 * appears when the Studio reaches the platform, and, where Accounts is,
 * also before anyone has signed in, to say so.
 */
export function settingsSections(opts: { authEnabled: boolean; platform?: boolean; single?: boolean; aiEnabled?: boolean; transfers?: boolean }): { id: SectionId; label: string; group: string | null }[] {
  const accounts = opts.authEnabled || !!opts.single
  // Transfers, beside Indexing, where the server has transfers.
  const xfer = opts.transfers ? [{ id: 'transfers' as SectionId, label: 'Transfers', group: 'Studio' }] : []
  const studio = [
    { id: 'general' as SectionId, label: 'General', group: 'Studio' },
    { id: 'indexing' as SectionId, label: 'Indexing', group: 'Studio' },
    ...xfer,
    ...(opts.platform || accounts ? [{ id: 'workflows' as SectionId, label: 'Workflows', group: 'Studio' }] : []),
    ...(accounts ? [{ id: 'accounts' as SectionId, label: 'Accounts', group: 'Studio' }] : []),
  ]
  // With AI features off, the assistant's sections leave the rail, and so
  // does Model access: there are no models to choose.
  if (opts.aiEnabled === false) return [...studio, { id: 'rag', label: 'External access', group: null }]
  return [
    ...studio,
    ...(accounts ? [{ id: 'access' as SectionId, label: 'Model access', group: 'Assistant' }] : []),
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

const NAV_WIDTH_OPTIONS = [
  { value: 'narrow', label: 'Narrow' },
  { value: 'default', label: 'Default' },
  { value: 'wide', label: 'Wide' },
]

/** Which of the Width choice's three a width is, or null for a dragged one. */
export function navWidthPreset(px: number): keyof typeof NAV_WIDTHS | null {
  return (Object.keys(NAV_WIDTHS) as (keyof typeof NAV_WIDTHS)[]).find(k => NAV_WIDTHS[k] === px) ?? null
}

/** `active`: whether the Settings page is the one shown; it stays mounted
 *  while hidden, and sections that must not hold a stale copy read again
 *  when it is shown. */
export function SettingsView({ active = true }: { active?: boolean } = {}) {
  const cfg = useAppConfig()
  const [form, setForm] = useState<Effective | null>(null)
  const [deploymentCred, setDeploymentCred] = useState<boolean | null>(null)
  const [claudeCode, setClaudeCode] = useState<ClaudeCodeState | null>(null)
  /** Why this person may not change the Studio's settings (GET
   *  /api/settings): on a Studio with sign-in, all but its administrators. */
  const [locked, setLocked] = useState<string | undefined>()
  const [toolsApproved, setToolsApproved] = useState(true)
  // STUDIO_CUSTOM_TOOLS=0: no custom tool runs, and none can be added or changed.
  const [toolsOff, setToolsOff] = useState(false)
  // Where the GUFI command log is written (server/src/gufiLog.ts).
  const [gufiLogFile, setGufiLogFile] = useState<{ path: string; bytes: number } | null>(null)
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  // Which Save row the note and the busy state belong to: several tabs have more than one.
  const [savedAt, setSavedAt] = useState('')
  const [models, setModels] = useState<string[]>([])
  const [catalog, setCatalog] = useState<CatalogTool[]>([])
  const [mcpStatus, setMcpStatus] = useState<{ name: string; url: string; tools: number; error: string | null }[]>([])
  const [ext, setExt] = useState<Extensions | null>(null)
  // Settings rail groups the visitor opened or closed this visit.
  const [railOpen, setRailOpen] = useState<Record<string, boolean>>({})
  const [specOpen, setSpecOpen] = useState<string | null>(null)
  const [me, setMe] = useState<{ authEnabled?: boolean; single?: boolean; verified: boolean; mode: 'stored' | 'session' | 'none'; last4?: string; addedAt?: string; sessionExpiresAt?: string; kind?: string; credExpiresAt?: string | null; credExpired?: boolean; baseUrl?: string | null; gatewayHost?: string
    shared?: { active: boolean; last4?: string; sharedBy?: string; sharedAt?: string; credExpired?: boolean; mine?: boolean }
    sharingAllowed?: boolean } | null>(null)
  // Setup runs only on a single-user install (sign-in off), which also
  // uploads its icons rather than naming them.
  const [setupAvailable, setSetupAvailable] = useState(false)
  const [branding, setBranding] = useState<Branding | null>(null)
  const [sectionsNote, setSectionsNote] = useState('')
  const [hasTransfers, setHasTransfers] = useState(false)
  useEffect(() => { void transfersPresent().then(setHasTransfers) }, [])
  const [target, setTarget] = useState(() => settingsTarget(location.hash.match(/^#view=settings:([a-z-]+)/)?.[1]))
  // The switch as this page last saved it, else as the app has it. Off
  // hides what only the assistant uses; on brings it back at once.
  const aiEnabled = form ? form.aiEnabled !== false : aiOnIn(cfg)
  const { section, tab } = shownSettingsTarget(target, aiEnabled)
  // An address of a section AI features off has hidden names the switch.
  const redirected = section !== target.section
  // The page stays there once AI features are switched on.
  useEffect(() => {
    if (!redirected) return
    setTarget({ section: 'general', tab: 'ai' })
    if (location.hash.startsWith('#view=settings')) history.replaceState(null, '', `${location.pathname}${location.search}#view=settings:ai`)
  }, [redirected])
  // #view=settings:branding opens Appearance at its Branding group.
  useEffect(() => {
    if (target.anchor !== 'branding' || !form) return
    const id = requestAnimationFrame(() => document.getElementById('settings-branding')?.scrollIntoView({ block: 'start' }))
    return () => cancelAnimationFrame(id)
  }, [target, !!form]) // eslint-disable-line react-hooks/exhaustive-deps
  const setSection = (id: string) => setTarget(settingsTarget(id))

  const goSection = (id: SectionId | SettingsTab) => {
    setSection(id)
    history.replaceState(null, '', `${location.pathname}${location.search}#view=settings:${id}`)
  }
  // On a phone the rail is a strip that scrolls sideways: the section open
  // is brought into it, rather than left past its right edge.
  // Checked after each render until done for the section, since the
  // sections listed arrive with the account and settings.
  const navRef = useRef<HTMLElement>(null)
  const navShown = useRef<string | null>(null)
  useEffect(() => {
    const nav = navRef.current
    const on = nav?.querySelector<HTMLElement>('button.active')
    if (!nav || !on || navShown.current === section) return
    navShown.current = section
    if (nav.scrollWidth <= nav.clientWidth) return
    const a = on.getBoundingClientRect(), n = nav.getBoundingClientRect()
    if (a.left < n.left || a.right > n.right) nav.scrollLeft += a.left - n.left - (n.width - a.width) / 2
  })
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
  /** The product name and icon addresses save once typing pauses, on
   *  leaving the box, or on Enter. */
  const [brandNote, setBrandNote] = useState('')
  const brandTimer = useRef<number | undefined>(undefined)
  const brandPending = useRef<Partial<Effective>>({})
  const saveBrand = (patch: Partial<Pick<Effective, 'appName' | 'iconUrl' | 'iconUrlDark'>>, delay = 800) => {
    if (Object.keys(patch).length) setForm(f => f && { ...f, ...patch })
    brandPending.current = { ...brandPending.current, ...patch }
    setBrandNote('')
    window.clearTimeout(brandTimer.current)
    brandTimer.current = window.setTimeout(() => {
      const next = brandPending.current
      brandPending.current = {}
      if (!Object.keys(next).length) return
      saveSettings(next).catch(e => setBrandNote(String((e as Error).message ?? e)))
    }, delay)
  }
  /** The classification banner saves with its own button: a marking typed
   *  a letter at a time would show every visitor a wrong marking until the
   *  last keystroke, so it changes only when the whole of it is set. */
  const [bannerBusy, setBannerBusy] = useState(false)
  const [bannerNote, setBannerNote] = useState('')
  const saveBanner = async () => {
    if (!form) return
    setBannerBusy(true)
    setBannerNote('')
    try {
      await saveSettings({ bannerText: form.bannerText ?? '', bannerColor: form.bannerColor ?? '', bannerWhenEmbedded: !!form.bannerWhenEmbedded })
      setBannerNote('Saved.')
    } catch (e) { setBannerNote(String((e as Error).message ?? e)) } finally { setBannerBusy(false) }
  }
  const [ragEp, setRagEp] = useState<{ state: string; name: string | null; url: string | null; detail: string | null } | null>(null)
  const [ragCalls, setRagCalls] = useState<RagCall[]>([])
  const refreshRagCalls = () => fetch('/api/rag/calls').then(r => r.json()).then(d => setRagCalls(d.calls ?? [])).catch(() => {})

  const [ragEpError, setRagEpError] = useState<string | null>(null)
  type RagEp = { state: string; name: string | null; url: string | null; detail: string | null }
  // An error answer is shown as such; the last good state is not kept, since
  // it may no longer hold.
  const takeRagEp = (p: Promise<Response>) => p.then(r => okJSON<RagEp>(r))
    .then(d => { setRagEp(d); setRagEpError(null) })
    .catch((e: Error) => { setRagEp(null); setRagEpError(e.message) })
  const refreshRagEp = () => takeRagEp(fetch('/api/rag/endpoint'))
  const ragEpAction = async (verb: 'start' | 'stop') => {
    await takeRagEp(fetch(`/api/rag/endpoint/${verb}`, { method: 'POST' }))
    setTimeout(refreshRagEp, 2500)
    setTimeout(refreshRagEp, 8000)
  }

  // The setup wizard saves some of the same settings; once it closes, the
  // form starts again from what is saved, so Save settings cannot put back
  // what the wizard changed.
  const loadForm = () => {
    setTheme(null)
    fetch('/api/settings').then(r => r.json())
      .then(d => { setForm(d.effective); setDeploymentCred(d.deploymentCredential ?? null); setGufiLogFile(d.gufiLogFile ?? null); setClaudeCode(d.claudeCode ?? null); setLocked(d.locked || undefined); setToolsApproved(d.customToolsApproved !== false); setToolsOff(d.customToolsOff === true) })
      .catch(() => setNote('Could not load settings.'))
    fetch('/api/setup/status').then(r => r.json()).then(d => { setSetupAvailable(!!d?.standalone); setBranding(d?.branding ?? null) })
      .catch(() => {}).finally(() => setSetupKnown(true))
    fetch('/api/me/model-key').then(r => r.json()).then(setMe).catch(() => {})
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

  // What only the assistant's settings show, read while AI features are on
  // and again when they are switched on. Attached MCP servers are contacted
  // to report their state, so with AI off they are left alone.
  const aiReads = (!!form || cfg.loaded) && aiEnabled
  useEffect(() => {
    if (!aiReads) return
    refreshModels()
    fetch('/api/settings/mcp-servers').then(r => r.json()).then(d => setMcpStatus(d.servers ?? [])).catch(() => {})
    fetch('/api/chat/tools').then(r => r.json())
      .then(d => setCatalog(d.tools ?? []))
      .catch(() => {})
    fetch('/api/extensions').then(r => r.json()).then(setExt).catch(() => {})
    refreshRagEp()
    refreshRagCalls()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [aiReads])

  useEffect(() => {
    loadForm()
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

  const save = async (where: string, reload = true) => {
    if (!form) return
    setBusy(true)
    setSavedAt(where)
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

  const sections = settingsSections({ authEnabled: !!me?.authEnabled, platform: !!cfg.platform, single: !!me?.single, aiEnabled, transfers: hasTransfers })

  const mcpJson = JSON.stringify({ mcpServers: { 'studio-kb': { type: 'http', url: `${location.origin}/api/mcp`, ...(me?.authEnabled ? { headers: { Authorization: 'Bearer <platform token>' } } : {}) } } }, null, 2)
  // Each row reports its own save; the others wait while one runs.
  const saveRow = (where: string, reload = true) => {
    const mine = savedAt === where
    return (
      <div className="query-actions">
        <Button variant="primary" onClick={() => void save(where, reload)} loading={busy && mine} disabled={busy && !mine}>{busy && mine ? 'Saving…' : 'Save settings'}</Button>
        {note && mine && <span className="muted" role="status">{note}</span>}
      </div>
    )
  }

  const ragEpTone: CalloutTone = ragEp?.state === 'running' ? 'success' : ragEp?.state === 'error' || ragEpError ? 'error' : 'default'
  const surfaces: SelectItem<string>[] = Object.entries(SURFACES).map(([k, v]) => ({ value: k, label: v.label }))

  return (
    <div className="settings-view">
      <div className="help-docs card">
        <SectionStrip navRef={navRef}>
          <div className="help-nav-head"><span>Settings</span></div>
          {/* Grouped like the user guide's rail: a group opens when it holds
              the section being edited, and a header click opens or closes it. */}
          {sections.reduce<{ name: string | null; items: typeof sections }[]>((acc, sec) => {
            const last = acc[acc.length - 1]
            if (last && last.name === sec.group && sec.group !== null) last.items.push(sec); else acc.push({ name: sec.group, items: [sec] })
            return acc
          }, []).map((g, i) => {
            const rows = g.items.map(s => (
              <button key={s.id} className={section === s.id ? 'active' : ''} onClick={() => goSection(s.id === 'indexing' ? INDEXING_TABS[0].value : s.id)}>
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
        </SectionStrip>
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
                  <p className="muted view-sub">Saved as you change them, apart from the classification banner, which has its own Save.</p>
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
                  <Locked why={locked}>
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

                  <h2 className="ix-h2 settings-group-head" id="settings-branding">Branding</h2>
                  <div className="settings-grid">
                    <Input label="Product name" value={form.appName}
                      onChange={e => saveBrand({ appName: e.target.value })}
                      onBlur={() => saveBrand({}, 0)} onKeyDown={e => { if (e.key === 'Enter') saveBrand({}, 0) }} />
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
                          onChange={e => saveBrand({ iconUrl: e.target.value })} onBlur={() => saveBrand({}, 0)} />
                        <Input label="Brand icon for dark mode (optional)" value={form.iconUrlDark} placeholder="leave empty to use the same image"
                          onChange={e => saveBrand({ iconUrlDark: e.target.value })} onBlur={() => saveBrand({}, 0)} />
                      </>
                    )}
                  </div>
                  {brandNote && <Callout tone="error" live>{brandNote}</Callout>}
                  <div className="banner-field settings-field">
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
                      <Checkbox disabled={!!locked} checked={!!form.bannerWhenEmbedded}
                        onCheckedChange={v => setForm({ ...form, bannerWhenEmbedded: v })} />
                      <span>Show it inside the platform frame too, which already draws its own</span>
                    </label>
                  </div>
                  <div className="query-actions">
                    <Button variant="primary" onClick={() => void saveBanner()} loading={bannerBusy}>{bannerBusy ? 'Saving…' : 'Save banner'}</Button>
                    {bannerNote && <span className="muted">{bannerNote}</span>}
                  </div>
                  </Locked>
                </TabPanel>

                <TabPanel value="sidebar">
                  <p className="muted view-sub">Pages the sidebar shows; Settings and Help always show. Saved as you switch.</p>
                  <Locked why={locked}>
                  <SidebarSwitches disabled={!!locked} value={form.sections} platform={!!cfg.platform} aiEnabled={aiEnabled} onChange={next => {
                    setForm({ ...form, sections: next })
                    setSectionsNote('')
                    saveSections(next).catch(e => setSectionsNote(String((e as Error).message ?? e)))
                  }} />
                  </Locked>
                  {sectionsNote && <Callout tone="error" live>{sectionsNote}</Callout>}
                  <Field group className="settings-field" label="Width"
                    description={(navWidthPreset(cfg.navWidth)
                      ? 'The sidebar\'s width when it is not collapsed. Its right edge can also be dragged; a double click there returns it to Default.'
                      : `Dragged to ${cfg.navWidth} pixels. Pick a width here, or double click the sidebar's right edge for Default.`)
                      + (cfg.settingsLocked ? ' Kept in this browser, for you alone.' : '')}>
                    <SegmentedControl options={NAV_WIDTH_OPTIONS} value={navWidthPreset(cfg.navWidth) ?? ''} aria-label="Sidebar width"
                      onValueChange={v => {
                        const px = NAV_WIDTHS[v as keyof typeof NAV_WIDTHS]
                        if (!px) return
                        setSectionsNote('')
                        saveNavWidth(px).catch(e => setSectionsNote(String((e as Error).message ?? e)))
                      }} />
                  </Field>
                </TabPanel>

                <TabPanel value="ai">
                  <Locked why={locked}>
                  {aiEnabled ? (
                    <p className="muted view-sub">
                      Off takes Assistant and Agents out of the sidebar, hides the settings only the assistant uses, and sends nothing
                      to a model; search, the library, query and statistics work as before. They can be switched back on here at
                      any time. Saved as you switch.
                      {(me?.single || me?.authEnabled) && <> Where models come from is under <a href="#view=settings:access">Model access</a>, and the accounts behind them under <a href="#view=settings:accounts">Accounts</a>.</>}
                    </p>
                  ) : (
                    <p className="muted view-sub">
                      Off. On adds Assistant and Agents, the assistant&apos;s settings, label suggestions and image descriptions.
                    </p>
                  )}
                  <div className="access-switch">
                    <Switch disabled={!!locked} checked={form.aiEnabled !== false} aria-label="AI features" onLabel="On" offLabel="Off"
                      onCheckedChange={v => {
                        setSectionsNote('')
                        saveAiEnabled(v, form.sections)
                          .then(sections => setForm(f => f && { ...f, aiEnabled: v, sections }))
                          .catch(e => setSectionsNote(String((e as Error).message ?? e)))
                      }} />
                    <span>Assistant, Agents, and other features that call a model</span>
                  </div>
                  {sectionsNote && <Callout tone="error" live>{sectionsNote}</Callout>}
                  {aiEnabled && (<>
                  {me?.authEnabled && (
                    <label className="key-persist">
                      <Checkbox disabled={!!locked} checked={form.requirePersonalKey}
                        onCheckedChange={v => setForm({ ...form, requirePersonalKey: v })} />
                      Require each user to add their own model credential (chat and models refuse the endpoint you set for everyone and the model served beside the Studio; browsing, search, and adding material stay open to everyone)
                    </label>
                  )}
                  {me?.authEnabled && (
                    <label className="key-persist">
                      <Checkbox disabled={!!locked} checked={form.chatSharedHistory}
                        onCheckedChange={v => setForm({ ...form, chatSharedHistory: v })} />
                      Allow users to see each other's chat sessions in the chat interface (off unless turned on here or by the deployment; the rail still defaults each user to their own, with a toggle to show all; off removes that option entirely; chats someone saved to the main library stay there either way)
                    </label>
                  )}
                  {me?.authEnabled && (
                    <p className="muted" data-testid="shared-history-note">Answers can quote files from people's own libraries, so turning this on shares those quotes with everyone who signs in.</p>
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
                      </> : form.visionModel && form.requirePersonalKey ? <>
                        Captions images as they are indexed, so they can be found by what they show. A person's own
                        bucket library is captioned with their own ACTIVATE sign-in, and not at all without one.
                      </> : 'Captions images as they are indexed, so they can be found by what they show.'}>
                      <ModelInput
                        value={form.visionModel}
                        models={pickableModels}
                        emptyLabel="disabled (no image captioning)"
                        onChange={v => setForm({ ...form, visionModel: v })}
                      />
                    </Field>
                  </div>
                  {saveRow('vision', false)}
                  </>)}
                  </Locked>
                </TabPanel>

                <TabPanel value="updates">
                  <Locked why={locked}>
                    <UpdatesSection locked={!!locked} onPolicySaved={updates => setForm(f => f && ({ ...f, updates } as Effective))} />
                  </Locked>
                </TabPanel>

                <TabPanel value="server">
                  <ServerSection />
                </TabPanel>
              </Tabs>
            </>
          )}

          {section === 'indexing' && (
            <>
              <h1>Indexing</h1>
              <Tabs label="Indexing settings" items={[...INDEXING_TABS]} value={tab} onValueChange={v => goSection(v)} className="settings-tabs">
                <TabPanel value="libraries">
                  <LibrariesSection heading={false} />
                </TabPanel>

                <TabPanel value="sync-search">
                  <Locked why={locked}>
                  <div className="ix-cards">
                  <IndexGroup title="Search" help="How a search covers several libraries, and how long each library may take.">
                    <Field label="Search across libraries"
                      description="Both return the same matches. gufi_vt answers its libraries one after another; Query uses it either way.">
                      <Select items={SEARCH_ENGINES} value={form.searchEngine ?? 'parallel'} onValueChange={v => v && setForm({ ...form, searchEngine: v })} />
                    </Field>
                    <Field label="Search time limit"
                      description="How long each library's search runs before it is stopped and left out of the results. Very large indexes may need longer. Assistant and the Search page both use it, and each can also be stopped by hand.">
                      <Select items={searchTimeLimits(form.searchTimeLimitSec)} value={form.searchTimeLimitSec ?? 20}
                        onValueChange={v => typeof v === 'number' && setForm({ ...form, searchTimeLimitSec: v })} />
                    </Field>
                  </IndexGroup>
                  <IndexGroup title="Sync and history" help="How often the background sync looks for changes, and how often the corpus history records a point.">
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
                  </IndexGroup>
                  </div>
                  {saveRow('history', false)}
                  </Locked>
                </TabPanel>

                <TabPanel value="building">
                  {/* How the main folder's index is built: threads, depth,
                      readers, embeddings (IndexingSection). Saves as it changes. */}
                  <IndexBuildSettings visionModel={form.visionModel} deploymentCred={deploymentCred} />
                </TabPanel>

                <TabPanel value="storage">
                  <Locked why={locked}>
                  <div className="ix-cards">
                  <IndexStorageSettings />
                  <IndexGroup title="GUFI command log" help="A record of the commands run to read an index, for finding a slow or failing search.">
                    <div>
                      <label className="key-persist">
                        <Checkbox disabled={!!locked} checked={!!form.gufiLog} onCheckedChange={v => setForm({ ...form, gufiLog: v })} />
                        Log GUFI commands: every command run to read an index, with when it ran, what asked for it, how long it took, how it ended, and the end of its error output
                      </label>
                      {form.gufiLog && gufiLogFile && (
                        <p className="muted view-sub gufi-log-path">
                          One JSON object per line in <code className="ix-log-path">{gufiLogFile.path}</code>
                          {gufiLogFile.bytes > 0 ? ` (${formatBytes(gufiLogFile.bytes)})` : ''}, one previous file kept past 10 MB.
                        </p>
                      )}
                    </div>
                  </IndexGroup>
                  </div>
                  {saveRow('gufi-log', false)}
                  </Locked>
                </TabPanel>
              </Tabs>
            </>
          )}

          {section === 'accounts' && <AccountsSection aiEnabled={aiEnabled} />}
          {section === 'access' && <ModelAccessSection requirePersonalKey={form.requirePersonalKey} />}

          {section === 'tools' && (
            <>
            <Locked why={locked}>
              <h1>Assistant tools</h1>
              <p className="muted view-sub">
                Everything the assistant can call. Built-in tools are part of the server; unchecking one hides it from the model, and clicking a name shows what it actually calls plus its exact specification, so you can copy the shape into a custom tool or extension file with your own changes. Custom tools are commands you define here (saved with these settings, not as files); each appears to the assistant as a callable tool and runs on this server with its output returned to the conversation.
              </p>
              <div className="settings-grid">
                <Field label="Most tool rounds per reply"
                  description="Each round is one model call and the tools it asks for. At the limit the assistant answers from what it has and says it stopped.">
                  <NumberInput min={5} max={200} value={form.maxToolRounds}
                    onValueChange={v => setForm({ ...form, maxToolRounds: v ?? 0 })} />
                </Field>
              </div>
              </Locked>
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
                          <Checkbox disabled={!!locked}
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
              <Locked why={locked} quiet>
              <div className="tool-group">Custom tools</div>
              {toolsOff ? (
                <p className="muted">
                  Custom tools are off on this Studio (STUDIO_CUSTOM_TOOLS=0). None is offered to the assistant or run, and
                  any saved here are kept as they are.
                </p>
              ) : <>
              {!toolsApproved && form.customTools.length > 0 && (
                <Callout tone="warning">
                  These custom tools do not run yet: they were not saved by one of this Studio's administrators. They run
                  once an administrator saves these settings.
                </Callout>
              )}
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
              </>}
              {saveRow('tools')}
            </Locked>
            </>
          )}

          {section === 'rag' && (
            <Locked why={locked}>
              <h1>External access</h1>
              {/* With AI features off, only MCP is left: the client brings its
                  own model. The grounded model, delegation and attached
                  servers all run this Studio's assistant. */}
              {aiEnabled ? <>
              <p className="muted view-sub">Two ways to use this knowledge base from outside. They are independent, and a deployment can serve either or both.</p>
              <DescriptionList className="access-compare">
                <DescriptionListItem label="Knowledge tools (MCP)">
                  The client brings its own model and calls the corpus tools directly. Answers come from the caller's model, grounded by our search. Suits pw code, other agentic clients, and IDE assistants.
                </DescriptionListItem>
                <DescriptionListItem label="Grounded model (OpenAI-compatible)">
                  This deployment answers as a model, with retrieval built in. Suits any client that can only point at an OpenAI base URL, and it is what the platform model catalog publishes.
                </DescriptionListItem>
              </DescriptionList>
              </> : (
                <p className="muted view-sub">Other tools can use this knowledge base over MCP, with their own model. The grounded model and the assistant&apos;s settings here return with AI features.</p>
              )}
              <div className="tool-group">Knowledge tools over MCP (your model, our tools)</div>
              <p className="muted view-sub">
                <code>{location.origin}/api/mcp</code><CopyButton text={`${location.origin}/api/mcp`} label="MCP address" /> serves
                this knowledge base over the Model Context Protocol: the client brings its own model and calls the corpus tools
                directly (search, read, list, query, labels, docs). Read-only; adding material, labels, and platform actions stay
                with {aiEnabled ? 'this assistant' : 'the Studio'}. {me?.authEnabled
                  ? 'This deployment requires sign-in, so clients send a platform token as the bearer.'
                  : 'This deployment is open, so no header is needed.'}
              </p>
              <div className="access-switch">
                <Switch disabled={!!locked} checked={form.mcpEnabled} onCheckedChange={v => setForm({ ...form, mcpEnabled: v })} onLabel="On" offLabel="Off" aria-label="Serve MCP" />
                <span>Serve MCP at /api/mcp.{aiEnabled && ' Individual tools can be disabled under Assistant tools.'}</span>
              </div>
              <p className="field-label">pw code</p>
              <CopyCodeBlock className="mcp-snippet" text={`pw code mcp add --transport http studio-kb ${location.origin}/api/mcp`} label="pw code command">
                pw code mcp add --transport http studio-kb {location.origin}/api/mcp
              </CopyCodeBlock>
              <p className="muted key-note">pw code signs its platform requests with your existing CLI login, so no token goes in the command.</p>
              {aiEnabled && <>
              {/* pw code needs ACTIVATE: a Studio run on its own answers with the
                  Studio's own and shows nothing of it, unless set otherwise. */}
              {(cfg.platform || (form.chatEngine ?? 'auto') !== 'auto') && <>
              <div className="tool-group">What answers in Assistant</div>
              <p className="muted view-sub">
                pw code is the ACTIVATE agent harness, and answers by default under ACTIVATE. A chat it answers
                runs in a pw code session of the person chatting, which uses this Studio's tools through its MCP
                server (search and statistics from the index, library files, workflows), works in files, runs
                commands they approve, and shows in PW Code on the web. On a Studio several people use, each session
                runs as its person through their own ACTIVATE sign-in. The Studio answers with its own
                tools and models, and is what answers without ACTIVATE. People with ACTIVATE can switch either way in
                the chat bar.
              </p>
              <div className="settings-grid">
                <Field label="Engine">
                  <Select items={CHAT_ENGINES} value={form.chatEngine ?? 'auto'} onValueChange={v => v && setForm({ ...form, chatEngine: v })} />
                </Field>
                <Field label="pw code model" description="The model new pw code chats start with. Empty uses the pw code default.">
                  <Input value={form.pwcodeModel ?? ''} placeholder="pw code default" onChange={e => setForm({ ...form, pwcodeModel: e.target.value })} />
                </Field>
                <Field label="pw code permissions" description="What new pw code chats may do without asking. Studio tools that only read never ask.">
                  <Select items={PWCODE_MODES} value={form.pwcodePermissionMode ?? ''} onValueChange={v => setForm({ ...form, pwcodePermissionMode: v ?? '' })} />
                </Field>
                <Field label="Claude Code accounts"
                  description={<>
                    Whether pw code may run on Anthropic's models through Claude Code accounts here. Automatic is on for a
                    Studio of one, and off where people sign in or ACTIVATE deployed the Studio, except for someone whose pw
                    code already had accounts. Off, nothing in the Studio offers them and the server refuses a session on
                    one.{claudeCode ? ` Now ${claudeCode.on ? 'on' : 'off'} for you.` : ''}
                    {claudeCode?.locked && <> {claudeCode.locked}</>}
                  </>}>
                  <Select items={CLAUDE_CODE} value={form.claudeCodeAccounts ?? 'auto'} disabled={!!claudeCode?.locked}
                    aria-label="Claude Code accounts"
                    onValueChange={v => v && setForm({ ...form, claudeCodeAccounts: v })} />
                </Field>
              </div>
              {saveRow('pwcode', false)}
              </>}

              <div className="tool-group">Delegation (agents working for the assistant)</div>
              <p className="muted view-sub">
                With this on, the assistant may hand a piece of work to pw code: a session in a library folder with the
                agent instructions it chooses written into that folder's <code>AGENTS.md</code>, and a goal it works
                toward. It shows on the Code page, and what it writes is indexed when it finishes. Standing agents on
                the Agents tab's Fleet page need it on too.
              </p>
              <p className="muted view-sub">
                Worth knowing before enabling: an agent's session runs until its goal is met or its turn limit is
                reached, and uses tokens the whole time, so delegation suits work that stands on its own rather than
                work you would steer at every step.
              </p>
              <div className="access-switch">
                <Switch disabled={!!locked} checked={form.delegationEnabled} onCheckedChange={v => setForm({ ...form, delegationEnabled: v })} onLabel="On" offLabel="Off" aria-label="Delegate to pw code agents" />
                <span>Let the assistant delegate work to pw code agents</span>
              </div>
              {saveRow('delegation', false)}

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
                        ? <StatusLabel variant="warning">{st.error}</StatusLabel>
                        : <StatusLabel variant="success">{st.tools} tools</StatusLabel>)
                        : <span className="muted">save to connect</span>}
                    </span>
                  </div>
                )
              })}
              <div className="query-actions">
                <Button variant="secondary" onClick={() => setForm({ ...form, mcpServers: [...(form.mcpServers ?? []), { name: '', url: '', header: '' }] })}>Attach a server</Button>
              </div>
              {saveRow('mcp-servers', false)}
              </>}

              <p className="field-label">Other MCP clients (settings JSON)</p>
              <CopyCodeBlock className="mcp-snippet" text={mcpJson} label="MCP settings">{mcpJson}</CopyCodeBlock>
              {saveRow('mcp-clients', false)}

              {aiEnabled && <>

              <div className="tool-group">Grounded model (OpenAI-compatible /v1)</div>
              <div className="access-switch">
                <Switch disabled={!!locked} checked={form.ragProxyEnabled} onCheckedChange={v => setForm({ ...form, ragProxyEnabled: v })} onLabel="On" offLabel="Off" aria-label="Serve the grounded model" />
                <span>Serve the grounded model at /v1.</span>
              </div>
              {!form.ragProxyEnabled && (
                <p className="muted view-sub">
                  Off: /v1 refuses calls, and any platform registration is taken down with it. Turn it on to configure the models, keys, and platform registration.
                </p>
              )}
              {!form.ragProxyEnabled && saveRow('grounded', false)}
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
                <Checkbox disabled={!!locked} checked={form.ragAdvertiseAgentModel}
                  onCheckedChange={v => setForm({ ...form, ragAdvertiseAgentModel: v })} />
                Advertise <code>studio-agent</code> in model listings
              </label>
              <p className="muted key-note toggle-note">
                Runs the full assistant pipeline on the server (system prompt, search, read, query, labels) and returns a finished cited answer. Slower, several model calls per question. The right choice inside tool-calling harnesses such as pw code.
              </p>
              <label className="key-persist">
                <Checkbox disabled={!!locked} checked={form.ragAdvertiseRagModel}
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
                <Checkbox disabled={!!locked} checked={form.ragEndpointAutoStart}
                  onCheckedChange={v => setForm({ ...form, ragEndpointAutoStart: v })} />
                Register on the platform automatically at startup
              </label>
              {saveRow('rag', false)}
              <div className="tool-group">Platform registration</div>
              <p className="muted view-sub">
                Publishes this /v1 surface into the platform chat and AI provider catalog (via <code>pw endpoints run --openai</code> around a local forwarder), so users pick the corpus-grounded models like any other. The platform calls it without the caller's credential, so the forwarder adds the deployment credential to calls that bring none{form.requirePersonalKey
                  ? <>; with each user required to use their own model credential it adds none, and those calls are refused (a caller's own key still works)</>
                  : null}. Requires an authenticated pw CLI on the host.
              </p>
              <Callout className="rag-banner" tone={ragEpTone}
                title={ragEpError ? 'Registration status unavailable' :
                  ragEp?.state === 'running' ? 'Connected: published in the platform model catalog' :
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
                {ragEpError && <p className="muted key-note" role="alert">{ragEpError}</p>}
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
              </>}
            </Locked>
          )}

          {section === 'previews' && (
            <Locked why={locked}>
              <h1>Feature previews</h1>
              <p className="muted view-sub">
                Capabilities that work end to end but are still being shaped. Each is off until switched on here, and each
                shows its control in the interface only when it is on and configured.
              </p>
              <div className="access-switch">
                <Switch disabled={!!locked} checked={form.voiceEnabled} onCheckedChange={v => setForm({ ...form, voiceEnabled: v })} onLabel="On" offLabel="Off" aria-label="Voice conversations" />
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
              {saveRow('previews')}
            </Locked>
          )}

          {section === 'workflows' && <WorkflowsSection />}
          {section === 'transfers' && (hasTransfers ? <TransfersSettingsSection active={active} /> : <p className="muted">This server has no transfers.</p>)}
          {section === 'ext' && (
            <>
              <h1>Extensions</h1>
              <p className="muted view-sub">
                File-based extensions load from <code>{ext?.dir ?? 'the extensions directory beside the index'}</code> and take effect without a restart. <code>tools/*.json</code> files (name, description, command) become chat tools like the custom tools above. <code>skills/*.md</code> files are instruction sets the assistant loads on demand through the use_skill tool, by name or when a task matches their description. <code>agents/default.md</code> is appended to the system prompt as this deployment's standing instructions.
              </p>
              <div className="ext-cols">
                <div>
                  <div className="tool-group">Tool files ({ext?.tools.length ?? 0})</div>
                  {toolsOff && <p className="muted">Tool files do not run on this Studio: custom tools are off (STUDIO_CUSTOM_TOOLS=0).</p>}
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
