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

export function applyAccent(name: string): void {
  const a = ACCENTS[name]
  let el = document.getElementById('accent-style') as HTMLStyleElement | null
  if (!el) {
    el = document.createElement('style')
    el.id = 'accent-style'
    document.head.appendChild(el)
  }
  el.textContent = !a || name === 'navy' ? '' : `
:root { --pw-navy: ${a.light[0]}; --pw-navy-2: ${a.light[1]}; --pw-active-pill: ${a.light[2]}; }
[data-theme='dark'] { --pw-navy: ${a.dark[0]}; --pw-navy-2: ${a.dark[1]}; --pw-active-pill: ${a.dark[2]}; }
`
}
