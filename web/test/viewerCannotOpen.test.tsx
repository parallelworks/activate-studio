import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { Viewer } from '../src/components/Viewer'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

function serve(status: number, error: string) {
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    if (url.startsWith('/api/kb/file')) return json({ error }, status)
    if (url.startsWith('/api/kb/tags')) return json({ own: [], inherited: [], stored: 'files' })
    if (url.startsWith('/api/tags')) return json({ tags: [] })
    return json({})
  }))
}

const MOVED = /moved, renamed, or deleted/

describe('a file that cannot be opened', () => {
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('says it may have moved when it is not there', async () => {
    serve(404, 'no such file')
    render(<Viewer path="docs/gone.md" />)
    expect(await screen.findByText(/no such file/)).toBeTruthy()
    expect(screen.getByText(MOVED)).toBeTruthy()
  })

  it('gives only the reason when the file is there but cannot be read', async () => {
    serve(409, "the library's folder on that host is not known: remove the library and add it again with its source directory, in Settings, Indexing, Libraries")
    render(<Viewer path="u-host-index/a/b.md" />)
    expect(await screen.findByText(/folder on that host is not known/)).toBeTruthy()
    expect(screen.queryByText(MOVED)).toBeNull()
  })
})
