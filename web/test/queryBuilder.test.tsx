import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { QueryView } from '../src/views/QueryView'
import { PRESETS, fromCanned, withField } from '../src/queryBuilder'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const ROWS = { columns: ['path', 'bytes', 'modified'], rows: [['reports/q1.pdf', '1000', '2019-06-01 00:00:00']], elapsedMs: 3, truncated: false }
const SAVED = [
  { id: 'old-recent', name: 'Recent, saved long ago', createdAt: '', payload: { canned: 'recent', n: 40, days: 3, subtree: 'reports' } },
]

let runs: any[] = []
let sqlAsked: any[] = []
beforeEach(() => {
  runs = []
  sqlAsked = []
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  // The results table measures itself.
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    if (url === '/api/query/saved') return json(SAVED)
    if (url === '/api/query') { runs.push(JSON.parse(String(init?.body))); return json(ROWS) }
    if (url === '/api/query/sql') {
      sqlAsked.push(JSON.parse(String(init?.body)))
      return json({ stages: [{ flag: '-E', sql: 'INSERT INTO t SELECT 1;' }], commands: [{ engine: 'gufi_query', command: 'gufi_query -E x /ix' }] })
    }
    if (url === '/api/config') return json({ appName: 'Studio', libraries: [] })
    return json({})
  }))
})
afterEach(() => vi.unstubAllGlobals())

/** Picks an option in one of the page's selects. */
async function pick(combo: HTMLElement, option: string) {
  fireEvent.click(combo)
  const o = await screen.findByRole('option', { name: option })
  fireEvent.pointerDown(o)
  fireEvent.click(o)
}
const fields = () => screen.getAllByRole('combobox', { name: 'Field' })
const conditions = () => screen.getAllByRole('combobox', { name: 'Condition' })
const values = () => screen.getAllByRole('textbox', { name: 'Value' })

describe('the Query builder', () => {
  it('opens on the builder, with presets and no separate canned tab', async () => {
    render(<QueryView onOpen={() => {}} />)
    expect(screen.getByRole('tab', { name: 'Builder', selected: true })).toBeInTheDocument()
    expect(screen.queryByRole('tab', { name: 'Canned queries' })).toBeNull()
    const presets = screen.getByRole('group', { name: 'Start from' })
    for (const p of PRESETS) expect(within(presets).getByRole('button', { name: p.label })).toBeInTheDocument()
  })

  it('a preset fills the form and runs it, to change and run again', async () => {
    render(<QueryView onOpen={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Changed in the last 7 days' }))
    await waitFor(() => expect(runs).toHaveLength(1))
    expect(runs[0].builder).toMatchObject({ source: 'files', filters: [{ field: 'modified', op: 'gt', value: '7 days' }], sort: 'modified', dir: 'desc' })
    expect(values()[0]).toHaveValue('7 days')
    expect(conditions()[0]).toHaveTextContent('is after')
    // Narrowed by size and run again.
    fireEvent.click(screen.getByRole('button', { name: '+ Add condition' }))
    await pick(fields()[1], 'Size')
    fireEvent.change(values()[1], { target: { value: '10 MB' } })
    fireEvent.click(screen.getByRole('button', { name: 'Run query' }))
    await waitFor(() => expect(runs).toHaveLength(2))
    expect(runs[1].builder.filters).toEqual([{ field: 'modified', op: 'gt', value: '7 days' }, { field: 'size', op: 'gt', value: '10 MB' }])
    expect(await screen.findByText('reports/q1.pdf')).toBeInTheDocument()
  })

  it('rows match all by default, or any; empty rows are left out; a row can be removed', async () => {
    render(<QueryView onOpen={() => {}} />)
    fireEvent.change(values()[0], { target: { value: 'report' } })
    fireEvent.click(screen.getByRole('button', { name: '+ Add condition' }))
    fireEvent.click(screen.getByRole('button', { name: '+ Add condition' }))
    await pick(fields()[1], 'Extension')
    fireEvent.change(values()[1], { target: { value: 'pdf' } })
    fireEvent.click(screen.getByRole('button', { name: 'Run query' }))
    await waitFor(() => expect(runs).toHaveLength(1))
    expect(runs[0].builder.match).toBe('all')
    // The new field keeps the condition, which it takes too.
    expect(runs[0].builder.filters).toEqual([{ field: 'name', op: 'contains', value: 'report' }, { field: 'extension', op: 'contains', value: 'pdf' }])

    await pick(screen.getByRole('combobox', { name: 'Match' }), 'any')
    fireEvent.click(screen.getAllByRole('button', { name: 'Remove condition' })[0])
    expect(values()).toHaveLength(2)
    fireEvent.click(screen.getByRole('button', { name: 'Run query' }))
    await waitFor(() => expect(runs).toHaveLength(2))
    expect(runs[1].builder.match).toBe('any')
    expect(runs[1].builder.filters).toEqual([{ field: 'extension', op: 'contains', value: 'pdf' }])
  })

  it('each field offers its own conditions and a hint for its value', async () => {
    render(<QueryView onOpen={() => {}} />)
    await pick(fields()[0], 'Modified')
    expect(values()[0]).toHaveAttribute('placeholder', '2026-01-01 or 7 days')
    fireEvent.click(conditions()[0])
    for (const o of ['is after', 'is before', 'is on']) expect(await screen.findByRole('option', { name: o })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'contains' })).toBeNull()
    expect(withField({ field: 'name', op: 'contains', value: 'x' }, 'size')).toEqual({ field: 'size', op: 'gt', value: 'x' })
    expect(withField({ field: 'name', op: 'eq', value: 'x' }, 'owner')).toEqual({ field: 'owner', op: 'eq', value: 'x' })
  })

  it('a query saved from the former canned list opens as the builder form that runs it', async () => {
    render(<QueryView onOpen={() => {}} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Recent, saved long ago' }))
    await waitFor(() => expect(runs).toHaveLength(1))
    expect(runs[0].builder).toMatchObject({ filters: [{ field: 'modified', op: 'gt', value: '3 days' }], sort: 'modified', dir: 'desc', limit: 40, subtree: 'reports' })
    expect(values()[0]).toHaveValue('3 days')
    expect(fromCanned({ canned: 'big_directories', n: 9 })).toMatchObject({ source: 'directories', sort: 'bytes', limit: 9 })
    expect(fromCanned({ canned: 'by_extension' })).toMatchObject({ groupBy: 'extension', sort: 'bytes' })
    expect(fromCanned({ canned: 'unknown' })).toBeNull()
  })

  it('shows the SQL of the form on screen', async () => {
    render(<QueryView onOpen={() => {}} />)
    fireEvent.change(values()[0], { target: { value: 'q1' } })
    fireEvent.click(screen.getByRole('button', { name: 'SQL for this query' }))
    expect(await screen.findByText('INSERT INTO t SELECT 1;')).toBeInTheDocument()
    expect(sqlAsked[0].builder.filters).toEqual([{ field: 'name', op: 'contains', value: 'q1' }])
  })

  it('directories offer only the fields a directory has', async () => {
    render(<QueryView onOpen={() => {}} />)
    await pick(screen.getByRole('combobox', { name: 'Source' }), 'Directories')
    fireEvent.click(fields()[0])
    expect(await screen.findByRole('option', { name: 'Owner' })).toBeInTheDocument()
    expect(screen.queryByRole('option', { name: 'Text content' })).toBeNull()
    expect(screen.queryByRole('option', { name: 'Path' })).toBeNull()
  })
})

describe('a query that left a library out', () => {
  it('names it, with the reason, above the rows it has', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      if (url === '/api/query/saved') return json([])
      if (url === '/api/query') {
        return json({ ...ROWS, omitted: [
          { id: 'shelf', label: 'Shelf', reason: 'being built; searchable once its first pass is done', partial: false },
          { id: 'archive', label: 'Archive', reason: 'its index has no extracted text, so text conditions match nothing there', partial: true },
        ], failed: [{ library: 'Shelf', error: 'being built; searchable once its first pass is done' }] })
      }
      if (url === '/api/config') return json({ appName: 'Studio', libraries: [] })
      return json({})
    }))
    render(<QueryView onOpen={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'Run query' }))
    const line = await screen.findByRole('status')
    expect(line).toHaveTextContent('Left out: Shelf (being built; searchable once its first pass is done). Answered in part: Archive (its index has no extracted text, so text conditions match nothing there).')
    expect(screen.getByText('reports/q1.pdf')).toBeInTheDocument()
    // Said once, in the line, not again beside the row count.
    expect(screen.queryByText(/not answered:/)).toBeNull()
  })
})
