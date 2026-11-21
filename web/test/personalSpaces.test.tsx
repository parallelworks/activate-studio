import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AddLibrary, addChoices } from '../src/components/AddLibrary'
import { addOptions, mayAddLibraries } from '../src/components/LibraryAddDialogs'
import { ShareDialog } from '../src/components/ShareDialog'
import { PlatformSignInPanel } from '../src/components/PlatformSignInPanel'

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
    return route(method, url, body) ?? json({ error: `no route for ${method} ${url}` }, 404)
  }))
  return calls
}

const button = (name: string) => screen.getByRole('button', { name })
const STATUS = { available: true, why: '', cluster: 'cluster-a', suggested: '~/studio', spaces: [] }
const PASSED = {
  ok: true, sourceRoot: '/home/ada/studio', create: ['/home/ada/studio'], account: 'ada', acl: true, fs: 'ext4',
  steps: [
    { name: 'account', ok: true, detail: 'Reached cluster-a as ada.' },
    { name: 'folder', ok: true, detail: '/home/ada/studio does not exist yet; it is made, closed to other accounts.' },
  ],
}

describe('a space of one\'s own', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('is offered beside the cluster the Studio runs on, and lets a signed-in person add libraries', () => {
    const user = { available: true, ssh: false, pwOffered: false, workflow: false }
    expect(addChoices(addOptions(false, user, false)).where).toEqual([])
    expect(mayAddLibraries(false, user, false)).toBe(false)
    const withSpace = { ...user, space: true }
    // A folder of one's own is on a system; with sign-in on, this computer
    // is the deployment's.
    expect(addChoices(addOptions(false, withSpace, false)).where).toEqual(['system'])
    expect(addChoices(addOptions(false, withSpace, false)).how('system')).toEqual(['own'])
    expect(mayAddLibraries(false, withSpace, false)).toBe(true)
  })

  it('asks a person without a sign-in to sign in with ACTIVATE, right there', async () => {
    let signedIn = false
    stub((m, u) => {
      if (u === '/api/user-libraries/space') {
        return json(signedIn ? { ...STATUS, credential: 'sign-in', signInOffered: true }
          : { ...STATUS, available: false, why: 'My library reaches the cluster with your ACTIVATE sign-in.', credential: '', signInOffered: true })
      }
      if (u === '/api/me/platform-sign-in' && m === 'GET') return json(signedIn ? { state: 'signed-in', account: 'ada', host: 'platform.example.com' } : { state: 'none', host: 'platform.example.com' })
      if (u === '/api/me/platform-sign-in' && m === 'POST') { signedIn = true; return json({ state: 'signed-in', account: 'ada', host: 'platform.example.com' }) }
    })
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />)
    expect(await screen.findByText('My library needs your ACTIVATE sign-in')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Make my library' })).toBeNull()
    fireEvent.click(await screen.findByRole('button', { name: 'Sign in with ACTIVATE' }))
    expect(await screen.findByLabelText('Folder')).toHaveValue('~/studio')
    expect(screen.getByText('Reached on cluster-a as you with your ACTIVATE sign-in.')).toBeInTheDocument()
  })

  it('says why My library is not available for another reason', async () => {
    stub((m, u) => (u === '/api/user-libraries/space' ? json({ ...STATUS, cluster: '', available: false, why: 'This Studio does not know the cluster it runs beside.', signInOffered: true }) : undefined))
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />)
    expect(await screen.findByText('My library is not available')).toBeInTheDocument()
    expect(screen.getByText(/does not know the cluster/)).toBeInTheDocument()
  })

  it('offers the sign-in again when the platform refuses the credential', async () => {
    stub((m, u) => {
      if (u === '/api/user-libraries/space' && m === 'GET') return json({ ...STATUS, credential: 'key', signInOffered: true })
      if (u === '/api/user-libraries/space/test') return json({ error: 'The platform refused the credential the Studio used for you on cluster-a (Unauthorized). Sign in with ACTIVATE under Settings, Model access, then try again.' }, 502)
      if (u === '/api/me/platform-sign-in') return json({ state: 'none', host: 'platform.example.com' })
    })
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true, home: 'cluster-a' }} onAdded={() => {}} />)
    expect(await screen.findByText(/with the API key you added under Settings, Model access/)).toBeInTheDocument()
    fireEvent.click(button('Check the folder'))
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
    const folder = await screen.findByLabelText('Folder')
    expect(folder).toHaveValue('~/studio')
    expect(button('Make my library')).toBeDisabled()
    fireEvent.click(button('Check the folder'))
    expect(await screen.findByText('Reached cluster-a as ada.')).toBeInTheDocument()
    fireEvent.click(button('Make my library'))
    fireEvent.click(await screen.findByRole('button', { name: 'Create it and build' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalled())
    const made = calls.find(c => c.method === 'POST' && c.url === '/api/user-libraries/space')
    expect(made?.body).toMatchObject({ sourceRoot: '~/studio', create: true })
    // A folder changed after the check is checked again before it is made.
    fireEvent.change(folder, { target: { value: '~/other' } })
    expect(button('Make my library')).toBeDisabled()
  })

  it('is made on the system chosen: the Studio\'s cluster first, another cluster, or a host over ssh where allowed', async () => {
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
    await waitFor(() => expect(screen.getByRole('combobox')).toHaveTextContent('cluster-a'))
    const pick = async (name: string) => {
      fireEvent.click(screen.getByRole('combobox'))
      const o = await screen.findByRole('option', { name })
      fireEvent.pointerDown(o)
      fireEvent.click(o)
    }
    fireEvent.click(button('Check the folder'))
    await waitFor(() => expect(calls.filter(c => c.url === '/api/user-libraries/space/test').at(-1)?.body).toEqual({ cluster: 'cluster-a', sourceRoot: '~/studio' }))
    await pick('cluster-b running')
    expect(button('Make my library')).toBeDisabled()
    fireEvent.click(button('Check the folder'))
    await waitFor(() => expect(calls.filter(c => c.url === '/api/user-libraries/space/test').at(-1)?.body).toEqual({ cluster: 'cluster-b', sourceRoot: '~/studio' }))
    expect(screen.getByText('Reached on cluster-b as you with your ACTIVATE sign-in.')).toBeInTheDocument()
    await pick('A host over ssh…')
    fireEvent.change(await screen.findByLabelText('Host'), { target: { value: 'ada@box' } })
    fireEvent.click(button('Check the folder'))
    await waitFor(() => expect(calls.filter(c => c.url === '/api/user-libraries/space/test').at(-1)?.body).toEqual({ destination: 'ada@box', sourceRoot: '~/studio' }))
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
    expect(await screen.findByRole('heading', { name: 'Shared with you on cluster-a' })).toBeInTheDocument()
    const table = await screen.findByRole('table', { name: 'Shared with me' })
    expect(within(table).getByText('ada, for team')).toBeInTheDocument()
    expect(within(table).getByText('Read and write')).toBeInTheDocument()
    fireEvent.click(within(table).getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalledWith(expect.objectContaining({ id: 'shared-proj' }), true))
    expect(await within(table).findByText('Added')).toBeInTheDocument()
    expect(calls.some(c => c.method === 'POST' && c.url.endsWith('/s-1/add'))).toBe(true)
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
