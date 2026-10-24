import { act, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { FOCUS_LIBRARY_KEY, OmittedLine, type Omission } from '../src/components/OmittedLine'
import { SearchView } from '../src/views/SearchView'

const building: Omission = { id: 'shelf', label: 'Shelf', reason: 'being built; searchable once its first pass is done', partial: false }
const noWords: Omission = { id: 'archive', label: 'Archive', reason: 'words not searched yet: its text is being read', partial: true }

describe('the line naming libraries left out of a search or a query', () => {
  afterEach(() => { vi.unstubAllGlobals(); sessionStorage.clear(); location.hash = '' })

  it('says nothing when nothing was left out', () => {
    const { container } = render(<OmittedLine omitted={[]} />)
    expect(container).toBeEmptyDOMElement()
    render(<OmittedLine omitted={undefined} />)
    expect(screen.queryByRole('status')).toBeNull()
  })

  it('names libraries left out and those searched in part, each with its reason', () => {
    render(<OmittedLine omitted={[building, noWords]} />)
    expect(screen.getByRole('status')).toHaveTextContent(
      'Left out: Shelf (being built; searchable once its first pass is done). Searched in part: Archive (words not searched yet: its text is being read).')
  })

  it('says answered in part for a query', () => {
    render(<OmittedLine what="query" omitted={[{ ...noWords, reason: 'its index has no extracted text, so text conditions match nothing there' }]} />)
    expect(screen.getByRole('status')).toHaveTextContent('Answered in part: Archive (its index has no extracted text, so text conditions match nothing there).')
  })

  it('opens the library\'s row in Settings, Libraries', () => {
    render(<OmittedLine omitted={[building]} />)
    const link = screen.getByRole('button', { name: 'Shelf' })
    expect(link).toHaveAttribute('title', 'Open in Settings, Libraries')
    fireEvent.click(link)
    expect(location.hash).toBe('#view=settings:libraries')
    expect(sessionStorage.getItem(FOCUS_LIBRARY_KEY)).toBe('shelf')
  })

  it('shows on the Search page under the result count, with the results it has', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const body = String(input).startsWith('/api/search')
        ? { hits: [{ path: 'notes/a.md', name: 'a.md', size: 1, mtime: 0, mode: 'name', snippet: '', library: { id: 'kb', label: 'Notes' } }], omitted: [building, noWords] }
        : { tags: [] }
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    render(<SearchView onOpen={() => {}} />)
    await act(async () => { window.dispatchEvent(new CustomEvent('ade:search', { detail: { q: 'notes' } })) })
    const line = await screen.findByRole('status')
    expect(line).toHaveTextContent('Left out: Shelf')
    expect(line).toHaveTextContent('Searched in part: Archive')
    expect(screen.getByText('notes/a.md')).toBeInTheDocument()
    // Under the count, above the hits.
    expect(screen.getByText('1 result').compareDocumentPosition(line) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
  })
})
