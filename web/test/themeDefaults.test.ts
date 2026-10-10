import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'
import { deriveTheme } from '@parallelworks/ui/theme'
import { SURFACES, themeFor } from '../src/accents'

/** The declarations of one block in the stylesheet's derived defaults. */
function block(css: string, selector: string): Record<string, string> {
  const region = css.slice(css.indexOf('/* BEGIN derived theme defaults'), css.indexOf('/* END derived theme defaults'))
  const at = region.indexOf(`${selector} {`)
  const body = region.slice(at, region.indexOf('}', at))
  const out: Record<string, string> = {}
  for (const m of body.matchAll(/(--theme-[a-z-]+):\s*([^;]+);/g)) out[m[1]] = m[2].trim()
  return out
}

describe('the theme', () => {
  const css = readFileSync(path.resolve(process.cwd(), 'src/styles.css'), 'utf8')

  it('ships fallback values equal to deriveTheme for navy on the cool surface', () => {
    expect(block(css, ':root')).toEqual(deriveTheme({ accent: '#06354f', background: SURFACES.cool.light }))
    expect(block(css, "[data-theme='dark']")).toEqual(deriveTheme({ accent: '#7fc0ec', background: SURFACES.cool.dark }))
  })

  it('derives the whole contract for every accent and surface, with readable text', () => {
    for (const surface of Object.keys(SURFACES)) {
      for (const accent of ['navy', 'teal', 'forest', 'burgundy', 'violet', 'slate', 'custom:#7a3b10']) {
        const t = themeFor(accent, surface)
        expect(Object.keys(t.light).length).toBe(45)
        expect(Object.keys(t.dark).length).toBe(45)
        expect(t.dark['--theme-app-bg']).toBe(SURFACES[surface].dark)
      }
    }
  })

  it('keeps the Studio color names as aliases of the contract', () => {
    for (const [name, token] of [['--pw-text', '--theme-app'], ['--pw-bg', '--theme-app-bg'], ['--pw-panel', '--theme-panel-bg'], ['--pw-border', '--theme-border'], ['--pw-link', '--theme-link']]) {
      expect(css).toContain(`${name}: var(${token});`)
    }
  })
})
