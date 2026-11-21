import { useEffect, useState } from 'react'

/**
 * The theme the page is actually painted in, live. App decides the
 * effective theme (explicit choice, system preference, deployment
 * default, or the theme an embed is told) and writes it to
 * document.documentElement.dataset.theme; everything that picks a
 * dark or light asset reads it from there. A one-time read at render is
 * how the empty-chat mark stayed on the old icon after a toggle, so this
 * subscribes to the attribute instead of sampling it.
 */
export function useEffectiveTheme(): 'dark' | 'light' {
  const read = () => (document.documentElement.dataset.theme === 'dark' ? 'dark' : 'light')
  const [theme, setTheme] = useState<'dark' | 'light'>(read)
  useEffect(() => {
    const obs = new MutationObserver(() => setTheme(read()))
    obs.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
    setTheme(read())
    return () => obs.disconnect()
  }, [])
  return theme
}

/** What a person picked for this browser: a theme of its own, or the
 *  system's (the device's preference, over the deployment's default). */
export type ThemeChoice = 'light' | 'dark' | 'system'

const CHOICE_KEY = 'ade-theme'
const CHOICE_EVENT = 'ade:theme-choice'

export function themeChoice(): ThemeChoice {
  try {
    const t = localStorage.getItem(CHOICE_KEY)
    return t === 'light' || t === 'dark' ? t : 'system'
  } catch { return 'system' }
}

/** The choice in effect: this browser's own, else Dark where the default is
 *  dark (which the device's setting does not change), else System. */
export function currentThemeChoice(defaultTheme: 'light' | 'dark'): ThemeChoice {
  const own = themeChoice()
  return own !== 'system' ? own : defaultTheme === 'dark' ? 'dark' : 'system'
}

/** Remember a choice for this browser and have the app paint in it at once. */
export function setThemeChoice(choice: ThemeChoice): void {
  try {
    if (choice === 'system') localStorage.removeItem(CHOICE_KEY)
    else localStorage.setItem(CHOICE_KEY, choice)
  } catch { /* applies to this page only */ }
  window.dispatchEvent(new CustomEvent<ThemeChoice>(CHOICE_EVENT, { detail: choice }))
}

/** Calls `fn` with each choice made through setThemeChoice. */
export function onThemeChoice(fn: (choice: ThemeChoice) => void): () => void {
  const on = (e: Event) => fn((e as CustomEvent<ThemeChoice>).detail)
  window.addEventListener(CHOICE_EVENT, on)
  return () => window.removeEventListener(CHOICE_EVENT, on)
}

/** The cookie the manifest reads for an installed app's splash
 *  (GROUND_COOKIE in server/src/routes.ts, GroundCookie in server-go). */
export const GROUND_COOKIE = 'ade_ground'

/** A CSS color as "#rrggbb", or null for one this cannot read or one that
 *  is not opaque. */
export function hexColor(color: string): string | null {
  const c = color.trim().toLowerCase()
  if (!c) return null
  if (/^#[0-9a-f]{6}$/.test(c)) return c
  const short = /^#([0-9a-f])([0-9a-f])([0-9a-f])$/.exec(c)
  if (short) return '#' + short[1] + short[1] + short[2] + short[2] + short[3] + short[3]
  const rgb = /^rgba?\(\s*(\d+(?:\.\d+)?)[\s,]+(\d+(?:\.\d+)?)[\s,]+(\d+(?:\.\d+)?)\s*(?:[,/]\s*([\d.]+%?)\s*)?\)$/.exec(c)
  if (rgb) {
    if (rgb[4] !== undefined && parseFloat(rgb[4]) < (rgb[4].endsWith('%') ? 100 : 1)) return null
    return '#' + [rgb[1], rgb[2], rgb[3]].map(v => Math.max(0, Math.min(255, Math.round(Number(v)))).toString(16).padStart(2, '0')).join('')
  }
  // Any other form (hsl, oklch, a name), through the canvas, which gives
  // an opaque color back as #rrggbb.
  try {
    const ctx = document.createElement('canvas').getContext('2d')
    if (!ctx) return null
    ctx.fillStyle = '#010203'
    ctx.fillStyle = c
    const out = String(ctx.fillStyle)
    return /^#[0-9a-f]{6}$/.test(out) && (out !== '#010203' || c === '#010203') ? out : null
  } catch { return null }
}

/**
 * Keeps the page's ground in a cookie for a year, so the splash an
 * installed app shows before its page paints is the color the person last
 * saw (the manifest's background_color) instead of the deployment's default
 * theme's. The manifest is fetched with credentials, so the cookie reaches
 * it behind the platform's session proxy too.
 */
export function rememberGround(color: string): void {
  const hex = hexColor(color)
  if (!hex) return
  const value = hex.slice(1)
  try {
    if (document.cookie.split(';').some(p => p.trim() === `${GROUND_COOKIE}=${value}`)) return
    document.cookie = `${GROUND_COOKIE}=${value}; Path=/; Max-Age=31536000; SameSite=Lax${location.protocol === 'https:' ? '; Secure' : ''}`
  } catch { /* cookies off: the splash keeps the default */ }
}
