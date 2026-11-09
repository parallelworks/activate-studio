import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  CAMERA0, centreCamera, centreView, freeCentre, neighborLines, project3d, openFromMap, SELECT_ZOOM, selectionReducer, SemanticMap, splitPath, tweenView, VIEW0,
  type MapNeighbor, type MapOverview,
} from '../src/components/CorpusMaps'

// jsdom has no PointerEvent; pointer events go as mouse events, which carry the position.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

describe('the selection', () => {
  const sourdough = { p: 'kitchen/bread/sourdough.md', x: 0.8, y: 0.7, z: 0.6, c: 1 }
  const rye: MapNeighbor = { p: 'kitchen/bread/rye.md', sim: 0.9, x: 0.85, y: 0.75, z: 0.65, c: 1 }

  it('selects a file, walks to a neighbor, keeps the same file as it is, and clears', () => {
    const a = selectionReducer(null, { type: 'select', file: sourdough })
    expect(a).toEqual(sourdough)
    expect(selectionReducer(a, { type: 'select', file: { ...sourdough } })).toBe(a)
    const b = selectionReducer(a, { type: 'neighbor', nb: rye })
    expect(b).toEqual({ p: 'kitchen/bread/rye.md', x: 0.85, y: 0.75, z: 0.65, c: 1 })
    // A nearest file with no place on the map is still selected; the view stays.
    expect(selectionReducer(b, { type: 'neighbor', nb: { p: 'far/away.md', sim: 0.5 } })).toEqual({ p: 'far/away.md', x: undefined, y: undefined, z: undefined, c: undefined })
    expect(selectionReducer(b, { type: 'clear' })).toBeNull()
  })

  it('opens a file after leaving full screen, and only opens it otherwise', () => {
    const steps: string[] = []
    openFromMap('a/b.md', true, f => steps.push(`full ${f}`), p => steps.push(`open ${p}`))
    expect(steps).toEqual(['full false', 'open a/b.md'])
    steps.length = 0
    openFromMap('a/b.md', false, f => steps.push(`full ${f}`), p => steps.push(`open ${p}`))
    expect(steps).toEqual(['open a/b.md'])
  })

  it('centres the view on a point and zooms in moderately, never out', () => {
    const v = centreView(VIEW0, { x: 250, y: 200 }, 600, 360)
    expect(v.k).toBe(SELECT_ZOOM)
    expect(250 * v.k + v.tx).toBe(300)
    expect(200 * v.k + v.ty).toBe(180)
    expect(centreView({ k: 8, tx: 0, ty: 0 }, { x: 250, y: 200 }, 600, 360).k).toBe(8)
    // Near an edge, as near the centre as the view allows.
    const edge = centreView(VIEW0, { x: 2, y: 2 }, 600, 360)
    expect(edge.tx).toBe(0)
    expect(edge.ty).toBe(0)
    const cam = centreCamera(CAMERA0, [0.2, -0.1, 0.3])
    expect(cam).toMatchObject({ yaw: CAMERA0.yaw, pitch: CAMERA0.pitch, k: SELECT_ZOOM, tx: 0.2, ty: -0.1, tz: 0.3 })
  })

  it('shows the selection beside its card: left of a card at the right, below one that spans the map', () => {
    expect(freeCentre(1200, 600, null)).toEqual({ x: 600, y: 300 })
    expect(freeCentre(1200, 600, { left: 912, bottom: 280, width: 280 })).toEqual({ x: 456, y: 300 })
    expect(freeCentre(380, 600, { left: 8, bottom: 280, width: 364 })).toEqual({ x: 190, y: 440 })
    // In 3D the camera puts the point there, at any turn.
    const t: [number, number, number] = [0.2, -0.1, 0.3]
    const cam = centreCamera(CAMERA0, t, SELECT_ZOOM, { x: -144, y: 0 }, 100)
    const p = project3d(t, cam, 600, 300, 100)
    expect(p.x).toBeCloseTo(456, 6)
    expect(p.y).toBeCloseTo(300, 6)
    expect(p.f).toBeCloseTo(1, 6)
  })

  it('moves from one view to the next in eased steps', () => {
    const a = VIEW0, b = { k: 4, tx: -900, ty: -540 }
    expect(tweenView(a, b, 0)).toEqual(a)
    expect(tweenView(a, b, 1)).toEqual(b)
    const mid = tweenView(a, b, 0.5)
    expect(mid.k).toBeCloseTo(2)
    expect(mid.tx).toBeCloseTo(-450)
  })

  it('draws a line to each nearest file: to its dot, to its place with a ring, or not at all', () => {
    const dots = new Map([['kitchen/bread/rye.md', { x: 10, y: 20 }]])
    const lines = neighborLines([
      rye,
      { p: 'kitchen/cake/sponge.md', sim: 0.7, x: 0.6, y: 0.6, z: 0.5, c: 1 },
      { p: 'nowhere.md', sim: 0.4 },
    ], p => dots.get(p) ?? null, (x, y) => ({ x: x * 100, y: y * 100 }))
    expect(lines).toEqual([
      { p: 'kitchen/bread/rye.md', x: 10, y: 20, sim: 0.9, c: 1, ring: false },
      { p: 'kitchen/cake/sponge.md', x: 60, y: 60, sim: 0.7, c: 1, ring: true },
    ])
    expect(splitPath('a/b/c.md')).toEqual({ name: 'c.md', folder: 'a/b' })
    expect(splitPath('top.md')).toEqual({ name: 'top.md', folder: '' })
  })
})

const OVERVIEW: MapOverview = {
  available: true, building: false, files: 9, shown: 4,
  clusters: [
    { c: 0, label: 'reports/turbines', count: 5, x: 0.2, y: 0.3, z: 0.4 },
    { c: 1, label: 'kitchen/bread', count: 4, x: 0.8, y: 0.7, z: 0.6 },
  ],
  points: [
    { p: 'reports/turbines/blade.md', x: 0.2, y: 0.3, z: 0.4, c: 0 },
    { p: 'reports/turbines/rotor.md', x: 0.25, y: 0.35, z: 0.45, c: 0 },
    { p: 'kitchen/bread/sourdough.md', x: 0.8, y: 0.7, z: 0.6, c: 1 },
    { p: 'kitchen/bread/rye.md', x: 0.85, y: 0.75, z: 0.65, c: 1 },
  ],
}
const NEIGHBORS: Record<string, MapNeighbor[]> = {
  'kitchen/bread/sourdough.md': [
    { p: 'kitchen/bread/rye.md', sim: 0.9, x: 0.85, y: 0.75, z: 0.65, c: 1 },
    { p: 'kitchen/cake/sponge.md', sim: 0.7, x: 0.6, y: 0.6, z: 0.5, c: 1 },
  ],
  'kitchen/bread/rye.md': [{ p: 'kitchen/bread/sourdough.md', sim: 0.9, x: 0.8, y: 0.7, z: 0.6, c: 1 }],
}

describe('selecting a file on the semantic map', () => {
  let asked: string[]
  beforeEach(() => {
    asked = []
    try { localStorage.clear() } catch { /* none */ }
    HTMLCanvasElement.prototype.getContext = (() => null) as never
    // Reduced motion: the view moves to a selection at once.
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: q.includes('reduce'), media: q, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      if (url === '/api/kb/semantic-map') return reply(OVERVIEW)
      if (url.startsWith('/api/kb/semantic-map/neighbors')) {
        const file = new URL(url, 'http://x').searchParams.get('file')!
        asked.push(file)
        return reply({ file, neighbors: NEIGHBORS[file] ?? [] })
      }
      return reply({}, 404)
    }))
  })
  afterEach(() => { vi.unstubAllGlobals() })

  const setup = async (opened: string[] = []) => {
    render(<SemanticMap onOpen={p => opened.push(p)} />)
    await screen.findByText(/files shown/)
    const canvas = () => document.querySelector('canvas.semantic-map') as HTMLCanvasElement
    // The canvas is 600 x 360 in jsdom, with a 26 px margin: (x, y) on the map.
    const at = (x: number, y: number) => ({ clientX: 26 + x * (600 - 52), clientY: 26 + (1 - y) * (360 - 52), button: 0, bubbles: true })
    const click = (x: number, y: number) => act(() => {
      canvas().dispatchEvent(new MouseEvent('pointerdown', at(x, y)))
      canvas().dispatchEvent(new MouseEvent('pointerup', at(x, y)))
    })
    const clickPx = (x: number, y: number) => act(() => {
      canvas().dispatchEvent(new MouseEvent('pointerdown', { clientX: x, clientY: y, button: 0, bubbles: true }))
      canvas().dispatchEvent(new MouseEvent('pointerup', { clientX: x, clientY: y, button: 0, bubbles: true }))
    })
    return { canvas, at, click, clickPx, opened }
  }

  it('selects a file: the view moves to it, its nearest files are drawn and listed, and the selection is announced', async () => {
    const { canvas, click } = await setup()
    // Links are off in 2D, and the selected file's lines are drawn all the same.
    expect(screen.getByRole('button', { name: 'Show links' })).toBeInTheDocument()
    click(0.8, 0.7)
    await waitFor(() => expect(canvas().dataset.selected).toBe('kitchen/bread/sourdough.md'))
    expect(asked).toEqual(['kitchen/bread/sourdough.md'])
    await waitFor(() => expect(canvas().dataset.zoom).toBe(SELECT_ZOOM.toFixed(2)))
    await waitFor(() => expect(canvas().dataset.selectedLines).toBe('2'))
    const card = screen.getByRole('group', { name: 'Selected file sourdough.md' })
    expect(within(card).getByText('kitchen/bread')).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'rye.md, similarity 0.90' })).toBeInTheDocument()
    expect(within(card).getByRole('button', { name: 'sponge.md, similarity 0.70' })).toBeInTheDocument()
    expect(screen.getByText('Selected sourdough.md in kitchen/bread. 2 nearest files shown. Enter opens it in the Library.')).toHaveAttribute('aria-live', 'polite')
    // Hovering another file leaves the selected file's lines drawn.
    act(() => { canvas().dispatchEvent(new MouseEvent('pointermove', { clientX: 5, clientY: 5, bubbles: true })) })
    expect(canvas().dataset.selectedLines).toBe('2')
  })

  it('walks to a neighbor from the card, and back from the map', async () => {
    const { canvas, click, clickPx } = await setup()
    click(0.8, 0.7)
    fireEvent.click(await screen.findByRole('button', { name: 'rye.md, similarity 0.90' }))
    await waitFor(() => expect(canvas().dataset.selected).toBe('kitchen/bread/rye.md'))
    expect(await screen.findByRole('group', { name: 'Selected file rye.md' })).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'sourdough.md, similarity 0.90' })).toBeInTheDocument()
    expect(asked).toEqual(['kitchen/bread/sourdough.md', 'kitchen/bread/rye.md'])
    // The view is centred on rye at zoom 3: sourdough's dot is where the map puts it.
    const k = SELECT_ZOOM
    const base = (x: number, y: number) => ({ x: 26 + x * 548, y: 26 + (1 - y) * 308 })
    const r = base(0.85, 0.75), s = base(0.8, 0.7)
    clickPx(300 + (s.x - r.x) * k, 180 + (s.y - r.y) * k)
    await waitFor(() => expect(canvas().dataset.selected).toBe('kitchen/bread/sourdough.md'))
  })

  it('clears with Escape, a click on an empty spot, and the close button', async () => {
    const { canvas, click, clickPx } = await setup()
    click(0.8, 0.7)
    await screen.findByRole('group', { name: 'Selected file sourdough.md' })
    fireEvent.keyDown(canvas(), { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('group', { name: /Selected file/ })).toBeNull())
    expect(canvas().dataset.selected).toBe('')
    fireEvent.keyDown(canvas(), { key: '0' })
    click(0.8, 0.7)
    await screen.findByRole('group', { name: 'Selected file sourdough.md' })
    clickPx(3, 3)
    await waitFor(() => expect(screen.queryByRole('group', { name: /Selected file/ })).toBeNull())
    fireEvent.keyDown(canvas(), { key: '0' })
    click(0.8, 0.7)
    const card = await screen.findByRole('group', { name: 'Selected file sourdough.md' })
    fireEvent.click(within(card).getByRole('button', { name: 'Clear selection' }))
    await waitFor(() => expect(screen.queryByRole('group', { name: /Selected file/ })).toBeNull())
    expect(document.activeElement).toBe(canvas())
  })

  it('opens the selected file with Enter, and a file with a double-click', async () => {
    const opened: string[] = []
    const { canvas, click, at } = await setup(opened)
    click(0.8, 0.7)
    await screen.findByRole('group', { name: 'Selected file sourdough.md' })
    fireEvent.keyDown(canvas(), { key: 'Enter' })
    expect(opened).toEqual(['kitchen/bread/sourdough.md'])
    fireEvent.keyDown(canvas(), { key: '0' })
    fireEvent.keyDown(canvas(), { key: 'Escape' })
    await waitFor(() => expect(canvas().dataset.zoom).toBe('1.00'))
    act(() => { canvas().dispatchEvent(new MouseEvent('dblclick', at(0.2, 0.3))) })
    expect(opened).toEqual(['kitchen/bread/sourdough.md', 'reports/turbines/blade.md'])
  })

  it('in full screen, Escape clears the selection first, and Open in Library leaves full screen and opens the file', async () => {
    const opened: string[] = []
    const { canvas } = await setup(opened)
    fireEvent.click(screen.getByRole('button', { name: 'Full screen' }))
    const dialog = await screen.findByRole('dialog', { name: 'Semantic map' })
    // The canvas filling the window measures as jsdom's 600 x 480 default.
    await waitFor(() => expect(canvas().closest('.map-full')).not.toBeNull())
    const clickFull = (x: number, y: number) => act(() => {
      const p = { clientX: 26 + x * 548, clientY: 26 + (1 - y) * (480 - 52), button: 0, bubbles: true }
      canvas().dispatchEvent(new MouseEvent('pointerdown', p))
      canvas().dispatchEvent(new MouseEvent('pointerup', p))
    })
    clickFull(0.8, 0.7)
    await within(dialog).findByRole('group', { name: 'Selected file sourdough.md' })
    act(() => { window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })) })
    await waitFor(() => expect(screen.queryByRole('group', { name: /Selected file/ })).toBeNull())
    expect(screen.getByRole('dialog', { name: 'Semantic map' })).toBeInTheDocument()
    fireEvent.keyDown(canvas(), { key: '0' })
    clickFull(0.8, 0.7)
    fireEvent.click(await within(screen.getByRole('dialog')).findByRole('button', { name: 'Open in Library' }))
    expect(opened).toEqual(['kitchen/bread/sourdough.md'])
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Semantic map' })).toBeNull())
  })

  it('moves the 3D camera to the selected file and zooms in', async () => {
    const { canvas } = await setup()
    fireEvent.click(screen.getByRole('button', { name: '3D' }))
    await waitFor(() => expect(canvas().dataset.mode).toBe('3d'))
    expect(canvas().dataset.zoom).toBe('1.00')
    // Find a file by hovering across the canvas, then click it.
    let hit: { x: number; y: number } | null = null
    for (let y = 0; y < 360 && !hit; y += 4) for (let x = 0; x < 600 && !hit; x += 4) {
      act(() => { canvas().dispatchEvent(new MouseEvent('pointermove', { clientX: x, clientY: y, bubbles: true })) })
      if (document.querySelector('.map-tip')?.textContent?.endsWith('.md')) hit = { x, y }
    }
    expect(hit).not.toBeNull()
    act(() => {
      canvas().dispatchEvent(new MouseEvent('pointerdown', { clientX: hit!.x, clientY: hit!.y, button: 0, bubbles: true }))
      canvas().dispatchEvent(new MouseEvent('pointerup', { clientX: hit!.x, clientY: hit!.y, button: 0, bubbles: true }))
    })
    await waitFor(() => expect(canvas().dataset.selected).not.toBe(''))
    await waitFor(() => expect(canvas().dataset.zoom).toBe(SELECT_ZOOM.toFixed(2)))
    expect(screen.getByRole('group', { name: /Selected file/ })).toBeInTheDocument()
  })
})
