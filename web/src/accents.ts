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

/** Surface tones: 'cool' is the stylesheet default (blue-tinted dark). */
export const SURFACES: Record<string, { label: string; dark?: Record<string, string>; light?: Record<string, string> }> = {
  cool: { label: 'Cool gray' },
  neutral: {
    label: 'Neutral gray',
    dark: {
      '--pw-bg': '#111214', '--pw-panel': '#191b1e', '--pw-border': '#2a2d31', '--pw-border-strong': '#41454b',
      '--pw-input-bg': '#17191c', '--pw-hover-row': '#1e2125',
      '--theme-muted-panel-bg': '#17191c',
      '--theme-hover': '#212428', '--theme-input-bg': '#17191c',
    },
  },
  warm: {
    label: 'Warm gray',
    light: { '--pw-bg': '#f5f4f1', '--pw-panel': '#fffefb', '--pw-border': '#e9e6df', '--pw-border-strong': '#d6d1c6' },
    dark: {
      '--pw-bg': '#151312', '--pw-panel': '#1d1a18', '--pw-border': '#302c28', '--pw-border-strong': '#48423a',
      '--pw-input-bg': '#1a1715', '--pw-hover-row': '#22201c',
      '--theme-muted-panel-bg': '#1a1715',
      '--theme-hover': '#262320', '--theme-input-bg': '#1a1715',
    },
  },
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

/** Panel backgrounds the stylesheet ships with; a surface may override
 *  them, and the derived tokens follow whichever is in effect. The panel,
 *  not the page, is the right seed: the shared components render inside a
 *  card, so seeding from the page background left the chat pane grey while
 *  every other view sat on white. */
const BASE_PANEL = { light: '#ffffff', dark: '#16202f' }

let current = { accent: 'navy', surface: 'cool' }

function backgrounds(): { light: string; dark: string } {
  const sfc = SURFACES[current.surface]
  return {
    light: sfc?.light?.['--pw-panel'] ?? BASE_PANEL.light,
    dark: sfc?.dark?.['--pw-panel'] ?? BASE_PANEL.dark,
  }
}

/** Emit both style blocks together: the platform derives its whole
 *  --theme-* token set from an accent plus a background, so the two cannot
 *  be computed independently. Shared components (the chat package, anything
 *  adopted from @parallelworks/ui) read those tokens, which is why hand
 *  mapping a few of them still left stock blue showing in dark mode. */
function emit(): void {
  const a = accentFor(current.accent) ?? ACCENTS.navy
  const bg = backgrounds()
  const solid = mix(a.light[0], '#ffffff', 0.18)
  const solidHover = mix(a.light[0], '#ffffff', 0.3)
  const vars = (v: Record<string, string>) => Object.entries(v).map(([k, x]) => `${k}: ${x};`).join(' ')

  let themeLight = ''
  let themeDark = ''
  try {
    themeLight = vars(deriveTheme({ accent: a.light[0], background: bg.light }))
    themeDark = vars(deriveTheme({ accent: a.dark[0], background: bg.dark }))
  } catch { /* our own variables still theme the app */ }

  const sfc = SURFACES[current.surface]
  const block = (v?: Record<string, string>) =>
    v ? Object.entries(v).map(([k, x]) => `${k}: ${x};`).join(' ') : ''
  const surfaceCss = !sfc || current.surface === 'cool' ? '' : `
:root { ${block(sfc.light)} }
[data-theme='dark'] { ${block(sfc.dark)} }
`
  const accentCss = `
:root { ${themeLight} --pw-navy: ${a.light[0]}; --pw-navy-2: ${a.light[1]}; --pw-active-pill: ${a.light[2]}; --pw-link: ${a.light[1]}; --theme-link: ${a.light[1]}; }
[data-theme='dark'] { ${themeDark} --pw-navy: ${a.dark[0]}; --pw-navy-2: ${a.dark[1]}; --pw-active-pill: ${a.dark[2]}; --pw-link: ${a.dark[0]}; --theme-link: ${a.dark[0]}; --theme-element: ${solid}; }
[data-theme='dark'] .btn-primary { background: ${solid}; }
[data-theme='dark'] .btn-primary:hover { background: ${solidHover}; }
[data-theme='dark'] .brand-badge { background: ${solid}; }
`
  styleEl('surface-style').textContent = surfaceCss
  styleEl('accent-style').textContent = accentCss
  remember('ade-surface-css', surfaceCss)
  remember('ade-accent-css', accentCss)
}

export function applyAccent(name: string): void {
  current = { ...current, accent: name }
  emit()
}

export function applySurface(name: string): void {
  current = { ...current, surface: name }
  emit()
}
