import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Viewer } from '../src/components/Viewer'
import { LibraryImage, LibraryPageFrame } from '../src/components/LibraryImage'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

/** A library with one PDF, which the test rewrites or removes. */
function library(start: { mtime: number; size: number; pages: number }) {
  const state: { file: { mtime: number; size: number; pages: number } | null; calls: string[] } = { file: start, calls: [] }
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    state.calls.push(url)
    const p = new URL(url, 'http://x').searchParams.get('path') ?? ''
    if (url.startsWith('/api/kb/tags')) return json({ own: [], inherited: [], stored: 'files' })
    if (url.startsWith('/api/tags')) return json({ tags: [] })
    if (!state.file) return json({ error: 'no such file' }, 404)
    if (url.startsWith('/api/kb/file')) return json({ path: p, kind: 'extracted', size: state.file.size, mtime: state.file.mtime, truncated: false, content: 'words', source: 'extracted', format: 'text' })
    if (url.startsWith('/api/kb/pdf-info')) return json({ pages: state.file.pages })
    return json({})
  }))
  return state
}

const pageImages = () => [...document.querySelectorAll<HTMLImageElement>('img.pdf-page-img')]
const asked = (calls: string[], route: string) => calls.filter(u => u.startsWith(route)).length

afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe('a file that changes while it is open', () => {
  it('says the file was moved or deleted, and asks for nothing more', async () => {
    const lib = library({ mtime: 10, size: 100, pages: 6 })
    render(<Viewer path="reports/sample.pdf" onDeleted={() => {}} />)
    await waitFor(() => expect(pageImages().length).toBe(6))
    lib.file = null
    // Several pages fail together.
    for (const img of pageImages().slice(2)) fireEvent.error(img)
    expect(await screen.findByText('This file was moved or deleted')).toBeTruthy()
    expect(screen.getByText('reports/sample.pdf')).toBeTruthy()
    expect(pageImages().length).toBe(0)
    expect(document.querySelector('.pdf-page-js, .extracted-fallback')).toBeNull()
    const after = lib.calls.length
    window.dispatchEvent(new Event('ade-kb-changed'))
    window.dispatchEvent(new Event('focus'))
    await new Promise(r => setTimeout(r, 20))
    expect(lib.calls.length).toBe(after)
  })

  it('shows the pages a rewritten, shorter PDF has', async () => {
    const lib = library({ mtime: 10, size: 100, pages: 6 })
    render(<Viewer path="reports/sample.pdf" onDeleted={() => {}} />)
    await waitFor(() => expect(pageImages().length).toBe(6))
    lib.file = { mtime: 20, size: 60, pages: 4 }
    fireEvent.error(pageImages()[5])
    await waitFor(() => expect(pageImages().length).toBe(4))
    expect(pageImages()[0].getAttribute('src')).toContain('v=20')
    expect(document.querySelector('.pdf-page-js, .extracted-fallback')).toBeNull()
    expect(screen.queryByText('This file was moved or deleted')).toBeNull()
  })

  it('reads the page count again when the file reads the same', async () => {
    const lib = library({ mtime: 10, size: 100, pages: 6 })
    render(<Viewer path="reports/sample.pdf" onDeleted={() => {}} />)
    await waitFor(() => expect(pageImages().length).toBe(6))
    lib.file = { mtime: 10, size: 100, pages: 4 }
    fireEvent.error(pageImages()[4])
    fireEvent.error(pageImages()[5])
    await waitFor(() => expect(pageImages().length).toBe(4))
    expect(asked(lib.calls, '/api/kb/pdf-info')).toBe(2)
  })

  it('takes a rewrite and a removal from a change to the library', async () => {
    const lib = library({ mtime: 10, size: 100, pages: 2 })
    render(<Viewer path="reports/sample.pdf" onDeleted={() => {}} />)
    await waitFor(() => expect(pageImages().length).toBe(2))
    lib.file = { mtime: 30, size: 300, pages: 5 }
    window.dispatchEvent(new Event('ade-kb-changed'))
    await waitFor(() => expect(pageImages().length).toBe(5))
    lib.file = null
    window.dispatchEvent(new Event('ade-kb-changed'))
    expect(await screen.findByText('This file was moved or deleted')).toBeTruthy()
  })
})

describe('a chat reply that shows a library file', () => {
  const answer = (status: number) => vi.stubGlobal('fetch', vi.fn(async () => json({ error: 'x' }, status)))

  it('notes a file that is gone in place of the broken picture', async () => {
    answer(404)
    render(<LibraryImage src="/api/kb/pdf-page?path=reports%2Fsample.pdf&page=1" alt="sample page 1" />)
    fireEvent.error(screen.getByAltText('sample page 1'))
    expect(await screen.findByText('This file is no longer in the library.')).toBeTruthy()
    expect(document.querySelector('img')).toBeNull()
    expect((fetch as unknown as { mock: { calls: unknown[] } }).mock.calls.length).toBe(1)
  })

  it('notes a page the file no longer has', async () => {
    answer(416)
    render(<LibraryImage src="/api/kb/pdf-page?path=reports%2Fsample.pdf&page=9" alt="sample page 9" />)
    fireEvent.error(screen.getByAltText('sample page 9'))
    expect(await screen.findByText('This page is no longer in the file.')).toBeTruthy()
  })

  it('notes an HTML page that is gone in place of its frame', async () => {
    answer(404)
    render(<LibraryPageFrame src="/api/kb/html?path=reports%2Fsample.html" />)
    expect(await screen.findByText('This file is no longer in the library.')).toBeTruthy()
    expect(document.querySelector('iframe')).toBeNull()
  })

  it('frames a page that is there', async () => {
    answer(200)
    render(<LibraryPageFrame src="/api/kb/html?path=reports%2Fsample.html" />)
    await waitFor(() => expect(document.querySelector('iframe')?.getAttribute('src')).toBe('/api/kb/html?path=reports%2Fsample.html'))
  })
})
