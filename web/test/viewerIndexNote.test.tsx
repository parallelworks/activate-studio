import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setLibrary } from '../src/api'
import { Viewer } from '../src/components/Viewer'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

/** A workbook in a library on another system whose index holds no text
 *  for it, with what the server says about the index. */
function serve(index?: unknown) {
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    const p = new URL(url, 'http://x').searchParams.get('path') ?? ''
    if (url.startsWith('/api/kb/file')) return json({ path: p, kind: 'other', size: 10, mtime: 1_795_429_000, truncated: false, content: null, source: 'none', ...(index ? { index } : {}) })
    if (url.startsWith('/api/kb/tags')) return json({ own: [], inherited: [], stored: 'files' })
    if (url.startsWith('/api/tags')) return json({ tags: [] })
    if (url.startsWith('/api/kb/sheet')) return json({ sheets: [{ name: 'Summary' }], index: 0, name: 'Summary', rows: 1, cols: 1, totalRows: 1, totalCols: 1, colWidths: [80], cells: [{ r: 0, c: [[0, 'from the grid']] }] })
    return json({})
  }))
}

async function indexedTab() {
  render(<Viewer path="orders/response.xlsx" onDeleted={() => {}} />)
  fireEvent.click(await screen.findByRole('tab', { name: 'Indexed text' }))
}

afterEach(() => { vi.unstubAllGlobals(); setLibrary('kb') })

describe('the Indexed text tab of a file with no text in the index', () => {
  it('says the library is indexing when it is', async () => {
    serve({ state: 'indexing' })
    await indexedTab()
    expect(await screen.findByText(/Not indexed yet\. The library is indexing now/)).toBeInTheDocument()
    expect(screen.queryByText(/No text is stored/)).toBeNull()
  })

  it('says the file changed after the last index pass, and when that was', async () => {
    serve({ state: 'newer', indexedAt: '2026-11-23T10:00:00Z' })
    await indexedTab()
    const note = await screen.findByText(/Added or changed after the library's last index pass on .*2026.*Rebuild the library to add its text\./)
    expect(note).toBeInTheDocument()
  })

  it('says no text is stored for a file that was indexed and has none', async () => {
    serve()
    await indexedTab()
    expect(await screen.findByText('No text is stored in the index for this file.')).toBeInTheDocument()
  })
})

describe('the Indexed text tab of a file in a library on this computer', () => {
  it('says the background sync adds a new file, and how to add it at once', async () => {
    serve({ state: 'newer', syncEvery: 300, catchUp: 'sync' })
    await indexedTab()
    expect(await screen.findByText("Added or changed since the library's last index pass. The background sync, every 5 minutes, adds its text; Sync now at the bottom of the navigation adds it at once.")).toBeInTheDocument()
    expect(screen.queryByText(/No text is stored/)).toBeNull()
  })

  it('names Rebuild for a library built of a folder here', async () => {
    serve({ state: 'newer', syncEvery: 60, catchUp: 'rebuild' })
    await indexedTab()
    expect(await screen.findByText(/The background sync, every minute, adds its text; Rebuild in Settings, Indexing, Libraries adds it at once\./)).toBeInTheDocument()
  })

  it('says the sync is paused when it is', async () => {
    serve({ state: 'newer', syncEvery: 0, catchUp: 'sync' })
    await indexedTab()
    expect(await screen.findByText(/The background sync is paused, so Sync now at the bottom of the navigation adds its text\./)).toBeInTheDocument()
  })
})

it('says a library on this computer with no index yet has none, and how to build it', async () => {
  serve({ state: 'unindexed', catchUp: 'sync' })
  await indexedTab()
  expect(await screen.findByText('This library has no index yet. Build index in Settings, Indexing, Building adds its text.')).toBeInTheDocument()
})
