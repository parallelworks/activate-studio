import { deriveTheme } from '@parallelworks/ui/theme'

/** Named accent schemes: [primary ink, hover ink, active pill] per theme.
 *  'navy' is the stylesheet default and applies no override. */
export interface Accent {
  label: string
  light: [string, string, string]
  dark: [string, string, string]
}

export const ACCENTS: Record<string, Accent> = {
  navy: { label: 'Navy', light: ['#06354f', '#0a4a6e', '#e1f2fb'], dark: ['#7fc0ec', '#9cd0f4', '#17395a'] },
  teal: { label: 'Teal', light: ['#0e5750', '#127066', '#dcf3f0'], dark: ['#6fd6cf', '#8ee2dc', '#123f3a'] },
  forest: { label: 'Forest', light: ['#1e5631', '#2a7443', '#e0f2e4'], dark: ['#7fd49a', '#9be0b1', '#14402a'] },
  burgundy: { label: 'Burgundy', light: ['#6e2436', '#8a2f45', '#f9e5e9'], dark: ['#e8a0b0', '#f0b8c4', '#4a1c28'] },
  violet: { label: 'Violet', light: ['#3f2e75', '#53409a', '#eae5f9'], dark: ['#b7a4f0', '#c9baf5', '#2c2350'] },
  slate: { label: 'Slate', light: ['#2f3e4e', '#41566b', '#e7edf3'], dark: ['#a9bccd', '#c0d0de', '#243546'] },
}

/* ---- small hex helpers for the custom accent ---- */

function rgb(hex: string): [number, number, number] {
  const h = hex.replace('#', '')
  return [parseInt(h.slice(0, 2), 16), parseInt(h.slice(2, 4), 16), parseInt(h.slice(4, 6), 16)]
}

function mix(a: string, b: string, t: number): string {
  const [ar, ag, ab] = rgb(a)
  const [br, bg, bb] = rgb(b)
  const c = (x: number, y: number) => Math.round(x + (y - x) * t).toString(16).padStart(2, '0')
  return `#${c(ar, br)}${c(ag, bg)}${c(ab, bb)}`
}

function accentFor(name: string): Accent | null {
  if (name.startsWith('custom:#') && /^custom:#[0-9a-fA-F]{6}$/.test(name)) {
    const hex = name.slice(7)
    return {
      label: 'Custom',
      light: [hex, mix(hex, '#ffffff', 0.16), mix(hex, '#ffffff', 0.9)],
      dark: [mix(hex, '#ffffff', 0.55), mix(hex, '#ffffff', 0.68), mix(hex, '#0b1420', 0.72)],
    }
  }
  return ACCENTS[name] ?? null
}

/** Surface tones: the background each theme is derived from. 'cool' is the
 *  platform's own pair (its light ground and GitHub-like dark), and the
 *  stylesheet's fallback values are derived from it. */
export const SURFACES: Record<string, { label: string; light: string; dark: string }> = {
  cool: { label: 'Cool gray', light: '#f3f4f6', dark: '#0d1117' },
  neutral: { label: 'Neutral gray', light: '#f4f4f5', dark: '#111214' },
  warm: { label: 'Warm gray', light: '#f5f4f1', dark: '#151312' },
}

function styleEl(id: string): HTMLStyleElement {
  let el = document.getElementById(id) as HTMLStyleElement | null
  if (!el) {
    el = document.createElement('style')
    el.id = id
  }
  // Always move it last in <head>. The pre-paint script in index.html
  // inserts these before the bundle's stylesheet, and on equal specificity
  // the later rule wins, so leaving it in place lets the bundle's stock
  // values override every token we derive.
  document.head.appendChild(el)
  return el
}

/** Cached so index.html can apply the same CSS before first paint. */
function remember(key: string, css: string): void {
  try { localStorage.setItem(key, css) } catch { /* storage unavailable */ }
}

let current = { accent: 'navy', surface: 'cool' }

/**
 * The whole --theme-* contract for one accent and surface, light and dark,
 * from the shared package's deriveTheme: the same derivation the platform's
 * other apps use, so the Studio's colors, contrast, and status hues match
 * theirs. The seed is the page background (the ground the navigation sits
 * on), as in the other apps; nothing derived is overridden afterward, and
 * the Studio's own --pw-* names are aliases of these in styles.css.
 */
export function themeFor(accent: string, surface: string): { light: Record<string, string>; dark: Record<string, string> } {
  const a = accentFor(accent) ?? ACCENTS.navy
  const s = SURFACES[surface] ?? SURFACES.cool
  return {
    light: deriveTheme({ accent: a.light[0], background: s.light }),
    dark: deriveTheme({ accent: a.dark[0], background: s.dark }),
  }
}

function emit(): void {
  const vars = (v: Record<string, string>) => Object.entries(v).map(([k, x]) => `${k}: ${x};`).join(' ')
  let css = ''
  try {
    const t = themeFor(current.accent, current.surface)
    // Doubled selectors (:root:root) outrank the plain :root rules the shared
    // packages ship, so these win wherever they land in the cascade. Without
    // that, the cached copy applied before first paint loses to the bundle's
    // stylesheet the moment it loads, and the page flashes stock blue before
    // settling on the configured theme.
    css = `
:root:root { ${vars(t.light)} }
:root:root[data-theme='dark'] { ${vars(t.dark)} }
`
  } catch { /* the stylesheet's derived defaults still theme the app */ }
  styleEl('accent-style').textContent = css
  styleEl('surface-style').textContent = ''
  remember('ade-accent-css', css)
  remember('ade-surface-css', '')
}

export function applyAccent(name: string): void {
  current = { ...current, accent: name }
  emit()
}

export function applySurface(name: string): void {
  current = { ...current, surface: name }
  emit()
}
