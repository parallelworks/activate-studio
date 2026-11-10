import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { LibraryImport } from '../src/components/LibraryImport'
import { LibraryExport } from '../src/components/LibraryExport'
import { importEntry } from '../src/libraryLists'
import type { SettingsLibrary, UserLibrary } from '../src/api'

// A checkbox forwards its press to its hidden input as a PointerEvent, which
// jsdom does not define.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}
const readBlob = (b: Blob) => new Promise<string>(ok => { const r = new FileReader(); r.onload = () => ok(String(r.result)); r.readAsText(b) })

type Answer = { status?: number; body: unknown }
/** fetch answered by route, in order for a route asked more than once. */
function stubRoutes(routes: Record<string, Answer | Answer[]>) {
  const calls: { method: string; url: string; body: Record<string, unknown> }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    calls.push({ method, url, body: JSON.parse(String(init?.body ?? '{}')) })
    const r = routes[`${method} ${url}`]
    const a = Array.isArray(r) ? r.shift() : r
    if (!a) return new Response(JSON.stringify({ error: `no stub for ${method} ${url}` }), { status: 404 })
    return new Response(JSON.stringify(a.body), { status: a.status ?? 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

const check = {
  version: 1, exportedAt: '2026-11-10T12:00:00.000Z', studioVersion: 'v1.191', localUser: 'me',
  entries: [
    { index: 0, state: 'new', where: '/mnt/gufi/shared', entry: { kind: 'index', label: 'Shared drive', indexRoot: '/mnt/gufi/shared' } },
    { index: 1, state: 'new', where: 'index.example:/gufi/index', user: '', host: 'index.example',
      entry: { kind: 'ssh', mode: 'connect', label: 'Index server', destination: 'index.example', sshOptions: ['-C'], indexRoot: '/gufi/index' } },
    { index: 2, state: 'present', why: 'Projects', where: '/srv/projects', entry: { kind: 'folder', label: 'Projects', sourceRoot: '/srv/projects' } },
    { index: 3, state: 'unavailable', why: 'Systems on the platform are reached with your own platform key.', where: 'c1:/scratch/gufi',
      entry: { kind: 'pw', mode: 'connect', label: 'Cluster index', cluster: 'c1', indexRoot: '/scratch/gufi' } },
    { index: 4, state: 'invalid', why: 'The kind "tape" is not one this Studio knows.', where: '', entry: { kind: 'tape' } },
  ],
}

const button = (name: string | RegExp) => screen.getByRole('button', { name })

describe('importing a library list', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('previews each entry, ticks only the new ones, and adds them through the Add a library routes', async () => {
    const calls = stubRoutes({
      'POST /api/libraries/import/check': { body: check },
      'POST /api/libraries': { body: { library: { id: 'shared-drive', label: 'Shared drive' } } },
      'POST /api/user-libraries/ssh': { status: 400, body: { error: 'The connection test did not pass. alice@index.example did not accept a key.' } },
    })
    const onAdded = vi.fn()
    const onDone = vi.fn()
    render(<LibraryImport onAdded={onAdded} onDone={onDone} />)
    fireEvent.click(button('Paste the list instead'))
    fireEvent.change(screen.getByLabelText('Library list'), { target: { value: '{"format":"activate-studio-libraries"}' } })
    fireEvent.click(button('Read the list'))
    await screen.findByText('Shared drive')
    expect(calls[0]).toMatchObject({ url: '/api/libraries/import/check', body: { text: '{"format":"activate-studio-libraries"}' } })

    const list = screen.getByRole('list', { name: 'Libraries in the list' })
    expect(within(list).getByText('GUFI index over ssh')).toBeInTheDocument()
    expect(within(list).getByText('index.example:/gufi/index')).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: 'Import Shared drive' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Import Index server' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Import Projects' })).not.toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Import Projects' })).toHaveAttribute('aria-disabled', 'true')
    expect(screen.getByText('Already added')).toBeInTheDocument()
    expect(screen.getByText('as Projects')).toBeInTheDocument()
    expect(screen.getByText(/reached with your own platform key/)).toBeInTheDocument()
    expect(screen.getByText(/"tape" is not one this Studio knows/)).toBeInTheDocument()

    // The file names no user: the login here is offered, and can be changed.
    const user = screen.getByLabelText('Connect as')
    expect(user).toHaveValue('me')
    fireEvent.change(user, { target: { value: 'alice' } })
    expect(screen.getByText(/Connects as alice@index.example/)).toBeInTheDocument()

    fireEvent.click(button('Add 2 libraries'))
    await waitFor(() => expect(onDone).toHaveBeenCalledWith(1))
    expect(calls[1]).toMatchObject({ method: 'POST', url: '/api/libraries', body: { label: 'Shared drive', indexRoot: '/mnt/gufi/shared' } })
    expect(calls[2]).toMatchObject({
      method: 'POST', url: '/api/user-libraries/ssh',
      body: { destination: 'alice@index.example', sshOptions: ['-C'], label: 'Index server', indexRoot: '/gufi/index' },
    })
    expect(onAdded).toHaveBeenCalledWith({ id: 'shared-drive', label: 'Shared drive' })
    expect(screen.getByText('Added')).toBeInTheDocument()
    expect(screen.getByText('Failed')).toBeInTheDocument()
    expect(screen.getByText(/did not accept a key/)).toBeInTheDocument()
  })

  it('a file that is not a list says so', async () => {
    stubRoutes({ 'POST /api/libraries/import/check': { status: 400, body: { error: 'This is not a library list exported from the Studio.' } } })
    render(<LibraryImport />)
    fireEvent.click(button('Paste the list instead'))
    fireEvent.change(screen.getByLabelText('Library list'), { target: { value: '{}' } })
    fireEvent.click(button('Read the list'))
    await screen.findByText('This is not a library list exported from the Studio.')
  })
})

describe('adding one entry', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('a folder to index here runs the build check, and fails when the folder is not here', async () => {
    const calls = stubRoutes({
      'POST /api/libraries/build/check': [
        { body: { ok: true, steps: [], create: ['/srv/missing'], sourceRoot: '/srv/missing', indexDir: null } },
        { body: { ok: true, steps: [], create: ['/fast/idx'], sourceRoot: '/srv/data', indexDir: '/fast/idx' } },
      ],
      'POST /api/libraries/build': { body: { library: { id: 'data', label: 'Data' }, job: {} } },
    })
    const missing = await importEntry({ kind: 'folder', label: 'Missing', sourceRoot: '/srv/missing' })
    expect(missing).toEqual({ state: 'failed', text: 'The folder /srv/missing is not on this computer.' })
    const ok = await importEntry({ kind: 'folder', label: 'Data', sourceRoot: '/srv/data', indexDir: '/fast/idx', depth: 'text' })
    expect(ok).toMatchObject({ state: 'added', id: 'data' })
    // The index folder the build needs is made, as the form does once confirmed.
    expect(calls[2]).toMatchObject({ url: '/api/libraries/build', body: { label: 'Data', sourceRoot: '/srv/data', indexRoot: '/fast/idx', depth: 'text', create: true } })
  })

  it('a folder on a cluster is checked there, then built, and an index connected over ssh gets its read-through host', async () => {
    const calls = stubRoutes({
      'POST /api/user-libraries/pw/build/test': { body: { ok: true, steps: [], create: [], sourceRoot: '/home/me/work', indexDir: '/scratch/i' } },
      'POST /api/user-libraries/pw/build': { body: { id: 'u-c1-work', label: 'Work' } },
      'POST /api/user-libraries/ssh': { body: { id: 'u-box-index', label: 'Box' } },
      'PUT /api/libraries/u-box-index/read-through': { body: { readThrough: { destination: 'files.example' } } },
    })
    expect(await importEntry({ kind: 'pw', mode: 'build', label: 'Work', cluster: 'c1', sourceRoot: '~/work', depth: 'names' }))
      .toMatchObject({ state: 'added', text: 'Building its index there.' })
    expect(calls[1]).toMatchObject({ url: '/api/user-libraries/pw/build', body: { cluster: 'c1', label: 'Work', sourceRoot: '~/work', depth: 'names' } })
    expect(calls[1].body).not.toHaveProperty('create')
    const r = await importEntry({ kind: 'ssh', mode: 'connect', label: 'Box', destination: 'admin@box', indexRoot: '/i', readThrough: { destination: 'files.example', root: '/f' } }, '')
    expect(r).toMatchObject({ state: 'added', text: 'Originals are read through files.example.' })
    expect(calls[2].body).toMatchObject({ destination: 'box' })
    expect(calls[3]).toMatchObject({ method: 'PUT', url: '/api/libraries/u-box-index/read-through', body: { destination: 'files.example', root: '/f' } })
  })
})

describe('exporting libraries', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.restoreAllMocks() })

  const libs = [
    { id: 'kb', label: 'Main', primary: true },
    { id: 'shared', label: 'Shared drive', primary: false, readThrough: null },
  ] as SettingsLibrary[]
  const users = [
    { id: 'u-box', label: 'Index server', transport: 'ssh', existing: true },
    { id: 'u-wf', label: 'Workflow built', transport: 'pw', existing: false, built: false },
  ] as UserLibrary[]

  it('exports the libraries ticked, leaves the user name out by default, and saves the file', async () => {
    const file = { format: 'activate-studio-libraries', version: 1, exportedAt: 'x', libraries: [{ kind: 'index', label: 'Shared drive', indexRoot: '/i' }] }
    const calls = stubRoutes({ 'POST /api/libraries/export': { body: { file, name: 'studio.studio-libraries.json', skipped: [] } } })
    const blobs: Blob[] = []
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: (b: Blob) => { blobs.push(b); return 'blob:x' }, revokeObjectURL: () => {} }))
    const names: string[] = []
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) { names.push(this.download) })
    const onClose = vi.fn()
    render(<LibraryExport open onClose={onClose} libraries={libs} userLibraries={users} />)
    expect(await screen.findByRole('checkbox', { name: 'Export Main' })).not.toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Export Shared drive' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Export Workflow built' })).not.toBeChecked()
    expect(screen.getByText(/not exported/)).toBeInTheDocument()
    expect(screen.getByRole('checkbox', { name: /Leave out the user name/ })).toBeChecked()

    fireEvent.click(button('Export'))
    await waitFor(() => expect(onClose).toHaveBeenCalled())
    expect(calls[0]).toMatchObject({ url: '/api/libraries/export', body: { ids: ['shared', 'u-box'], primary: false, keepUser: false } })
    expect(names).toEqual(['studio.studio-libraries.json'])
    expect(JSON.parse(await readBlob(blobs[0]))).toEqual(file)
  })

  it('the main folder and the user name only when chosen', async () => {
    const calls = stubRoutes({ 'POST /api/libraries/export': { body: { file: {}, name: 'x.studio-libraries.json', skipped: [{ id: 'u-wf', label: 'W', why: 'workflow' }] } } })
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: () => 'blob:x', revokeObjectURL: () => {} }))
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    render(<LibraryExport open onClose={() => {}} libraries={libs} userLibraries={users} />)
    fireEvent.click(await screen.findByRole('checkbox', { name: 'Export Main' }))
    fireEvent.click(screen.getByRole('checkbox', { name: /Leave out the user name/ }))
    fireEvent.click(button('Export'))
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0].body).toMatchObject({ primary: true, keepUser: true })
    expect((calls[0].body.ids as string[]).sort()).toEqual(['kb', 'shared', 'u-box'])
    expect(screen.queryByRole('checkbox', { name: /Share the indexes built here/ })).toBeNull()
  })

  it('a library built here goes as its folder unless its index is shared', async () => {
    const built = [...libs, { id: 'team', label: 'Team share', primary: false, readThrough: null, build: { indexDir: '/srv/idx', managed: true, state: 'ready' } }] as SettingsLibrary[]
    const calls = stubRoutes({ 'POST /api/libraries/export': [{ body: { file: {}, name: 'a.studio-libraries.json', skipped: [] } }, { body: { file: {}, name: 'b.studio-libraries.json', skipped: [] } }] })
    vi.stubGlobal('URL', Object.assign(URL, { createObjectURL: () => 'blob:x', revokeObjectURL: () => {} }))
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
    const { unmount } = render(<LibraryExport open onClose={() => {}} libraries={built} userLibraries={[]} />)
    const share = await screen.findByRole('checkbox', { name: /Share the indexes built here/ })
    expect(share).not.toBeChecked()
    fireEvent.click(button('Export'))
    await waitFor(() => expect(calls).toHaveLength(1))
    expect(calls[0].body).toMatchObject({ shareBuilt: false })
    unmount()
    render(<LibraryExport open onClose={() => {}} libraries={built} userLibraries={[]} />)
    fireEvent.click(await screen.findByRole('checkbox', { name: /Share the indexes built here/ }))
    fireEvent.click(button('Export'))
    await waitFor(() => expect(calls).toHaveLength(2))
    expect(calls[1].body).toMatchObject({ ids: ['shared', 'team'], shareBuilt: true })
  })
})
