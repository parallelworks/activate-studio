import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AddLibrary } from '../src/components/AddLibrary'
import { addChoices } from '../src/components/addLibraryTarget'
import { libraryNames } from '../src/libraryNames'
import { reloadAppConfig } from '../src/config'
import { addOptions, mayAddLibraries } from '../src/components/LibraryAddDialogs'
import { ShareDialog } from '../src/components/ShareDialog'
import { checked, chooseComputer, chooseFolder, closeMenu, computers, folderRoute, nameComputer } from './addLibraryHelpers'
import { PlatformSignInPanel } from '../src/components/PlatformSignInPanel'

// A checkbox forwards its press to its hidden input as a PointerEvent,
// which jsdom does not have.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })

type Route = (method: string, url: string, body: Record<string, unknown> | undefined) => Response | undefined
function stub(route: Route) {
  const calls: { method: string; url: string; body: Record<string, unknown> | undefined }[] = []
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
    const url = String(input)
    const method = init?.method ?? 'GET'
    const body = typeof init?.body === 'string' ? JSON.parse(init.body) : undefined
    calls.push({ method, url, body })
    return route(method, url, body) ?? folderRoute(url) ?? (url.endsWith('/count') ? json({ count: null }) : url === '/api/user-libraries/ssh/hosts' ? json({ hosts: [] }) : url === '/api/user-libraries/pw/systems' ? json(HOME_SYSTEMS) : json({ error: `no route for ${method} ${url}` }, 404))
  }))
  return calls
}

// The viewer's clusters on a Studio cluster-a hosts.
const HOME_SYSTEMS = { usable: true, why: null, error: null, systems: [
  { name: 'cluster-a', label: 'cluster-a', status: 'active', running: true, type: null },
] }
/** The Studio's own cluster is never chosen for the person: they choose it. */
const chooseHome = () => chooseComputer('cluster-a')

const button = (name: string) => screen.getByRole('button', { name })
const STATUS = { available: true, why: '', cluster: 'cluster-a', suggested: '~/studio', spaces: [] }
const PASSED = {
  ok: true, sourceRoot: '/home/ada/studio', create: ['/home/ada/studio'], account: 'ada', acl: true, fs: 'ext4',
  owned: true, readOnly: null, canShare: true, shareWhy: null, myLibraryWhy: null,
  steps: [
    { name: 'account', ok: true, detail: 'Reached cluster-a as ada.' },
    { name: 'folder', ok: true, detail: '/home/ada/studio does not exist yet; it is made, closed to other accounts.' },
  ],
}

describe('a space of one\'s own', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('is offered beside the cluster the Studio runs on, and lets a signed-in person add libraries', () => {
    const user = { available: true, ssh: false, pwOffered: false, workflow: false }
    expect(addChoices(addOptions(false, user, false), HOME_SYSTEMS).clusters).toBe(false)
    expect(mayAddLibraries(false, user, false)).toBe(false)
    const withSpace = { ...user, space: true }
    // A folder of one's own is on a cluster; with sign-in on, this computer
    // is the deployment's.
    const offered = addChoices(addOptions(false, withSpace, false), HOME_SYSTEMS)
    expect([offered.here, offered.clusters, offered.ssh]).toEqual([false, true, false])
    expect(mayAddLibraries(false, withSpace, false)).toBe(true)
  })

  it('asks a person without a sign-in to sign in with ACTIVATE, right there', async () => {
    let signedIn = false
    stub((m, u) => {
      if (u === '/api/user-libraries/space') {
        return json(signedIn ? { ...STATUS, credential: 'sign-in', signInOffered: true }
          : { ...STATUS, available: false, why: 'My library reaches the cluster with your ACTIVATE sign-in.', credential: '', signInOffered: true })
      }
      if (u === '/api/user-libraries/pw/systems') return json(signedIn ? HOME_SYSTEMS : { usable: false, why: 'Sign in with ACTIVATE.', systems: [], error: null })
      if (u === '/api/me/platform-sign-in' && m === 'GET') return json(signedIn ? { state: 'signed-in', account: 'ada', host: 'platform.example.com' } : { state: 'none', host: 'platform.example.com' })
      if (u === '/api/me/platform-sign-in' && m === 'POST') { signedIn = true; return json({ state: 'signed-in', account: 'ada', host: 'platform.example.com' }) }
    })
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />)
    expect(await screen.findByText('Sign in with ACTIVATE to use this')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Index' })).toBeNull()
    fireEvent.click(await screen.findByRole('button', { name: 'Sign in with ACTIVATE' }))
    await waitFor(() => expect(screen.getByTestId('add-library-computer')).toHaveTextContent('Choose a cluster'))
    await chooseHome()
    await waitFor(() => expect(screen.getByTestId('add-library-folder')).toBeInTheDocument())
    expect(screen.queryByText(/^Reached on/)).toBeNull()
  })

  // Until the viewer's status came, the box that sends people to Settings,
  // Accounts showed, and then the sign-in took its place under the pointer.
  it('shows one prompt, once it knows the person can sign in here', async () => {
    let answer = (_: Response) => {}
    const status = new Promise<Response>(r => { answer = r })
    stub((m, u) => {
      if (u === '/api/user-libraries/space') return status as unknown as Response
      if (u === '/api/user-libraries/pw/systems') return json({ usable: false, why: 'Sign in with ACTIVATE under Settings, Accounts.', systems: [], error: null })
      if (u === '/api/me/platform-sign-in' && m === 'GET') return json({ state: 'none', host: 'platform.example.com' })
    })
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />)
    expect(await screen.findByText('Checking your sign-in…')).toBeInTheDocument()
    expect(screen.queryByText('Sign in with ACTIVATE to use this')).toBeNull()
    await act(async () => { answer(json({ ...STATUS, available: false, why: 'My library reaches the cluster with your ACTIVATE sign-in.', credential: '', signInOffered: true })); await status })
    expect(await screen.findByRole('button', { name: 'Sign in with ACTIVATE' })).toBeInTheDocument()
    expect(screen.getAllByText('Sign in with ACTIVATE to use this')).toHaveLength(1)
    expect(screen.queryByRole('button', { name: 'Open Accounts' })).toBeNull()
  })

  it('says why My library is not available for another reason', async () => {
    stub((m, u) => (u === '/api/user-libraries/space' ? json({ ...STATUS, cluster: '', available: false, why: 'This Studio does not know the cluster it runs beside.', signInOffered: true }) : undefined))
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />)
    await chooseHome()
    expect(await screen.findByText('A folder on a remote computer is not available')).toBeInTheDocument()
    expect(screen.getByText(/does not know the cluster/)).toBeInTheDocument()
  })

  it('offers the sign-in again when the platform refuses the credential', async () => {
    stub((m, u) => {
      if (u === '/api/user-libraries/space' && m === 'GET') return json({ ...STATUS, credential: 'key', signInOffered: true })
      if (u === '/api/user-libraries/space/test') return json({ error: 'The platform refused the credential the Studio used for you on cluster-a (Unauthorized). Sign in with ACTIVATE under Settings, Accounts, then try again.' }, 502)
      if (u === '/api/me/platform-sign-in') return json({ state: 'none', host: 'platform.example.com' })
    })
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />)
    await chooseHome()
    await chooseFolder('/data')
    expect(await screen.findByText(/refused the credential/)).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Sign in with ACTIVATE' })).toBeInTheDocument()
  })

  it('checks the folder first, asks before making it, then makes the space', async () => {
    const calls = stub((m, u) => {
      if (u === '/api/user-libraries/space' && m === 'GET') return json(STATUS)
      if (u === '/api/user-libraries/space/test') return json(PASSED)
      if (u === '/api/user-libraries/space' && m === 'POST') return json({ id: 'space-studio', label: 'My library', state: 'building' })
    })
    const onAdded = vi.fn()
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={onAdded} />)
    await chooseHome()
    await chooseFolder('~/studio')
    expect(button('Index')).toBeDisabled()
    await checked()
    fireEvent.click(button('Index'))
    fireEvent.click(await screen.findByRole('button', { name: 'Create it and build' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalled())
    const made = calls.find(c => c.method === 'POST' && c.url === '/api/user-libraries/space')
    expect(made?.body).toMatchObject({ sourceRoot: '~/studio', create: true })
    // A folder changed after the check is checked again before it is made.
    await chooseFolder('~/other')
    expect(button('Index')).toBeDisabled()
  })

  it('is made on the system chosen: the Studio\'s cluster listed first but not chosen for them, another cluster, or a host over ssh where allowed', async () => {
    const SYSTEMS = { usable: true, why: null, error: null, systems: [
      { name: 'cluster-b', label: 'cluster-b', status: 'active', running: true, type: null },
      { name: 'cluster-a', label: 'cluster-a', status: 'active', running: true, type: null },
    ] }
    const calls = stub((m, u) => {
      if (u === '/api/user-libraries/space' && m === 'GET') return json({ ...STATUS, pw: true, ssh: true })
      if (u === '/api/user-libraries/pw/systems') return json(SYSTEMS)
      if (u === '/api/user-libraries/space/test') return json(PASSED)
    })
    render(<AddLibrary options={{ buildHere: false, ssh: true, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />)
    // Nothing is chosen until the person chooses: the Studio's own cluster
    // is not a default, and is not called a remote computer.
    await waitFor(() => expect(screen.getByTestId('add-library-computer')).toHaveTextContent('Choose a computer'))
    expect(screen.queryByTestId('add-library-folder')).toBeNull()
    const names = (await computers()).map(o => o.textContent)
    expect(names[0]).toBe('cluster-aWhere this Studio runs')
    expect(names.join('|')).not.toContain("the Studio's cluster")
    closeMenu()
    const last = () => calls.filter(c => c.url === '/api/user-libraries/space/test').at(-1)?.body
    await chooseComputer('cluster-a')
    await chooseFolder('/data')
    await waitFor(() => expect(last()).toEqual({ cluster: 'cluster-a', sourceRoot: '/data' }))
    await chooseComputer('cluster-b')
    await chooseFolder('/data')
    await waitFor(() => expect(last()).toEqual({ cluster: 'cluster-b', sourceRoot: '/data' }))
    await nameComputer('host', 'ada@box')
    await chooseFolder('/data')
    await waitFor(() => expect(last()).toEqual({ destination: 'ada@box', sourceRoot: '/data' }))
  })

  it('names a system whose shares could not be checked, rather than saying nothing is shared there', async () => {
    stub((m, u) => {
      if (u === '/api/user-libraries/shared-with-me' && m === 'GET') {
        return json({ account: 'bob', shares: [], unreached: [{ site: 'cluster-a', name: 'cluster-a', why: 'cluster-a did not answer within 20 seconds.' }] })
      }
    })
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={vi.fn()} />)
    await chooseHome()
    await waitFor(() => expect(document.body).toHaveTextContent(/Folders shared with you on cluster-a could not be checked: cluster-a did not answer within 20 seconds\./))
    expect(screen.queryByText(/Nothing is shared with your account yet/)).not.toBeInTheDocument()
  })

  it('lists what others shared, and adds one as the viewer\'s own library', async () => {
    let added = false
    const calls = stub((m, u) => {
      if (u === '/api/user-libraries/shared-with-me' && m === 'GET') {
        return json({ account: 'bob', shares: [{ id: 's-1', from: 'ada', space: 'space-studio', path: 'proj', label: 'proj', folder: '/home/ada/studio/proj', kind: 'group', name: 'team', access: 'write', createdAt: '', recipients: 0, site: 'cluster-a', ...(added ? { library: 'shared-proj' } : {}) }] })
      }
      if (u === '/api/user-libraries/shared-with-me/s-1/add') { added = true; return json({ id: 'shared-proj', label: 'proj (ada)', state: 'building' }) }
    })
    const onAdded = vi.fn()
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={onAdded} />)
    // Picking a system as oneself also lists what others shared there.
    await chooseHome()
    expect(await screen.findByRole('heading', { name: 'Shared with me on cluster-a' })).toBeInTheDocument()
    expect(screen.getByTestId('shared-on')).toHaveTextContent('on cluster-a')
    expect(document.body).not.toHaveTextContent('Shared with you')
    const table = await screen.findByRole('table', { name: 'Shared with me' })
    expect(within(table).getByText('ada, for team')).toBeInTheDocument()
    expect(within(table).getByText('Read and write')).toBeInTheDocument()
    fireEvent.click(within(table).getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalledWith(expect.objectContaining({ id: 'shared-proj' }), true))
    expect(await within(table).findByText('Added')).toBeInTheDocument()
    expect(calls.some(c => c.method === 'POST' && c.url.endsWith('/s-1/add'))).toBe(true)
  })
})

describe('what a folder of one\'s own is called', () => {
  afterEach(async () => { vi.unstubAllGlobals(); await act(async () => { reloadAppConfig() }) })

  it('on a Studio only one person uses, a library named by its folder; the main library is My library', async () => {
    stub(u => undefined)
    expect(libraryNames({ signIn: false, kbLabel: 'My library' })).toEqual({
      main: 'My library', own: 'a folder of yours on one of your remote computers', writable: 'My library or a folder of yours on one of your remote computers' })
    const calls = stub((m, u) => {
      if (u === '/api/user-libraries/space' && m === 'GET') return json(STATUS)
      if (u === '/api/user-libraries/space/test') return json({ ...PASSED, create: [] })
      if (u === '/api/user-libraries/space' && m === 'POST') return json({ id: 'space-runs', label: 'runs', state: 'building' })
    })
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />)
    await chooseHome()
    expect(await screen.findByTestId('add-library-folder')).toHaveTextContent('Choose a folder')
    await chooseFolder('~/runs')
    await checked()
    expect(screen.getByLabelText('Name')).toHaveValue('runs')
    expect(screen.queryByText(/My library/)).toBeNull()
    fireEvent.click(button('Index'))
    await waitFor(() => expect(calls.find(c => c.method === 'POST' && c.url === '/api/user-libraries/space')?.body).toEqual({ cluster: 'cluster-a', sourceRoot: '~/runs', depth: 'meaning-documents' }))
  })

  it('where people sign in, the first folder is My library unless they say otherwise; any other is named by its folder', async () => {
    expect(libraryNames({ signIn: true, kbLabel: 'Shared library' }).writable).toBe('the main library or My library')
    let spaces: string[] = []
    const calls = stub((m, u) => {
      if (u.startsWith('/api/config')) return json({ appName: 'Studio', kbLabel: 'Shared library', signIn: true, libraries: [] })
      if (u === '/api/user-libraries/space' && m === 'GET') return json({ ...STATUS, spaces })
      if (u === '/api/user-libraries/space/test') return json({ ...PASSED, create: [] })
      if (u === '/api/user-libraries/space' && m === 'POST') return json({ id: 'space-studio', label: 'My library', state: 'building' })
    })
    const form = <AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />
    const { unmount } = render(form)
    // The page's config is read once and kept: read it again, as this Studio's.
    await act(async () => { reloadAppConfig(); await new Promise(r => setTimeout(r, 20)) })
    await chooseHome()
    await waitFor(() => expect(screen.getByTestId('add-library-folder')).toHaveTextContent('~/studio'))
    await checked()
    expect(screen.getByLabelText('Name')).toHaveValue('My library')
    expect(screen.getByTestId('my-library-line')).toHaveTextContent('your chats and the assistant\'s files are saved')
    fireEvent.click(button('Index'))
    await waitFor(() => expect(calls.find(c => c.method === 'POST' && c.url === '/api/user-libraries/space')?.body).toMatchObject({ sourceRoot: '~/studio', myLibrary: true }))
    // Unticked, it is a folder like any other, and the one suggested goes.
    fireEvent.click(button('More options'))
    fireEvent.click(await screen.findByRole('checkbox', { name: 'Use as My library' }))
    expect(screen.getByTestId('add-library-folder')).toHaveTextContent('Choose a folder')
    await chooseFolder('~/runs')
    await checked()
    expect(screen.getByLabelText('Name')).toHaveValue('runs')
    fireEvent.click(button('Index'))
    await waitFor(() => expect(calls.filter(c => c.method === 'POST' && c.url === '/api/user-libraries/space').at(-1)?.body).toMatchObject({ sourceRoot: '~/runs', myLibrary: false }))
    unmount()
    // Someone who has one is not asked to make another.
    spaces = ['space-studio']
    render(form)
    await chooseHome()
    expect(await screen.findByTestId('add-library-folder')).toHaveTextContent('Choose a folder')
    await chooseFolder('~/runs')
    await checked()
    expect(screen.queryByTestId('my-library-line')).toBeNull()
  })
})

/** Index a folder as a person signed in on a Studio several people use:
 *  the check answers `check`, and adding answers `added`. */
async function indexForm(check: Record<string, unknown>, added: Record<string, unknown> = {}, options: { ssh?: string } = {}) {
  const calls = stub((m, u) => {
    if (u.startsWith('/api/config')) return json({ appName: 'Studio', kbLabel: 'Shared library', signIn: !options.ssh, libraries: [] })
    if (u === '/api/user-libraries/space' && m === 'GET') return json({ ...STATUS, spaces: ['space-studio'] })
    if (u === '/api/user-libraries/space/test') return json({ ...PASSED, create: [], ...check })
    if (u === '/api/user-libraries/space' && m === 'POST') return json({ id: 'u-x', label: 'data', state: 'building', ...added })
  })
  const onAdded = vi.fn()
  render(<AddLibrary options={{ buildHere: false, ssh: !!options.ssh, pw: false, workflow: false, space: true, home: options.ssh ? undefined : 'cluster-a' }} onAdded={onAdded} />)
  await act(async () => { reloadAppConfig(); await new Promise(r => setTimeout(r, 20)) })
  const test = async () => {
    if (options.ssh) await nameComputer('host', options.ssh)
    else await chooseHome()
    await chooseFolder('/data')
    await checked()
  }
  const added_ = () => calls.filter(c => c.method === 'POST' && c.url === '/api/user-libraries/space').at(-1)?.body
  return { calls, onAdded, test, added: added_ }
}

describe('who sees a folder that is indexed', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('is one plain choice, with nothing about sharing until it is chosen', async () => {
    const f = await indexForm({})
    await f.test()
    expect(screen.getByText('Who sees it')).toBeInTheDocument()
    expect(button('Only me')).toHaveAttribute('aria-pressed', 'true')
    expect(button('Shared')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'A person' })).toBeNull()
  })

  it('shares with a person, a group or everyone; everyone only reads', async () => {
    const f = await indexForm({})
    await f.test()
    fireEvent.click(button('Shared'))
    // A shared folder is not where one's own chats are saved.
    fireEvent.click(button('More options'))
    expect(screen.queryByRole('checkbox', { name: 'Use as My library' })).toBeNull()
    expect(button('Index')).toBeDisabled()
    fireEvent.change(screen.getByLabelText('Account on cluster-a'), { target: { value: ' bob ' } })
    fireEvent.click(button('Read and write'))
    fireEvent.click(button('Index'))
    await waitFor(() => expect(f.added()).toMatchObject({ sourceRoot: '/data', myLibrary: false, share: { kind: 'user', name: 'bob', access: 'write' } }))
    await waitFor(() => expect(button('A group')).toBeEnabled())
    fireEvent.click(button('A group'))
    expect(screen.getByLabelText('Group')).toBeInTheDocument()
    fireEvent.click(button('Everyone'))
    expect(screen.queryByRole('button', { name: 'Read and write' })).toBeNull()
    fireEvent.click(button('Index'))
    await waitFor(() => expect(f.added()?.share).toEqual({ kind: 'everyone', access: 'read' }))
  })

  it('a folder others can write in is written and shared, with a warning, and is not My library', async () => {
    const detail = 'Members of the group rc can write in /data (mode 2775). They can add, change and remove what is in it. The Library writes in it as you, and a write cannot be sent outside it.'
    const f = await indexForm({
      othersWrite: 'Members of the group rc can write in /data (mode 2775).', readOnly: null, canShare: true,
      myLibraryWhy: 'My library is where your chats are saved, so it has to be a folder no other account can change.',
      steps: [{ name: 'folder', ok: true, warn: true, detail }],
    })
    await f.test()
    expect(screen.getByText(detail)).toBeInTheDocument()
    expect(screen.queryByTestId('no-share')).toBeNull()
    fireEvent.click(button('Shared'))
    fireEvent.click(button('Everyone'))
    fireEvent.click(button('Index'))
    await waitFor(() => expect(f.added()).toMatchObject({ myLibrary: false, share: { kind: 'everyone', access: 'read' } }))
  })

  it('a warning does not stop it: the folder is indexed read only, and the form says why it is not shared', async () => {
    const why = 'Another account owns the folder, and only its owner can share it.'
    const f = await indexForm({
      owned: false, readOnly: 'x', canShare: false, shareWhy: why, myLibraryWhy: 'My library is where your chats are saved, so it has to be a folder no other account can change.',
      steps: [{ name: 'folder', ok: true, warn: true, detail: '/data belongs to another account (uid 0). It is indexed and read as you, and the Library does not write in it.' }],
    })
    await f.test()
    expect(screen.getByText(/belongs to another account \(uid 0\)\. It is indexed/)).toBeInTheDocument()
    // It is not shared, and the form says why.
    expect(screen.queryByRole('button', { name: 'Shared' })).toBeNull()
    expect(screen.getByTestId('no-share')).toHaveTextContent(`Shared is not offered here. ${why}`)
    fireEvent.click(button('More options'))
    expect(screen.getByRole('checkbox', { name: 'Use as My library' })).not.toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Use as My library' })).toHaveAttribute('aria-disabled', 'true')
    expect(screen.getByText(/^My library is where your chats are saved/)).toBeInTheDocument()
    fireEvent.click(button('Index'))
    await waitFor(() => expect(f.added()).toMatchObject({ myLibrary: false }))
    expect(f.added()).not.toHaveProperty('share')
  })

  it('a refusal stops it', async () => {
    const f = await indexForm({ ok: false, canShare: false, steps: [{ name: 'folder', ok: false, detail: 'Your account cannot open /data.' }] })
    await f.test()
    expect(screen.getByText('Your account cannot open /data.')).toBeInTheDocument()
    expect(button('Index')).toBeDisabled()
    expect(button('Check again')).toBeInTheDocument()
  })

  it('without ACLs there, offers everyone, for reading', async () => {
    const f = await indexForm({ acl: false, fs: 'nfs4' })
    await f.test()
    fireEvent.click(button('Shared'))
    expect(screen.queryByRole('button', { name: 'A person' })).toBeNull()
    expect(screen.getByText(/\(nfs4\) does not take POSIX ACLs/)).toBeInTheDocument()
    fireEvent.click(button('Index'))
    await waitFor(() => expect(f.added()?.share).toEqual({ kind: 'everyone', access: 'read' }))
  })

  it('says so when it was added and the share could not be set', async () => {
    const said = vi.spyOn((await import('@parallelworks/ui')).toast, 'error')
    const f = await indexForm({}, { shareError: 'There is no account bob there.' })
    await f.test()
    fireEvent.click(button('Shared'))
    fireEvent.change(screen.getByLabelText('Account on cluster-a'), { target: { value: 'bob' } })
    fireEvent.click(button('Index'))
    await waitFor(() => expect(f.onAdded).toHaveBeenCalled())
    expect(said).toHaveBeenCalledWith('data is added, and not shared: There is no account bob there.')
  })

  it('a host over ssh is yours alone, with nothing to choose; its options wait under More options', async () => {
    const f = await indexForm({}, {}, { ssh: 'me@box:2222' })
    await f.test()
    expect(screen.queryByText('Who sees it')).toBeNull()
    expect(screen.queryByLabelText('SSH options')).toBeNull()
    fireEvent.click(button('More options'))
    fireEvent.change(screen.getByLabelText('SSH options'), { target: { value: '-J jump' } })
    fireEvent.change(screen.getByLabelText('Index folder'), { target: { value: '~/idx' } })
    fireEvent.change(screen.getByLabelText('GUFI bin directory'), { target: { value: '/opt/g/bin' } })
    await checked()
    expect(f.calls.filter(c => c.url === '/api/user-libraries/space/test').at(-1)?.body).toEqual({ destination: 'me@box:2222', sshOptions: '-J jump', sourceRoot: '/data', indexRoot: '~/idx', gufiBin: '/opt/g/bin' })
  })
})

describe('sharing a folder of a space', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('lists the folder\'s shares, shares with a group for writing, and stops sharing', async () => {
    let shares = [{ id: 's-1', from: 'ada', space: 'space-studio', path: 'proj', label: 'proj', folder: '/home/ada/studio/proj', kind: 'user', name: 'bob', access: 'read', createdAt: '', recipients: 1 },
      { id: 's-2', from: 'ada', space: 'space-studio', path: 'other', label: 'other', folder: '/home/ada/studio/other', kind: 'user', name: 'cy', access: 'read', createdAt: '', recipients: 0 }]
    const calls = stub((m, u, b) => {
      if (u === '/api/user-libraries/space-studio/shares' && m === 'GET') return json({ shares, acl: true, fs: 'ext4', account: 'ada' })
      if (u === '/api/user-libraries/space-studio/shares' && m === 'POST') {
        shares = [...shares, { id: 's-3', from: 'ada', space: 'space-studio', path: 'proj', label: 'proj', folder: '', kind: String(b?.kind), name: String(b?.name), access: String(b?.access), createdAt: '', recipients: 0 }]
        return json(shares[shares.length - 1])
      }
      if (u === '/api/user-libraries/space-studio/shares/s-1' && m === 'DELETE') { shares = shares.filter(s => s.id !== 's-1'); return json({ ok: true }) }
    })
    render(<ShareDialog open onClose={() => {}} library="space-studio" path="proj" />)
    const table = await screen.findByRole('table', { name: 'Shared with' })
    expect(within(table).getByText('bob')).toBeInTheDocument()
    expect(within(table).queryByText('cy')).toBeNull()
    fireEvent.click(button('A group'))
    fireEvent.change(screen.getByLabelText('Group'), { target: { value: 'team' } })
    fireEvent.click(button('Read and write'))
    fireEvent.click(button('Share'))
    expect(await within(table).findByText('Group team')).toBeInTheDocument()
    expect(calls.find(c => c.method === 'POST')?.body).toEqual({ path: 'proj', kind: 'group', name: 'team', access: 'write' })
    fireEvent.click(within(table).getAllByRole('button', { name: 'Stop sharing' })[0])
    await waitFor(() => expect(within(table).queryByText('bob')).toBeNull())
  })

  it('offers every account, for reading, where the file system takes no ACLs', async () => {
    stub((m, u) => (u.endsWith('/shares') && m === 'GET' ? json({ shares: [], acl: false, fs: 'nfs4', account: 'ada' }) : undefined))
    render(<ShareDialog open onClose={() => {}} library="space-studio" path="" />)
    expect(await screen.findByText(/does not take POSIX ACLs/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'A person' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Read and write' })).toBeNull()
  })
})

describe('signing in with ACTIVATE', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })

  it('shows the code to approve, looks again while it waits, and says who is signed in', async () => {
    let state: Record<string, string> = { state: 'none', host: 'platform.example.com' }
    stub((m, u) => {
      if (u === '/api/me/platform-sign-in' && m === 'GET') return json(state)
      if (u === '/api/me/platform-sign-in' && m === 'POST') {
        state = { state: 'waiting', url: 'https://platform.example.com/device', code: 'ABCD-EFGH' }
        return json(state)
      }
      if (u.endsWith('/sign-out')) { state = { state: 'none' }; return json(state) }
    })
    const changed = vi.fn()
    render(<PlatformSignInPanel onChanged={changed} />)
    fireEvent.click(await screen.findByRole('button', { name: 'Sign in with ACTIVATE' }))
    expect(await screen.findByText('ABCD-EFGH')).toBeInTheDocument()
    state = { state: 'signed-in', account: 'ada', host: 'platform.example.com', signedInAt: '2026-11-17T12:00:00Z' }
    expect(await screen.findByText('Signed in as ada on platform.example.com', {}, { timeout: 4000 })).toBeInTheDocument()
    // onChanged runs in an effect after the panel shows the new state, so
    // each count is waited for rather than read at once.
    await waitFor(() => expect(changed).toHaveBeenCalledTimes(1))
    fireEvent.click(button('Sign out'))
    expect(await screen.findByRole('button', { name: 'Sign in with ACTIVATE' })).toBeInTheDocument()
    await waitFor(() => expect(changed).toHaveBeenCalledTimes(2))
  })

  it('says what the pw CLI said when the sign-in fails, once', async () => {
    stub((m, u) => (u === '/api/me/platform-sign-in' ? json({ state: 'failed', host: 'platform.example.com', message: 'The pw CLI stopped: Error: expired_token' }) : undefined))
    render(<PlatformSignInPanel />)
    expect(await screen.findByText('The pw CLI stopped: Error: expired_token')).toBeInTheDocument()
    expect(screen.getAllByText('The sign-in did not finish')).toHaveLength(1)
    expect(button('Try again')).toBeInTheDocument()
  })

  it('is hidden where the Studio keeps no personal sign-ins', async () => {
    const calls = stub(() => json({ error: 'This Studio has one user.' }, 409))
    const { container } = render(<PlatformSignInPanel />)
    await waitFor(() => expect(calls.length).toBe(1))
    expect(container).toBeEmptyDOMElement()
  })
})
