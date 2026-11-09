import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NAV_WIDTHS, NAV_WIDTH_MAX, NAV_WIDTH_MIN, clampNavWidth } from '../src/config'
import { NavResize } from '../src/components/NavResize'
import { SettingsView, navWidthPreset } from '../src/views/SettingsView'

if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string; pointerId: number
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? ''; this.pointerId = init.pointerId ?? 1 }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const navW = () => document.documentElement.style.getPropertyValue('--nav-w')

function stub(navWidth = 240) {
  const puts: Record<string, unknown>[] = []
  let saved: Record<string, unknown> = { appName: 'Studio', theme: 'light', accent: 'none', surface: 'warm', navWidth, sections: [], aiEnabled: true,
    suggestedPrompts: [], disabledTools: [], customTools: [], mcpServers: [], bannerText: '', bannerColor: '#24612e' }
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    if (url === '/api/settings' && init?.method === 'PUT') {
      const body = JSON.parse(String(init.body))
      puts.push(body)
      saved = { ...saved, ...body }
      return reply({ effective: saved })
    }
    if (url === '/api/settings') return reply({ effective: saved })
    if (url === '/api/config') return reply({ appName: 'Studio', navWidth: saved.navWidth, user: { id: 'u', username: 'u' } })
    if (url.startsWith('/api/setup/status')) return reply({ standalone: true, branding: { light: null, dark: null } })
    if (url === '/api/updates') return reply({ error: 'not here' }, 404)
    return reply({})
  }))
  return puts
}

beforeEach(() => { localStorage.clear(); document.documentElement.style.removeProperty('--nav-w') })
afterEach(() => { vi.unstubAllGlobals(); history.replaceState(null, '', location.pathname) })

describe('the sidebar width', () => {
  it('keeps a saved width whole and in range, and Default for anything else', () => {
    expect(clampNavWidth(undefined)).toBe(NAV_WIDTHS.default)
    expect(clampNavWidth('wide')).toBe(NAV_WIDTHS.default)
    expect(clampNavWidth(10)).toBe(NAV_WIDTH_MIN)
    expect(clampNavWidth(9000)).toBe(NAV_WIDTH_MAX)
    expect(clampNavWidth(271.6)).toBe(272)
    expect(NAV_WIDTHS).toEqual({ narrow: 200, default: 240, wide: 320 })
    expect(navWidthPreset(320)).toBe('wide')
    expect(navWidthPreset(272)).toBeNull()
  })

  // First: the app's configuration is read once per page, as in the app.
  it('says when the width was dragged rather than chosen', async () => {
    stub(272)
    history.replaceState(null, '', `${location.pathname}#view=settings:sidebar`)
    render(<SettingsView />)
    expect(await screen.findByText(/Dragged to 272 pixels/)).toBeInTheDocument()
  })

  it('Settings, General, Sidebar, Width saves the choice and applies it at once', async () => {
    const puts = stub()
    history.replaceState(null, '', `${location.pathname}#view=settings:sidebar`)
    render(<SettingsView />)
    fireEvent.click(await screen.findByRole('button', { name: 'Wide' }))
    expect(navW()).toBe('320px')
    await waitFor(() => expect(puts).toContainEqual({ navWidth: 320 }))
    fireEvent.click(screen.getByRole('button', { name: 'Narrow' }))
    expect(navW()).toBe('200px')
    await waitFor(() => expect(puts).toContainEqual({ navWidth: 200 }))
  })

  it('a drag of the edge resizes within range and saves on release; a double click returns to Default', async () => {
    const puts = stub(240)
    render(<div style={{ display: 'flex' }}><nav className="sidenav" /><NavResize width={240} /></div>)
    const edge = screen.getByRole('separator', { name: 'Sidebar width' })
    // jsdom lays nothing out, so the sidebar measures as the width given.
    fireEvent.pointerDown(edge, { button: 0, clientX: 240 })
    fireEvent.pointerMove(edge, { clientX: 300 })
    expect(navW()).toBe('300px')
    fireEvent.pointerMove(edge, { clientX: 900 })
    expect(navW()).toBe(`${NAV_WIDTH_MAX}px`)
    expect(puts).toEqual([])
    fireEvent.pointerUp(edge, { clientX: 900 })
    await waitFor(() => expect(puts).toEqual([{ navWidth: NAV_WIDTH_MAX }]))

    fireEvent.doubleClick(edge)
    expect(navW()).toBe('240px')
    await waitFor(() => expect(puts.at(-1)).toEqual({ navWidth: 240 }))

    // The keyboard moves it in steps.
    fireEvent.keyDown(edge, { key: 'ArrowLeft' })
    await waitFor(() => expect(puts.at(-1)).toEqual({ navWidth: 230 }))
    fireEvent.keyDown(edge, { key: 'Home' })
    await waitFor(() => expect(puts.at(-1)).toEqual({ navWidth: NAV_WIDTH_MIN }))
  })

  it('a click on the edge focuses it, so the arrow keys work after it, and focusing it scrolls nothing', async () => {
    const puts = stub(240)
    const focus = vi.spyOn(HTMLElement.prototype, 'focus')
    const reveal = vi.fn()
    const had = Element.prototype.scrollIntoView
    Element.prototype.scrollIntoView = reveal
    render(<div style={{ display: 'flex' }}><nav className="sidenav"><button>sync now</button></nav><NavResize width={240} /><main><button>first</button></main></div>)
    const edge = screen.getByRole('separator', { name: 'Sidebar width' })
    screen.getByRole('button', { name: 'first' }).focus()
    fireEvent.pointerDown(edge, { button: 0, clientX: 240 })
    fireEvent.pointerUp(edge, { clientX: 240 })
    expect(document.activeElement).toBe(edge)
    expect(focus).toHaveBeenLastCalledWith({ preventScroll: true })
    expect(reveal).not.toHaveBeenCalled()
    // A press shows no focus ring; a key brings it back.
    expect(edge).toHaveClass('pressed')
    // A click that moved nothing saves nothing.
    expect(puts).toEqual([])
    fireEvent.keyDown(document.activeElement!, { key: 'ArrowRight' })
    await waitFor(() => expect(puts.at(-1)).toEqual({ navWidth: 250 }))
    expect(edge).not.toHaveClass('pressed')
    // Another button than the main one leaves the focus where it was.
    screen.getByRole('button', { name: 'first' }).focus()
    fireEvent.pointerDown(edge, { button: 2, clientX: 240 })
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'first' }))
    focus.mockRestore()
    Element.prototype.scrollIntoView = had
  })
})
