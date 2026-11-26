import { fireEvent, render, screen } from '@testing-library/react'
import { useRef, useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TREE_MAX, TREE_MIN, TreeResize, clampTree, treeMax } from '../src/components/TreeResize'

if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string; pointerId: number
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? ''; this.pointerId = init.pointerId ?? 1 }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const saved = vi.fn()
/** The tree at its width, with names of 6 px a letter in rows 60 px narrower. */
function Page({ names = ['short.md'], tagged, cardWidth = 0 }: { names?: string[]; tagged?: boolean; cardWidth?: number }) {
  const rail = useRef<HTMLElement>(null)
  const [width, setWidth] = useState(320)
  const max = treeMax(cardWidth)
  const shown = clampTree(width, max)
  return (
    <>
      <aside ref={rail} data-testid="rail" style={{ width: shown }}>
        <div className="explorer">
          {names.map(n => (
            <div key={n} className="tree-row"><span className="tree-text"><span className="tree-label">{n}</span>{tagged && <span className="tree-tags">x</span>}</span></div>
          ))}
        </div>
      </aside>
      <TreeResize width={shown} max={max} rail={() => rail.current} onWidth={w => { saved(w); setWidth(w) }} />
    </>
  )
}
const edge = () => screen.getByRole('separator', { name: 'Tree width' })
const now = () => Number(edge().getAttribute('aria-valuenow'))
const setWindow = (w: number) => { Object.defineProperty(window, 'innerWidth', { configurable: true, value: w }) }
const had = window.innerWidth

beforeEach(() => {
  setWindow(1440)
  saved.mockReset()
  Object.assign(Range.prototype, { getBoundingClientRect(this: Range) { return { width: (this.startContainer.textContent ?? '').length * 6 } as DOMRect } })
  vi.spyOn(Element.prototype, 'getBoundingClientRect').mockImplementation(function (this: Element) {
    const rail = parseFloat(screen.getByTestId('rail').style.width)
    return { width: this.matches('.tree-text') ? rail - 60 : this.matches('.tree-tags') ? 40 : 0 } as DOMRect
  })
})
afterEach(() => {
  setWindow(had)
  vi.restoreAllMocks()
  Reflect.deleteProperty(Range.prototype, 'getBoundingClientRect')
})

describe("the edge of the Library's tree", () => {
  it('is a separator the keyboard reaches, with its range', () => {
    render(<Page />)
    expect(edge()).toHaveAttribute('tabindex', '0')
    expect(edge()).toHaveAttribute('aria-orientation', 'vertical')
    expect(edge()).toHaveAttribute('aria-valuemin', String(TREE_MIN))
    expect(edge()).toHaveAttribute('aria-valuemax', '720')
    expect(now()).toBe(320)
  })

  it('the arrow keys move it, further with Shift, and Home and End take it to its ends', () => {
    render(<Page />)
    fireEvent.keyDown(edge(), { key: 'ArrowRight' })
    expect(now()).toBe(330)
    fireEvent.keyDown(edge(), { key: 'ArrowLeft', shiftKey: true })
    expect(now()).toBe(290)
    fireEvent.keyDown(edge(), { key: 'Home' })
    expect(now()).toBe(TREE_MIN)
    fireEvent.keyDown(edge(), { key: 'ArrowLeft' })
    expect(now()).toBe(TREE_MIN)
    fireEvent.keyDown(edge(), { key: 'End' })
    expect(now()).toBe(720)
    // A key for the browser or the app is left alone.
    expect(fireEvent.keyDown(edge(), { key: 'ArrowLeft', altKey: true })).toBe(true)
    expect(now()).toBe(720)
  })

  it('a double click fits the widest name with room for the row button, wider or narrower', () => {
    render(<Page names={['a'.repeat(10), 'b'.repeat(60)]} />)
    fireEvent.doubleClick(edge())
    // 60 letters, what the row takes, and the button with its fade.
    expect(now()).toBe(360 + 60 + 34 + 2)
    const at = now()
    fireEvent.doubleClick(edge())
    expect(now()).toBe(at)
    fireEvent.keyDown(edge(), { key: 'End' })
    fireEvent.keyDown(edge(), { key: 'Enter' })
    expect(now()).toBe(at)
  })

  it('labels after a name are fitted with it', () => {
    render(<Page names={['b'.repeat(60)]} tagged />)
    fireEvent.doubleClick(edge())
    expect(now()).toBe(360 + 60 + 2 + 40 + 7)
  })

  it('a fit goes as far as a drag can: half the window, or TREE_MAX in a narrower one', () => {
    const { unmount } = render(<Page names={['c'.repeat(400)]} />)
    fireEvent.doubleClick(edge())
    expect(now()).toBe(720)
    unmount()
    for (const w of [900, 800]) {
      setWindow(w)
      const { unmount: gone } = render(<Page names={['c'.repeat(400)]} />)
      fireEvent.doubleClick(edge())
      expect(now()).toBe(TREE_MAX)
      // Where a drag stops.
      fireEvent.keyDown(edge(), { key: 'Home' })
      fireEvent.pointerDown(edge(), { button: 0, clientX: TREE_MIN })
      fireEvent.pointerMove(edge(), { clientX: 5000 })
      fireEvent.pointerUp(edge(), { clientX: 5000 })
      expect(now()).toBe(TREE_MAX)
      gone()
    }
  })

  it('after a fit, a narrower window takes the tree in as far as it must, and a wider one gives the fit back', () => {
    const { rerender } = render(<Page names={['c'.repeat(400)]} cardWidth={900} />)
    fireEvent.doubleClick(edge())
    expect(now()).toBe(720)
    setWindow(900)
    rerender(<Page names={['c'.repeat(400)]} cardWidth={600} />)
    expect(now()).toBe(480)
    expect(screen.getByTestId('rail').style.width).toBe('480px')
    setWindow(1440)
    rerender(<Page names={['c'.repeat(400)]} cardWidth={900} />)
    expect(now()).toBe(720)
    expect(saved).toHaveBeenCalledTimes(1)
  })

  it('a fit keeps the buttons over the tree whole, and an empty tree stays as it is', () => {
    const { unmount } = render(<Page names={['a']} />)
    fireEvent.doubleClick(edge())
    expect(now()).toBe(300)
    unmount()
    saved.mockReset()
    render(<Page names={[]} />)
    fireEvent.doubleClick(edge())
    expect(saved).not.toHaveBeenCalled()
  })

  it('a drag moves the tree at once and keeps the width on release; a click that moved nothing keeps nothing', () => {
    render(<Page />)
    fireEvent.pointerDown(edge(), { button: 0, clientX: 320 })
    expect(document.activeElement).toBe(edge())
    expect(edge()).toHaveClass('dragging', 'pressed')
    fireEvent.pointerMove(edge(), { clientX: 400 })
    expect(screen.getByTestId('rail').style.width).toBe('400px')
    expect(saved).not.toHaveBeenCalled()
    fireEvent.pointerMove(edge(), { clientX: 5000 })
    fireEvent.pointerUp(edge(), { clientX: 5000 })
    expect(saved).toHaveBeenCalledExactlyOnceWith(720)
    expect(document.documentElement).not.toHaveClass('nav-resizing')
    saved.mockReset()
    fireEvent.pointerDown(edge(), { button: 0, clientX: 720 })
    fireEvent.pointerUp(edge(), { clientX: 720 })
    expect(saved).not.toHaveBeenCalled()
    fireEvent.pointerDown(edge(), { button: 2, clientX: 720 })
    expect(edge()).not.toHaveClass('dragging')
  })

  it('leaves the file its room, and takes what a narrow window has', () => {
    setWindow(1440)
    expect(treeMax(0)).toBe(720)
    expect(treeMax(1180)).toBe(720)
    expect(treeMax(900)).toBe(720)
    expect(treeMax(660)).toBe(540)
    expect(treeMax(300)).toBe(TREE_MIN)
    setWindow(800)
    expect(treeMax(0)).toBe(TREE_MAX)
  })
})
