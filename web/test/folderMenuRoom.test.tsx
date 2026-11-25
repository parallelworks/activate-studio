import { readFileSync } from 'node:fs'
import { act, fireEvent, render, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AddLibrary, type AddOptions } from '../src/components/AddLibrary'
import { reloadAppConfig } from '../src/config'
import { closeMenu, folderRoute, openFolders } from './addLibraryHelpers'

// The folder menu always opened down, so a row low on the page (Settings,
// Indexing, Add a library) left it room for one folder of 32.

if (!('PointerEvent' in window)) Object.assign(window, { PointerEvent: class extends MouseEvent {} })

const json = (body: unknown) => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
const HERE: AddOptions = { buildHere: true, ssh: false, pw: false, workflow: false, space: false }
const VIEW = window.innerHeight
let row = { top: 0, bottom: 0 }

beforeEach(() => {
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (input: string) => folderRoute(String(input)) ?? json({ appName: 'Studio', libraries: [] })))
  const measured = Element.prototype.getBoundingClientRect
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
    return this.matches('.lib-where-folder') ? new DOMRect(0, row.top, 300, row.bottom - row.top) : measured.call(this)
  })
})
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); Object.assign(window, { innerHeight: VIEW }) })

const openAt = async (top: number, height: number) => {
  row = { top, bottom: top + 40 }
  Object.assign(window, { innerHeight: height })
  await openFolders()
  return waitFor(() => {
    const menu = document.querySelector<HTMLElement>('.code-path-popover')
    expect(menu?.dataset.side).toBeTruthy()
    return menu!
  })
}
const show = async () => {
  render(<AddLibrary options={HERE} onAdded={vi.fn()} />)
  await act(async () => { reloadAppConfig(); await new Promise(f => setTimeout(f, 20)) })
}

describe('the folder menu', () => {
  it('opens up from a row low on the page', async () => {
    await show()
    expect((await openAt(726, 900)).dataset.side).toBe('top')
  })
  it('opens down where it fits', async () => {
    await show()
    expect((await openAt(200, 900)).dataset.side).toBe('bottom')
  })
  it('takes the larger side where it fits on neither', async () => {
    await show()
    expect((await openAt(250, 600)).dataset.side).toBe('bottom')
    closeMenu()
    await waitFor(() => expect(document.querySelector('.code-path-popover')).toBeNull())
    expect((await openAt(330, 600)).dataset.side).toBe('top')
  })
  it('turns when the window becomes too short for it', async () => {
    await show()
    const menu = await openAt(420, 1000)
    expect(menu.dataset.side).toBe('bottom')
    act(() => { Object.assign(window, { innerHeight: 560 }); window.dispatchEvent(new Event('resize')) })
    await waitFor(() => expect(menu.dataset.side).toBe('top'))
  })
  it('stays on its side as the list changes', async () => {
    await show()
    const menu = await openAt(726, 900)
    fireEvent.change(document.querySelector('.code-path-field input')!, { target: { value: '/home/ada/pap' } })
    await waitFor(() => expect(document.querySelectorAll('[role="option"]').length).toBeGreaterThan(0))
    expect(menu.dataset.side).toBe('top')
  })
  it('keeps its height opened up and on a phone, so the field does not move as the list narrows', () => {
    const css = readFileSync('src/styles.css', 'utf8')
    expect(css).toMatch(/\.code-path-popover\[data-side="top"\] \.code-path-list \{ height: 20rem; \}/)
    expect(css).toMatch(/@media \(max-width: 639\.98px\) \{ \.code-path-popover \.code-path-list \{ height: 20rem; \}/)
  })
})
