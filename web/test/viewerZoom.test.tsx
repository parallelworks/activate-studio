import { fireEvent, render, screen } from '@testing-library/react'
import { useRef, useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { pageWidth, stepZoom, wheelFactor, ZoomView, type ZoomControl, type Zooming } from '../src/components/ZoomView'
import { pageDpi } from '../src/components/Viewer'

describe('zoom steps', () => {
  it('steps in and out from any zoom, within 25% and 400%', () => {
    expect(stepZoom(1, 1)).toBe(1.1)
    expect(stepZoom(1, -1)).toBe(0.9)
    expect(stepZoom(1.3, 1)).toBe(1.5)
    expect(stepZoom(1.3, -1)).toBe(1.25)
    expect(stepZoom(4, 1)).toBe(4)
    expect(stepZoom(0.25, -1)).toBe(0.25)
  })

  it('zooms a mouse wheel notch by about a quarter, and a pinch by a little', () => {
    expect(wheelFactor(-100)).toBeCloseTo(1.26, 2)
    expect(wheelFactor(100)).toBeCloseTo(1 / 1.26, 2)
    expect(wheelFactor(-3)).toBeGreaterThan(1)
    expect(wheelFactor(-3)).toBeLessThan(1.01)
    // Lines (Firefox's mouse wheel) count as 16 pixels each.
    expect(wheelFactor(-3, 1)).toBeCloseTo(wheelFactor(-48), 5)
  })

  it('fits a page to the pane less its padding, no wider than 1100 pixels', () => {
    expect(pageWidth(0, 2)).toBe(0)
    expect(pageWidth(832, 1)).toBe(800)
    expect(pageWidth(832, 1.5)).toBe(1200)
    expect(pageWidth(3000, 1)).toBe(1100)
  })

  it('asks for finer pages only when zoomed in', () => {
    expect(pageDpi(800, false, 2)).toBe(130)
    expect(pageDpi(1200, true, 1)).toBe(200)
    expect(pageDpi(1200, true, 2)).toBe(300)
    expect(pageDpi(4000, true, 2)).toBe(400)
  })
})

/** Keeps the zoom as the Viewer does, and reports each change. */
function Harness({ zoom: initial, onZoom, onShown }: { zoom: number; onZoom: (z: number) => void; onShown: (s: boolean) => void }) {
  const control = useRef<ZoomControl | null>(null)
  const [zoom, setZoom] = useState(initial)
  const zooming: Zooming = { zoom, onZoom: z => { onZoom(z); setZoom(z) }, control, onShown }
  return (
    <>
      <button onClick={() => control.current?.zoomTo(2)}>Zoom to 200%</button>
      <ZoomView zooming={zooming} label="Pages">
        {fit => <div data-zoom-item style={{ width: fit * zoom }}>page</div>}
      </ZoomView>
    </>
  )
}

describe('a zoomable document', () => {
  it('zooms with Ctrl or Cmd and the wheel, and keeps the page from zooming', () => {
    const onZoom = vi.fn()
    render(<Harness zoom={1} onZoom={onZoom} onShown={() => {}} />)
    const pane = screen.getByRole('region', { name: 'Pages, 100%' })
    const ctrl = new WheelEvent('wheel', { deltaY: -100, ctrlKey: true, bubbles: true, cancelable: true })
    pane.dispatchEvent(ctrl)
    expect(ctrl.defaultPrevented).toBe(true)
    expect(onZoom.mock.calls[0][0]).toBeCloseTo(1.26, 2)
    const meta = new WheelEvent('wheel', { deltaY: 100, metaKey: true, bubbles: true, cancelable: true })
    pane.dispatchEvent(meta)
    // Out by the same notch, back to where it was.
    expect(onZoom.mock.calls[1][0]).toBeCloseTo(1, 2)
    // A plain wheel scrolls, as before.
    const plain = new WheelEvent('wheel', { deltaY: 100, bubbles: true, cancelable: true })
    pane.dispatchEvent(plain)
    expect(plain.defaultPrevented).toBe(false)
    expect(onZoom).toHaveBeenCalledTimes(2)
  })

  it('zooms with a pinch reported as gesture events', () => {
    const onZoom = vi.fn()
    render(<Harness zoom={1.5} onZoom={onZoom} onShown={() => {}} />)
    const pane = screen.getByRole('region')
    pane.dispatchEvent(new Event('gesturestart', { cancelable: true }))
    const change = Object.assign(new Event('gesturechange', { cancelable: true }), { scale: 2, clientX: 0, clientY: 0 })
    pane.dispatchEvent(change)
    expect(change.defaultPrevented).toBe(true)
    expect(onZoom).toHaveBeenCalledWith(3)
  })

  it('takes + and - and 0 when focused, and leaves the window its own zoom keys', () => {
    const onZoom = vi.fn()
    render(<Harness zoom={1} onZoom={onZoom} onShown={() => {}} />)
    const pane = screen.getByRole('region')
    fireEvent.keyDown(pane, { key: '+' })
    fireEvent.keyDown(pane, { key: '-' })
    fireEvent.keyDown(pane, { key: '=', ctrlKey: true })
    fireEvent.keyDown(pane, { key: '-', metaKey: true })
    expect(onZoom.mock.calls.map(c => c[0])).toEqual([1.1, 1])
    fireEvent.keyDown(pane, { key: '+' })
    fireEvent.keyDown(pane, { key: '0' })
    expect(onZoom.mock.calls.map(c => c[0])).toEqual([1.1, 1, 1.1, 1])
  })

  it('gives the Viewer a handle while shown, and says when it goes', () => {
    const onZoom = vi.fn()
    const onShown = vi.fn()
    const { unmount } = render(<Harness zoom={1} onZoom={onZoom} onShown={onShown} />)
    expect(onShown).toHaveBeenLastCalledWith(true)
    fireEvent.click(screen.getByText('Zoom to 200%'))
    expect(onZoom).toHaveBeenCalledWith(2)
    unmount()
    expect(onShown).toHaveBeenLastCalledWith(false)
  })

  it('holds the zoom within 25% and 400%', () => {
    const onZoom = vi.fn()
    render(<Harness zoom={4} onZoom={onZoom} onShown={() => {}} />)
    const pane = screen.getByRole('region')
    pane.dispatchEvent(new WheelEvent('wheel', { deltaY: -100, ctrlKey: true, cancelable: true }))
    expect(onZoom).not.toHaveBeenCalled()
  })
})
