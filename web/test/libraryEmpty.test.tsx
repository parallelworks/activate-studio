import { act, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { LibraryView } from '../src/views/LibraryView'
import { setLibrary } from '../src/api'
import { reloadAppConfig } from '../src/config'
import { resetTransfersProbe } from '../src/transferApi'

// An empty library says so at the top of the tree, with how to add to it
// where one may; a library that cannot be listed says why, rather than
// looking empty.
const lib = (id: string, label: string, writable: boolean) => ({
  id, label, primary: id === 'kb', writable, pinned: false, source: true, caps: { index: true, fullText: true, vectors: false },
})
const json = (b: unknown, status = 200) => new Response(JSON.stringify(b), { status, headers: { 'Content-Type': 'application/json' } })

function serve(tree: () => Response) {
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = String(input)
    if (url.startsWith('/api/config')) return json({ appName: 'Studio', kbLabel: 'Projects', libraries: [lib('kb', 'Projects', true), lib('u-mine', 'My library', true), lib('ref', 'Reference', false)] })
    if (url.startsWith('/api/kb/tree')) return tree()
    if (url.startsWith('/api/transfers')) return json({ locations: [] })
    return json({})
  }))
}

beforeEach(() => {
  resetTransfersProbe()
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('ResizeObserver', class { observe() {} unobserve() {} disconnect() {} })
})
afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks(); setLibrary('kb') })

async function open(id: string) {
  setLibrary(id)
  render(<LibraryView display={null} onDisplay={() => {}} />)
  await act(async () => { reloadAppConfig(); await new Promise(r => setTimeout(r, 20)) })
}

describe('an empty library', () => {
  it('says it is empty and how to add to it', async () => {
    serve(() => json({ path: '', entries: [] }))
    await open('u-mine')
    expect(await screen.findByText(/Nothing here yet\. Add files with \+ Add/)).toBeInTheDocument()
  })

  it('says it is empty, without offering to add, when it is read only', async () => {
    serve(() => json({ path: '', entries: [] }))
    await open('ref')
    expect(await screen.findByText('Nothing here yet.')).toBeInTheDocument()
  })

  it('says why when it cannot be listed, rather than looking empty', async () => {
    serve(() => json({ error: 'the session to cluster-a could not be opened' }, 502))
    await open('u-mine')
    expect(await screen.findByRole('alert')).toHaveTextContent('This library could not be listed: the session to cluster-a could not be opened')
    expect(screen.queryByText(/Nothing here yet/)).toBeNull()
  })

  it('shows no hint once there are files', async () => {
    serve(() => json({ path: '', entries: [{ name: 'a.md', path: 'a.md', type: 'file', size: 1, mtime: 0 }] }))
    await open('u-mine')
    expect(await screen.findByText('a.md')).toBeInTheDocument()
    expect(screen.queryByText(/Nothing here yet/)).toBeNull()
  })
})
