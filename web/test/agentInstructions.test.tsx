import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AgentsView } from '../src/views/AgentsView'
import { LibraryFolders, instructionLibraries } from '../src/components/ApplyToFolder'

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

let folders: unknown[]
let posts: { url: string; body: Record<string, unknown> }[]
let trees: string[]

beforeEach(() => {
  localStorage.setItem('ade-agents-page', 'library')
  posts = []
  trees = []
  folders = [{ library: 'main', label: 'Engineering Library', folder: 'agents/reviewer', path: 'agents/reviewer/AGENTS.md', space: false, exists: true, instructions: ['reviewer'], skills: [] }]
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    if (init?.method === 'POST' && url.startsWith('/api/agents-md/')) {
      const body = JSON.parse(String(init.body))
      posts.push({ url, body })
      if (url.endsWith('/remove')) folders = []
      return json({ library: 'main', label: 'Engineering Library', folder: body.folder, path: `${body.folder}/AGENTS.md`, space: false, exists: true, instructions: [body.instructions], skills: [] })
    }
    if (url === '/api/agents-md/folders') return json({ folders })
    if (url === '/api/libraries') return json({ libraries: [
      { id: 'main', label: 'Engineering Library', primary: true, writable: true },
      { id: 'mine', label: 'My library', primary: false, writable: true, space: true, site: 'cluster' },
      { id: 'team', label: 'Team folder', primary: false, writable: true, space: true, sharedFrom: 'pat', site: 'cluster' },
      { id: 'docs', label: 'Docs', primary: false, writable: false },
    ] })
    if (url.startsWith('/api/kb/tree')) {
      const q = new URL(url, 'http://x').searchParams
      trees.push(`${q.get('library')}:${q.get('path')}`)
      const kids: Record<string, string[]> = { '': ['reports', 'agents'], reports: ['q3'], 'reports/q3': [] }
      const at = q.get('path') ?? ''
      return json({ entries: [...(kids[at] ?? []).map(n => ({ name: n, path: at ? `${at}/${n}` : n, type: 'dir' })), { name: 'notes.md', path: 'notes.md', type: 'file' }] })
    }
    if (url.startsWith('/api/extensions')) {
      return json({ dir: '/kb/.agents', tools: [], personas: [{ name: 'reviewer', description: 'Reviews drafts.', shared: true, icon: '' }], skills: [{ name: 'pump', description: 'Sizes a pump.', file: 'pump.md' }] })
    }
    return json({ runs: [], tasks: [], agents: [] })
  }))
})
afterEach(() => { vi.unstubAllGlobals(); localStorage.clear() })

describe('agent instructions', () => {
  it('names the page for instructions and lists the folders that carry them', async () => {
    render(<AgentsView onOpen={() => {}} />)
    expect(await screen.findByText('Instructions and skills')).toBeInTheDocument()
    expect(await screen.findByText(/Agent instructions \(1\)/)).toBeInTheDocument()
    expect(await screen.findByText('· in 1 folder')).toBeInTheDocument()
    expect(screen.getByText('Folders with agent instructions (1)')).toBeInTheDocument()
    expect(screen.getByText('agents/reviewer')).toBeInTheDocument()
  })

  it('applies instructions to a folder it suggests under agents/', async () => {
    render(<AgentsView onOpen={() => {}} />)
    const row = (await screen.findByTitle('reviewer')).closest('.ov-row') as HTMLElement
    fireEvent.click(within(row).getByText('apply to a folder'))
    const folder = await screen.findByLabelText('Folder')
    expect(folder).toHaveValue('agents/reviewer')
    fireEvent.change(folder, { target: { value: 'reports/q3' } })
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0]).toEqual({ url: '/api/agents-md/apply', body: { library: '', folder: 'reports/q3', instructions: 'reviewer' } })
  })

  it('takes instructions off a folder', async () => {
    render(<AgentsView onOpen={() => {}} />)
    fireEvent.click(await screen.findByText('reviewer · take off'))
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0]).toEqual({ url: '/api/agents-md/remove', body: { library: '', folder: 'agents/reviewer', instructions: 'reviewer' } })
    expect(await screen.findByText('Folders with agent instructions (0)')).toBeInTheDocument()
  })

  it('offers the main library and the person\'s own library, never a shared folder or a read-only library', () => {
    const ids = instructionLibraries([
      { id: 'main', label: 'Main', primary: true, writable: true },
      { id: 'mine', label: 'Mine', primary: false, writable: true, space: true },
      { id: 'team', label: 'Team', primary: false, writable: true, space: true, sharedFrom: 'pat' },
      { id: 'docs', label: 'Docs', primary: false, writable: false },
    ]).map(l => l.id)
    expect(ids).toEqual(['main', 'mine'])
  })

  it('picks the folder by opening it in the library\'s folders', async () => {
    render(<AgentsView onOpen={() => {}} />)
    const row = (await screen.findByTitle('reviewer')).closest('.ov-row') as HTMLElement
    fireEvent.click(within(row).getByText('apply to a folder'))
    const list = await screen.findByRole('group', { name: 'Folders in Engineering Library' })
    expect(within(list).getByText('Top of Engineering Library')).toBeInTheDocument()
    // Folders only, in order; a file is not offered.
    expect((await within(list).findAllByRole('button')).map(b => b.textContent)).toEqual(['Up', 'agents', 'reports'])
    fireEvent.click(within(list).getByRole('button', { name: 'reports' }))
    expect(screen.getByLabelText('Folder')).toHaveValue('reports')
    fireEvent.click(await within(list).findByRole('button', { name: 'q3' }))
    expect(screen.getByLabelText('Folder')).toHaveValue('reports/q3')
    expect(await within(list).findByText('No folders inside')).toBeInTheDocument()
    fireEvent.click(within(list).getByRole('button', { name: 'Up' }))
    expect(screen.getByLabelText('Folder')).toHaveValue('reports')
    fireEvent.click(screen.getByRole('button', { name: 'Apply' }))
    await waitFor(() => expect(posts).toHaveLength(1))
    expect(posts[0].body).toEqual({ library: '', folder: 'reports', instructions: 'reviewer' })
    expect(trees).toContain('main:reports/q3')
  })

  it('lists the folders of the library chosen, as My library', async () => {
    const picked: string[] = []
    render(<LibraryFolders library="mine" libraryLabel="My library (yours)" onPick={f => picked.push(f)} />)
    fireEvent.click(await screen.findByRole('button', { name: 'agents' }))
    expect(picked).toEqual(['agents'])
    await waitFor(() => expect(trees).toEqual(['mine:', 'mine:agents']))
  })
})
