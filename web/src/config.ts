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
  accent: 'navy',
  surface: 'neutral',
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
      faviconUrl: c.faviconUrl, faviconUrlDark: c.faviconUrlDark,
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
          accent: d.accent || 'navy',
          surface: d.surface || 'neutral',
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
          workflowCount: typeof d.workflowCount === 'number' ? d.workflowCount : 0,
          loaded: true,
        }
        rememberBrand(cached)
        setCfg(cached)
      })
      .catch(() => setCfg({ ...DEFAULTS, loaded: true }))
    if (!cached) void load()
    // A change the server knows about (a library that finished building)
    // asks every view for the configuration again.
    const reload = () => { void load() }
    window.addEventListener('ade:config-changed', reload)
    return () => window.removeEventListener('ade:config-changed', reload)
  }, [])
  return cfg
}

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
]
/** Always reachable, whatever is switched off. */
export const ALWAYS_SHOWN = ['settings', 'help']

/** Whether a page shows: in the saved sections (none saved means all), or
 *  always shown. Workflows lists platform workflows, so it also needs one. */
export function sectionAllowed(id: string, cfg: { sections?: string[]; platform?: boolean }): boolean {
  if (id === 'workflows' && !cfg.platform) return false
  return !cfg.sections?.length || cfg.sections.includes(id) || ALWAYS_SHOWN.includes(id)
}

/** The page to show for a requested one: itself when allowed, otherwise the
 *  first allowed page in sidebar order. */
export function shownView<T extends string>(view: T, order: readonly T[], cfg: { sections?: string[]; platform?: boolean }): T {
  if (sectionAllowed(view, cfg)) return view
  return order.find(id => sectionAllowed(id, cfg)) ?? view
}

/** The sections setting for the pages switched on: empty when all are, so a
 *  page added in a later version shows too. */
export function sectionsSetting(shown: string[]): string[] {
  if (SECTION_SWITCHES.every(s => shown.includes(s.id))) return []
  return [...SECTION_SWITCHES.map(s => s.id).filter(id => shown.includes(id)), ...ALWAYS_SHOWN]
}

/** Save which pages the sidebar shows, and have the app pick it up. */
export async function saveSections(sections: string[]): Promise<void> {
  const res = await fetch('/api/settings', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ sections }),
  })
  if (!res.ok) throw new Error(((await res.json().catch(() => ({}))) as { error?: string }).error ?? `${res.status}`)
  reloadAppConfig()
}
