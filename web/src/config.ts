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
  /** People sign in to this Studio. Without sign-in the main library is the
   *  person's own (My library); with it, the main library is the one they
   *  share, and My library is each person's folder on a system. */
  signIn?: boolean
  /** The person here has not signed in, on a Studio with sign-in. */
  signedOut?: boolean
  /** The person's kept ACTIVATE sign-in stopped working: what needs it asks
   *  them to sign in again. */
  signInStopped?: boolean
  /** The Studio's own sign-in page, where it has one. */
  signInUrl?: string
  /** Models answer a person only with an account of their own
   *  (REQUIRE_PERSONAL_KEY), so signing in with ACTIVATE comes first. */
  signInFirst?: boolean
  /** The Studio's settings are its administrators' (a Studio with sign-in,
   *  and this person is not one): preferences stay in this browser. */
  settingsLocked?: boolean
  /** The Code page lists this computer's own pw code sessions (no platform). */
  localCode?: boolean
  /** The person can set up accounts of their own here (Settings, Accounts),
   *  so a page that needs ACTIVATE shows, asking them to sign in, before
   *  they have; without it such a page is left out, as on a deployment that
   *  keeps no personal credentials. */
  accounts?: boolean
  /** False when AI features are off: no Assistant, no Agents, no model line. */
  aiEnabled?: boolean
  /** How many workflows this Studio offers on its Workflows tab. */
  workflowCount?: number
  /** The address links to items start with, where the Studio has a lasting
   *  one; empty, and the page's own is used. */
  linkBase?: string
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

/** Reads of the configuration under way. */
let reading = 0
/** Each read's number, in the order they were asked, and the newest whose
 *  answer was kept: an answer to an older read that arrives after a newer
 *  one (a read begun just before an ACTIVATE sign-in finished, answered
 *  after the read the sign-in asked for) is not kept over it. */
let asked = 0
let kept = 0

export function useAppConfig(): AppConfig {
  const [cfg, setCfg] = useState<AppConfig>(() => {
    if (cached) return cached
    const brand = rememberedBrand()
    return { ...DEFAULTS, ...brand, brandRemembered: !!brand.appName }
  })
  useEffect(() => {
    const load = () => { reading++; const n = ++asked; return fetch('/api/config')
      .then(r => r.json())
      .then((d: Partial<AppConfig>) => {
        if (n < kept) {
          // A newer answer is in: show it, not this one.
          if (cached) setCfg(cached)
          return
        }
        kept = n
        cached = {
          appName: d.appName || DEFAULTS.appName,
          iconUrl: d.iconUrl ?? null,
          iconUrlDark: d.iconUrlDark ?? null,
          faviconUrl: d.faviconUrl ?? null,
          faviconUrlDark: d.faviconUrlDark ?? null,
          theme: d.theme === 'dark' ? 'dark' : 'light',
          accent: d.accent || 'none',
          surface: d.surface || 'warm',
          navWidth: clampNavWidth((d.settingsLocked && ownNavWidth()) || d.navWidth),
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
          signIn: !!d.signIn,
          signedOut: !!d.signedOut,
          signInStopped: !!d.signInStopped,
          signInUrl: typeof d.signInUrl === 'string' ? d.signInUrl : '',
          signInFirst: !!d.signInFirst,
          settingsLocked: !!d.settingsLocked,
          localCode: !!d.localCode,
          accounts: !!d.accounts,
          aiEnabled: d.aiEnabled !== false,
          workflowCount: typeof d.workflowCount === 'number' ? d.workflowCount : 0,
          linkBase: typeof d.linkBase === 'string' ? d.linkBase : '',
          loaded: true,
        }
        rememberBrand(cached)
        setCfg(cached)
      })
      .catch(() => setCfg(n < kept && cached ? cached : { ...DEFAULTS, loaded: true }))
      .finally(() => { reading-- }) }
    // A part that mounts while it is read again (the chat page does, as a
    // sign-in changes) was not there for the event, and what is kept is old.
    if (!cached || reading > 0) void load()
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

/** Whether the viewer may change the main library, as the server's index
 *  actions on it (Sync now, Stop) ask: where people sign in it may be read
 *  only for them. */
export function mainWritable(cfg: Pick<AppConfig, 'libraries'>): boolean {
  const main = cfg.libraries?.find(l => l.primary)
  return !main || (main.writable !== false && !main.readOnlyWhy)
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

/** Where a person who may not change the Studio's settings keeps their own
 *  sidebar width: the saved navWidth is the Studio's, for everyone. */
const OWN_NAV_WIDTH = 'ade-nav-width'

/** This browser's own sidebar width, or 0. */
export function ownNavWidth(): number {
  try { return Number(localStorage.getItem(OWN_NAV_WIDTH)) || 0 } catch { return 0 }
}

/** Save the navigation's width, applied at once: for the Studio, or only in
 *  this browser for a person whose Studio's settings are its administrators'. */
export async function saveNavWidth(px: number): Promise<void> {
  const w = clampNavWidth(px)
  applyNavWidth(w)
  if (cached) cached = { ...cached, navWidth: w }
  if (cached?.settingsLocked) {
    try { localStorage.setItem(OWN_NAV_WIDTH, String(w)) } catch { /* storage unavailable */ }
    reloadAppConfig()
    return
  }
  await saveSettings({ navWidth: w })
}

/** The configuration as last read, without asking for it: for a label in a
 *  component that should not start a request of its own. */
export function knownAppConfig(): { aiEnabled?: boolean } { return cached ?? rememberedBrand() }

/** Asks the app to fetch its configuration again, as after a library is added. */
export function reloadAppConfig(): void { window.dispatchEvent(new Event('ade:config-changed')) }

/** Sidebar pages that can be switched off, in sidebar order. */
export const SECTION_SWITCHES: { id: string; label: string }[] = [
  { id: 'chat', label: 'Assistant' },
  { id: 'library', label: 'Library' },
  { id: 'transfers', label: 'Transfers' },
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

/** Whether AI features are on. Unset (before the configuration arrives,
 *  or from an older server) counts as on, as it always has. */
export function aiOn(cfg: { aiEnabled?: boolean }): boolean { return cfg.aiEnabled !== false }

/** Whether the AI pages may be mounted now: on as the configuration says,
 *  or, before it arrives, as the previous visit remembered it. A first
 *  visit waits for the configuration, so a Studio with AI features off
 *  never starts Assistant or Agents and their requests. */
export function aiPagesMounted(cfg: { aiEnabled?: boolean; loaded: boolean }): boolean {
  return cfg.loaded ? aiOn(cfg) : cfg.aiEnabled === true
}

type Gate = { sections?: string[]; platform?: boolean; localCode?: boolean; aiEnabled?: boolean; accounts?: boolean }

/** Whether the person has ACTIVATE here: the platform answers them with a
 *  credential of their own, or this computer's pw code has one. */
export const hasActivate = (cfg: Gate): boolean => !!cfg.platform || !!cfg.localCode

/** Whether a page shows: in the saved sections (none saved means all), or
 *  always shown. Workflows needs the platform, or Accounts to sign in from:
 *  without a credential it shows "Sign in with ACTIVATE to use this". Code is
 *  for people who have ACTIVATE, so without it there is no Code at all.
 *  Assistant and Agents need AI features on. */
export function sectionAllowed(id: string, cfg: Gate): boolean {
  // Notices is of whatever pages show, and is not a page to switch off.
  if (id === 'notices') return true
  if (id === 'workflows' && !cfg.platform && !cfg.accounts) return false
  // Code lists the platform's sessions, or this computer's own where the
  // Studio offers it to pw code with no platform.
  if (id === 'code' && !hasActivate(cfg)) return false
  if (AI_SECTIONS.includes(id) && cfg.aiEnabled === false) return false
  return !cfg.sections?.length || cfg.sections.includes(id) || ALWAYS_SHOWN.includes(id)
}

/** The page to show for a requested one: itself when allowed, otherwise the
 *  first allowed page in sidebar order. */
export function shownView<T extends string>(view: T, order: readonly T[], cfg: Gate): T {
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

/** Turn AI features on or off. Assistant and Agents leave the sidebar's saved
 *  pages with it, and come back with it. */
export async function saveAiEnabled(on: boolean, sections: string[] | undefined): Promise<string[]> {
  const shown = SECTION_SWITCHES.map(s => s.id).filter(id => !AI_SECTIONS.includes(id) && (!sections?.length || sections.includes(id)))
  const next = sectionsSetting(on ? [...shown, ...AI_SECTIONS] : shown)
  await saveSettings({ aiEnabled: on, sections: next })
  return next
}
