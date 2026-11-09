import { render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Viewer } from '../src/components/Viewer'

const pdfjs = vi.hoisted(() => ({ result: null as null | { numPages: number } | Error, urls: [] as string[] }))
vi.mock('pdfjs-dist/legacy/build/pdf.worker.min.mjs?url', () => ({ default: '/assets/pdf.worker.mjs' }))
vi.mock('pdfjs-dist/legacy/build/pdf.mjs', () => ({
  GlobalWorkerOptions: {},
  getDocument: ({ url }: { url: string }) => {
    pdfjs.urls.push(url)
    const r = pdfjs.result
    const doc = r instanceof Error ? null : {
      numPages: r?.numPages ?? 0,
      getPage: async () => ({
        getViewport: ({ scale }: { scale: number }) => ({ width: 612 * scale, height: 792 * scale }),
        render: () => ({ promise: Promise.resolve(), cancel() {} }),
      }),
    }
    return { promise: doc ? Promise.resolve(doc) : Promise.reject(r), destroy: async () => {} }
  },
}))

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

/** A server without poppler: page images are refused with the reason. */
function serve() {
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    const p = new URL(url, 'http://x').searchParams.get('path') ?? ''
    if (url.startsWith('/api/kb/file')) return json({ path: p, kind: 'extracted', size: 10, mtime: 1, truncated: false, content: 'the extracted words', source: 'extracted', format: 'text' })
    if (url.startsWith('/api/kb/tags')) return json({ own: [], inherited: [], stored: 'files' })
    if (url.startsWith('/api/tags')) return json({ tags: [] })
    if (url.startsWith('/api/kb/pdf-info')) return json({ error: "poppler's pdfinfo is not installed" }, 415)
    return json({})
  }))
}

afterEach(() => { vi.unstubAllGlobals(); pdfjs.result = null; pdfjs.urls = [] })

describe('a PDF where the server cannot draw its pages', () => {
  it('draws its pages here with pdf.js, from the raw file', async () => {
    serve()
    pdfjs.result = { numPages: 3 }
    render(<Viewer path="papers/a.pdf" onDeleted={() => {}} />)
    await waitFor(() => expect(document.querySelectorAll('.pdf-page-js').length).toBe(3))
    expect(pdfjs.urls[0]).toContain('/api/kb/raw?path=papers%2Fa.pdf')
    await waitFor(() => expect(document.querySelectorAll('.pdf-page-js canvas').length).toBe(3))
    expect(document.querySelector('.extracted-fallback')).toBeNull()
  })

  it('shows the extracted text, with both reasons, when pdf.js cannot read it either', async () => {
    serve()
    pdfjs.result = new Error('Invalid PDF structure')
    render(<Viewer path="papers/b.pdf" onDeleted={() => {}} />)
    expect(await screen.findByText('the extracted words')).toBeInTheDocument()
    expect(screen.getByText(/poppler's pdfinfo is not installed; the PDF could not be read here \(Invalid PDF structure\)/)).toBeInTheDocument()
  })
})
