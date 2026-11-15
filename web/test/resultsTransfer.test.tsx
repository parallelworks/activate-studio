import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as ui from '@parallelworks/ui'
import { SearchView } from '../src/views/SearchView'
import { QueryView } from '../src/views/QueryView'
import { groupByLibrary, groupsText, itemKey, keyItem, keyPaths } from '../src/components/ResultsTransfer'
import { libraryRelative, resetTransfersProbe } from '../src/transferApi'
import { resetScopesForTests } from '../src/scope'
import { MOCK_LOCATIONS, createTransfersMock, type MockLocation } from './transfersMock'

// A checkbox forwards its press to its hidden input as a PointerEvent, which
// jsdom lacks.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const lib = (id: string, label: string, writable = true) => ({
  id, label, primary: id === 'kb', writable, pinned: false, source: true, caps: { index: true, fullText: true, vectors: false },
})
const LIBS = [lib('kb', 'Projects'), lib('archive', 'Archive'), lib('reference', 'Reference', false), lib('remote', 'Remote')]
const LOCATIONS: MockLocation[] = [
  ...MOCK_LOCATIONS,
  { id: 'library:remote', kind: 'library', label: 'Remote', detail: 'remote-host, /srv/data', writable: false, indexed: true, browsable: true, available: false, why: 'the host did not answer' },
]

type Hit = { path: string; library: { id: string; label: string } }
const hit = (library: string, label: string, path: string): Hit & Record<string, unknown> => ({
  path, name: path.split('/').pop(), size: 1, mtime: 0, mode: 'name', snippet: '', library: { id: library, label },
})

interface Serve {
  hits?: Record<string, unknown>[]
  query?: Record<string, unknown>
  transfers?: boolean
  sections?: string[]
}
let mock: ReturnType<typeof createTransfersMock>
let tagPosts: unknown[] = []

function serve(o: Serve = {}) {
  mock = createTransfersMock({ locations: LOCATIONS, locationsStatus: o.transfers === false ? 404 : 200 })
  tagPosts = []
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const body = init?.body ? JSON.parse(String(init.body)) : undefined
    const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } })
    if (url.startsWith('/api/transfers')) {
      const r = mock.handle(init?.method ?? 'GET', url, body)
      return json(r.body, r.status)
    }
    if (url.startsWith('/api/config')) return json({ libraries: LIBS, sections: o.sections })
    if (url.startsWith('/api/search')) return json({ hits: o.hits ?? [] })
    if (url === '/api/query' && init?.method === 'POST') return json(o.query ?? { columns: [], rows: [], elapsedMs: 1, truncated: false })
    if (url === '/api/query/saved') return json([])
    if (url === '/api/kb/tags' && init?.method === 'POST') { tagPosts.push(body); return json({ updated: {} }) }
    if (url.startsWith('/api/kb/tags')) return json({ own: [], inherited: [] })
    if (url.startsWith('/api/tags')) return json({ tags: [] })
    return json({})
  }))
}

beforeEach(() => {
  resetTransfersProbe()
  resetScopesForTests()
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
})
afterEach(() => {
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  history.replaceState(null, '', location.pathname)
})

async function search(hits: Record<string, unknown>[], extra: Serve = {}) {
  serve({ hits, ...extra })
  render(<SearchView onOpen={() => {}} />)
  await act(async () => { window.dispatchEvent(new CustomEvent('ade:search', { detail: { q: 'run' } })) })
  await screen.findByText(`${hits.length} result${hits.length === 1 ? '' : 's'}`)
}
const rowOf = (path: string, library?: string) => {
  const rows = screen.getAllByText(path).map(el => el.closest('.result-row') as HTMLElement)
  return library ? rows.find(r => within(r).queryByText(library))! : rows[0]
}
const tick = (row: HTMLElement) => fireEvent.click(within(row).getByRole('checkbox'))
const transferButton = () => screen.queryByRole('button', { name: /^Transfer…/ })

async function openDialog() {
  fireEvent.click(transferButton()!)
  return await screen.findByRole('dialog', { name: /^Transfer \d+ items?$/ })
}
/** Waits for the destination pane to list the folder it opened on. */
async function destListed(dialog: HTMLElement, name: string) {
  const files = await within(dialog).findByRole('region', { name: 'Destination: files' })
  await within(files).findByText(name)
  return files
}

describe('the Transfer button', () => {
  it('shows in Search next to Labels when Transfers is available, enabled once something is selected', async () => {
    await search([hit('kb', 'Projects', 'datasets/labels.json')])
    const b = await waitFor(() => { const x = transferButton(); expect(x).not.toBeNull(); return x! })
    expect(b).toBeDisabled()
    const toolbar = b.closest('.results-actions')!
    const names = within(toolbar as HTMLElement).getAllByRole('button').map(x => x.textContent)
    expect(names.indexOf('Transfer…')).toBe(names.indexOf('Labels…') + 1)
    tick(rowOf('datasets/labels.json'))
    expect(transferButton()).toBeEnabled()
    expect(transferButton()).toHaveTextContent('Transfer… (1)')
  })

  it('is not in Search when the server has no transfers', async () => {
    await search([hit('kb', 'Projects', 'datasets/labels.json')], { transfers: false })
    await act(async () => { await Promise.resolve() })
    expect(screen.getByRole('button', { name: /^Labels…/ })).toBeInTheDocument()
    expect(transferButton()).toBeNull()
  })

  it('shows in Query only with a path or directory column, and leaves out rows totalled over libraries', async () => {
    serve({ query: { columns: ['extension', 'files'], rows: [['pdf', '3']], elapsedMs: 1, truncated: false } })
    render(<QueryView onOpen={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Run query' }))
    await screen.findByText('pdf')
    await act(async () => { await Promise.resolve() })
    expect(transferButton()).toBeNull()
    expect(screen.queryByRole('button', { name: /^Labels…/ })).toBeNull()
  })

  it('in Query, a row totalled over all libraries is labeled but not transferred, and says why', async () => {
    serve({ query: { columns: ['directory', 'bytes'], rows: [['reports', '10'], ['datasets', '20']], rowLibraries: ['kb', 'all'], elapsedMs: 1, truncated: false } })
    render(<QueryView onOpen={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Run query' }))
    await screen.findByText('reports')
    const b = await waitFor(() => { const x = transferButton(); expect(x).not.toBeNull(); return x! })
    const totalled = screen.getByText('datasets').closest('tr')!
    expect(totalled.querySelector('.sel-col')).toHaveAttribute('title', expect.stringMatching(/^Totalled over all libraries/))
    fireEvent.click(within(totalled).getByRole('checkbox'))
    expect(b).toHaveAttribute('aria-disabled', 'true')
    expect(b.getAttribute('title')).toMatch(/1 selected row names no single file or folder in one library/)
    expect(screen.getByRole('button', { name: /^Labels…/ })).toBeEnabled()
    fireEvent.click(within(screen.getByText('reports').closest('tr')!).getByRole('checkbox'))
    expect(transferButton()).toHaveTextContent('Transfer… (1)')
    const dialog = await openDialog()
    expect(dialog).toHaveAccessibleName('Transfer 1 item')
    expect(within(dialog).getByText('1 folder in Projects')).toBeInTheDocument()
  })
})

describe('the selection', () => {
  it('keys Search results by library and path, so the same path in two libraries is two', async () => {
    await search([hit('kb', 'Projects', 'shared/plan.md'), hit('archive', 'Archive', 'shared/plan.md')])
    tick(rowOf('shared/plan.md', 'Projects'))
    const [a, b] = screen.getAllByText('shared/plan.md').map(el => el.closest('.result-row')!)
    expect(a).toHaveClass('multi')
    expect(b).not.toHaveClass('multi')
    await waitFor(() => expect(transferButton()).toHaveTextContent('Transfer… (1)'))
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }))
    expect(transferButton()).toHaveTextContent('Transfer… (2)')
    expect(screen.getByRole('button', { name: 'Clear selection' })).toBeInTheDocument()
  })

  it('still labels: the labels panel gets each path once', async () => {
    await search([hit('kb', 'Projects', 'shared/plan.md'), hit('archive', 'Archive', 'shared/plan.md'), hit('kb', 'Projects', 'notes.md')])
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }))
    fireEvent.click(screen.getByRole('button', { name: 'Labels… (3)' }))
    expect(await screen.findByText('Labels for 2 items')).toBeInTheDocument()
    const input = screen.getByPlaceholderText('e.g. validated, research, experiment')
    fireEvent.change(input, { target: { value: 'checked' } })
    fireEvent.keyDown(input, { key: 'Enter' })
    await waitFor(() => expect(tagPosts).toEqual([{ paths: ['shared/plan.md', 'notes.md'], add: ['checked'], remove: [] }]))
  })

  it('has helpers that key, group and name by library', () => {
    expect(keyItem(itemKey('kb', 'a/b.txt'))).toEqual({ library: 'kb', path: 'a/b.txt' })
    expect(keyPaths([itemKey('kb', 'x'), itemKey('archive', 'x'), itemKey('kb', 'y')])).toEqual(['x', 'y'])
    const groups = groupByLibrary(
      [{ library: 'kb', path: 'a' }, { library: 'archive', path: 'b' }, { library: 'kb', path: 'c' }],
      id => ({ kb: 'Projects', archive: 'Archive' })[id] ?? id,
    )
    expect(groups).toEqual([{ library: 'kb', label: 'Projects', paths: ['a', 'c'] }, { library: 'archive', label: 'Archive', paths: ['b'] }])
    expect(groupsText(groups, 'file')).toBe('2 files in Projects, 1 in Archive')
    expect(groupsText([{ label: 'Projects', paths: ['a'] }], 'folder')).toBe('1 folder in Projects')
    // Search and Query print paths relative to the index root, as a
    // library location takes them; anything else names no path there.
    expect(libraryRelative('./reports/q3.pdf')).toBe('reports/q3.pdf')
    expect(libraryRelative('reports/')).toBe('reports')
    expect(libraryRelative('/home/user/index')).toBeNull()
    expect(libraryRelative('')).toBeNull()
    expect(libraryRelative('../up')).toBeNull()
  })
})

describe('the dialog', () => {
  it('groups by library, and plans one transfer for one library into the folder chosen', async () => {
    await search([hit('kb', 'Projects', 'datasets/labels.json'), hit('kb', 'Projects', 'reports/q3-review.pdf')])
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }))
    const dialog = await openDialog()
    expect(dialog).toHaveAccessibleName('Transfer 2 items')
    expect(within(dialog).getByText('2 files in Projects')).toBeInTheDocument()
    // The destination opens on the first item's library, browsed as on
    // the Transfers page, with nothing to tick.
    const files = await destListed(dialog, 'simulations')
    expect(within(files).queryByRole('checkbox')).toBeNull()
    fireEvent.click(within(files).getByRole('button', { name: 'simulations' }))
    await within(files).findByText('README.md')
    expect(within(dialog).getByText('To Projects: simulations')).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Continue' }))
    const plan = await screen.findByRole('alertdialog')
    expect(plan).toHaveAccessibleName('Copy: confirm the plan')
    const posts = mock.calls.filter(c => c.path === '/api/transfers/plan')
    expect(posts).toHaveLength(1)
    expect(posts[0].body).toMatchObject({
      action: 'copy',
      source: { location: 'library:kb', paths: ['datasets/labels.json', 'reports/q3-review.pdf'] },
      destination: { location: 'library:kb', path: 'simulations' },
      options: { replace: false, mover: 'auto' },
    })
  })

  it('steps through one plan per library, "1 of 2", and Cancel stops the rest', async () => {
    await search([hit('kb', 'Projects', 'datasets/labels.json'), hit('archive', 'Archive', '2025/run-001/out.h5'), hit('kb', 'Projects', 'reports/q3-review.pdf')])
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }))
    const dialog = await openDialog()
    expect(dialog).toHaveAccessibleName('Transfer 3 items')
    expect(within(dialog).getByText('2 files in Projects, 1 in Archive')).toBeInTheDocument()
    const files = await destListed(dialog, 'simulations')
    fireEvent.click(within(files).getByRole('button', { name: 'simulations' }))
    await within(files).findByText('README.md')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Continue' }))
    const first = await screen.findByRole('alertdialog', { name: 'Copy: confirm the plan (1 of 2)' })
    expect(mock.calls.filter(c => c.path === '/api/transfers/plan').map(c => (c.body as { source: { location: string } }).source.location))
      .toEqual(['library:kb', 'library:archive'])
    fireEvent.click(within(first).getByRole('button', { name: /^Copy / }))
    const second = await screen.findByRole('alertdialog', { name: 'Copy: confirm the plan (2 of 2)' })
    expect(second).toHaveTextContent('Archive')
    fireEvent.click(within(second).getByRole('button', { name: 'Cancel' }))
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    // One started, one not: the selection clears, as something started.
    expect(mock.calls.filter(c => c.method === 'POST' && c.path === '/api/transfers')).toHaveLength(1)
    expect(transferButton()).toBeDisabled()
  })

  it('leaves out a library whose files cannot be reached, with its reason', async () => {
    await search([hit('kb', 'Projects', 'datasets/labels.json'), hit('remote', 'Remote', 'data/x.csv'), hit('remote', 'Remote', 'data/y.csv')])
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }))
    const dialog = await openDialog()
    expect(await within(dialog).findByText('2 files in Remote left out')).toBeInTheDocument()
    expect(within(dialog).getByText('Remote is not reachable: the host did not answer.')).toBeInTheDocument()
    await destListed(dialog, 'simulations')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Continue' }))
    await screen.findByRole('alertdialog', { name: 'Copy: confirm the plan' })
    const posts = mock.calls.filter(c => c.path === '/api/transfers/plan')
    expect(posts.map(c => (c.body as { source: { location: string } }).source.location)).toEqual(['library:kb'])
  })

  it('refuses a move out of a read-only library and offers Copy', async () => {
    await search([hit('reference', 'Reference', 'standards.pdf')])
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }))
    const dialog = await openDialog()
    // Reference is read only, so the destination opens on the first place that takes files.
    await within(dialog).findByRole('region', { name: 'Destination: files' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Move' }))
    expect(await within(dialog).findByText('1 file in Reference left out')).toBeInTheDocument()
    expect(within(dialog).getByText('Reference is read only, so nothing can be moved out of it.')).toBeInTheDocument()
    const go = within(dialog).getByRole('button', { name: 'Continue' })
    expect(go).toHaveAttribute('aria-disabled', 'true')
    expect(go).toHaveAttribute('title', 'Nothing selected can be sent.')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Copy instead' }))
    expect(within(dialog).queryByText('1 file in Reference left out')).toBeNull()
    expect(within(dialog).getByRole('button', { name: 'Copy' })).toHaveAttribute('aria-pressed', 'true')
    await waitFor(() => expect(within(dialog).getByRole('button', { name: 'Continue' })).not.toHaveAttribute('aria-disabled', 'true'))
  })

  it('shows the server\'s reason when it refuses a plan', async () => {
    await search([hit('kb', 'Projects', 'notes.md')])
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }))
    const dialog = await openDialog()
    await destListed(dialog, 'simulations')
    // notes.md is already at the top of Projects, and Replace is off.
    fireEvent.click(within(dialog).getByRole('button', { name: 'Continue' }))
    expect(await within(dialog).findByText(/notes\.md already exists in Projects/)).toBeInTheDocument()
    expect(screen.queryByRole('alertdialog')).toBeNull()
  })

  it('after a start, stays on the page with a toast that opens the transfer, and clears the selection', async () => {
    const success = vi.spyOn(ui.toast, 'success')
    await search([hit('kb', 'Projects', 'datasets/labels.json')])
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }))
    const dialog = await openDialog()
    const files = await destListed(dialog, 'simulations')
    fireEvent.click(within(files).getByRole('button', { name: 'reports' }))
    await within(files).findByText('q3-review.pdf')
    fireEvent.click(within(dialog).getByRole('button', { name: 'Continue' }))
    const plan = await screen.findByRole('alertdialog')
    fireEvent.click(within(plan).getByRole('button', { name: 'Copy 1 file' }))
    await waitFor(() => expect(success).toHaveBeenCalledTimes(1))
    const [message, opts] = success.mock.calls[0]
    const id = (mock.transfers[0] as { id: string }).id
    expect(message).toBe('Copy 1 file from Projects to Projects')
    expect(opts).toMatchObject({ title: 'Transfer started', action: { label: 'Open in Transfers', href: `#view=transfers?transfer=${encodeURIComponent(id)}` } })
    await waitFor(() => expect(screen.queryByRole('alertdialog')).toBeNull())
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(location.hash).not.toMatch(/transfers/)
    expect(transferButton()).toBeDisabled()
    expect(screen.getByText('datasets/labels.json').closest('.result-row')).not.toHaveClass('multi')
  })
})
