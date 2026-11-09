import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { MARKDOWN_FORMAT_MAX, Viewer } from '../src/components/Viewer'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

function serve(content: string) {
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    if (url.startsWith('/api/kb/file')) return json({ path: 'notes/a.docx', kind: 'extracted', size: 10, mtime: 1, truncated: false, content, source: 'extracted', format: 'markdown' })
    if (url.startsWith('/api/kb/tags')) return json({ own: [], inherited: [], stored: 'files' })
    if (url.startsWith('/api/tags')) return json({ tags: [] })
    if (url.startsWith('/api/kb/pdf-info') || url.startsWith('/api/kb/preview')) return json({ error: 'no preview' }, 415)
    return json({})
  }))
}

async function indexedBody() {
  render(<Viewer path="notes/a.docx" onDeleted={() => {}} />)
  fireEvent.click(await screen.findByText('Indexed text'))
  return document.querySelector('.viewer-body')!
}

describe('the indexed text of a file', () => {
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

  it('is formatted as Markdown when short', async () => {
    serve('# Title\n\nSome **bold** words.')
    const body = await indexedBody()
    expect(body.querySelector('.md-body')).toBeTruthy()
    expect(body.querySelector('pre.text-body')).toBeNull()
  })

  it('is plain text when too long to format quickly', async () => {
    serve('# Title\n\n' + 'word '.repeat(MARKDOWN_FORMAT_MAX / 5 + 10))
    const body = await indexedBody()
    expect(body.querySelector('pre.text-body')?.textContent).toMatch(/^# Title/)
    expect(body.querySelector('.md-body')).toBeNull()
  })
})
