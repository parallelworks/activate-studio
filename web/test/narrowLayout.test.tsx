import { act, fireEvent, render, screen } from '@testing-library/react'
import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LibraryView } from '../src/views/LibraryView'
import { SettingsView } from '../src/views/SettingsView'
import { setLibrary } from '../src/api'
import { reloadAppConfig } from '../src/config'
import { resetTransfersProbe } from '../src/transferApi'

// Below 761px (the phone layout) the Library's tree and the open file take the
// whole width in turn, and the Settings section strip says when it goes on
// past an edge. Before, the open file was drawn 0px wide beside a tree at full
// width, and the strip ended mid-word with nothing to show it scrolled.

const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } })
const phone = (on: boolean) => vi.stubGlobal('matchMedia', (q: string) => ({
  matches: on && /max-width: 760px/.test(q), media: q, addEventListener() {}, removeEventListener() {},
}))

beforeEach(() => {
  localStorage.clear()
  resetTransfersProbe()
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
})
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); setLibrary('kb'); history.replaceState(null, '', location.pathname) })

/** The rules inside every `@media (max-width: 760px)` block of styles.css, one per line. */
function phoneRules(): string[] {
  const css = readFileSync('src/styles.css', 'utf8')
  const out: string[] = []
  let at = 0
  for (;;) {
    const start = css.indexOf('@media (max-width: 760px) {', at)
    if (start < 0) return out
    let depth = 0, i = css.indexOf('{', start)
    const from = i + 1
    for (; i < css.length; i++) {
      if (css[i] === '{') depth++
      else if (css[i] === '}' && --depth === 0) break
    }
    out.push(...css.slice(from, i).replace(/\/\*[\s\S]*?\*\//g, '').split('\n').map(l => l.trim()).filter(Boolean))
    at = i
  }
}

describe('a file opened in the Library at the phone width', () => {
  function serve() {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      if (url.startsWith('/api/config')) return json({ appName: 'Studio', kbLabel: 'Notes', libraries: [{ id: 'kb', label: 'Notes', primary: true, writable: true, pinned: false, source: true, caps: { index: true, fullText: true, vectors: false } }] })
      if (url.startsWith('/api/kb/tree')) return json({ path: '', entries: [{ name: 'notes', isDir: true }] })
      if (url.startsWith('/api/kb/file')) return json({ path: 'notes/readme.md', kind: 'text', content: '# Readme', size: 8, mtime: 1 })
      if (url.includes('/tags')) return json({ own: [], inherited: [] })
      if (url.startsWith('/api/transfers')) return json({ locations: [] })
      return json({})
    }))
  }
  async function show(display: Parameters<typeof LibraryView>[0]['display'], onDisplay = vi.fn()) {
    render(<LibraryView display={display} onDisplay={onDisplay} />)
    await act(async () => { reloadAppConfig(); await new Promise(r => setTimeout(r, 20)) })
    return onDisplay
  }

  it('stacks the tree and the file, and shows one of them at a time', () => {
    const rules = phoneRules()
    // The card that holds the tree and the file was left a row, so the tree at
    // full width pushed the file to 0px past the card's clipped edge.
    expect(rules.some(r => /(^|,\s*)\.library-card[\s,{]/.test(r) && r.includes('flex-direction: column'))).toBe(true)
    expect(rules).toContain('.library:not(.has-open) .library-main { display: none; }')
    expect(rules).toContain('.library.has-open .library-rail { display: none; }')
  })

  it('offers a way back to the list above the file', async () => {
    phone(true); serve()
    const onDisplay = await show({ kind: 'file', target: 'notes/readme.md' })
    expect(document.querySelector('.library')).toHaveClass('has-open')
    fireEvent.click(screen.getByRole('button', { name: 'Back to the list' }))
    expect(onDisplay).toHaveBeenCalledWith(null)
  })

  it('shows the tree when nothing is open, even if it was collapsed at a wider width', async () => {
    phone(true); serve()
    localStorage.setItem('ade-rail-collapsed', '1')
    await show(null)
    expect(document.querySelector('.library')).not.toHaveClass('has-open')
    expect(document.querySelector('.library-rail')).not.toBeNull()
    expect(screen.queryByRole('button', { name: 'Collapse tree' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Back to the list' })).toBeNull()
  })

  it('keeps the tree beside the file, and its collapse button, above the phone width', async () => {
    phone(false); serve()
    await show({ kind: 'file', target: 'notes/readme.md' })
    expect(screen.queryByRole('button', { name: 'Back to the list' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Collapse tree' })).toBeInTheDocument()
  })
})

describe('the Settings section strip at the phone width', () => {
  const W = 300
  const EFFECTIVE = { appName: 'Studio', kbLabel: 'Notes', theme: 'light', accent: 'navy', surface: 'neutral', navWidth: 240, sections: [], aiEnabled: true, customTools: [], disabledTools: [], mcpServers: [], suggestedPrompts: [], workflowCollection: [] }
  function serve() {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      if (url === '/api/settings') return json({ effective: EFFECTIVE })
      if (url === '/api/config') return json({ appName: 'Studio', aiEnabled: true, platform: true, signIn: true, navWidth: 240, user: { id: 'u', username: 'u' } })
      if (url.startsWith('/api/setup/status')) return json({ firstRun: false, standalone: false, branding: { light: null, dark: null } })
      return json({})
    }))
  }

  /** The strip as a narrow window lays it out: each section 86px wide with 4px between, more of them than fit. */
  function layOut(nav: HTMLElement) {
    const buttons = [...nav.querySelectorAll<HTMLElement>('button')]
    const width = buttons.length * 90 - 4
    expect(width).toBeGreaterThan(W)
    let left = 0
    Object.defineProperty(nav, 'scrollWidth', { configurable: true, get: () => width })
    Object.defineProperty(nav, 'clientWidth', { configurable: true, get: () => W })
    Object.defineProperty(nav, 'scrollLeft', { configurable: true, get: () => left, set: (v: number) => { left = Math.max(0, Math.min(width - W, v)) } })
    nav.getBoundingClientRect = () => ({ left: 0, right: W, width: W, top: 0, bottom: 44, height: 44, x: 0, y: 0, toJSON() {} })
    buttons.forEach((b, i) => {
      b.getBoundingClientRect = () => ({ left: i * 90 - left, right: i * 90 + 86 - left, width: 86, top: 0, bottom: 44, height: 44, x: i * 90 - left, y: 0, toJSON() {} })
    })
    return { buttons, left: () => left, end: width - W }
  }

  async function open() {
    phone(true); serve()
    history.replaceState(null, '', `${location.pathname}#view=settings:general`)
    render(<SettingsView />)
    await screen.findByRole('heading', { name: 'General' })
    return layOut(document.querySelector<HTMLElement>('.help-nav')!)
  }

  it('shows an arrow on each side with sections past it, which moves the strip along', async () => {
    const strip = await open()
    fireEvent.scroll(document.querySelector('.help-nav')!)
    expect(screen.queryByRole('button', { name: 'Show earlier sections' })).toBeNull()
    for (let i = 0; i < 5 && screen.queryByRole('button', { name: 'Show more sections' }); i++) {
      fireEvent.click(screen.getByRole('button', { name: 'Show more sections' }))
    }
    expect(strip.left()).toBe(strip.end)
    expect(screen.queryByRole('button', { name: 'Show more sections' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Show earlier sections' }))
    expect(strip.left()).toBeLessThan(strip.end)
    expect(screen.getByRole('button', { name: 'Show more sections' })).toBeInTheDocument()
  })

  it('brings a section reached with Tab into view, clear of the arrows', async () => {
    const strip = await open()
    // The fourth section ends at 356px, past the 300px strip.
    const fourth = strip.buttons[3]!
    fireEvent.focus(fourth)
    expect(fourth.getBoundingClientRect().right).toBeLessThanOrEqual(W - 44)
    expect(fourth.getBoundingClientRect().left).toBeGreaterThanOrEqual(44)
    const first = strip.buttons[0]!
    fireEvent.focus(first)
    expect(strip.left()).toBe(0)
  })
})
