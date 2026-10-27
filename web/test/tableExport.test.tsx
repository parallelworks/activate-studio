import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { exportName, toCsv, toTsv } from '../src/tableExport'
import { SearchView, hitTable } from '../src/views/SearchView'

describe('exporting results', () => {
  it('writes CSV with a header, quoting only fields that need it, values as given', () => {
    const csv = toCsv(['path', 'size'], [['a/b.txt', '10'], ['say "hi", then go', '-5'], ['two\nlines', '']])
    expect(csv).toBe('path,size\r\na/b.txt,10\r\n"say ""hi"", then go",-5\r\n"two\nlines",\r\n')
  })

  it('writes tab-separated text for a paste, with tabs and line breaks inside a value as spaces', () => {
    expect(toTsv(['path', 'snippet'], [['a/b.txt', 'one\ttwo\r\nthree']])).toBe('path\tsnippet\na/b.txt\tone two three\n')
  })

  it('names the file by kind, a few words, and the local date and time', () => {
    const at = new Date(2026, 9, 27, 14, 5)
    expect(exportName('query', '', at)).toBe('query-2026-10-27-1405.csv')
    expect(exportName('search', '  Budget *.xlsx  "Q3 report" ', at)).toBe('search-budget-xlsx-q3-report-2026-10-27-1405.csv')
    expect(exportName('search', 'x'.repeat(60), at)).toBe(`search-${'x'.repeat(40)}-2026-10-27-1405.csv`)
  })

  it('lists search results with every way each was found, size in bytes and time in UTC', () => {
    const hits = [
      { path: 'docs/a.pdf', name: 'a.pdf', size: 2048, mtime: 1761573600, snippet: 'the [budget], in short', mode: 'fts' as const, kinds: ['fts', 'vector'] as ('fts' | 'vector')[], tags: ['review', 'q3'], library: { id: 'l1', label: 'Home' } },
      { path: 'b.txt', name: 'b.txt', size: 0, mtime: 0, snippet: '', mode: 'name' as const },
    ]
    expect(hitTable(hits, false)).toEqual({
      columns: ['path', 'name', 'found by', 'size', 'modified', 'labels', 'snippet'],
      rows: [
        ['docs/a.pdf', 'a.pdf', 'words meaning', '2048', '2025-10-27T14:00:00Z', 'review q3', 'the [budget], in short'],
        ['b.txt', 'b.txt', 'name', '0', '', '', ''],
      ],
    })
    expect(hitTable(hits, true).columns[0]).toBe('library')
    expect(hitTable(hits, true).rows.map(r => r[0])).toEqual(['Home', ''])
  })
})

describe('the Export CSV button on search results', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

  it('saves the results listed as a CSV file named for the search', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const body = String(input).startsWith('/api/search')
        ? { hits: [{ path: 'notes/a, b.md', name: 'a, b.md', size: 12, mtime: 0, mode: 'name', snippet: '' }] }
        : { tags: [] }
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    const blobs: Blob[] = []
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: (b: Blob) => { blobs.push(b); return 'blob:x' }, revokeObjectURL: () => {} }))
    const names: string[] = []
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) { names.push(this.download) })
    render(<SearchView onOpen={() => {}} />)
    await act(async () => { window.dispatchEvent(new CustomEvent('ade:search', { detail: { q: 'notes' } })) })
    fireEvent.click(await screen.findByRole('button', { name: 'Export CSV' }))
    expect(names[0]).toMatch(/^search-notes-\d{4}-\d\d-\d\d-\d{4}\.csv$/)
    const text = await new Promise<string>(ok => { const r = new FileReader(); r.onload = () => ok(String(r.result)); r.readAsText(blobs[0]) })
    expect(text).toBe('path,name,found by,size,modified,labels,snippet\r\n"notes/a, b.md","a, b.md",name,12,,,\r\n')
  })
})
