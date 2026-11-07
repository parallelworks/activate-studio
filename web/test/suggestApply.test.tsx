import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LibraryView } from '../src/views/LibraryView'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

afterEach(() => vi.unstubAllGlobals())

describe('applying proposed labels', () => {
  it('says when the server refuses them, and keeps the proposals that were not saved', async () => {
    const posts: unknown[] = []
    vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
      const url = String(input)
      if (url === '/api/config') return json({ appName: 'Studio', aiEnabled: true, platform: false, kbLabel: 'Library' })
      if (url.startsWith('/api/kb/tree')) {
        return json({ path: '', entries: url.includes('path=&') || url.endsWith('path=') ? [{ name: 'docs', path: 'docs', type: 'dir', size: 0, mtime: 0, kind: 'dir' }] : [] })
      }
      if (url.startsWith('/api/kb/label-suggestions')) {
        return json({ considered: 2, proposals: [{ path: 'docs/a.md', labels: ['draft'], why: '' }, { path: 'docs/b.md', labels: ['final'], why: '' }] })
      }
      if (url === '/api/kb/tags' && init?.method === 'POST') {
        const body = JSON.parse(String(init.body))
        posts.push(body)
        return body.add[0] === 'final'
          ? json({ error: 'no such file: docs/b.md' }, 404)
          : json({ updated: { 'docs/a.md': ['draft'] }, stored: 'files' })
      }
      return json({})
    }))
    render(<LibraryView display={null} onDisplay={() => {}} />)

    fireEvent.contextMenu(await screen.findByText('docs'))
    fireEvent.click(await screen.findByText('Suggest labels…'))
    fireEvent.click(await screen.findByRole('button', { name: 'Apply 2' }))

    await waitFor(() => expect(posts).toHaveLength(2))
    expect(await screen.findByText('These labels were not saved: no such file: docs/b.md')).toBeInTheDocument()
    expect(screen.getByText('docs/b.md')).toBeInTheDocument()
    expect(screen.queryByText('docs/a.md')).toBeNull()
  })
})
