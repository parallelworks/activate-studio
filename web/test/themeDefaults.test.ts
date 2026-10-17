import { readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'
import { deriveTheme, THEME_TOKENS } from '@parallelworks/ui/theme'
import { SURFACES, themeFor } from '../src/accents'

/** The declarations of one block in the stylesheet's derived defaults. */
function block(css: string, selector: string): Record<string, string> {
  const region = css.slice(css.indexOf('/* BEGIN derived theme defaults'), css.indexOf('/* END derived theme defaults'))
  const at = region.indexOf(`${selector} {`)
  const body = region.slice(at, region.indexOf('}', at))
  const out: Record<string, string> = {}
  for (const m of body.matchAll(/(--theme-[a-z0-9-]+):\s*([^;]+);/g)) out[m[1]] = m[2].trim()
  return out
}

/** The derived-defaults block as deriveTheme gives it today. */
function derivedBlock(light: Record<string, string>, dark: Record<string, string>): string {
  const rules = (selector: string, scheme: string, t: Record<string, string>) =>
    `${selector} {\n  color-scheme: ${scheme};\n${Object.entries(t).map(([k, v]) => `  ${k}: ${v};\n`).join('')}}\n`
  return '/* BEGIN derived theme defaults (web/test/themeDefaults.test.ts) */\n' + rules(':root', 'light', light) + rules("[data-theme='dark']", 'dark', dark)
}

describe('the theme', () => {
  const file = path.resolve(process.cwd(), 'src/styles.css')
  const css = readFileSync(file, 'utf8')
  const light = deriveTheme({ interface: { base: SURFACES.cool.light, accent: '#06354f' } })
  const dark = deriveTheme({ interface: { base: SURFACES.cool.dark, accent: '#7fc0ec' } })

  // A UI package release that changes deriveTheme fails this; rerun it with
  // UPDATE_THEME_DEFAULTS=1 to rewrite the block from the new release.
  it('ships fallback values equal to deriveTheme for navy on the cool surface', () => {
    if (process.env.UPDATE_THEME_DEFAULTS) {
      const start = css.indexOf('/* BEGIN derived theme defaults'), end = css.indexOf('/* END derived theme defaults')
      writeFileSync(file, css.slice(0, start) + derivedBlock(light, dark) + css.slice(end))
      return
    }
    expect(block(css, ':root')).toEqual(light)
    expect(block(css, "[data-theme='dark']")).toEqual(dark)
  })

  it('derives the whole contract for every accent and surface, with readable text', () => {
    for (const surface of Object.keys(SURFACES)) {
      for (const accent of ['navy', 'teal', 'forest', 'burgundy', 'violet', 'slate', 'custom:#7a3b10']) {
        const t = themeFor(accent, surface)
        expect(Object.keys(t.light).length).toBe(THEME_TOKENS.length)
        expect(Object.keys(t.dark).length).toBe(THEME_TOKENS.length)
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
