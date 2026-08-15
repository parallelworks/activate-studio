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
  bannerColor: '#2e6b2e',
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
      bannerText: c.bannerText, bannerColor: c.bannerColor, bannerWhenEmbedded: c.bannerWhenEmbedded,
    }))
  } catch { /* storage unavailable */ }
}

export function useAppConfig(): AppConfig {
  const [cfg, setCfg] = useState<AppConfig>(() => {
    if (cached) return cached
    const brand = rememberedBrand()
    return { ...DEFAULTS, ...brand, brandRemembered: !!brand.appName }
  })
  useEffect(() => {
    if (cached) return
    fetch('/api/config')
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
          loaded: true,
        }
        rememberBrand(cached)
        setCfg(cached)
      })
      .catch(() => setCfg({ ...DEFAULTS, loaded: true }))
  }, [])
  return cfg
}
