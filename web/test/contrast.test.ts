import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'
import { contrastRatio, deriveTheme } from '@parallelworks/ui/theme'
import { ACCENTS, SURFACES } from '../src/accents'

/**
 * Every text color on every ground it is drawn on stays at WCAG AA (4.5:1),
 * for each accent, surface, and scheme a deployment can pick. A palette
 * change that breaks a pair fails here, not in someone's browser.
 */

const css = readFileSync(path.resolve(__dirname, '../src/styles.css'), 'utf8')

/** The --pw-* hex values in the first block that opens with `selector {`. */
function palette(selector: string): Record<string, string> {
  const start = css.indexOf(`${selector} {`)
  const body = css.slice(start, css.indexOf('\n}', start))
  return Object.fromEntries([...body.matchAll(/(--pw-[\w-]+):\s*(#[0-9a-fA-F]{6})\b/g)].map(m => [m[1], m[2]]))
}

/** color-mix(in srgb, a p%, b), as the stylesheet computes the nav colors. */
function mix(a: string, b: string, p: number): string {
  const ch = (h: string, i: number) => parseInt(h.slice(1 + 2 * i, 3 + 2 * i), 16)
  return '#' + [0, 1, 2].map(i => Math.round(ch(a, i) * p + ch(b, i) * (1 - p)).toString(16).padStart(2, '0')).join('')
}

const base = { light: palette(':root'), dark: palette("[data-theme='dark']") }

const cases: [string, string, 'light' | 'dark'][] = []
for (const accent of Object.keys(ACCENTS)) for (const surface of Object.keys(SURFACES)) for (const scheme of ['light', 'dark'] as const) cases.push([accent, surface, scheme])

describe('text contrast', () => {
  it('reads the base palette from the stylesheet', () => {
    for (const s of ['light', 'dark'] as const) {
      for (const t of ['--pw-text', '--pw-muted', '--pw-bg', '--pw-panel', '--pw-navy', '--pw-link']) expect(base[s][t], `${s} ${t}`).toMatch(/^#/)
    }
  })

  it.each(cases)('%s accent, %s surface, %s', (accent, surface, scheme) => {
    const p = { ...base[scheme], ...(SURFACES[surface][scheme] ?? {}) }
    const ink = ACCENTS[accent][scheme]
    const navBg = scheme === 'light' ? p['--pw-bg'] : p['--pw-panel']
    const pairs: [string, string, string][] = [
      ['text on page', p['--pw-text'], p['--pw-bg']],
      ['text on panel', p['--pw-text'], p['--pw-panel']],
      ['muted on page', p['--pw-muted'], p['--pw-bg']],
      ['muted on panel', p['--pw-muted'], p['--pw-panel']],
      ['nav label on active pill', mix(p['--pw-text'], navBg, 0.78), mix(p['--pw-text'], navBg, 0.09)],
      ['accent ink on panel', ink[0], p['--pw-panel']],
      ['accent link on panel', ink[1], p['--pw-panel']],
    ]
    const derived = deriveTheme({ accent: ink[0], background: p['--pw-panel'] })
    pairs.push(
      ['theme text on app ground', derived['--theme-app'], derived['--theme-app-bg']],
      ['theme muted on panel', derived['--theme-muted-text-color'], derived['--theme-panel-bg']],
      ['theme muted on hover', derived['--theme-muted-text-color'], derived['--theme-hover']],
    )
    const failing = pairs
      .map(([what, fg, bg]) => ({ what, fg, bg, ratio: contrastRatio(fg, bg) }))
      .filter(x => x.ratio < 4.5)
      .map(x => `${x.what}: ${x.fg} on ${x.bg} is ${x.ratio.toFixed(2)}:1`)
    expect(failing).toEqual([])
  })
})
