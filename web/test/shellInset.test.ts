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

  const NO_BANNER = 'html.shell-inset .app:not(:has(> .cls-banner))'

  it('scopes every rule that uses the row to html.shell-inset', () => {
    // Linux, Windows and browsers never get the class, so nothing here may
    // apply to them: each selector in a rule naming the row starts there.
    for (const r of all.filter(r => /--shell-(top|controls)\b/.test(r.body))) {
      for (const sel of topLevel(r.selector)) expect(sel.trim(), r.selector).toMatch(/^html\.shell-inset($|[\s.:])/)
    }
  })

  it('puts each column\'s first row on the controls\' row, with no empty strip above it', () => {
    // No column keeps a transparent strip at its top on the desktop layout.
    for (const r of inset.filter(r => !/max-width/.test(r.media))) expect(r.body, r.selector).not.toMatch(/border-top:\s*var\(--shell-top\)/)
    const row = (sel: string, decl: RegExp) => inset.some(r => r.selector.startsWith(NO_BANNER) && r.selector.includes(sel) && decl.test(r.body))
    // The navigation's brand row is the row, past the controls.
    expect(row('.sidenav-head', /min-height:\s*var\(--shell-top\)/)).toBe(true)
    expect(row('.sidenav-head', /padding-left:\s*calc\(var\(--shell-controls\)/)).toBe(true)
    // Collapsed, the rail is as wide as the controls and keeps their row.
    expect(row('.sidenav.collapsed', /width:\s*max\(64px,\s*var\(--shell-controls\)\)/)).toBe(true)
    // The other columns' heads are the row's height.
    expect(row('.page-head', /min-height:\s*var\(--shell-top\)/)).toBe(true)
    expect(row('.status-panel-head', /min-height:\s*var\(--shell-top\)/)).toBe(true)
    expect(row('.full-view-bar', /padding-left:\s*var\(--shell-controls\)/)).toBe(true)
    expect(inset.find(r => r.selector === 'html.shell-inset' && r.body.includes('--app-inset-top'))).toBeTruthy()
  })

  it('keeps the phone layout, whose page starts under the controls, clear of them', () => {
    const phone = inset.find(r => r.selector.includes('main.content') && /border-top:\s*var\(--shell-top\)/.test(r.body))
    expect(phone?.media).toMatch(/max-width:\s*760px/)
    const nav = inset.find(r => r.selector.includes('.sidenav-head'))
    expect(nav?.media).toMatch(/min-width:\s*761px/)
  })

  it('lets a classification banner be the row, clear of the controls', () => {
    const banner = inset.find(r => /\.cls-banner:not\(\.banner-preview\)$/.test(r.selector))
    expect(banner?.body).toMatch(/min-height:\s*var\(--shell-top\)/)
    expect(banner?.body).toMatch(/padding:[^;]*var\(--shell-controls\)/)
  })
})
