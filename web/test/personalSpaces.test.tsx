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
    expect(addChoices(addOptions(false, user, false)).where).toEqual(['here'])
    expect(mayAddLibraries(false, user, false)).toBe(false)
    const withSpace = { ...user, space: true }
    expect(addChoices(addOptions(false, withSpace, false)).where).toEqual(['here', 'space', 'shared'])
    expect(addChoices(addOptions(false, withSpace, false)).how('space')).toEqual([])
    expect(mayAddLibraries(false, withSpace, false)).toBe(true)
  })

  it('says why a space cannot be made yet', async () => {
    stub((m, u) => (u === '/api/user-libraries/space' ? json({ ...STATUS, available: false, why: 'Sign the pw CLI in under Settings, Model access, so the Studio can reach the cluster as you.' }) : undefined))
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true }} onAdded={() => {}} />)
    fireEvent.click(button('My space'))
    expect(await screen.findByText(/Sign the pw CLI in under Settings/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Make my space' })).toBeNull()
  })

  it('checks the folder first, asks before making it, then makes the space', async () => {
    const calls = stub((m, u) => {
      if (u === '/api/user-libraries/space' && m === 'GET') return json(STATUS)
      if (u === '/api/user-libraries/space/test') return json(PASSED)
      if (u === '/api/user-libraries/space' && m === 'POST') return json({ id: 'space-studio', label: 'My space', state: 'building' })
    })
    const onAdded = vi.fn()
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true }} onAdded={onAdded} />)
    fireEvent.click(button('My space'))
    const folder = await screen.findByLabelText('Folder')
    expect(folder).toHaveValue('~/studio')
    expect(button('Make my space')).toBeDisabled()
    fireEvent.click(button('Check the folder'))
    expect(await screen.findByText('Reached cluster-a as ada.')).toBeInTheDocument()
    fireEvent.click(button('Make my space'))
    fireEvent.click(await screen.findByRole('button', { name: 'Create it and build' }))
    await waitFor(() => expect(onAdded).toHaveBeenCalled())
    const made = calls.find(c => c.method === 'POST' && c.url === '/api/user-libraries/space')
    expect(made?.body).toMatchObject({ sourceRoot: '~/studio', create: true })
    // A folder changed after the check is checked again before it is made.
    fireEvent.change(folder, { target: { value: '~/other' } })
    expect(button('Make my space')).toBeDisabled()
  })

  it('lists what others shared, and adds one as the viewer\'s own library', async () => {
    let added = false
    const calls = stub((m, u) => {
      if (u === '/api/user-libraries/shared-with-me' && m === 'GET') {
        return json({ account: 'bob', shares: [{ id: 's-1', from: 'ada', space: 'space-studio', path: 'proj', label: 'proj', folder: '/home/ada/studio/proj', kind: 'group', name: 'team', access: 'write', createdAt: '', recipients: 0, ...(added ? { library: 'shared-proj' } : {}) }] })
      }
      if (u === '/api/user-libraries/shared-with-me/s-1/add') { added = true; return json({ id: 'shared-proj', label: 'proj (ada)', state: 'building' }) }
    })
    const onAdded = vi.fn()
    render(<AddLibrary options={{ buildHere: false, ssh: false, pw: false, workflow: false, space: true }} onAdded={onAdded} />)
    fireEvent.click(button('Shared with me'))
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

describe('the per-person pw CLI sign-in', () => {
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
    render(<PlatformSignInPanel />)
    fireEvent.click(await screen.findByRole('button', { name: 'Sign in with the pw CLI' }))
    expect(await screen.findByText('ABCD-EFGH')).toBeInTheDocument()
    state = { state: 'signed-in', account: 'ada', host: 'platform.example.com', signedInAt: '2026-11-17T12:00:00Z' }
    expect(await screen.findByText('Signed in as ada on platform.example.com', {}, { timeout: 4000 })).toBeInTheDocument()
    fireEvent.click(button('Sign out'))
    expect(await screen.findByRole('button', { name: 'Sign in with the pw CLI' })).toBeInTheDocument()
  })

  it('is hidden where the Studio keeps no personal sign-ins', async () => {
    const calls = stub(() => json({ error: 'This Studio has one user.' }, 409))
    const { container } = render(<PlatformSignInPanel />)
    await waitFor(() => expect(calls.length).toBe(1))
    expect(container).toBeEmptyDOMElement()
  })
})
