import type { PublicLibrary } from './api'
import { useEffect, useState } from 'react'

export interface AppConfig {
  appName: string
  iconUrl: string | null
  iconUrlDark: string | null
  faviconUrl: string | null
  faviconUrlDark: string | null
  theme: 'light' | 'dark'
  accent: string
  surface: string
  /** The left navigation's width in pixels, when not collapsed. */
  navWidth: number
  kbLabel: string
  suggestedPrompts: string[]
  /** Classification banner across the top; empty means none. */
  bannerText: string
  bannerColor: string
  /** Draw it even inside the platform frame, which shows its own. */
  bannerWhenEmbedded: boolean
  features?: { voice?: { enabled: boolean; url: string } }
  /** Every mounted library, primary first. */
  libraries?: PublicLibrary[]
  /** Which sections the app shows; unset means all. */
  sections?: string[]
  /** Running on the ACTIVATE platform, with a credential to reach it. */
  platform?: boolean
  /** False when AI features are off: no Chat, no Agents, no model line. */
  aiEnabled?: boolean
  /** How many workflows this Studio offers on its Workflows tab. */
  workflowCount?: number
  user: { id: string; username: string; name?: string }
  loaded: boolean
  /** True when the name and icon came from the previous visit, so the header
   *  can paint them before the configuration request returns. */
  brandRemembered?: boolean
}

const DEFAULTS: AppConfig = {
  appName: 'Studio',
  iconUrl: null,
  iconUrlDark: null,
  faviconUrl: null,
  faviconUrlDark: null,
  theme: 'light',
  accent: 'none',
  surface: 'warm',
  navWidth: 240,
  kbLabel: 'Knowledge base',
  suggestedPrompts: [
    'What is in this knowledge base?',
    'Summarize the most recently changed documents.',
    'What workflows are available in this account?',
    'Find everything related to a topic I name.',
  ],
  bannerText: '',
  bannerColor: '#24612e',
  bannerWhenEmbedded: false,
  user: { id: 'user', username: 'user' },
  loaded: false,
}

let cached: AppConfig | null = null

/** The branding half of the configuration, remembered between visits so the
 *  header paints the deployment's own name and icon on the first frame
 *  instead of showing a generic one and swapping a moment later. The fetched
 *  configuration still wins; this only decides what the first frame says. */
const BRAND_KEY = 'ade-brand'

function rememberedBrand(): Partial<AppConfig> {
  try {
    const raw = localStorage.getItem(BRAND_KEY)
    return raw ? JSON.parse(raw) as Partial<AppConfig> : {}
  } catch { return {} }
}

function rememberBrand(c: AppConfig): void {
  try {
    localStorage.setItem(BRAND_KEY, JSON.stringify({
      appName: c.appName, iconUrl: c.iconUrl, iconUrlDark: c.iconUrlDark, kbLabel: c.kbLabel,
      faviconUrl: c.faviconUrl, faviconUrlDark: c.faviconUrlDark, navWidth: c.navWidth, aiEnabled: c.aiEnabled,
      bannerText: c.bannerText, bannerColor: c.bannerColor, bannerWhenEmbedded: c.bannerWhenEmbedded,
    }))
  } catch { /* storage unavailable */ }
}

/** Set the tab icon from the previous visit, before React renders. The
 *  configuration request takes a moment, and until it returned the browser
 *  showed the bundled default mark and then swapped to the deployment's,
 *  which reads as the tab flickering on every load. */
export function applyRememberedFavicon(): void {
  try {
    const brand = rememberedBrand()
    const stored = localStorage.getItem('ade-theme')
    const dark = stored === 'dark' || (stored !== 'light' && matchMedia('(prefers-color-scheme: dark)').matches)
    const href = (dark ? brand.faviconUrlDark ?? brand.faviconUrl : brand.faviconUrl) ?? null
    if (!href) return
    let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    if (!link) {
      link = document.createElement('link')
      link.rel = 'icon'
      document.head.appendChild(link)
    }
    link.href = href
  } catch { /* storage unavailable */ }
}

export function useAppConfig(): AppConfig {
  const [cfg, setCfg] = useState<AppConfig>(() => {
    if (cached) return cached
    const brand = rememberedBrand()
    return { ...DEFAULTS, ...brand, brandRemembered: !!brand.appName }
  })
  useEffect(() => {
    const load = () => fetch('/api/config')
      .then(r => r.json())
      .then((d: Partial<AppConfig>) => {
        cached = {
          appName: d.appName || DEFAULTS.appName,
          iconUrl: d.iconUrl ?? null,
          iconUrlDark: d.iconUrlDark ?? null,
          faviconUrl: d.faviconUrl ?? null,
          faviconUrlDark: d.faviconUrlDark ?? null,
          theme: d.theme === 'dark' ? 'dark' : 'light',
          accent: d.accent || 'none',
          surface: d.surface || 'warm',
          navWidth: clampNavWidth(d.navWidth),
          kbLabel: d.kbLabel || DEFAULTS.kbLabel,
          suggestedPrompts: d.suggestedPrompts?.length ? d.suggestedPrompts : DEFAULTS.suggestedPrompts,
          bannerText: d.bannerText ?? '',
          bannerColor: d.bannerColor || DEFAULTS.bannerColor,
          bannerWhenEmbedded: !!d.bannerWhenEmbedded,
          user: d.user?.id ? (d.user as AppConfig['user']) : DEFAULTS.user,
          features: d.features ?? undefined,
          libraries: Array.isArray(d.libraries) ? d.libraries : undefined,
          sections: Array.isArray(d.sections) && d.sections.length ? d.sections : undefined,
          platform: !!d.platform,
          aiEnabled: d.aiEnabled !== false,
          workflowCount: typeof d.workflowCount === 'number' ? d.workflowCount : 0,
          loaded: true,
        }
        rememberBrand(cached)
        setCfg(cached)
      })
      .catch(() => setCfg({ ...DEFAULTS, loaded: true }))
    if (!cached) void load()
    // A change the server knows about (a library that finished building)
    // asks every view for the configuration again. So does a change of model
    // access: whether the Workflows page shows depends on a platform
    // credential, and without this it appeared only after a reload.
    const reload = () => { void load() }
    window.addEventListener('ade:config-changed', reload)
    window.addEventListener('ade:model-access-changed', reload)
    return () => {
      window.removeEventListener('ade:config-changed', reload)
      window.removeEventListener('ade:model-access-changed', reload)
    }
  }, [])
  return cfg
}

/** The left navigation's widths: the Width choice's three, and the range a
 *  drag of its edge keeps to. Default is the width it had before it could
 *  be changed. */
export const NAV_WIDTHS = { narrow: 200, default: 240, wide: 320 } as const
export const NAV_WIDTH_MIN = NAV_WIDTHS.narrow
export const NAV_WIDTH_MAX = 400

/** A saved width as a whole number of pixels in range, Default otherwise. */
export function clampNavWidth(v: unknown): number {
  const n = typeof v === 'number' ? v : Number(v)
  if (v === undefined || v === null || v === '' || !Number.isFinite(n)) return NAV_WIDTHS.default
  return Math.round(Math.min(NAV_WIDTH_MAX, Math.max(NAV_WIDTH_MIN, n)))
}

/** Set the navigation's width: the --nav-w variable the sidebar's CSS reads. */
export function applyNavWidth(px: number): void {
  document.documentElement.style.setProperty('--nav-w', `${clampNavWidth(px)}px`)
}

/** The width from the previous visit, before React renders, so the sidebar
 *  does not jump once the configuration arrives. */
export function applyRememberedNavWidth(): void {
  const w = rememberedBrand().navWidth
  if (typeof w === 'number') applyNavWidth(w)
}

/** Save the navigation's width, applied at once. */
export async function saveNavWidth(px: number): Promise<void> {
  const w = clampNavWidth(px)
  applyNavWidth(w)
  if (cached) cached = { ...cached, navWidth: w }
  await saveSettings({ navWidth: w })
}

/** The configuration as last read, without asking for it: for a label in a
 *  component that should not start a request of its own. */
export function knownAppConfig(): { aiEnabled?: boolean } { return cached ?? rememberedBrand() }

/** Asks the app to fetch its configuration again, as after a library is added. */
export function reloadAppConfig(): void { window.dispatchEvent(new Event('ade:config-changed')) }

/** Sidebar pages that can be switched off, in sidebar order. */
export const SECTION_SWITCHES: { id: string; label: string }[] = [
  { id: 'chat', label: 'Chat' },
  { id: 'library', label: 'Library' },
  { id: 'search', label: 'Search' },
  { id: 'query', label: 'Query' },
  { id: 'overview', label: 'Stats' },
  { id: 'history', label: 'History' },
  { id: 'workflows', label: 'Workflows' },
  { id: 'agents', label: 'Agents' },
  { id: 'code', label: 'Code' },
]
/** Always reachable, whatever is switched off. */
export const ALWAYS_SHOWN = ['settings', 'help']
/** The pages that call a model, gone while AI features are off. */
export const AI_SECTIONS = ['chat', 'agents']

/** What Settings calls the section with the ACTIVATE credential: Model
 *  access, or ACTIVATE access while AI features are off and it holds only
 *  that credential. */
export function accessLabel(cfg: { aiEnabled?: boolean }): string { return aiOn(cfg) ? 'Model access' : 'ACTIVATE access' }

/** Whether AI features are on. Unset (before the configuration arrives,
 *  or from an older server) counts as on, as it always has. */
export function aiOn(cfg: { aiEnabled?: boolean }): boolean { return cfg.aiEnabled !== false }

/** Whether the AI pages may be mounted now: on as the configuration says,
 *  or, before it arrives, as the previous visit remembered it. A first
 *  visit waits for the configuration, so a Studio with AI features off
 *  never starts Chat or Agents and their requests. */
export function aiPagesMounted(cfg: { aiEnabled?: boolean; loaded: boolean }): boolean {
  return cfg.loaded ? aiOn(cfg) : cfg.aiEnabled === true
}

/** Whether a page shows: in the saved sections (none saved means all), or
 *  always shown. Workflows and Code list what is on the platform, so they need one,
 *  and Chat and Agents need AI features on. */
export function sectionAllowed(id: string, cfg: { sections?: string[]; platform?: boolean; aiEnabled?: boolean }): boolean {
  if ((id === 'workflows' || id === 'code') && !cfg.platform) return false
  if (AI_SECTIONS.includes(id) && cfg.aiEnabled === false) return false
  return !cfg.sections?.length || cfg.sections.includes(id) || ALWAYS_SHOWN.includes(id)
}

/** The page to show for a requested one: itself when allowed, otherwise the
 *  first allowed page in sidebar order. */
export function shownView<T extends string>(view: T, order: readonly T[], cfg: { sections?: string[]; platform?: boolean; aiEnabled?: boolean }): T {
  if (sectionAllowed(view, cfg)) return view
  return order.find(id => sectionAllowed(id, cfg)) ?? view
}

/** The sections setting for the pages switched on: empty when all are, so a
 *  page added in a later version shows too. */
export function sectionsSetting(shown: string[]): string[] {
  if (SECTION_SWITCHES.every(s => shown.includes(s.id))) return []
  return [...SECTION_SWITCHES.map(s => s.id).filter(id => shown.includes(id)), ...ALWAYS_SHOWN]
}

/** Save some settings and have the app pick them up. */
export async function saveSettings(patch: Record<string, unknown>): Promise<void> {
  const res = await fetch('/api/settings', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(patch),
  })
  if (!res.ok) throw new Error(((await res.json().catch(() => ({}))) as { error?: string }).error ?? `${res.status}`)
  reloadAppConfig()
}

/** Save which pages the sidebar shows, and have the app pick it up. */
export async function saveSections(sections: string[]): Promise<void> {
  await saveSettings({ sections })
}

/** Turn AI features on or off. Chat and Agents leave the sidebar's saved
 *  pages with it, and come back with it. */
export async function saveAiEnabled(on: boolean, sections: string[] | undefined): Promise<string[]> {
  const shown = SECTION_SWITCHES.map(s => s.id).filter(id => !AI_SECTIONS.includes(id) && (!sections?.length || sections.includes(id)))
  const next = sectionsSetting(on ? [...shown, ...AI_SECTIONS] : shown)
  await saveSettings({ aiEnabled: on, sections: next })
  return next
}
