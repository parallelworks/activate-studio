import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

/** Every rule in the stylesheet, as [selector, declarations], with the
 *  at-rule (media query) it sits in. Comments are dropped first. */
function rules(css: string): { media: string; selector: string; body: string }[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const out: { media: string; selector: string; body: string }[] = []
  const walk = (src: string, media: string) => {
    let i = 0
    while (i < src.length) {
      const open = src.indexOf('{', i)
      if (open < 0) break
      const head = src.slice(i, open).trim()
      let depth = 1, j = open + 1
      while (j < src.length && depth) { if (src[j] === '{') depth++; else if (src[j] === '}') depth--; j++ }
      const body = src.slice(open + 1, j - 1)
      if (head.startsWith('@')) walk(body, head)
      else out.push({ media, selector: head, body })
      i = j
    }
  }
  walk(text, '')
  return out
}

/** A selector list split at its top-level commas, not those inside :is(). */
function topLevel(list: string): string[] {
  const out: string[] = []
  let depth = 0, start = 0
  for (let i = 0; i < list.length; i++) {
    if (list[i] === '(') depth++
    else if (list[i] === ')') depth--
    else if (list[i] === ',' && !depth) { out.push(list.slice(start, i)); start = i + 1 }
  }
  return [...out, list.slice(start)]
}

describe('the Mac window without a title bar', () => {
  const css = readFileSync(path.resolve(process.cwd(), 'src/styles.css'), 'utf8')
  const all = rules(css)
  const inset = all.filter(r => r.selector.includes('shell-inset'))

  it('scopes every rule that uses the strip to html.shell-inset', () => {
    // Linux, Windows and browsers never get the class, so nothing here may
    // apply to them: each selector in a rule naming the strip starts there.
    for (const r of all.filter(r => /--(shell-top|inset-strip|rail-strip)\b/.test(r.body))) {
      for (const sel of topLevel(r.selector)) expect(sel.trim(), r.selector).toMatch(/^html\.shell-inset($|[\s.:])/)
    }
  })

  it('leaves the strip at the top of every top-level column, in its own color', () => {
    const strip = (name: string) => inset.some(r => r.selector.includes(name) && /border-top:\s*var\(--(inset|rail)-strip/.test(r.body))
    for (const col of ['.sidenav', 'main.content', '.status-panel', '.full-view', '.setup-overlay', '.library-card > *', '.help-docs > *', '.chat-canvas > div > :is(aside, main)']) {
      expect(strip(col), col).toBe(true)
    }
    expect(inset.find(r => r.selector === 'html.shell-inset' && r.body.includes('--app-inset-top'))).toBeTruthy()
  })

  it('keeps the phone layout, whose navigation sits at the bottom, free of a strip on it', () => {
    const nav = inset.find(r => r.selector.includes('.sidenav') && r.body.includes('border-top'))
    expect(nav?.media).toMatch(/min-width:\s*761px/)
  })

  it('lets a classification banner fill the strip in place of the columns', () => {
    expect(inset.some(r => r.selector.includes('.cls-banner ~') && /--inset-strip:\s*0/.test(r.body))).toBe(true)
    const banner = inset.find(r => /\.cls-banner:not\(\.banner-preview\)$/.test(r.selector))
    expect(banner?.body).toMatch(/min-height:\s*var\(--shell-top\)/)
  })
})
