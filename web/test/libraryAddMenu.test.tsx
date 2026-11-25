import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// Uploads are queued through the transfers module; here they are only
// recorded, so a test can say what was sent where.
const uploads = vi.hoisted(() => [] as { dir: string; rels: string[] }[])
vi.mock('../src/transfers', async importOriginal => ({
  ...(await importOriginal<typeof import('../src/transfers')>()),
  startUpload: vi.fn(async (items: { rel: string }[], dir: string) => {
    uploads.push({ dir, rels: items.map(i => i.rel) })
    return { done: items.length }
  }),
}))

import { LibraryView } from '../src/views/LibraryView'
import { mayAddLibraries } from '../src/components/LibraryAddDialogs'

// A checkbox forwards its press to its hidden input as a PointerEvent, which
// jsdom does not define.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

// jsdom's File has no text(), which browsers give every Blob.
if (!Blob.prototype.text) {
  Blob.prototype.text = function text(this: Blob) {
    return new Promise<string>((ok, fail) => { const r = new FileReader(); r.onload = () => ok(String(r.result)); r.onerror = () => fail(r.error); r.readAsText(this) })
  }
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

const MAIN = { id: 'kb', label: 'Main folder', primary: true, writable: true }
const SHARED = { id: 'shared-drive', label: 'Shared drive', writable: false }

/** A Studio: sign-in off (`canBuild`) or on, and what the viewer may add of
 *  their own. `after` is the config once a library has been added. */
function studio({ canBuild = true, user = { available: true, ssh: true, pw: true, pwOffered: true, workflow: false }, sections }: {
  canBuild?: boolean
  user?: Record<string, boolean>
  sections?: string[]
} = {}) {
  const calls: { method: string; url: string; body: unknown }[] = []
  let libraries = [MAIN]
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
    calls.push({ method, url, body })
    if (url === '/api/config') return json({ appName: 'Studio', aiEnabled: true, platform: false, kbLabel: 'Library', libraries, sections })
    if (url.startsWith('/api/kb/tree')) return json({ path: '', entries: [] })
    if (url === '/api/libraries' && method === 'GET') return json({ libraries: [], canBuild })
    if (url === '/api/user-libraries' && method === 'GET') return json({ libraries: [], ...user })
    if (url === '/api/setup/status') return json({ standalone: canBuild })
    if (url === '/api/libraries/import/check') {
      const text = String(body?.text ?? '')
      if (!text.includes('activate-studio-libraries')) return json({ error: 'This is not a library list exported from the Studio.' }, 400)
      return json({
        version: 1, exportedAt: '2026-11-10T12:00:00.000Z', studioVersion: 'v1.200', localUser: 'me',
        entries: [{ index: 0, state: 'new', where: '/mnt/gufi/shared', entry: { kind: 'index', label: 'Shared drive', indexRoot: '/mnt/gufi/shared' } }],
      })
    }
    if (url === '/api/libraries' && method === 'POST') {
      libraries = [MAIN, SHARED]
      return json({ library: { id: 'shared-drive', label: 'Shared drive' } })
    }
    return json({})
  }))
  return calls
}

const LIST = JSON.stringify({ format: 'activate-studio-libraries', version: 1, exportedAt: '2026-11-10T12:00:00.000Z', libraries: [{ kind: 'index', label: 'Shared drive', indexRoot: '/mnt/gufi/shared' }] })

/** A drop of files, as the browser hands it to the page. */
function dropOf(files: File[]) {
  const items = files.map(file => ({ kind: 'file', webkitGetAsEntry: () => ({ isFile: true, name: file.name, file: (ok: (f: File) => void) => ok(file) }) }))
  return { dataTransfer: { items, files, types: ['Files'] } }
}

const more = () => screen.findByRole('button', { name: 'More ways to add' })
async function openMenu() {
  fireEvent.click(await more())
  return screen.findByRole('menu')
}

beforeEach(() => { uploads.length = 0 })
afterEach(() => vi.unstubAllGlobals())

describe('who may add libraries', () => {
  it('anyone on a Studio with sign-in off; with sign-in on, a signed-in viewer with pw ssh or the workflow', () => {
    const none = { available: true, ssh: false, pwOffered: false, workflow: false }
    expect(mayAddLibraries(true, { ...none, ssh: true }, false)).toBe(true)
    expect(mayAddLibraries(false, none, true)).toBe(false)
    expect(mayAddLibraries(false, { ...none, pwOffered: true }, false)).toBe(true)
    expect(mayAddLibraries(false, { ...none, workflow: true }, true)).toBe(true)
    // The workflow needs the platform; nothing is offered to a viewer who is not signed in.
    expect(mayAddLibraries(false, { ...none, workflow: true }, false)).toBe(false)
    expect(mayAddLibraries(false, { ...none, available: false, pwOffered: true }, false)).toBe(false)
    expect(mayAddLibraries(true, null, false)).toBe(false)
  })
})

describe('the Library\'s + Add menu', () => {
  it('offers Add files, New file, Add a library and Import a library list', async () => {
    studio()
    render(<LibraryView display={null} onDisplay={() => {}} />)
    const menu = await openMenu()
    const items = within(menu).getAllByRole('menuitem').map(i => i.textContent)
    expect(items).toHaveLength(4)
    expect(items[0]).toMatch(/^Add files…/)
    expect(items[1]).toMatch(/^New file…/)
    expect(items[2]).toMatch(/^Add a library…/)
    expect(items[3]).toMatch(/^Import a library list…/)
  })

  it('is + Add alone for a viewer who may not add libraries, with no Add a library in its panel', async () => {
    studio({ canBuild: false, user: { available: true, ssh: false, pw: false, pwOffered: false, workflow: false } })
    render(<LibraryView display={null} onDisplay={() => {}} />)
    await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(c => c[0] === '/api/user-libraries')).toBe(true))
    await act(async () => {})
    expect(screen.queryByRole('button', { name: 'More ways to add' })).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: '+ Add' }))
    expect(await screen.findByLabelText('Destination folder')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Add a library…' })).toBeNull()
  })

  it('offers libraries with sign-in on to a viewer who can add over pw ssh', async () => {
    studio({ canBuild: false, user: { available: true, ssh: false, pw: true, pwOffered: true, workflow: false } })
    render(<LibraryView display={null} onDisplay={() => {}} />)
    const menu = await openMenu()
    expect(within(menu).getByRole('menuitem', { name: /Add a library/ })).toBeInTheDocument()
    expect(within(menu).getByRole('menuitem', { name: /Import a library list/ })).toBeInTheDocument()
  })

  it('opens Add a library in a dialog over the page', async () => {
    studio()
    render(<LibraryView display={null} onDisplay={() => {}} />)
    fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: /Add a library/ }))
    const dialog = await screen.findByRole('dialog', { name: 'Add a library' })
    // The form Settings shows: where, how, and the form for the pair.
    expect(await within(dialog).findByRole('button', { name: 'This computer' })).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Remote computer' })).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Index a folder' })).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Folder to index')).toBeInTheDocument()
  })

  it('imports a pasted list, closes, and the new library joins the switcher', async () => {
    const calls = studio()
    render(<LibraryView display={null} onDisplay={() => {}} />)
    fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: /Import a library list/ }))
    const dialog = await screen.findByRole('dialog', { name: 'Import a library list' })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Paste the list instead' }))
    fireEvent.change(within(dialog).getByLabelText('Library list'), { target: { value: LIST } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Read the list' }))
    expect(await within(dialog).findByText('Shared drive')).toBeInTheDocument()
    const configsBefore = calls.filter(c => c.url === '/api/config').length
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add 1 library' }))

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Import a library list' })).toBeNull())
    expect(calls.find(c => c.method === 'POST' && c.url === '/api/libraries')?.body).toMatchObject({ label: 'Shared drive', indexRoot: '/mnt/gufi/shared' })
    // The app's configuration is read again, and the switcher shows the library.
    await waitFor(() => expect(calls.filter(c => c.url === '/api/config').length).toBeGreaterThan(configsBefore))
    const picker = await screen.findByRole('combobox', { name: 'Library' })
    fireEvent.click(picker)
    expect(await screen.findByRole('option', { name: /Shared drive/ })).toBeInTheDocument()
  })

  it('reads a file dropped onto the import dialog', async () => {
    studio()
    render(<LibraryView display={null} onDisplay={() => {}} />)
    fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: /Import a library list/ }))
    const dialog = await screen.findByRole('dialog', { name: 'Import a library list' })
    const file = new File([LIST], 'team.studio-libraries.json', { type: 'application/json' })
    fireEvent.drop(within(dialog).getByTestId('library-import'), dropOf([file]))
    expect(await within(dialog).findByText('Shared drive')).toBeInTheDocument()
    // The drop is the dialog's: nothing was added to the library as a file.
    expect(uploads).toEqual([])
  })

  it('works with Library the only page shown', async () => {
    studio({ sections: ['library'] })
    render(<LibraryView display={null} onDisplay={() => {}} />)
    fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: /Import a library list/ }))
    expect(await screen.findByRole('dialog', { name: 'Import a library list' })).toBeInTheDocument()
  })
})

describe('adding files', () => {
  it('+ Add opens the panel, and a chosen file goes to the destination folder', async () => {
    studio()
    render(<LibraryView display={null} onDisplay={() => {}} />)
    await more()
    fireEvent.click(screen.getByRole('button', { name: '+ Add' }))
    expect(await screen.findByLabelText('Destination folder')).toHaveValue('uploads')
    const input = document.querySelector<HTMLInputElement>('.upload-pop input[type=file][multiple]')!
    fireEvent.change(input, { target: { files: [new File(['hello'], 'notes.md')] } })
    await waitFor(() => expect(uploads).toEqual([{ dir: 'uploads', rels: ['notes.md'] }]))
    // The panel closes, leaving the viewer where the file opens uncovered
    // (phase 4, F).
    await waitFor(() => expect(screen.queryByLabelText('Destination folder')).not.toBeInTheDocument())
  })

  it('the menu\'s Add files opens the same panel', async () => {
    studio()
    render(<LibraryView display={null} onDisplay={() => {}} />)
    fireEvent.click(within(await openMenu()).getByRole('menuitem', { name: /Add files/ }))
    expect(await screen.findByLabelText('Destination folder')).toBeInTheDocument()
  })

  it('a dropped library list offers its import; any other JSON file is added as a file', async () => {
    studio()
    const { container } = render(<LibraryView display={null} onDisplay={() => {}} />)
    await more()
    const page = container.querySelector('.library')!

    fireEvent.drop(page, dropOf([new File(['{"a":1}'], 'data.json', { type: 'application/json' })]))
    await waitFor(() => expect(uploads).toEqual([{ dir: 'uploads', rels: ['data.json'] }]))

    fireEvent.drop(page, dropOf([new File([LIST], 'team.studio-libraries.json', { type: 'application/json' })]))
    const dialog = await screen.findByRole('dialog', { name: 'Import a library list' })
    expect(await within(dialog).findByText('Shared drive')).toBeInTheDocument()
    expect(uploads).toHaveLength(1)
    // Adding it as a file is still a choice.
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add the file to the library instead' }))
    await waitFor(() => expect(uploads).toEqual([
      { dir: 'uploads', rels: ['data.json'] },
      { dir: 'uploads', rels: ['team.studio-libraries.json'] },
    ]))
  })

  it('a dropped library list is added as a file for a viewer who may not add libraries', async () => {
    studio({ canBuild: false, user: { available: true, ssh: false, pw: false, pwOffered: false, workflow: false } })
    const { container } = render(<LibraryView display={null} onDisplay={() => {}} />)
    await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(c => c[0] === '/api/user-libraries')).toBe(true))
    await act(async () => {})
    fireEvent.drop(container.querySelector('.library')!, dropOf([new File([LIST], 'team.studio-libraries.json')]))
    await waitFor(() => expect(uploads).toEqual([{ dir: 'uploads', rels: ['team.studio-libraries.json'] }]))
    expect(screen.queryByRole('dialog')).toBeNull()
  })
})
