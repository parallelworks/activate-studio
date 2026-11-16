import { readFileSync } from 'node:fs'
import path from 'node:path'
import { describe, expect, it } from 'vitest'

/** Every rule in the stylesheet, as [selector, declarations], with the
 *  at-rule (media or container query) it sits in. Comments are dropped. */
function rules(css: string): { at: string; selector: string; body: string }[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const out: { at: string; selector: string; body: string }[] = []
  const walk = (src: string, at: string) => {
    let i = 0
    while (i < src.length) {
      const open = src.indexOf('{', i)
      if (open < 0) break
      const head = src.slice(i, open).trim()
      let depth = 1, j = open + 1
      while (j < src.length && depth) { if (src[j] === '{') depth++; else if (src[j] === '}') depth--; j++ }
      const body = src.slice(open + 1, j - 1)
      if (head.startsWith('@')) walk(body, head)
      else out.push({ at, selector: head, body })
      i = j
    }
  }
  walk(text, '')
  return out
}

const all = rules(readFileSync(path.resolve(process.cwd(), 'src/styles.css'), 'utf8'))
const find = (selector: string, at = '') => all.filter(r => r.selector === selector && r.at === at)
const decl = (selector: string, prop: string, at = ''): string | undefined => {
  for (const r of find(selector, at).reverse()) {
    const m = r.body.match(new RegExp(`(?:^|;|\\s)${prop.replace(/[-]/g, '\\-')}\\s*:\\s*([^;]+)`))
    if (m) return m[1].trim()
  }
  return undefined
}
const px = (v: string | undefined) => Number(v?.match(/^(\d+)px$/)?.[1] ?? NaN)

describe('the file lists of New transfer', () => {
  it('size their columns by the pane, not the window, so the Status pane cannot squeeze the names out', () => {
    expect(decl('.xfer-pane', 'container')).toBe('xfer-pane / inline-size')
    // No window-width rule hides a column; the pane's width decides.
    expect(all.some(r => r.at.startsWith('@media') && /xfer-col-(mtime|size)/.test(r.selector))).toBe(false)
  })

  it('keep the Name column at least --xfer-name-min wide, with the fixed columns summed as they hide', () => {
    expect(decl('.xfer-col-name', 'min-width')).toBe('var(--xfer-name-min)')
    expect(px(decl('.xfer-table', '--xfer-name-min'))).toBeGreaterThanOrEqual(120)
    expect(decl('.xfer-table', 'min-width')).toBe('calc(var(--xfer-fixed) + var(--xfer-name-min))')
    expect(decl('.xfer-table', 'table-layout')).toBe('fixed')
    // Each fixed column's width plus 16px of padding.
    const col = (sel: string) => px(decl(sel, 'width')) + 16
    const check = col('.xfer-col-check'), size = col('.xfer-col-size'), mtime = col('.xfer-col-mtime')
    expect(px(decl('.xfer-table', '--xfer-fixed'))).toBe(check + size + mtime)

    // Modified goes first as the pane narrows, then Size.
    const queries = [...new Set(all.filter(r => r.at.startsWith('@container xfer-pane')).map(r => r.at))]
    const width = (q: string) => Number(q.match(/max-width:\s*(\d+)px/)![1])
    const [wide, narrow] = queries.sort((a, b) => width(b) - width(a))
    expect(decl('.xfer-col-mtime', 'display', wide)).toBe('none')
    expect(px(decl('.xfer-table', '--xfer-fixed', wide))).toBe(check + size)
    expect(decl('.xfer-col-size', 'display', narrow)).toBe('none')
    expect(px(decl('.xfer-table', '--xfer-fixed', narrow))).toBe(check)
    // Where a column hides, the name still had its minimum beside it.
    expect(width(wide)).toBeGreaterThanOrEqual(check + size + mtime + 120)
    expect(width(narrow)).toBeGreaterThanOrEqual(check + size + 100)
  })

  it('cut a long name with an ellipsis', () => {
    expect(decl('.xfer-table td', 'text-overflow')).toBe('ellipsis')
    expect(decl('.xfer-file', 'text-overflow')).toBe('ellipsis')
    expect(decl('.xfer-dir', 'text-overflow')).toBe('ellipsis')
  })
})

describe('the Start buttons of New transfer', () => {
  it('stay put as their notes change: half the column each, the first note above its button, the second below', () => {
    expect(decl('.xfer-middle', 'display')).toBe('grid')
    expect(decl('.xfer-middle', 'grid-template-rows')).toBe('minmax(0, 1fr) minmax(0, 1fr)')
    expect(decl('.xfer-middle > .xfer-start', 'justify-content')).toBe('flex-start')
    expect(decl('.xfer-middle > .xfer-start:first-child', 'flex-direction')).toBe('column-reverse')
    expect(decl('.xfer-middle', 'justify-content')).toBeUndefined()
  })
})

describe('the transfer details', () => {
  it('cover the Transfers view, which is the drawer\'s positioned ancestor', () => {
    expect(decl('.view:has(> .xfer-view)', 'position')).toBe('relative')
  })
})

describe('the Query page', () => {
  it('never shrinks the results, so the page scrolls to them on a short window', () => {
    expect(decl('.query-results', 'flex')).toBe('1 0 auto')
    // The rows keep a floor of height however short the window.
    expect(decl('.table-scroll', 'max-height')).toMatch(/^max\(240px,/)
    // The old rule that kept them only on a short or narrow window is gone.
    expect(all.some(r => r.selector === '.query-results' && r.at.startsWith('@media'))).toBe(false)
  })
})
