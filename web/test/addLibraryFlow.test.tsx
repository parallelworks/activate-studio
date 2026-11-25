import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { AddLibrary, type AddOptions } from '../src/components/AddLibrary'
import { addChoices, firstTarget } from '../src/components/addLibraryTarget'
import { reloadAppConfig } from '../src/config'
import { checked, chooseComputer, chooseFolder, closeMenu, computers, folderRoute, nameComputer, openFolders } from './addLibraryHelpers'

// Add a library showed eight decisions and five paragraphs at once. It is
// two steps: a computer and a folder, checked by itself, then a name, who
// sees it and an index depth, each with a default.

if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
type Call = { method: string; url: string; body?: Record<string, unknown>; signal?: AbortSignal | null }
const SYSTEMS = { usable: true, why: null, error: null, systems: [
  { name: 'cluster-a', label: 'Cluster A', status: 'active', running: true, type: 'existing' },
  { name: 'cluster-c', label: 'Cluster C', status: 'off', running: false, type: 'aws-slurm' },
] }
const STEPS = [{ name: 'account', ok: true, detail: 'Reached as ada.' }]
const SPACE = { ok: true, sourceRoot: '/data', create: [], account: 'ada', acl: true, fs: 'ext4', owned: true, writable: true, readOnly: null, canShare: true, shareWhy: null, myLibraryWhy: null, embedMB: null, steps: STEPS }
const COUNT = { count: { files: 1200, bytes: 5_000_000, kinds: [{ name: 'pdf', files: 900 }, { name: 'csv', files: 300 }], partial: false } }

let space: Record<string, unknown> = SPACE
let hold: ((url: string) => Promise<void> | undefined) | null = null
function stub(more: (m: string, u: string, b?: Record<string, unknown>) => Response | undefined = () => undefined, o: { signIn?: boolean; spaces?: string[]; systems?: unknown; hosts?: string[] } = {}) {
  const calls: Call[] = []
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input), method = init?.method ?? 'GET'
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
    calls.push({ method, url, body, signal: init?.signal })
    await hold?.(url)
    const own = more(method, url, body)
    if (own) return own
    const folder = folderRoute(url)
    if (folder) return folder
    if (url.startsWith('/api/config')) return json({ appName: 'Studio', kbLabel: 'Shared library', signIn: !!o.signIn, libraries: [] })
    if (url === '/api/user-libraries/pw/systems') return json(o.systems ?? SYSTEMS)
    if (url === '/api/user-libraries/ssh/hosts') return json({ hosts: o.hosts ?? ['example-box', 'lab-2'] })
    if (url === '/api/user-libraries/space' && method === 'GET') return json({ available: true, why: '', cluster: 'cluster-a', suggested: '~/studio', spaces: o.spaces ?? ['mine'] })
    if (url === '/api/user-libraries/space/test') return json(space)
    if (url.endsWith('/count')) return json(COUNT)
    if (url === '/api/libraries/build/check') return json({ ok: true, steps: [{ name: 'source', ok: true, detail: 'It can be read.' }], create: [] })
    if (url === '/api/libraries/check') return json({ ok: true, steps: [{ name: 'index', ok: true, detail: 'A GUFI index.' }], fullText: true, vectors: false })
    return json({ error: `no route for ${method} ${url}` }, 404)
  }))
  return calls
}
const tests = (calls: Call[]) => calls.filter(c => c.url === '/api/user-libraries/space/test')
const SIGN_IN: AddOptions = { buildHere: false, ssh: false, pw: true, workflow: false, space: true }
const ONE: AddOptions = { buildHere: true, ssh: true, pw: true, workflow: false, space: true, copy: true, bucket: true }
const STANDALONE: AddOptions = { ...ONE, pw: true }
const button = (name: string | RegExp) => screen.getByRole('button', { name })
const show = async (options: AddOptions, onAdded = vi.fn()) => {
  const r = render(<AddLibrary options={options} onAdded={onAdded} />)
  await act(async () => { reloadAppConfig(); await new Promise(f => setTimeout(f, 20)) })
  return { onAdded, ...r }
}

beforeEach(() => { space = SPACE; hold = null })
afterEach(() => vi.unstubAllGlobals())

describe('Add a library, step one', () => {
  it('is one row and a link, with no settings, no paragraphs and no Check button', async () => {
    stub()
    const { container } = await show(ONE)
    expect(await screen.findByTestId('add-library-folder')).toHaveTextContent('Choose a folder')
    expect(screen.getByTestId('add-library-computer')).toHaveTextContent('This computer')
    expect(button('Connect an existing index')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /check the folder/i })).toBeNull()
    for (const t of ['Who sees it', 'Index depth', 'Build with', 'What']) expect(screen.queryByText(t)).toBeNull()
    expect(container.querySelectorAll('p')).toHaveLength(0)
    expect(button('Index')).toBeDisabled()
  })

  it('checks the folder by itself, and cancels the check when the folder changes', async () => {
    const calls = stub()
    let release = () => {}
    hold = url => (url === '/api/user-libraries/space/test' && tests(calls).length === 1 ? new Promise<void>(f => { release = f }) : undefined)
    await show(SIGN_IN)
    await waitFor(() => expect(screen.getByTestId('add-library-computer')).toHaveTextContent('Cluster A'))
    await chooseFolder('/data')
    await waitFor(() => expect(tests(calls)).toHaveLength(1))
    expect(screen.getByTestId('folder-check')).toHaveAttribute('aria-busy', 'true')
    expect(button('Index')).toBeDisabled()
    const first = tests(calls)[0]
    expect(first.body).toEqual({ cluster: 'cluster-a', sourceRoot: '/data' })
    expect(first.signal?.aborted).toBe(false)
    await chooseFolder('/other')
    expect(first.signal?.aborted).toBe(true)
    await checked()
    expect(tests(calls).at(-1)?.body).toEqual({ cluster: 'cluster-a', sourceRoot: '/other' })
    // What the cancelled one answers is not shown.
    space = { ...SPACE, ok: false, steps: [{ name: 'folder', ok: false, detail: 'Cannot open /data.' }] }
    await act(async () => { release(); await new Promise(f => setTimeout(f, 20)) })
    expect(screen.queryByText('Cannot open /data.')).toBeNull()
    expect(button('Index')).toBeEnabled()
  })

  it('is cancelled when the computer changes, and nothing chosen for one is kept for another', async () => {
    const calls = stub()
    hold = url => (url === '/api/user-libraries/space/test' ? new Promise<void>(() => {}) : undefined)
    await show(ONE)
    await chooseComputer('Cluster A')
    await chooseFolder('/data')
    await waitFor(() => expect(tests(calls)).toHaveLength(1))
    await chooseComputer('lab-2')
    expect(tests(calls)[0].signal?.aborted).toBe(true)
    expect(await screen.findByTestId('add-library-folder')).toHaveTextContent('Choose a folder')
    expect(screen.queryByTestId('folder-check')).toBeNull()
  })

  it('lists a cluster that is off greyed, with what to do', async () => {
    stub()
    await show(SIGN_IN)
    const off = (await computers()).find(e => e.textContent?.startsWith('Cluster C'))!
    expect(off).toHaveTextContent('Start it on ACTIVATE')
    expect(off).toHaveAttribute('aria-disabled', 'true')
  })

  it('lists the folders of the computer chosen, as the person', async () => {
    const calls = stub()
    await show(ONE)
    await chooseComputer('lab-2')
    await openFolders()
    await waitFor(() => expect(calls.some(c => c.url.startsWith('/api/user-libraries/folders?destination=lab-2'))).toBe(true))
  })

  it('names a folder that is not there yet', async () => {
    const calls = stub((m, u) => (u.startsWith('/api/libraries/folders') ? json({ path: '/home/ada', home: '/home/ada', entries: [{ name: 'papers' }] }) : undefined))
    await show(ONE)
    const field = await openFolders()
    fireEvent.change(field, { target: { value: '/home/ada/new-notes' } })
    fireEvent.click(await screen.findByTestId('directory-new'))
    await waitFor(() => expect(calls.find(c => c.url === '/api/libraries/build/check')?.body).toMatchObject({ sourceRoot: '/home/ada/new-notes' }))
  })
})

describe('what the check found', () => {
  const card = () => screen.getByTestId('folder-check')

  it('is one card: the folder, its files, size and main kinds, with every step on request', async () => {
    stub()
    await show(SIGN_IN)
    await chooseFolder('/data')
    await checked()
    await waitFor(() => expect(screen.getByTestId('folder-check-line')).toHaveTextContent(/1,200 files · 4\.77 MB · 900 PDF, 300 CSV|1,200 files · 5 MB · 900 PDF, 300 CSV/))
    expect(within(card()).getByText('/data')).toBeInTheDocument()
    expect(within(card()).queryByText('Reached as ada.')).toBeNull()
    fireEvent.click(within(card()).getByRole('button', { name: 'Details' }))
    expect(within(card()).getByText('Reached as ada.')).toBeInTheDocument()
  })

  it('says when the folder is new', async () => {
    space = { ...SPACE, create: ['/data'] }
    stub()
    await show(SIGN_IN)
    await chooseFolder('/data')
    await checked()
    expect(screen.getByTestId('folder-check-line')).toHaveTextContent('New, made when you index it')
  })

  it('warns in a few words, with the detail and the chmod on expand, and indexes all the same', async () => {
    const chmod = 'Every account there can write in /data (mode 777). To keep it to yourself, run chmod go-w on it.'
    space = { ...SPACE, writable: false, embedMB: 70, steps: [{ name: 'folder', ok: true, warn: true, short: 'Others can write it', detail: chmod }] }
    stub()
    await show(SIGN_IN)
    await chooseFolder('/data')
    await checked()
    const said = within(card()).getAllByRole('listitem').map(l => l.querySelector('summary, .lib-check-plain')?.textContent)
    expect(said).toEqual(['Others can write it', 'Read only for you', 'Search by meaning sends its model there (about 70 MB, once)'])
    const detail = within(card()).getByText(chmod).closest('details')!
    expect(detail.open).toBe(false)
    expect(button('Index')).toBeEnabled()
  })

  it('says nothing of the model at a depth that does not read meaning', async () => {
    space = { ...SPACE, embedMB: 70 }
    stub()
    await show(SIGN_IN)
    await chooseFolder('/data')
    await checked()
    expect(within(card()).getByText(/about 70 MB/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('combobox', { name: 'Index depth' }))
    const o = await screen.findByRole('option', { name: 'Text' })
    fireEvent.pointerDown(o); fireEvent.click(o)
    await waitFor(() => expect(within(card()).queryByText(/about 70 MB/)).toBeNull())
  })

  it('what stops it is one line that says what to do, with the review put away', async () => {
    space = { ...SPACE, ok: false, steps: [{ name: 'folder', ok: false, detail: 'Your account cannot open /data.' }] }
    const calls = stub()
    await show(SIGN_IN)
    await chooseFolder('/data')
    await checked()
    expect(within(card()).getByText('Your account cannot open /data.')).toBeInTheDocument()
    expect(screen.queryByLabelText('Name')).toBeNull()
    expect(button('Index')).toBeDisabled()
    space = SPACE
    fireEvent.click(within(card()).getByRole('button', { name: 'Check again' }))
    await checked()
    expect(tests(calls)).toHaveLength(2)
    expect(button('Index')).toBeEnabled()
  })
})

describe('Add a library, step two', () => {
  it('has a default for each setting, and Index sends them', async () => {
    const calls = stub((m, u) => (m === 'POST' && u === '/api/user-libraries/space' ? json({ id: 'u-data', label: 'data', state: 'building' }) : undefined), { signIn: true })
    const { onAdded } = await show(SIGN_IN)
    await chooseFolder('/data')
    await checked()
    expect(screen.getByLabelText('Name')).toHaveValue('data')
    expect(button('Only me')).toHaveAttribute('aria-pressed', 'true')
    expect(screen.getByRole('combobox', { name: 'Index depth' })).toHaveTextContent('Meaning on documents')
    expect(screen.getByText('Adds search by meaning over documents and images.')).toBeInTheDocument()
    fireEvent.click(button('Index'))
    await waitFor(() => expect(onAdded).toHaveBeenCalled())
    expect(calls.find(c => c.method === 'POST' && c.url === '/api/user-libraries/space')?.body).toEqual({ cluster: 'cluster-a', sourceRoot: '/data', depth: 'meaning-documents', myLibrary: false })
  })

  it('Shared reveals with whom, and read only or read and write', async () => {
    stub(undefined, { signIn: true })
    await show(SIGN_IN)
    await chooseFolder('/data')
    await checked()
    for (const n of ['A person', 'Read only', 'Read and write']) expect(screen.queryByRole('button', { name: n })).toBeNull()
    fireEvent.click(button('Shared'))
    for (const n of ['A person', 'A group', 'Everyone', 'Read only', 'Read and write']) expect(button(n)).toBeInTheDocument()
    expect(button('Read only')).toHaveAttribute('aria-pressed', 'true')
    expect(button('Index')).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Account on Cluster A'), { target: { value: 'bob' } })
    expect(button('Index')).toBeEnabled()
  })

  it('keeps the rest under More options, with Build with gone', async () => {
    stub(undefined, { signIn: true, spaces: [] })
    await show(SIGN_IN)
    await chooseFolder('/data')
    await checked()
    expect(screen.getByTestId('my-library-line')).toHaveTextContent('It becomes My library')
    expect(screen.queryByRole('checkbox')).toBeNull()
    fireEvent.click(button('More options'))
    expect(screen.getByRole('checkbox', { name: 'Use as My library' })).toBeChecked()
    for (const l of ['Index folder', 'GUFI bin directory']) expect(screen.getByLabelText(l)).toBeInTheDocument()
    expect(screen.queryByText(/Build with|Workflow job/)).toBeNull()
  })
})

describe('Connect an existing index', () => {
  it('goes through the same two steps with the index\'s path', async () => {
    const calls = stub((m, u) => (m === 'POST' && u === '/api/user-libraries/ssh' ? json({ id: 'u-i', label: 'idx' })
      : u === '/api/user-libraries/ssh/test' ? json({ ok: true, steps: STEPS, directories: 12, files: 340, fullText: true }) : undefined))
    const { onAdded } = await show(ONE)
    await chooseComputer('lab-2')
    fireEvent.click(button('Connect an existing index'))
    expect(await screen.findByTestId('add-library-folder')).toHaveTextContent('Choose the index')
    await chooseFolder('/idx')
    await checked()
    expect(calls.find(c => c.url === '/api/user-libraries/ssh/test')?.body).toEqual({ destination: 'lab-2', indexRoot: '/idx' })
    expect(screen.getByTestId('folder-check-line')).toHaveTextContent('12 folders · 340 files · searched by words')
    expect(screen.queryByText('Index depth')).toBeNull()
    expect(screen.getByLabelText('Name')).toHaveValue('idx')
    fireEvent.click(button('Connect'))
    await waitFor(() => expect(onAdded).toHaveBeenCalledWith(expect.objectContaining({ id: 'u-i' }), false))
    fireEvent.click(button('Index a folder instead'))
    expect(await screen.findByTestId('add-library-folder')).toHaveTextContent('Choose a folder')
  })

  it('is checked on this computer too', async () => {
    const calls = stub((m, u) => (m === 'POST' && u === '/api/libraries' ? json({ library: { id: 'idx', label: 'idx' } }) : undefined))
    const { onAdded } = await show(ONE)
    fireEvent.click(await screen.findByRole('button', { name: 'Connect an existing index' }))
    await chooseFolder('/idx')
    await checked()
    expect(calls.find(c => c.url === '/api/libraries/check')?.body).toEqual({ indexRoot: '/idx' })
    fireEvent.click(button('Connect'))
    await waitFor(() => expect(onAdded).toHaveBeenCalled())
  })
})

describe('the computers each kind of Studio offers', () => {
  const rows = async () => (await computers()).map(e => e.textContent ?? '').join('|')

  it('with no ACTIVATE: this computer and hosts over ssh, with no word of clusters', async () => {
    stub(undefined, { systems: { usable: false, why: 'No pw CLI.', systems: [], error: null } })
    const { container } = await show(STANDALONE)
    await waitFor(() => expect(screen.getByTestId('add-library-computer')).toHaveTextContent('This computer'))
    expect(await rows()).toMatch(/^This computer\|example-box.*\|lab-2.*\|Another host…/)
    expect(document.body.textContent).not.toMatch(/ACTIVATE|pw ssh|cluster/i)
    closeMenu()
    await chooseComputer('lab-2')
    await chooseFolder('/data')
    await checked()
    fireEvent.click(button('More options'))
    fireEvent.click(button('About adding a library'))
    await screen.findByText(/your own ssh keys/)
    // The folder the Studio keeps there is named for the Studio.
    expect(document.body.textContent?.replaceAll('~/.activate-studio', '')).not.toMatch(/ACTIVATE|pw ssh|cluster/i)
    expect(container.querySelector('[role="alert"]')).toBeNull()
  })

  it('one person with ACTIVATE: clusters and hosts together', async () => {
    stub()
    await show(ONE)
    await screen.findByTestId('add-library-computer')
    await act(async () => { await new Promise(f => setTimeout(f, 30)) })
    const all = await rows()
    for (const n of ['This computer', 'Cluster A', 'Cluster C', 'Another cluster…', 'example-box', 'lab-2', 'Another host…', 'A bucket']) expect(all).toContain(n)
  })

  it('where people sign in: their clusters only', async () => {
    const calls = stub(undefined, { signIn: true })
    await show(SIGN_IN)
    await waitFor(() => expect(screen.getByTestId('add-library-computer')).toHaveTextContent('Cluster A'))
    expect(await rows()).not.toMatch(/This computer|Another host|lab-2|bucket/i)
    expect(calls.some(c => c.url.includes('/ssh/hosts'))).toBe(false)
  })

  it('a host typed by name is checked over ssh, with its options', async () => {
    const calls = stub()
    await show(ONE)
    await nameComputer('host', 'me@box:2222')
    fireEvent.click(await screen.findByRole('button', { name: 'More options' }))
    fireEvent.change(screen.getByLabelText('SSH options'), { target: { value: '-J jump' } })
    await chooseFolder('/data')
    await checked()
    expect(tests(calls).at(-1)?.body).toEqual({ destination: 'me@box:2222', sshOptions: '-J jump', sourceRoot: '/data' })
  })

  it('the workflow appears only where it is the one way to build, with why', async () => {
    const only: AddOptions = { buildHere: false, ssh: false, pw: false, workflow: true }
    expect(addChoices(only).workflowOnly).toBe(true)
    expect(addChoices({ ...SIGN_IN, workflow: true }, SYSTEMS as never).workflowOnly).toBe(false)
    expect(firstTarget({ ...SIGN_IN, home: 'cluster-a' }, SYSTEMS as never)).toBeNull()
    stub(undefined, { signIn: true })
    await show(only)
    expect(screen.getByText(/cannot reach your clusters over pw ssh, so a folder on one is indexed by a job/)).toBeInTheDocument()
    expect(button('Fewer options')).toBeInTheDocument()
  })
})
