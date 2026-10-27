import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { currentLibrary, setLibrary, type PublicLibrary } from '../src/api'
import {
  CAMERA0, clampView, defaultMapLibrary, nearestIndex, pan3d, project3d, SemanticMap, shortLabels, TopologyMap, VIEW0, viewPlane, zoom3d, zoomAround,
  type Camera, type MapCluster, type MapOverview,
} from '../src/components/CorpusMaps'
import { resetMapLibrariesForTests } from '../src/mapLibrary'
import { resetScopesForTests } from '../src/scope'

// A radio forwards its press to its hidden input as a PointerEvent, which
// jsdom does not have.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

describe('zoom and pan', () => {
  it('zooms about the pointer: the point under it stays where it is', () => {
    const v = zoomAround(VIEW0, 150, 90, 2, 600, 360)
    expect(v.k).toBe(2)
    // The base point under (150, 90) was (150, 90); it is still there.
    expect(150 * v.k + v.tx).toBe(150)
    expect(90 * v.k + v.ty).toBe(90)
    expect(zoomAround(v, 0, 0, 1 / 4, 600, 360)).toEqual({ k: 1, tx: 0, ty: 0 })
  })

  it('keeps the content over the view, and the zoom between 1 and 40', () => {
    expect(clampView({ k: 2, tx: 100, ty: -1000 }, 600, 360)).toEqual({ k: 2, tx: 0, ty: -360 })
    expect(clampView({ k: 0.5, tx: -5, ty: 3 }, 600, 360).k).toBe(1)
    expect(zoomAround(VIEW0, 0, 0, 1000, 600, 360).k).toBe(40)
  })
})

describe('3D', () => {
  const cam: Camera = { yaw: 0, pitch: 0, dist: 2, k: 1, tx: 0, ty: 0, tz: 0 }
  it('projects with perspective: the centre plane at scale, nearer points larger', () => {
    const a = project3d([0.5, 0, 0], cam, 300, 200, 100)
    expect(a).toMatchObject({ x: 350, y: 200, depth: 2, f: 1 })
    const near = project3d([0.5, 0, -0.5], cam, 300, 200, 100)
    expect(near.depth).toBe(1.5)
    expect(near.x).toBeCloseTo(300 + 0.5 * (2 / 1.5) * 100)
    expect(project3d([0, 0.5, 0], cam, 300, 200, 100).y).toBe(150)
  })

  it('turns with the camera: a quarter turn brings a point from the side to the front', () => {
    const turned = project3d([0.5, 0, 0], { ...cam, yaw: Math.PI / 2 }, 300, 200, 100)
    expect(turned.x).toBeCloseTo(300)
    expect(turned.depth).toBeCloseTo(1.5)
    const tilted = project3d([0, 0, -0.5], { ...cam, pitch: Math.PI / 2 }, 300, 200, 100)
    expect(tilted.y).toBeCloseTo(200 - 0.5 * 100)
  })

  // A turned and tilted camera, a 600 x 400 canvas centred at (300, 200), 100 px per unit.
  const turned: Camera = { ...CAMERA0 }
  const onTargetPlane = (c: Camera, sx: number, sy: number): [number, number, number] => {
    const [a, b, d] = viewPlane(c, (sx - 300) / (100 * c.k), -(sy - 200) / (100 * c.k))
    return [c.tx + a, c.ty + b, c.tz + d]
  }

  it('zooms by magnifying: the point under the pointer stays put, the rest moves away from it', () => {
    const under = onTargetPlane(turned, 380, 150)
    const other = onTargetPlane(turned, 330, 230)
    const before = project3d(other, turned, 300, 200, 100)
    expect(project3d(under, turned, 300, 200, 100).x).toBeCloseTo(380, 6)
    for (const factor of [2, 1.6, 5]) {
      const z = zoom3d(turned, 380, 150, factor, 300, 200, 100)
      expect(z.k).toBeCloseTo(factor)
      const p = project3d(under, z, 300, 200, 100)
      expect(p.x).toBeCloseTo(380, 6)
      expect(p.y).toBeCloseTo(150, 6)
      // A point on the same plane: its distance from the pointer grows with k.
      const q = project3d(other, z, 300, 200, 100)
      expect(Math.hypot(q.x - 380, q.y - 150)).toBeCloseTo(Math.hypot(before.x - 380, before.y - 150) * factor, 6)
    }
    // Between 1 and 40; back at 1 the map is centred again.
    const deep = zoom3d(turned, 380, 150, 1000, 300, 200, 100)
    expect(deep.k).toBe(40)
    expect(zoom3d(deep, 10, 10, 1 / 1000, 300, 200, 100)).toMatchObject({ k: 1, tx: 0, ty: 0, tz: 0 })
  })

  it('moves by panning: a point on the target plane follows the pointer exactly, at any zoom', () => {
    const z = zoom3d(turned, 360, 220, 3, 300, 200, 100)
    const p = onTargetPlane(z, 320, 180)
    const moved = pan3d(z, 25, -15, 100)
    const q = project3d(p, moved, 300, 200, 100)
    expect(q.x).toBeCloseTo(345, 6)
    expect(q.y).toBeCloseTo(165, 6)
  })

  it('hovers the nearest point within reach, the one in front where two meet', () => {
    const xs = [10, 50, 52], ys = [10, 50, 50]
    expect(nearestIndex(xs, ys, 49, 50, 10)).toBe(1)
    expect(nearestIndex(xs, ys, 200, 200, 10)).toBe(-1)
    // Drawn order 2 then 1: at equal distance the later (front) one wins.
    expect(nearestIndex([50, 50], [50, 50], 50, 50, 10, [1, 0])).toBe(0)
    expect(nearestIndex([50, 50], [50, 50], 50, 50, 10, [0, 1])).toBe(1)
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

describe('the semantic map', () => {
  let calls: string[]
  beforeEach(() => {
    calls = []
    HTMLCanvasElement.prototype.getContext = (() => null) as never
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      calls.push(url)
      if (url === '/api/kb/semantic-map') return reply(OVERVIEW)
      const m = /cluster\/(\d+)\?offset=(\d+)/.exec(url)
      if (m) {
        const c = Number(m[1])
        const all = Array.from({ length: OVERVIEW.clusters![c].count }, (_, i) => ({ p: `${OVERVIEW.clusters![c].label}/file-${i}.md`, x: 0.1 + i * 0.05, y: 0.2, z: 0.3 }))
        return reply({ c, label: OVERVIEW.clusters![c].label, count: all.length, offset: Number(m[2]), points: all.slice(Number(m[2])) })
      }
      return reply({}, 404)
    }))
  })
  afterEach(() => { vi.unstubAllGlobals() })

  it('shows the clusters with a sample, opens a cluster for all of its files, and goes back', async () => {
    render(<SemanticMap onOpen={() => {}} />)
    expect(await screen.findByText('4 of 9 files shown; click a cluster for all of its files.')).toBeInTheDocument()
    const chip = screen.getByRole('button', { name: 'reports/turbines, 5 files' })
    fireEvent.click(chip)
    expect(await screen.findByText('5 of 5 files in reports/turbines.')).toBeInTheDocument()
    expect(calls).toContain('/api/kb/semantic-map/cluster/0?offset=0&limit=2000')
    fireEvent.click(screen.getByRole('button', { name: 'Back to all clusters' }))
    // Back in the overview, the cluster's files loaded since stay on the map.
    expect(await screen.findByText('7 of 9 files shown; click a cluster for all of its files.')).toBeInTheDocument()
  })

  it('zooms from the keyboard and resets; switches to 3D', async () => {
    render(<SemanticMap onOpen={() => {}} />)
    await screen.findByText(/files shown/)
    const canvas = document.querySelector('canvas.semantic-map') as HTMLCanvasElement
    await waitFor(() => expect(canvas.dataset.zoom).toBe('1.00'))
    fireEvent.keyDown(canvas, { key: '+' })
    await waitFor(() => expect(canvas.dataset.zoom).toBe('1.60'))
    fireEvent.keyDown(canvas, { key: '0' })
    await waitFor(() => expect(canvas.dataset.zoom).toBe('1.00'))
    fireEvent.click(screen.getByRole('button', { name: 'Zoom in' }))
    await waitFor(() => expect(canvas.dataset.zoom).toBe('1.60'))
    fireEvent.click(screen.getByRole('button', { name: '3D' }))
    await waitFor(() => expect(canvas.dataset.mode).toBe('3d'))
    expect(canvas.dataset.zoom).toBe('1.00')
    fireEvent.keyDown(canvas, { key: '+' })
    await waitFor(() => expect(Number(canvas.dataset.zoom)).toBeGreaterThan(1.5))
    expect(screen.getByRole('button', { name: 'Turn slowly' })).toBeInTheDocument()
  })

  it('names the file under the pointer, and opens it on a click', async () => {
    const opened: string[] = []
    render(<SemanticMap onOpen={p => opened.push(p)} />)
    await screen.findByText(/files shown/)
    const canvas = document.querySelector('canvas.semantic-map') as HTMLCanvasElement
    // jsdom lays nothing out: the canvas is 600 x 360, with a 26 px margin.
    // jsdom has no PointerEvent, so pointer events go as mouse events, which carry the position.
    const pointer = (type: string, x: number, y: number) => act(() => {
      canvas.dispatchEvent(new MouseEvent(type, { clientX: 26 + x * (600 - 52), clientY: 26 + (1 - y) * (360 - 52), button: 0, bubbles: true }))
    })
    pointer('pointermove', 0.8, 0.7)
    expect(await screen.findByText('kitchen/bread/sourdough.md')).toBeInTheDocument()
    pointer('pointerdown', 0.8, 0.7)
    pointer('pointerup', 0.8, 0.7)
    expect(opened).toEqual(['kitchen/bread/sourdough.md'])
  })

  it('fills the window, and Escape brings it back', async () => {
    render(<SemanticMap onOpen={() => {}} />)
    await screen.findByText(/files shown/)
    fireEvent.click(screen.getByRole('button', { name: 'Full screen' }))
    const dialog = await screen.findByRole('dialog', { name: 'Semantic map' })
    expect(within(dialog).getByRole('button', { name: 'Close full screen' })).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: '2D' })).toBeInTheDocument()
    act(() => { window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' })) })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Semantic map' })).toBeNull())
  })

  it('says why there is no map', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply({ available: false, reason: 'off', building: false })))
    render(<SemanticMap onOpen={() => {}} />)
    expect(await screen.findByText('The main folder has no search by meaning. Set its depth to Meaning on documents to draw it here; that reads its documents once, in the background.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Set to Meaning on documents' })).toBeInTheDocument()
    expect(document.querySelector('canvas')).toBeNull()
  })
})

describe('the corpus topology', () => {
  beforeEach(() => {
    HTMLCanvasElement.prototype.getContext = (() => null) as never
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('fetch', vi.fn(async () => reply({ available: true, dirs: [
      { dir: 'reports', files: 3, bytes: 3000 }, { dir: 'reports/2026', files: 9, bytes: 90000 }, { dir: 'kitchen', files: 4, bytes: 40000 },
    ] })))
  })
  afterEach(() => { vi.unstubAllGlobals() })

  it('zooms and resets from the keyboard, opens a directory from the legend, and fills the window', async () => {
    render(<TopologyMap />)
    const chip = await screen.findByRole('button', { name: /reports/ })
    const canvas = document.querySelector('canvas.treemap-canvas') as HTMLCanvasElement
    await waitFor(() => expect(canvas.dataset.zoom).toBe('1.00'))
    fireEvent.keyDown(canvas, { key: '=' })
    fireEvent.keyDown(canvas, { key: '=' })
    await waitFor(() => expect(canvas.dataset.zoom).toBe('2.56'))
    fireEvent.keyDown(canvas, { key: '0' })
    await waitFor(() => expect(canvas.dataset.zoom).toBe('1.00'))
    // A new directory starts from the whole of it.
    fireEvent.keyDown(canvas, { key: '+' })
    fireEvent.click(chip)
    expect(await screen.findByRole('button', { name: '/reports' })).toBeInTheDocument()
    await waitFor(() => expect((document.querySelector('canvas.treemap-canvas') as HTMLCanvasElement).dataset.zoom).toBe('1.00'))
    fireEvent.click(screen.getByRole('button', { name: 'Full screen' }))
    const dialog = await screen.findByRole('dialog', { name: 'Corpus topology' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Close full screen' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })
})

describe('cluster names on the map', () => {
  const cl = (c: number, label: string, terms?: string[]): MapCluster => ({ c, label, count: 10 - c, x: 0, y: 0, z: 0, ...(terms ? { terms, folder: 'x' } : {}) })
  it('keeps a name made of words; shows folders by their own names, with parents where two would read alike', () => {
    const names = shortLabels([
      cl(0, 'kubernetes, helm, ingress', ['kubernetes', 'helm', 'ingress']),
      cl(1, 'projects/pw/docs + archive/gs/docs'),
      cl(2, 'reports/2026/q3'),
      cl(3, 'a/team/notes'),
      cl(4, 'b/team/notes'),
      cl(5, 'a very long run of words that goes on, and on', ['a', 'b']),
    ])
    expect(names.get(0)).toBe('kubernetes, helm, ingress')
    expect(names.get(1)).toBe('pw/docs + gs/docs')
    expect(names.get(2)).toBe('q3')
    expect(names.get(3)).toBe('a/team/notes')
    expect(names.get(4)).toBe('b/team/notes')
    expect(names.get(5)).toHaveLength(30)
    expect(new Set(names.values()).size).toBe(6)
  })
})

// A drawing context that keeps count of what is drawn.
const recorder = () => {
  const counts = { lineTo: 0, arc: 0 }
  const g = new Proxy({} as Record<string, unknown>, {
    get: (t, k: string) => {
      if (k === 'measureText') return () => ({ width: 40 })
      if (k in counts) return () => { counts[k as keyof typeof counts]++ }
      if (k in t) return t[k]
      return () => {}
    },
    set: (t, k: string, v) => { t[k] = v; return true },
  })
  return { counts, g }
}

describe('links on the semantic map', () => {
  const WITH_LINKS: MapOverview = {
    ...OVERVIEW,
    clusters: OVERVIEW.clusters!.map(c => ({ ...c, folder: c.label })),
    links: [{ a: 0, b: 1, sim: 0.62 }],
  }
  let rec: ReturnType<typeof recorder>
  let overview: MapOverview
  beforeEach(() => {
    rec = recorder()
    overview = WITH_LINKS
    try { localStorage.clear() } catch { /* none */ }
    HTMLCanvasElement.prototype.getContext = (() => rec.g) as never
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      if (url === '/api/kb/semantic-map') return reply(overview)
      if (url.startsWith('/api/kb/semantic-map/neighbors')) {
        expect(url).toContain(encodeURIComponent('kitchen/bread/sourdough.md'))
        return reply({ file: 'kitchen/bread/sourdough.md', neighbors: [
          { p: 'kitchen/bread/rye.md', sim: 0.9, x: 0.85, y: 0.75, z: 0.65, c: 1 },
          { p: 'kitchen/cake/sponge.md', sim: 0.7, x: 0.6, y: 0.6, z: 0.5, c: 1 },
        ] })
      }
      return reply({}, 404)
    }))
  })
  afterEach(() => { vi.unstubAllGlobals() })

  it('draws links in 3D by default and not in 2D; the switch is kept per view', async () => {
    const { unmount } = render(<SemanticMap onOpen={() => {}} />)
    await screen.findByText(/files shown/)
    const canvas = () => document.querySelector('canvas.semantic-map') as HTMLCanvasElement
    await waitFor(() => expect(canvas().dataset.links).toBe('0'))
    expect(screen.getByRole('button', { name: 'Show links' })).toHaveAttribute('aria-pressed', 'false')
    expect(rec.counts.lineTo).toBe(0)
    fireEvent.click(screen.getByRole('button', { name: '3D' }))
    await waitFor(() => expect(canvas().dataset.mode).toBe('3d'))
    await waitFor(() => expect(canvas().dataset.links).toBe('1'))
    expect(rec.counts.lineTo).toBeGreaterThan(0)
    // Hidden in 3D: no lines, and remembered for the next visit.
    fireEvent.click(screen.getByRole('button', { name: 'Hide links' }))
    await waitFor(() => expect(canvas().dataset.links).toBe('0'))
    rec.counts.lineTo = 0
    fireEvent.keyDown(canvas(), { key: '+' })
    await waitFor(() => expect(canvas().dataset.zoom).toBe('1.60'))
    expect(rec.counts.lineTo).toBe(0)
    unmount()
    render(<SemanticMap onOpen={() => {}} />)
    await screen.findByText(/files shown/)
    fireEvent.click(screen.getByRole('button', { name: '3D' }))
    await waitFor(() => expect(canvas().dataset.mode).toBe('3d'))
    expect(screen.getByRole('button', { name: 'Show links' })).toBeInTheDocument()
  })

  it('draws no links for a map made before there were any', async () => {
    overview = OVERVIEW
    render(<SemanticMap onOpen={() => {}} />)
    await screen.findByText(/files shown/)
    fireEvent.click(screen.getByRole('button', { name: '3D' }))
    const canvas = document.querySelector('canvas.semantic-map') as HTMLCanvasElement
    await waitFor(() => expect(canvas.dataset.mode).toBe('3d'))
    expect(canvas.dataset.links).toBe('0')
    expect(rec.counts.lineTo).toBe(0)
  })

  it('draws lines from a hovered file to its nearest files by meaning, with a ring for one not on the map yet', async () => {
    render(<SemanticMap onOpen={() => {}} />)
    await screen.findByText(/files shown/)
    fireEvent.click(screen.getByRole('button', { name: 'Show links' }))
    const canvas = document.querySelector('canvas.semantic-map') as HTMLCanvasElement
    await waitFor(() => expect(canvas.dataset.links).toBe('1'))
    const linesBefore = rec.counts.lineTo
    act(() => {
      canvas.dispatchEvent(new MouseEvent('pointermove', { clientX: 26 + 0.8 * (600 - 52), clientY: 26 + 0.3 * (360 - 52), button: 0, bubbles: true }))
    })
    expect(await screen.findByText(/lines to its 2 nearest files by meaning/)).toBeInTheDocument()
    await waitFor(() => expect(rec.counts.lineTo - linesBefore).toBeGreaterThanOrEqual(2))
  })
})

describe('legends', () => {
  const MANY: MapOverview = {
    available: true, building: false, files: 12, shown: 12,
    clusters: Array.from({ length: 12 }, (_, c) => ({ c, label: `topic${c}, words`, terms: [`topic${c}`, 'words'], folder: `f${c}`, count: 20 - c, x: c / 12, y: 0.5, z: 0.5 })),
    points: Array.from({ length: 12 }, (_, c) => ({ p: `f${c}/a.md`, x: c / 12, y: 0.5, z: 0.5, c })),
  }
  beforeEach(() => {
    HTMLCanvasElement.prototype.getContext = (() => null) as never
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  })
  afterEach(() => { vi.unstubAllGlobals() })

  it('shows the largest eight, then the rest on request; hovering one highlights it on the map', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply(MANY)))
    render(<SemanticMap onOpen={() => {}} />)
    await screen.findByText(/files shown/)
    const chips = () => screen.queryAllByRole('button', { name: /^topic\d+, words, \d+ files$/ })
    expect(chips()).toHaveLength(8)
    expect(chips()[0]).toHaveAccessibleName('topic0, words, 20 files')
    fireEvent.click(screen.getByRole('button', { name: '+ 4 more' }))
    expect(chips()).toHaveLength(12)
    expect(screen.getByRole('button', { name: 'Show fewer' })).toHaveAttribute('aria-expanded', 'true')
    fireEvent.click(screen.getByRole('button', { name: 'Show fewer' }))
    expect(chips()).toHaveLength(8)
    const canvas = document.querySelector('canvas.semantic-map') as HTMLCanvasElement
    fireEvent.mouseEnter(chips()[2])
    await waitFor(() => expect(canvas.dataset.highlight).toBe('2'))
    fireEvent.mouseLeave(chips()[2].parentElement!)
    await waitFor(() => expect(canvas.dataset.highlight).toBe(''))
    // The folder name is kept on hover.
    expect(chips()[2]).toHaveAttribute('title', 'topic2, words · f2')
  })

  it('folds a long topology legend the same way, and highlights a directory from it', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => reply({ available: true, dirs: Array.from({ length: 11 }, (_, i) => ({ dir: `d${i}`, files: 1, bytes: 1000 * (20 - i) })) })))
    render(<TopologyMap />)
    await screen.findByRole('button', { name: /^d0\b/ })
    expect(screen.queryAllByRole('button', { name: /^d\d+ / })).toHaveLength(8)
    fireEvent.click(screen.getByRole('button', { name: '+ 3 more' }))
    expect(screen.queryAllByRole('button', { name: /^d\d+ / })).toHaveLength(11)
    const canvas = document.querySelector('canvas.treemap-canvas') as HTMLCanvasElement
    fireEvent.mouseEnter(screen.getByRole('button', { name: /^d3 / }))
    await waitFor(() => expect(canvas.dataset.highlight).toBe('d3'))
    fireEvent.click(screen.getByRole('button', { name: /^d3 / }))
    expect(await screen.findByRole('button', { name: '/d3' })).toBeInTheDocument()
  })
})

it('starts 3D from a camera a little turned and tilted', () => {
  expect(CAMERA0.yaw).toBeGreaterThan(0)
  expect(CAMERA0.pitch).toBeGreaterThan(0)
})

const lib = (id: string, label: string, extra: Partial<PublicLibrary> = {}): PublicLibrary => ({
  id, label, primary: id === 'kb', writable: id === 'kb', pinned: id === 'kb', source: true,
  caps: { index: true, fullText: true, vectors: false }, meaning: 'off', depth: 'ocr', ...extra,
})
const LIBS = [
  lib('kb', 'Notes', { caps: { index: true, fullText: true, vectors: true }, meaning: 'documents', depth: 'meaning-documents' }),
  lib('papers', 'Papers', { caps: { index: true, fullText: true, vectors: true }, meaning: 'documents', depth: 'meaning-documents' }),
  lib('desk', 'Desktop'),
  lib('lab', 'Lab index', { meaning: null, depth: null }),
  lib('far', 'Scratch', { site: 'cluster-a', meaning: null, depth: null }),
]
const pick = async (name: string) => {
  fireEvent.click(screen.getByRole('button', { name: /^Library: / }))
  const group = await screen.findByRole('radiogroup', { name: 'Library to show' })
  fireEvent.click(within(group).getByRole('radio', { name }))
}

describe('the library a map starts on', () => {
  it('is the largest map, else a library with vectors, else the main folder', () => {
    const maps = [
      { id: 'kb', available: true, files: 5 },
      { id: 'papers', available: true, files: 12_000 },
      { id: 'desk', available: false, files: 0 },
    ]
    expect(defaultMapLibrary(maps, LIBS)).toBe('papers')
    expect(defaultMapLibrary(maps, LIBS.filter(l => l.id !== 'papers')), 'a library gone is not picked').toBe('kb')
    expect(defaultMapLibrary([], [LIBS[2], LIBS[1]])).toBe('papers')
    expect(defaultMapLibrary([], [LIBS[2]])).toBe('kb')
  })
})

describe('maps of each library', () => {
  let calls: { url: string; method: string; body?: string }[]
  let overviews: Record<string, MapOverview>
  beforeEach(() => {
    calls = []
    resetMapLibrariesForTests()
    resetScopesForTests()
    act(() => setLibrary('kb'))
    HTMLCanvasElement.prototype.getContext = (() => null) as never
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
    overviews = {
      '/api/kb/semantic-map': OVERVIEW,
      '/api/libraries/papers/semantic-map': { ...OVERVIEW, files: 12_000, library: { id: 'papers', label: 'Papers', kind: 'built', depth: 'meaning-documents', vectors: true } },
      '/api/libraries/desk/semantic-map': { available: false, reason: 'off', building: false, library: { id: 'desk', label: 'Desktop', kind: 'built', depth: 'ocr', vectors: false } },
      '/api/libraries/lab/semantic-map': { available: false, reason: 'connected', library: { id: 'lab', label: 'Lab index', kind: 'connected', depth: null, vectors: false } },
      '/api/libraries/far/semantic-map': { available: false, reason: 'remote', library: { id: 'far', label: 'Scratch', kind: 'remote', depth: null, vectors: false } },
    }
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
      const url = String(input)
      calls.push({ url, method: init?.method ?? 'GET', body: init?.body as string | undefined })
      if (url === '/api/semantic-maps') {
        return reply({ maps: [{ id: 'kb', available: true, files: 9 }, { id: 'papers', available: true, files: 12_000 }, { id: 'desk', available: false, files: 0 }] })
      }
      if (url in overviews) return reply(overviews[url])
      if (url === '/api/libraries/desk/depth' && init?.method === 'PUT') {
        overviews['/api/libraries/desk/semantic-map'] = { available: false, reason: 'none', building: false }
        return reply({ id: 'desk', depth: 'meaning-documents', meaning: 'documents' })
      }
      const topo = /^\/api\/(?:kb|libraries\/(\w+))\/topology(?:\?dir=(.*))?$/.exec(url)
      if (topo) {
        const name = topo[1] ?? 'kb'
        const dir = topo[2] ? decodeURIComponent(topo[2]) : ''
        if (name === 'lab') return reply({ available: false, reason: 'none' })
        if (!dir) return reply({ available: true, levels: 2, root: '', dirs: [{ dir: `${name}-top`, files: 2, bytes: 2000 }, { dir: `${name}-top/deep`, files: 9, bytes: 90000 }] })
        return reply({ available: true, levels: 2, root: dir, dirs: [{ dir: `${dir}/below`, files: 9, bytes: 90000 }] })
      }
      return reply({}, 404)
    }))
  })
  afterEach(() => { vi.unstubAllGlobals() })

  it('the semantic map starts on the largest map, switches library, and keeps the choice', async () => {
    const { unmount } = render(<SemanticMap onOpen={() => {}} libraries={LIBS} />)
    expect(await screen.findByText('4 of 12,000 files shown; click a cluster for all of its files.')).toBeInTheDocument()
    expect(calls.map(c => c.url)).toContain('/api/libraries/papers/semantic-map')
    expect(screen.getByRole('button', { name: 'Library: Papers' })).toBeInTheDocument()
    await pick('Notes')
    expect(await screen.findByText('4 of 9 files shown; click a cluster for all of its files.')).toBeInTheDocument()
    // A cluster of the main folder is asked for at the main folder's address.
    fireEvent.click(screen.getByRole('button', { name: 'reports/turbines, 5 files' }))
    await waitFor(() => expect(calls.map(c => c.url)).toContain('/api/kb/semantic-map/cluster/0?offset=0&limit=2000'))
    unmount()
    render(<SemanticMap onOpen={() => {}} libraries={LIBS} />)
    expect(await screen.findByText('4 of 9 files shown; click a cluster for all of its files.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Library: Notes' })).toBeInTheDocument()
  })

  it('says why a library has no map, and sets the depth of one the Studio builds', async () => {
    localStorage.setItem('ade-map-library-semantic', 'desk')
    render(<SemanticMap onOpen={() => {}} libraries={LIBS} />)
    expect(await screen.findByText('Desktop has no search by meaning. Set its depth to Meaning on documents to draw it here; that reads its documents once, in the background.')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Set to Meaning on documents' }))
    expect(await screen.findByText('Reading the documents of Desktop for search by meaning; the map appears when that is done.')).toBeInTheDocument()
    expect(calls.find(c => c.method === 'PUT')).toEqual({ url: '/api/libraries/desk/depth', method: 'PUT', body: JSON.stringify({ depth: 'meaning-documents' }) })
    await pick('Lab index (read only)')
    expect(await screen.findByText('This index was built elsewhere and has no meaning vectors the Studio can read.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Set to Meaning on documents' })).toBeNull()
    await pick('Scratch (on cluster-a)')
    expect(await screen.findByText('Scratch is on another system, which keeps its vectors, so there is no map of it here.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Set to Meaning on documents' })).toBeNull()
  })

  it('opens a file of another library in that library', async () => {
    const opened: string[] = []
    render(<SemanticMap onOpen={p => opened.push(p)} libraries={LIBS} />)
    await screen.findByText(/of 12,000 files shown/)
    const canvas = document.querySelector('canvas.semantic-map') as HTMLCanvasElement
    const pointer = (type: string, x: number, y: number) => act(() => {
      canvas.dispatchEvent(new MouseEvent(type, { clientX: 26 + x * (600 - 52), clientY: 26 + (1 - y) * (360 - 52), button: 0, bubbles: true }))
    })
    pointer('pointermove', 0.8, 0.7)
    await screen.findByText('kitchen/bread/sourdough.md')
    pointer('pointerdown', 0.8, 0.7)
    pointer('pointerup', 0.8, 0.7)
    expect(opened).toEqual(['kitchen/bread/sourdough.md'])
    expect(currentLibrary()).toBe('papers')
  })

  it('the topology starts on the main folder, switches library, reads below its last level, and says when there is no index', async () => {
    render(<TopologyMap libraries={LIBS} />)
    expect(await screen.findByRole('button', { name: /kb-top/ })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Library: Notes' })).toBeInTheDocument()
    await pick('Scratch (on cluster-a)')
    const chip = await screen.findByRole('button', { name: /far-top/ })
    expect(calls.map(c => c.url)).toContain('/api/libraries/far/topology')
    // Opening a folder at the last level fetched asks for the levels below it.
    fireEvent.click(chip)
    await waitFor(() => expect(calls.map(c => c.url)).toContain('/api/libraries/far/topology?dir=far-top'))
    expect(localStorage.getItem('ade-map-library-topology')).toBe('far')
    await pick('Lab index (read only)')
    expect(await screen.findByText('Lab index has no index yet, so there is nothing to draw.')).toBeInTheDocument()
  })
})
