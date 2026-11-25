import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { NameTips, nameTip } from '../src/components/NameTip'
import { Explorer } from '../src/components/Explorer'
import { TransferPane } from '../src/components/TransferPane'
import { MentionPalette } from '../src/components/MentionPalette'
import { api } from '../src/api'
import type { TransferLocation } from '../src/transferApi'

if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

/** jsdom lays nothing out: a name of more than `room` letters is the one cut. */
const room = 20
const cut = (el: Element) => (el.textContent ?? '').length > room
beforeEach(() => {
  Object.defineProperty(HTMLElement.prototype, 'clientWidth', { configurable: true, get() { return 100 } })
  Object.defineProperty(HTMLElement.prototype, 'scrollWidth', { configurable: true, get(this: HTMLElement) { return cut(this) ? 300 : 100 } })
})
afterEach(() => {
  for (const p of ['clientWidth', 'scrollWidth']) Reflect.deleteProperty(HTMLElement.prototype, p)
  vi.restoreAllMocks(); vi.useRealTimers(); localStorage.clear()
})

vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
Element.prototype.scrollIntoView ??= () => {}

const LONG = 'sensor-array-calibration-summary-first-quarter.pdf'
const tip = () => screen.queryByRole('tooltip')
const over = (el: Element) => fireEvent.pointerOver(el, { pointerType: 'mouse' })

const tree = (props: Partial<Parameters<typeof Explorer>[0]> = {}) => (
  <>
    <NameTips />
    <Explorer onOpen={() => {}} onDirFocus={() => {}} selected={null} rootLabel="Library" multiSelected={new Set()} onToggleMulti={() => {}}
      onSelectRange={() => {}} onMoveInto={() => {}} onCopyInto={() => {}} onDropInto={() => {}} onLabel={() => {}} onContext={() => {}}
      selectMode={false} showLabels={false} showHidden={false} tagsTick={0} refreshTick={0} dragKind={() => null} dropKind={() => 'none'} {...props} />
  </>
)
const FOLDER = 'instrument-readings-from-the-second-campaign'
function stubTree() {
  vi.spyOn(api, 'tree').mockImplementation(async (path: string) => ({ entries: path === '' ? [
    { type: 'dir', name: FOLDER, path: FOLDER },
    { type: 'file', name: LONG, path: LONG, size: 2048 },
    { type: 'file', name: 'notes.md', path: 'notes.md', size: 10 },
  ] : [{ type: 'file', name: LONG, path: `${path}/${LONG}`, size: 3072 }] }) as never)
}
const row = (path: string) => document.querySelector<HTMLElement>(`.tree-row[data-path="${path}"]`)!

describe('a cut name shown whole', () => {
  it('shows a cut name, its folder and its size when the pointer rests on the row, and nothing for a name that fits', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    stubTree()
    render(tree())
    await screen.findByText('notes.md')
    over(row('notes.md').querySelector('.tree-label')!)
    act(() => { vi.advanceTimersByTime(1000) })
    expect(tip()).toBeNull()

    over(row(LONG).querySelector('.tree-label')!)
    expect(tip()).toBeNull()
    act(() => { vi.advanceTimersByTime(250) })
    expect(tip()).toHaveTextContent(`${LONG}2 KB`)
    // No slow native title beside it.
    expect(row(LONG)).not.toHaveAttribute('title')

    // The next cut name follows at once, a folder's as a file's; one that fits closes it.
    over(row(FOLDER))
    expect(tip()).toHaveTextContent(FOLDER)
    over(row('notes.md'))
    expect(tip()).toBeNull()
  })

  it('says which folder a file in one is in', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    stubTree()
    render(tree())
    fireEvent.click(await screen.findByText(FOLDER))
    await waitFor(() => expect(row(`${FOLDER}/${LONG}`)).toBeTruthy())
    over(row(`${FOLDER}/${LONG}`))
    act(() => { vi.advanceTimersByTime(250) })
    expect(tip()).toHaveTextContent(`${LONG}${FOLDER}/ · 3 KB`)
  })

  it('a press, a scroll or the row going away closes it, and the row button keeps its own label', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    stubTree()
    const { rerender } = render(tree())
    await screen.findByText('notes.md')
    const rest = () => { over(document.body); over(row(LONG)); act(() => { vi.advanceTimersByTime(700) }) }
    rest()
    expect(tip()).not.toBeNull()
    fireEvent.pointerDown(row(LONG))
    expect(tip()).toBeNull()
    // Quiet after a press until the pointer has left the row.
    over(row(LONG).querySelector('.tree-label')!)
    act(() => { vi.advanceTimersByTime(700) })
    expect(tip()).toBeNull()
    rest()
    expect(tip()).not.toBeNull()
    fireEvent.scroll(document.querySelector('.explorer')!)
    expect(tip()).toBeNull()
    rest()
    over(row(LONG).querySelector('.row-tag')!)
    act(() => { vi.advanceTimersByTime(700) })
    expect(tip()).toBeNull()
    rest()
    expect(tip()).not.toBeNull()
    vi.spyOn(api, 'tree').mockResolvedValue({ entries: [{ type: 'file', name: 'notes.md', path: 'notes.md', size: 10 }] } as never)
    rerender(tree({ refreshTick: 1 }))
    await waitFor(() => expect(row(LONG)).toBeNull())
    act(() => { vi.advanceTimersByTime(700) })
    expect(tip()).toBeNull()
  })

  it('a finger is not a pointer resting on a row', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    stubTree()
    render(tree())
    await screen.findByText('notes.md')
    fireEvent.pointerOver(row(LONG), { pointerType: 'touch' })
    act(() => { vi.advanceTimersByTime(700) })
    expect(tip()).toBeNull()
  })

  it('a name that ends under the button a hovered row shows counts as cut', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    stubTree()
    const range = vi.fn(() => ({ width: 90, right: 190 }) as DOMRect)
    Object.assign(Range.prototype, { getBoundingClientRect: range })
    vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
      return { left: this.matches('.row-tag') ? 180 : 100, right: 200, top: 0, bottom: 24, width: 100, height: 24 } as DOMRect
    })
    render(tree({ selected: 'notes.md' }))
    await screen.findByText('notes.md')
    const button = row('notes.md').querySelector<HTMLElement>('.row-tag')!
    over(row('notes.md'))
    act(() => { vi.advanceTimersByTime(700) })
    // Beside the name, as on a touch screen, the button covers nothing.
    expect(tip()).toBeNull()
    over(document.body)
    button.style.position = 'absolute'
    over(row('notes.md'))
    act(() => { vi.advanceTimersByTime(700) })
    expect(tip()).toHaveTextContent('notes.md')
    Reflect.deleteProperty(Range.prototype, 'getBoundingClientRect')
  })
})

describe('the tree from the keyboard', () => {
  it('Tab reaches one row, the arrow keys move from it and show a cut name whole, and Enter opens', async () => {
    stubTree()
    const opened = vi.fn()
    render(tree({ onOpen: opened }))
    await screen.findByText('notes.md')
    await waitFor(() => expect(document.querySelectorAll('.tree-row[tabindex="0"]')).toHaveLength(1))
    await userEvent.tab()
    expect(document.activeElement).toBe(row(FOLDER))
    await waitFor(() => expect(tip()).toHaveTextContent(FOLDER))
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(row(LONG))
    expect(row(LONG).tabIndex).toBe(0)
    expect(row(FOLDER).tabIndex).toBe(-1)
    await waitFor(() => expect(tip()).toHaveTextContent(LONG))
    await userEvent.keyboard('{ArrowDown}')
    expect(document.activeElement).toBe(row('notes.md'))
    await waitFor(() => expect(tip()).toBeNull())
    await userEvent.keyboard('{Enter}')
    expect(opened).toHaveBeenCalledWith('notes.md')
    // A click moves the focus with no tip and no ring; a key after it brings both.
    await userEvent.click(row(LONG))
    const explorer = document.querySelector('.explorer')!
    expect(explorer).not.toHaveAttribute('data-keys')
    await userEvent.click(row('notes.md'))
    row('notes.md').focus()
    await userEvent.keyboard('{ArrowUp}')
    expect(document.activeElement).toBe(row(LONG))
    expect(explorer).toHaveAttribute('data-keys')
    await waitFor(() => expect(tip()).toHaveTextContent(LONG))
    // Right opens a folder and then goes in; left closes it, or goes out to it.
    await userEvent.keyboard('{Home}{ArrowRight}')
    expect(row(FOLDER)).toHaveAttribute('aria-expanded', 'true')
    await waitFor(() => expect(row(`${FOLDER}/${LONG}`)).toBeTruthy())
    await userEvent.keyboard('{ArrowRight}')
    expect(document.activeElement).toBe(row(`${FOLDER}/${LONG}`))
    await userEvent.keyboard('{ArrowLeft}')
    expect(document.activeElement).toBe(row(FOLDER))
    await userEvent.keyboard('{ArrowLeft}')
    expect(row(FOLDER)).toHaveAttribute('aria-expanded', 'false')
    // Keys on a row's own button are the button's.
    const button = row(LONG).querySelector<HTMLElement>('.row-tag')!
    button.focus()
    fireEvent.keyDown(button, { key: 'ArrowDown' })
    expect(document.activeElement).toBe(button)
  })

  it('when the row that takes Tab goes, another does', async () => {
    stubTree()
    const { rerender } = render(tree())
    await screen.findByText('notes.md')
    await waitFor(() => expect(row(FOLDER).tabIndex).toBe(0))
    vi.spyOn(api, 'tree').mockResolvedValue({ entries: [{ type: 'file', name: 'notes.md', path: 'notes.md', size: 10 }] } as never)
    rerender(tree({ refreshTick: 1 }))
    await waitFor(() => expect(row(FOLDER)).toBeNull())
    await waitFor(() => expect(row('notes.md').tabIndex).toBe(0))
  })
})

describe('the other lists', () => {
  it('Transfers: a cut name, with the size of a file, for a file and a folder', async () => {
    const location = { id: 'here', kind: 'local', label: 'This computer', detail: '', available: true, writable: true, browsable: true } as unknown as TransferLocation
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ path: '', parent: null, truncated: false, entries: [
      { name: FOLDER, type: 'dir', size: 0, mtime: '' }, { name: LONG, type: 'file', size: 2048, mtime: '' }, { name: 'a.txt', type: 'file', size: 1, mtime: '' },
    ] }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    render(<><NameTips /><TransferPane name="First location" locations={[location]} state={{ locationId: 'here', path: '', selected: [] }}
      onChange={() => {}} onLoaded={() => {}} refreshKey={0} /></>)
    const file = await screen.findByText(LONG)
    vi.useFakeTimers({ shouldAdvanceTime: true })
    over(file)
    act(() => { vi.advanceTimersByTime(250) })
    expect(tip()?.querySelector('.name-tip-name')).toHaveTextContent(LONG)
    expect(tip()?.querySelector('.name-tip-detail')).not.toBeNull()
    over(screen.getByText(FOLDER))
    expect(tip()).toHaveTextContent(FOLDER)
    over(screen.getByText('a.txt'))
    expect(tip()).toBeNull()
    expect(screen.getByText(FOLDER)).not.toHaveAttribute('title')
  })

  it('the @ palette: the arrow keys show the cut name they reach, while the focus stays in the message', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ entries: [
      { type: 'file', name: 'a.txt', path: 'a.txt' }, { type: 'file', name: LONG, path: LONG },
    ] }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    const canvas = { current: null as HTMLDivElement | null }
    function Page() { return <><NameTips /><div ref={el => { canvas.current = el }}><textarea /></div><MentionPalette canvas={canvas} /></> }
    render(<Page />)
    const ta = document.querySelector('textarea')!
    ta.focus()
    await userEvent.type(ta, '@')
    await screen.findByText(LONG)
    expect(tip()).toBeNull()
    await userEvent.keyboard('{ArrowDown}')
    await waitFor(() => expect(tip()).toHaveTextContent(LONG))
    expect(document.activeElement).toBe(ta)
    await userEvent.keyboard('{ArrowUp}')
    await waitFor(() => expect(tip()).toBeNull())
  })

  it('a list with its highlight marked for its field, as the folder menu: the option the keys reach', async () => {
    function Menu() {
      return <><NameTips /><input role="combobox" aria-expanded aria-controls="l" aria-activedescendant="o1" readOnly />
        <div id="l" role="listbox"><div id="o1" role="option" aria-selected {...nameTip(FOLDER)}><span data-tip-text="">{FOLDER}</span></div></div></>
    }
    render(<Menu />)
    screen.getByRole('combobox').focus()
    expect(tip()).toBeNull()
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'ArrowDown' })
    // The scroll that brings the option into view comes before the tip.
    fireEvent.scroll(screen.getByRole('listbox'))
    await waitFor(() => expect(tip()).toHaveTextContent(FOLDER))
    // The list scrolls the option into view, and the tip stays; a wheel closes it.
    fireEvent.scroll(screen.getByRole('listbox'))
    expect(tip()).toHaveTextContent(FOLDER)
    fireEvent.wheel(screen.getByRole('listbox'))
    expect(tip()).toBeNull()
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'ArrowDown' })
    await waitFor(() => expect(tip()).toHaveTextContent(FOLDER))
    fireEvent.keyDown(screen.getByRole('combobox'), { key: 'x' })
    expect(tip()).toBeNull()
  })
})
