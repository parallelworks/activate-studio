import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { setLibrary, withLibrary } from '../src/api'
import { Viewer } from '../src/components/Viewer'
import { TagMenu } from '../src/components/TagMenu'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const file = (path: string, content = 'the indexed text') => ({ path, kind: 'extracted', size: 10, mtime: 1_700_000_000, truncated: false, content, source: 'extracted', format: 'text' })
const outline = {
  kind: 'slides', truncated: false,
  slides: [{ n: 1, title: 'Opening', blocks: [{ t: 'p', text: 'Point one', level: 0 }, { t: 'img', media: 'ppt/media/image1.png' }] }],
}

/** A server without LibreOffice: page images are refused; the outline is
 *  there for the formats read without it. */
function serve(urls: string[], opts: { outline?: boolean; stored?: 'files' | 'studio' } = {}) {
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    urls.push(url)
    const p = new URL(url, 'http://x').searchParams.get('path') ?? ''
    if (url.startsWith('/api/kb/file')) return json(file(p))
    if (url.startsWith('/api/kb/tags')) return json({ own: [], inherited: [], stored: opts.stored ?? 'files' })
    if (url.startsWith('/api/tags')) return json({ tags: [] })
    if (url.startsWith('/api/kb/pdf-info')) return json({ error: 'LibreOffice is not available on this deployment' }, 415)
    if (url.startsWith('/api/kb/office-preview')) return opts.outline === false ? json({ error: 'no preview' }, 415) : json(outline)
    return json({})
  }))
}

afterEach(() => { vi.unstubAllGlobals(); setLibrary('kb') })

describe('the Office preview without LibreOffice', () => {
  it('shows a deck\'s slides read from the file, under a Slides tab, with its pictures from the package', async () => {
    const urls: string[] = []
    serve(urls)
    render(<Viewer path="talks/deck.pptx" onDeleted={() => {}} />)
    expect(await screen.findByText('Opening')).toBeInTheDocument()
    expect(screen.getByText('Point one')).toBeInTheDocument()
    expect(screen.getByText('Slide 1')).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Slides' })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: 'Indexed text' })).toBeInTheDocument()
    expect(screen.getByText(/A rendering of its pages needs LibreOffice/)).toBeInTheDocument()
    const img = document.querySelector('img.office-img') as HTMLImageElement
    expect(img.getAttribute('src')).toBe('/api/kb/office-media?path=talks%2Fdeck.pptx&name=ppt%2Fmedia%2Fimage1.png')
    expect(document.querySelector('img.pdf-page-img')).toBeNull()
  })

  it('a file with nothing to preview opens on its indexed text, which says why, and has no Preview tab', async () => {
    const urls: string[] = []
    serve(urls, { outline: false })
    render(<Viewer path="old/deck.ppt" onDeleted={() => {}} />)
    expect(await screen.findByText(/A preview of this file needs LibreOffice, so this is its indexed text/)).toBeInTheDocument()
    expect(screen.getByText('the indexed text')).toBeInTheDocument()
    expect(screen.queryByRole('tab', { name: 'Slides' })).toBeNull()
    // A .ppt is not read for an outline; only the page images were asked for.
    expect(urls.some(u => u.startsWith('/api/kb/office-preview'))).toBe(false)
  })

  it('page images carry the library, so another library\'s pages are found', async () => {
    const urls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      urls.push(url)
      if (url.startsWith('/api/kb/file')) return json({ ...file('paper.pdf'), kind: 'extracted' })
      if (url.startsWith('/api/kb/tags')) return json({ own: [], inherited: [] })
      if (url.startsWith('/api/kb/pdf-info')) return json({ pages: 2 })
      return json({})
    }))
    setLibrary('site')
    render(<Viewer path="paper.pdf" onDeleted={() => {}} />)
    await waitFor(() => expect(document.querySelectorAll('img.pdf-page-img').length).toBe(2))
    const src = (document.querySelector('img.pdf-page-img') as HTMLImageElement).getAttribute('src')
    expect(src).toBe('/api/kb/pdf-page?path=paper.pdf&page=1&library=site')
    expect(screen.getByRole('tab', { name: 'Pages' })).toBeInTheDocument()
  })
})

describe('read-only libraries', () => {
  it('Delete is disabled with the reason; Labels stays', async () => {
    serve([])
    render(<Viewer path="reports/q3.md" onDeleted={() => {}} readOnly="Site index is read only" />)
    const del = await screen.findByRole('button', { name: 'Delete' })
    expect(del).toHaveAttribute('aria-disabled', 'true')
    expect(screen.getByRole('button', { name: 'Labels' })).not.toHaveAttribute('aria-disabled')
  })

  it('the Labels panel says where labels are saved for a library whose files are not written', async () => {
    serve([], { stored: 'studio' })
    render(<TagMenu paths={['reports/q3.md']} onApplied={() => {}} onClose={() => {}} />)
    expect(await screen.findByText('Saved in the Studio; the files are read only.')).toBeInTheDocument()
  })

  it('no such line for the main library', async () => {
    serve([], { stored: 'files' })
    render(<TagMenu paths={['notes/a.md']} onApplied={() => {}} onClose={() => {}} />)
    await screen.findByText('New label')
    expect(screen.queryByText(/Saved in the Studio/)).toBeNull()
  })
})

describe('All libraries', () => {
  it('a delete goes to the file\'s own library; a read takes the combined path as it is', () => {
    setLibrary('all')
    expect(withLibrary('/api/kb/file?path=kb%2Freports%2Fa.pdf', 'DELETE')).toBe('/api/kb/file?path=reports%2Fa.pdf')
    expect(withLibrary('/api/kb/file?path=site%2Freports%2Fa.pdf', 'DELETE')).toBe('/api/kb/file?path=reports%2Fa.pdf&library=site')
    expect(withLibrary('/api/kb/file?path=kb%2Freports%2Fa.pdf')).toBe('/api/kb/file?path=kb%2Freports%2Fa.pdf&library=all')
  })
})
