import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { useState } from 'react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { TransfersView } from '../src/views/TransfersView'
import { TransferPane, type PaneState } from '../src/components/TransferPane'
import { TransferActivity, progressText } from '../src/components/TransferActivity'
import { TransferPlanDialog } from '../src/components/TransferPlanDialog'
import {
  confirmText, plural, rangeSelect, resetTransfersProbe, sourcePaths, touchesMainLibrary, transferTitle, transfersPresent,
  type Plan, type Transfer,
} from '../src/transferApi'
import { MOCK_LOCATIONS, createTransfersMock, mockTransfers, type MockOptions } from './transfersMock'

// A checkbox forwards its press to its hidden input as a PointerEvent, which
// jsdom lacks.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

type Mock = ReturnType<typeof createTransfersMock>
let mock: Mock

function serve(opts: MockOptions = {}): Mock {
  mock = createTransfersMock(opts)
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const r = mock.handle(init?.method ?? 'GET', String(input), init?.body ? JSON.parse(String(init.body)) : undefined)
    return new Response(JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } })
  }))
  return mock
}

beforeEach(() => {
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
  history.replaceState(null, '', location.pathname)
})

/** A pane on its own, its state held as the page holds it. */
function PaneHarness({ start, onState }: { start: PaneState; onState?: (s: PaneState) => void }) {
  const [s, setS] = useState(start)
  return <TransferPane name="First location" locations={MOCK_LOCATIONS} state={s} onChange={n => { setS(n); onState?.(n) }} onLoaded={() => {}} refreshKey={0} />
}

const rowNames = (region: HTMLElement) => within(region).getAllByRole('row').slice(1).map(r => within(r).getAllByRole('cell')[1].textContent)

describe('a pane', () => {
  it('lists folders first, drills into a folder by its name and by double-click, and climbs with the breadcrumb and Up', async () => {
    serve()
    render(<PaneHarness start={{ locationId: 'library:kb', path: '', selected: [] }} />)
    const files = await screen.findByRole('region', { name: 'First location: files' })
    await waitFor(() => expect(rowNames(files)).toEqual(['datasets', 'reports', 'simulations', 'latest', 'notes.md', 'plan.xlsx']))
    expect(screen.getByText('Indexed')).toBeInTheDocument()

    fireEvent.click(within(files).getByRole('button', { name: 'simulations' }))
    await waitFor(() => expect(rowNames(files)).toEqual(['2026', 'README.md']))
    fireEvent.doubleClick(within(files).getByText('2026').closest('tr')!)
    await waitFor(() => expect(rowNames(files)).toEqual(['run-042', 'run-043']))
    fireEvent.click(within(files).getByRole('button', { name: 'run-042' }))
    await waitFor(() => expect(rowNames(files)).toEqual(['output', 'input.yaml', 'job.log']))
    expect(mock.calls.at(-1)?.path).toBe('/api/transfers/browse?location=library%3Akb&path=simulations%2F2026%2Frun-042')

    fireEvent.click(screen.getByRole('button', { name: 'Up one folder' }))
    await waitFor(() => expect(rowNames(files)).toEqual(['run-042', 'run-043']))
    const crumbs = screen.getByRole('navigation', { name: 'First location: path' })
    fireEvent.click(within(crumbs).getByRole('button', { name: 'Projects' }))
    await waitFor(() => expect(rowNames(files)).toContain('datasets'))
  })

  it('selects several with the boxes, a range with shift, all with the header box, and narrows with the filter', async () => {
    serve()
    let last: PaneState | null = null
    render(<PaneHarness start={{ locationId: 'library:kb', path: '', selected: [] }} onState={s => { last = s }} />)
    const files = await screen.findByRole('region', { name: 'First location: files' })
    await waitFor(() => expect(rowNames(files)).toHaveLength(6))
    fireEvent.click(within(files).getByRole('checkbox', { name: 'Select datasets' }))
    fireEvent.click(within(files).getByRole('checkbox', { name: 'Select notes.md' }), { shiftKey: true })
    expect(last!.selected).toEqual(['datasets', 'reports', 'simulations', 'latest', 'notes.md'])
    expect(screen.getByText('5 of 6 selected')).toBeInTheDocument()
    fireEvent.click(within(files).getByRole('checkbox', { name: 'Select reports' }))
    expect(last!.selected).not.toContain('reports')

    fireEvent.click(within(files).getByRole('checkbox', { name: 'Select all in First location' }))
    expect(last!.selected).toHaveLength(6)
    fireEvent.click(within(files).getByRole('checkbox', { name: 'Select all in First location' }))
    expect(last!.selected).toHaveLength(0)

    fireEvent.change(screen.getByRole('searchbox', { name: 'Filter First location' }), { target: { value: 'parq' } })
    // datasets holds the parquet files; the filter matches names in this folder only.
    await waitFor(() => expect(within(files).getByText('Nothing here matches “parq”.')).toBeInTheDocument())
  })

  it('says when a folder is empty, and when a location is unreachable', async () => {
    serve()
    const { unmount } = render(<PaneHarness start={{ locationId: 'library:archive', path: '2026', selected: [] }} />)
    expect(await screen.findByText('This folder is empty')).toBeInTheDocument()
    unmount()
    render(<PaneHarness start={{ locationId: 'cluster:cpu-cluster', path: '', selected: [] }} />)
    expect(await screen.findByText('cpu-cluster is not reachable')).toBeInTheDocument()
    expect(screen.getByText('the cluster is stopped')).toBeInTheDocument()
  })

  it('shows the no-locations state', () => {
    serve({ locations: [] })
    render(<TransferPane name="First location" locations={[]} state={{ locationId: null, path: '', selected: [] }} onChange={() => {}} onLoaded={() => {}} refreshKey={0} />)
    expect(screen.getByText('No locations')).toBeInTheDocument()
  })
})

describe('the rules', () => {
  const entries = [{ name: 'a', type: 'dir' as const, size: 0, mtime: '' }, { name: 'f.txt', type: 'file' as const, size: 1, mtime: '' }]
  const kb = MOCK_LOCATIONS[0]
  const ro = MOCK_LOCATIONS[1]
  it('take a selection for copy, one folder or the open folder for sync, and refuse a move out of a read-only library', () => {
    expect(sourcePaths('copy', { location: kb, path: 'x', selected: ['a', 'f.txt'], entries })).toEqual({ paths: ['x/a', 'x/f.txt'] })
    expect(sourcePaths('copy', { location: kb, path: 'x', selected: [], entries })).toHaveProperty('why')
    expect(sourcePaths('sync', { location: kb, path: 'x', selected: [], entries })).toEqual({ paths: ['x'] })
    expect(sourcePaths('sync', { location: kb, path: 'x', selected: ['a'], entries })).toEqual({ paths: ['x/a'] })
    expect(sourcePaths('sync', { location: kb, path: 'x', selected: ['f.txt'], entries })).toHaveProperty('why')
    expect(sourcePaths('sync', { location: kb, path: 'x', selected: ['a', 'f.txt'], entries })).toHaveProperty('why')
    expect(sourcePaths('move', { location: ro, path: '', selected: ['a'], entries })).toEqual({ why: 'Reference is read only, so nothing can be moved out of it.' })
  })
  it('select a shift range in list order', () => {
    expect(rangeSelect(['a', 'b', 'c', 'd'], ['a'], 'a', 'c')).toEqual(['a', 'b', 'c'])
    expect(rangeSelect(['a', 'b', 'c', 'd'], [], 'd', 'b')).toEqual(['b', 'c', 'd'])
  })
  it('put the consequence on the confirm button', () => {
    const p = { files: 12, dirs: 2, replacesCount: 0, deletesCount: 0 }
    expect(confirmText({ ...p, action: 'copy' })).toBe('Copy 12 files')
    expect(confirmText({ ...p, action: 'move' })).toBe('Move 12 files')
    expect(confirmText({ ...p, action: 'copy', replacesCount: 3 })).toBe('Copy 12 files and replace 3')
    expect(confirmText({ ...p, action: 'sync', deletesCount: 3 })).toBe('Sync and delete 3 files')
    expect(confirmText({ ...p, action: 'sync', files: 1 })).toBe('Sync 1 file')
    expect(confirmText({ ...p, action: 'copy', files: -1 })).toBe('Copy files')
  })
})

/** Open New transfer with the first pane on Projects and the second on gpu-cluster. */
async function openNew() {
  history.replaceState(null, '', `${location.pathname}#view=transfers:new`)
  render(<TransfersView />)
  const panes = await screen.findAllByRole('region', { name: /: files$/ })
  await waitFor(() => expect(rowNames(panes[0])).toContain('datasets'))
  return panes
}

/** Picks a location from a pane's chooser. Its list draws only the rows that
 *  fit its height, which jsdom measures as zero, so layout is given a size
 *  while it is open. */
async function pickLocation(paneName: string, label: string) {
  const rect = Element.prototype.getBoundingClientRect
  const sizes = ['offsetHeight', 'clientHeight', 'offsetWidth', 'clientWidth'] as const
  for (const k of sizes) Object.defineProperty(HTMLElement.prototype, k, { configurable: true, get: () => 400 })
  Element.prototype.getBoundingClientRect = () => ({ x: 0, y: 0, top: 0, left: 0, width: 400, height: 400, right: 400, bottom: 400, toJSON() {} }) as DOMRect
  try {
    const u = userEvent.setup()
    await u.click(screen.getByRole('combobox', { name: `${paneName}: location` }))
    await u.click(await screen.findByRole('option', { name: new RegExp(`^${label}`) }))
  } finally {
    Element.prototype.getBoundingClientRect = rect
    for (const k of sizes) delete (HTMLElement.prototype as unknown as Record<string, unknown>)[k]
  }
}

describe('new transfer and the plan', () => {
  it('asks which movers reach between the two locations, and names them plainly', async () => {
    serve()
    await openNew()
    await waitFor(() => expect(mock.calls.some(c => c.path === '/api/transfers/movers?source=library%3Akb&destination=computer%3ADownloads')).toBe(true))
    await pickLocation('Second location', 'gpu-cluster')
    await waitFor(() => expect(mock.calls.some(c => c.path.startsWith('/api/transfers/movers?source=library%3Akb&destination=cluster'))).toBe(true))
  })

  it('starts off until something is selected, then plans a copy and shows it plainly', async () => {
    serve()
    const panes = await openNew()
    const right = screen.getByRole('button', { name: /^Start: copy from Projects to/ })
    expect(right).toHaveAttribute('aria-disabled', 'true')
    fireEvent.click(within(panes[0]).getByRole('button', { name: 'simulations' }))
    await waitFor(() => expect(rowNames(panes[0])).toEqual(['2026', 'README.md']))
    fireEvent.click(within(panes[0]).getByRole('checkbox', { name: 'Select 2026' }))
    await waitFor(() => expect(right).not.toHaveAttribute('aria-disabled', 'true'))
    fireEvent.click(right)
    const dialog = await screen.findByRole('alertdialog')
    expect(mock.calls.find(c => c.path === '/api/transfers/plan')?.body).toMatchObject({
      action: 'copy', source: { location: 'library:kb', paths: ['simulations/2026'] }, destination: { location: 'computer:Downloads', path: '' },
      options: { mover: 'auto', replace: false, checksum: false, preserveTimes: true },
    })
    expect(within(dialog).getByText('Projects: simulations/2026')).toBeInTheDocument()
    expect(within(dialog).getByText('Downloads: /')).toBeInTheDocument()
    expect(within(dialog).getByText('7 files and 4 folders')).toBeInTheDocument()
    expect(within(dialog).getByText('rsync')).toBeInTheDocument()
    expect(within(dialog).getByText(/conduit: only when both ends are in its namespace/)).toBeInTheDocument()
    expect(within(dialog).getByText(/2 hidden, 1 link\./)).toBeInTheDocument()
    expect(within(dialog).getByText('512 GB')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Copy 7 files' })).toBeInTheDocument()

    fireEvent.click(within(dialog).getByRole('button', { name: 'Copy 7 files' }))
    // Back to the list, with the new transfer's details open beside it.
    await waitFor(() => expect(location.hash).toMatch(/^#view=transfers\?transfer=t-new-/))
    window.dispatchEvent(new HashChangeEvent('hashchange'))
    expect(mock.calls.find(c => c.method === 'POST' && c.path === '/api/transfers')?.body).toEqual({ planId: 'plan-1' })
    const row = await waitFor(() => {
      const r = document.querySelector('.xfer-row.new') as HTMLElement
      expect(r).not.toBeNull()
      return r
    })
    expect(row).toHaveTextContent('Copy 7 files')
    // The new transfer heads the active list, behind its details, which
    // cover the view below the page header.
    const active = document.querySelector('section.xfer-group[aria-label="Active"]') as HTMLElement
    expect(active.querySelector('.xfer-row')).toBe(row)
    expect(await screen.findByRole('dialog')).toHaveTextContent('Copy 7 files')
    expect(screen.queryByRole('region', { name: /: files$/ })).toBeNull()
  })

  it('goes back to the list from New transfer when the sidebar\'s Transfers is clicked', async () => {
    serve()
    await openNew()
    act(() => { window.dispatchEvent(new Event('ade:transfers-home')) })
    expect(location.hash).toBe('#view=transfers')
    act(() => { window.dispatchEvent(new HashChangeEvent('hashchange')) })
    await waitFor(() => expect(screen.queryByRole('region', { name: /: files$/ })).toBeNull())
    expect(screen.getAllByRole('button', { name: 'New transfer' }).length).toBeGreaterThan(0)
  })

  it('names a move by what it does, in a warning button', async () => {
    serve()
    const panes = await openNew()
    fireEvent.click(screen.getByRole('button', { name: 'Move', pressed: false }))
    fireEvent.click(within(panes[0]).getByRole('checkbox', { name: 'Select notes.md' }))
    fireEvent.click(within(panes[0]).getByRole('checkbox', { name: 'Select plan.xlsx' }))
    fireEvent.click(screen.getByRole('button', { name: /^Start: move from Projects/ }))
    const dialog = await screen.findByRole('alertdialog')
    expect(within(dialog).getByText('The originals are removed')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Move 2 files' })).toBeInTheDocument()
  })

  it('lists a sync\'s deletions in the error tone and says so on the button', async () => {
    serve()
    const panes = await openNew()
    fireEvent.click(screen.getByRole('button', { name: 'Sync', pressed: false }))
    fireEvent.click(screen.getByText('Transfer options'))
    fireEvent.click(await screen.findByText('Delete files at the destination that are not in the source'))
    fireEvent.click(within(panes[0]).getByRole('checkbox', { name: 'Select datasets' }))
    fireEvent.click(screen.getByRole('button', { name: /^Start: sync from Projects/ }))
    const dialog = await screen.findByRole('alertdialog')
    expect(mock.calls.find(c => c.path === '/api/transfers/plan')?.body).toMatchObject({ action: 'sync', source: { paths: ['datasets'] }, options: { deleteExtra: true } })
    expect(within(dialog).getByText('Deletes 3 files at the destination that are not in the source')).toBeInTheDocument()
    expect(within(dialog).getByText('old/run-017.h5')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Sync and delete 3 files' })).toBeInTheDocument()
  })

  it('keeps a read-only location from being a destination', async () => {
    serve()
    await openNew()
    const panes = screen.getAllByRole('region', { name: /: files$/ })
    fireEvent.click(within(panes[0]).getByRole('checkbox', { name: 'Select notes.md' }))
    await pickLocation('Second location', 'Reference')
    await waitFor(() => expect(screen.getAllByText('Read only').length).toBeGreaterThan(0))
    expect(screen.getByRole('button', { name: /^Start: copy from Projects to Reference/ })).toHaveAttribute('aria-disabled', 'true')
    expect(screen.getByText('Reference is read only.')).toBeInTheDocument()
  })

  it('offers a new plan when the files changed (409)', async () => {
    serve({ startStatus: 409 })
    const panes = await openNew()
    fireEvent.click(within(panes[0]).getByRole('checkbox', { name: 'Select notes.md' }))
    fireEvent.click(screen.getByRole('button', { name: /^Start: copy from Projects/ }))
    const dialog = await screen.findByRole('alertdialog')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Copy 1 file' }))
    expect(await within(dialog).findByText(/The files changed since this plan was made/)).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Plan again' }))
    await waitFor(() => expect(mock.calls.filter(c => c.path === '/api/transfers/plan')).toHaveLength(2))
    expect(location.hash).toBe('#view=transfers:new')
  })

  it('shows a refused plan\'s reason', async () => {
    serve()
    const panes = await openNew()
    await pickLocation('Second location', 'Projects')
    await waitFor(() => expect(rowNames(panes[1])).toContain('datasets'))
    fireEvent.click(within(panes[0]).getByRole('checkbox', { name: 'Select notes.md' }))
    fireEvent.click(screen.getAllByRole('button', { name: /^Start: copy from Projects to Projects/ })[0])
    expect(await screen.findByText(/notes.md already exists in Projects/)).toBeInTheDocument()
  })
})

describe('the plan dialog', () => {
  const plan: Plan = {
    id: 'p', action: 'copy', label: '', mover: 'studio-copy', moverWhy: 'chosen', movers: [{ name: 'studio-copy', ok: true }],
    source: { location: 'a', label: 'A', paths: ['x'] }, destination: { location: 'b', label: 'B', path: 'y' },
    files: 3, dirs: 0, bytes: 10 * 1024 ** 3, replaces: ['x'], deletes: [], replacesCount: 60, deletesCount: 0,
    skipped: { hidden: 0, links: 0 }, freeBytes: 1024 ** 3, warnings: ['One file is open for writing.'], expiresAt: new Date(Date.now() - 1000).toISOString(),
  }
  it('shows replacements with a count beyond the names, low space, warnings, and an expired plan', () => {
    serve()
    render(<TransferPlanDialog plan={plan} request={{ action: 'copy', source: { location: 'a', paths: ['x'] }, destination: { location: 'b', path: 'y' } }}
      onClose={() => {}} onStarted={() => {}} onReplanned={() => {}} />)
    expect(screen.getByText('Replaces 60 files at the destination')).toBeInTheDocument()
    expect(screen.getByText('and 59 more')).toBeInTheDocument()
    expect(screen.getByText(/reports 1 GB free, less than the 10 GB/)).toBeInTheDocument()
    expect(screen.getByText('One file is open for writing.')).toBeInTheDocument()
    expect(screen.getByText('This plan expired')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Plan again' })).toBeInTheDocument()
  })
})

/** The list groups finished transfers by day, and the mock dates them
 *  hours before now: near midnight, "2 hours ago" is yesterday. Tests that
 *  name the day run at a fixed afternoon, with only the date faked. */
function atAfternoon() {
  vi.useFakeTimers({ shouldAdvanceTime: true, toFake: ['Date'] })
  vi.setSystemTime(new Date(2026, 10, 14, 15, 0, 0))
}

describe('activity', () => {
  it('opens on the list, active first: progress, rate and time left, the assistant\'s with a link, failures with their reason', async () => {
    atAfternoon()
    serve()
    history.replaceState(null, '', `${location.pathname}#view=transfers`)
    render(<TransfersView />)
    const active = await screen.findByRole('region', { name: 'Active' })
    const finished = screen.getByRole('region', { name: 'Finished' })
    expect(screen.getByRole('button', { name: 'New transfer' })).toBeInTheDocument()
    const running = active.querySelector('[data-transfer="t-run"]') as HTMLElement
    expect(running).toHaveTextContent('Copy 12 files')
    expect(running).toHaveTextContent('gpu-cluster: scratch/run-042 → Projects: simulations/2026')
    expect(running).toHaveTextContent('5 of 12 files · 1.6 GB of 4 GB · 50 MB/s · 49s left')
    expect(within(running).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '1717986918')
    const waiting = active.querySelector('[data-transfer="t-await"]') as HTMLElement
    expect(waiting).toHaveTextContent('Awaiting approval')
    expect(within(waiting).getByRole('link', { name: 'Approve or deny it there' })).toHaveAttribute('href', '#chat=6f1c2a90-3b7d-4e55-9c1a-2b8f0e7d4a11')
    // Finished ones are one line each, under their day.
    await waitFor(() => expect(finished.querySelector('[data-transfer="t-fail"]')).not.toBeNull())
    const failed = finished.querySelector('[data-transfer="t-fail"]') as HTMLElement
    expect(within(failed).getByRole('button', { name: 'Details of Sync 3 files, Failed' })).toBeInTheDocument()
    expect(failed).toHaveTextContent('Projects: datasets → Scratch on archive-host: shared')
    expect(failed).toHaveTextContent('2.8 GB')
    const done = finished.querySelector('[data-transfer="t-done"]') as HTMLElement
    expect(within(done).getByRole('link', { name: 'Assistant' })).toHaveAttribute('href', '#chat=0a7e4c21-9d3b-4f60-8a2e-5c1b7f9d3e44')
    expect(within(finished).getByRole('group', { name: 'Yesterday' })).toContainElement(done)
    expect(mock.calls.some(c => c.path === '/api/transfers?state=active&limit=50')).toBe(true)
    expect(mock.calls.some(c => c.path === '/api/transfers?state=done&limit=50')).toBe(true)

    fireEvent.click(screen.getByRole('tab', { name: 'Finished' }))
    expect(screen.queryByRole('region', { name: 'Active' })).toBeNull()
  })

  it('reads the active list every 2 seconds while one runs', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    serve()
    render(<TransferActivity highlightId={null} added={null} onLanded={() => {}} onNew={() => {}} />)
    await screen.findByRole('region', { name: 'Active' })
    const reads = () => mock.calls.filter(c => c.path.startsWith('/api/transfers?state=active')).length
    const before = reads()
    await act(async () => { await vi.advanceTimersByTimeAsync(2100) })
    expect(reads()).toBe(before + 1)
  })

  it('with nothing under way reads slowly, and a transfer just started brings the next read in 2 seconds', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const m = serve({ transfers: mockTransfers().filter(t => !['t-run', 't-await'].includes(t.id)) })
    const props = { highlightId: null, onLanded: () => {}, onNew: () => {} }
    const { rerender } = render(<TransferActivity {...props} added={null} />)
    await screen.findByText('Nothing under way.')
    const reads = () => m.calls.filter(c => c.path.startsWith('/api/transfers?state=active')).length
    const before = reads()
    await act(async () => { await vi.advanceTimersByTimeAsync(2100) })
    expect(reads()).toBe(before)
    const started = { ...(mockTransfers().find(t => t.id === 't-run') as unknown as Transfer), id: 't-new', state: 'queued' as const }
    rerender(<TransferActivity {...props} added={started} />)
    await act(async () => { await vi.advanceTimersByTimeAsync(2100) })
    expect(reads()).toBe(before + 1)
  })

  it('reports a finished transfer that landed, so its pane lists again', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const m = serve()
    const landed = vi.fn()
    render(<TransferActivity highlightId={null} added={null} onLanded={landed} onNew={() => {}} />)
    await screen.findByRole('region', { name: 'Active' })
    const run = m.transfers.find(t => t.id === 't-run')!
    run.state = 'succeeded'
    await act(async () => { await vi.advanceTimersByTimeAsync(2100) })
    await waitFor(() => expect(landed).toHaveBeenCalledWith(expect.objectContaining({ id: 't-run', state: 'succeeded' })))
  })

  it('tells the Library to read the main library again when a transfer that wrote there ends', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const m = serve()
    const changed = vi.fn()
    window.addEventListener('ade-kb-changed', changed)
    try {
      render(<TransferActivity highlightId={null} added={null} onLanded={() => {}} onNew={() => {}} />)
      await screen.findByRole('region', { name: 'Active' })
      expect(changed).not.toHaveBeenCalled()
      // t-run copies into Projects, the main library.
      const run = m.transfers.find(t => t.id === 't-run')!
      run.state = 'succeeded'; run.endedAt = new Date().toISOString()
      await act(async () => { await vi.advanceTimersByTimeAsync(2100) })
      await waitFor(() => expect(changed).toHaveBeenCalledTimes(1))
    } finally {
      window.removeEventListener('ade-kb-changed', changed)
    }
  })

  it('counts a move out of the main library, or a write into it, as a change to it, and nothing else', () => {
    const t = (action: Transfer['action'], from: string, to: string) =>
      ({ action, source: { location: from, label: '', paths: [] }, destination: { location: to, label: '', path: '' } })
    expect(touchesMainLibrary(t('copy', 'cluster:gpu-cluster', 'library:kb'))).toBe(true)
    expect(touchesMainLibrary(t('sync', 'cluster:gpu-cluster', 'library:kb'))).toBe(true)
    expect(touchesMainLibrary(t('move', 'library:kb', 'computer:Downloads'))).toBe(true)
    expect(touchesMainLibrary(t('copy', 'library:kb', 'computer:Downloads'))).toBe(false)
    expect(touchesMainLibrary(t('move', 'library:archive', 'computer:Downloads'))).toBe(false)
  })

  it('says one file, one item and one transfer in the singular', () => {
    const one = { ...(mockTransfers().find(x => x.id === 't-run') as unknown as Transfer), state: 'succeeded' as const }
    one.files = { done: 1, total: 1 }; one.bytes = { done: 18, total: 18 }
    expect(progressText(one)).toBe('1 of 1 file · 18 B of 18 B')
    expect(progressText({ ...one, files: { done: 1, total: -1 }, bytes: { done: 18, total: -1 } })).toBe('1 file, counting · 18 B')
    expect(progressText({ ...one, files: { done: 2, total: 3 } })).toMatch(/^2 of 3 files · /)
    expect(transferTitle({ label: '', action: 'move', files: { done: 0, total: -1 }, source: { location: '', label: '', paths: ['a', 'b'] } })).toBe('Move 2 items')
    expect(transferTitle({ label: '', action: 'copy', files: { done: 0, total: 1 }, source: { location: '', label: '', paths: ['a'] } })).toBe('Copy 1 file')
    expect(plural(1, 'transfer')).toBe('1 transfer')
    expect(plural(2, 'library', 'libraries')).toBe('2 libraries')
  })

  it('opens the details over the list below the page header, not over the whole window', async () => {
    serve()
    render(<div className="view"><div className="xfer-view"><TransferActivity highlightId={null} added={null} onLanded={() => {}} onNew={() => {}} /></div></div>)
    fireEvent.click(await screen.findByRole('button', { name: 'Details of Copy 12 files' }))
    const drawer = await screen.findByRole('dialog')
    // Contained in the view, so its close button sits under the header's
    // Status pane toggle, never on it.
    expect(drawer.closest('.xfer-view')).not.toBeNull()
  })

  it('shows the events and error in the detail, says why a cancel was refused, and removes a finished one', async () => {
    serve({ cancelRefused: 'CONDUIT has no way to stop a transfer once it starts.' })
    render(<TransferActivity highlightId={null} added={null} onLanded={() => {}} onNew={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Details of Copy 12 files' }))
    const drawer = await screen.findByRole('dialog')
    expect(await within(drawer).findByText('Started rsync on gpu-cluster to this computer')).toBeInTheDocument()
    fireEvent.click(within(drawer).getByRole('button', { name: 'Cancel transfer' }))
    expect(await within(drawer).findByText('CONDUIT has no way to stop a transfer once it starts.')).toBeInTheDocument()
    expect(within(drawer).getByText('Not cancelled')).toBeInTheDocument()
    fireEvent.click(within(drawer).getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())

    fireEvent.click(screen.getByRole('button', { name: /^Details of Sync/ }))
    const d2 = await screen.findByRole('dialog')
    expect(await within(d2).findByText('Why it failed')).toBeInTheDocument()
    expect(within(d2).queryByRole('button', { name: 'Cancel transfer' })).toBeNull()
    fireEvent.click(within(d2).getByRole('button', { name: 'Remove from list' }))
    await waitFor(() => expect(mock.calls.some(c => c.method === 'DELETE' && c.path === '/api/transfers/t-fail')).toBe(true))
    await waitFor(() => expect(document.querySelector('[data-transfer="t-fail"]')).toBeNull())
  })

  it('opens a transfer\'s details from a link to it, as the assistant gives it, and drops the id on close', async () => {
    serve()
    history.replaceState(null, '', `${location.pathname}#view=transfers?transfer=t-run`)
    render(<TransferActivity highlightId={null} added={null} onLanded={() => {}} onNew={() => {}} />)
    const drawer = await screen.findByRole('dialog')
    expect(await within(drawer).findByText('Started rsync on gpu-cluster to this computer')).toBeInTheDocument()
    fireEvent.click(within(drawer).getByRole('button', { name: 'Close' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(location.hash).toBe('#view=transfers')
    // A later link opens that one.
    act(() => {
      history.replaceState(null, '', `${location.pathname}#view=transfers?transfer=t-fail`)
      window.dispatchEvent(new HashChangeEvent('hashchange'))
    })
    expect(await within(await screen.findByRole('dialog')).findByText('Why it failed')).toBeInTheDocument()
  })

  it('with no transfers yet, says what is ready and offers a new transfer or a location', async () => {
    serve({ transfers: [] })
    const onNew = vi.fn()
    render(<TransferActivity highlightId={null} added={null} onLanded={() => {}} onNew={onNew} />)
    expect(await screen.findByText('No transfers yet')).toBeInTheDocument()
    expect(await screen.findByText('8 places ready')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Add a location' })).toHaveAttribute('href', '#view=settings:transfers')
    const news = screen.getAllByRole('button', { name: 'New transfer' })
    fireEvent.click(news.at(-1)!)
    expect(onNew).toHaveBeenCalled()
  })

  it('writes progress with an unknown total while counting', () => {
    const t = mockTransfers().find(x => x.id === 't-cancel') as unknown as Transfer
    expect(progressText(t)).toBe('30 files, counting · 90 MB')
  })
})

describe('history at scale', () => {
  const lines = () => document.querySelectorAll('.xfer-line').length
  const doneReads = () => mock.calls.filter(c => c.path.startsWith('/api/transfers?state=done')).map(c => new URL(c.path, 'http://x').searchParams)

  it('pages 2,000 finished transfers 50 at a time, searches and filters on the server, and stays quick with all of them loaded', async () => {
    atAfternoon()
    serve({ transfers: [], finished: 2000 })
    render(<TransferActivity highlightId={null} added={null} onLanded={() => {}} onNew={() => {}} />)
    await waitFor(() => expect(lines()).toBe(50))
    expect(screen.getByRole('group', { name: 'Today' })).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Show more' }))
    await waitFor(() => expect(lines()).toBe(100))
    expect(doneReads().at(-1)!.get('before')).toBe('50')

    // Search and filters go to the server and start again from the first page.
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search finished transfers' }), { target: { value: 'Nightly results 1' } })
    await waitFor(() => expect(doneReads().at(-1)!.get('q')).toBe('Nightly results 1'))
    // 1, 10..19 at steps of 10 among labels: "Nightly results 10", "100", "1000" ... all start with 1.
    await waitFor(() => expect(lines()).toBeGreaterThan(0))
    expect([...document.querySelectorAll('.xfer-line-title')].every(e => e.textContent!.startsWith('Nightly results 1'))).toBe(true)
    fireEvent.change(screen.getByRole('searchbox', { name: 'Search finished transfers' }), { target: { value: '' } })
    await waitFor(() => expect(lines()).toBe(50))

    const u = userEvent.setup()
    await u.click(screen.getByRole('combobox', { name: 'Outcome' }))
    await u.click(await screen.findByRole('option', { name: 'Failed' }))
    await waitFor(() => expect(doneReads().at(-1)!.get('outcome')).toBe('failed'))
    await waitFor(() => expect(document.querySelectorAll('.xfer-line [title="Failed"]').length).toBe(50))
    expect(lines()).toBe(50)
    await u.click(screen.getByRole('combobox', { name: 'Started by' }))
    await u.click(await screen.findByRole('option', { name: 'The assistant' }))
    await waitFor(() => expect(doneReads().at(-1)!.get('startedBy')).toBe('assistant'))
    await waitFor(() => expect([...document.querySelectorAll('.xfer-line-who')].every(e => e.textContent === 'Assistant')).toBe(true))

    // Every page loaded: 40 more reads, and the list still renders in time.
    await u.click(screen.getByRole('combobox', { name: 'Outcome' }))
    await u.click(await screen.findByRole('option', { name: 'Any outcome' }))
    await u.click(screen.getByRole('combobox', { name: 'Started by' }))
    await u.click(await screen.findByRole('option', { name: 'Anyone' }))
    await waitFor(() => expect(lines()).toBe(50))
    let slowest = 0
    while (screen.queryByRole('button', { name: 'Show more' })) {
      const before = lines()
      const t0 = performance.now()
      fireEvent.click(screen.getByRole('button', { name: 'Show more' }))
      await waitFor(() => expect(lines()).toBeGreaterThan(before))
      slowest = Math.max(slowest, performance.now() - t0)
    }
    expect(lines()).toBe(2000)
    // jsdom is far slower than a browser; this bounds a page added to the
    // end of a 1,950-row list, which re-renders only the new rows.
    expect(slowest).toBeLessThan(1500)
  }, 60_000)

  it('asks only for transfers finished since the newest seen, and reports one that landed', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const m = serve({ finished: 120 })
    const landed = vi.fn()
    render(<TransferActivity highlightId={null} added={null} onLanded={landed} onNew={() => {}} />)
    await waitFor(() => expect(lines()).toBe(50 - 0))
    const firstPages = doneReads().filter(p => !p.get('since')).length
    const run = m.transfers.find(t => t.id === 't-run')!
    run.state = 'succeeded'; run.endedAt = new Date().toISOString()
    await act(async () => { await vi.advanceTimersByTimeAsync(2100) })
    await waitFor(() => expect(landed).toHaveBeenCalledWith(expect.objectContaining({ id: 't-run' })))
    const since = doneReads().filter(p => p.get('since'))
    expect(since.length).toBeGreaterThan(0)
    expect(since[0].get('before')).toBeNull()
    // The whole history is not read again.
    expect(doneReads().filter(p => !p.get('since')).length).toBe(firstPages)
    expect(document.querySelector('.xfer-day [data-transfer="t-run"]')).not.toBeNull()
  })
})

describe('menu entry', () => {
  it('is present unless the server answers 404 on the locations', async () => {
    serve({ locationsStatus: 404 })
    resetTransfersProbe()
    expect(await transfersPresent()).toBe(false)
    serve()
    resetTransfersProbe()
    expect(await transfersPresent()).toBe(true)
    serve({ locationsStatus: 502 })
    resetTransfersProbe()
    expect(await transfersPresent()).toBe(true)
  })
})
