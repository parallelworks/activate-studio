import { describe, expect, it } from 'vitest'
import { contrastRatio } from '@parallelworks/ui/theme'
import { ACCENTS, SURFACES, themeFor } from '../src/accents'

/**
 * Every text color on every ground it is drawn on stays at WCAG AA (4.5:1),
 * for each accent, surface, and scheme a deployment can pick. The colors are
 * the derived theme contract (styles.css aliases the Studio's names to it),
 * checked on the pairs the other Parallel Works apps check, plus the ones the
 * Studio's navigation and buttons draw. A palette change that breaks a pair
 * fails here, not in someone's browser.
 */

const cases: [string, string, 'light' | 'dark'][] = []
for (const accent of [...Object.keys(ACCENTS), 'custom:#7a3b10']) for (const surface of Object.keys(SURFACES)) for (const scheme of ['light', 'dark'] as const) cases.push([accent, surface, scheme])

describe('text contrast', () => {
  it.each(cases)('%s accent, %s surface, %s', (accent, surface, scheme) => {
    const t = themeFor(accent, surface)[scheme]
    const pairs: [string, string, string][] = [
      ['text on the page ground', t['--theme-app'], t['--theme-app-bg']],
      ['text on a panel', t['--theme-app'], t['--theme-panel-bg']],
      ['muted on the page ground', t['--theme-muted-text-color'], t['--theme-app-bg']],
      ['muted on a panel', t['--theme-muted-text-color'], t['--theme-panel-bg']],
      ['muted nav label on hover', t['--theme-muted-text-color'], t['--theme-hover']],
      ['text on the active nav row', t['--theme-app'], t['--theme-hover']],
      ['button text on the accent', t['--theme-accent-text'], t['--theme-accent']],
      ['link on a panel', t['--theme-link'], t['--theme-panel-bg']],
    ]
    const failing = pairs
      .map(([what, fg, bg]) => ({ what, fg, bg, ratio: contrastRatio(fg, bg) }))
      .filter(x => x.ratio < 4.5)
      .map(x => `${x.what}: ${x.fg} on ${x.bg} is ${x.ratio.toFixed(2)}:1`)
    expect(failing).toEqual([])
  })
})
