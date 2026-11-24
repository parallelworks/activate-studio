import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SshLibraryForm } from '../src/components/SshLibraryForm'

const passing = {
  ok: true,
  steps: [
    { name: 'ssh', ok: true, detail: 'Connected to me@box, a macOS host.' },
    { name: 'gufi', ok: true, detail: 'gufi_query 0.7.0 in /usr/local/bin.' },
    { name: 'index', ok: true, detail: 'A GUFI index of 12 directories and 40 files.' },
  ],
  gufiBin: '/usr/local/bin', gufiVersion: '0.7.0', directories: 12,
}
const failing = {
  ok: false,
  steps: [{ name: 'ssh', ok: false, detail: 'me@box did not accept a key and asked for a password or another prompt.' }],
}

function stubFetch(answers: unknown[]) {
  const calls: { url: string; body: Record<string, unknown> }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, body: JSON.parse(String(init?.body ?? '{}')) })
    const next = answers.shift()
    return new Response(JSON.stringify(next), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } })
const button = (name: string) => screen.getByRole('button', { name })

describe('adding a library over ssh', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('tests first, shows each step, fills the GUFI directory, then adds', async () => {
    const calls = stubFetch([passing, { id: 'u-box-index', label: 'index on box' }])
    const onAdded = vi.fn()
    render(<SshLibraryForm onAdded={onAdded} />)
    expect(button('Test connection')).toBeDisabled()
    expect(button('Add library')).toBeDisabled()
    fill('Host', ' me@box ')
    fill('Index directory', '/data/index')
    fireEvent.click(button('Test connection'))
    await screen.findByText('Connected to me@box, a macOS host.')
    expect(calls[0]).toEqual({ url: '/api/user-libraries/ssh/test', body: { destination: 'me@box', indexRoot: '/data/index' } })
    expect(screen.getByText('A GUFI index of 12 directories and 40 files.')).toBeInTheDocument()
    expect(screen.getByLabelText('GUFI bin directory')).toHaveValue('/usr/local/bin')
    expect(screen.getByLabelText('Label')).toHaveAttribute('placeholder', 'index')
    await waitFor(() => expect(button('Add library')).toBeEnabled())

    fireEvent.click(button('Add library'))
    await waitFor(() => expect(onAdded).toHaveBeenCalledWith({ id: 'u-box-index', label: 'index on box' }))
    expect(calls[1]).toEqual({ url: '/api/user-libraries/ssh', body: { destination: 'me@box', indexRoot: '/data/index', gufiBin: '/usr/local/bin' } })
    // The form is ready for the next one.
    expect(screen.getByLabelText('Host')).toHaveValue('')
    expect(button('Add library')).toBeDisabled()
  })

  it('a change after a passing test asks for another', async () => {
    stubFetch([passing])
    render(<SshLibraryForm compact />)
    fill('Host', 'me@box')
    fill('Index directory', '/data/index')
    fireEvent.click(button('Test connection'))
    await waitFor(() => expect(button('Add library')).toBeEnabled())
    fill('Index directory', '/data/other')
    expect(button('Add library')).toBeDisabled()
    expect(screen.getByText('Fields changed: test again to add.')).toBeInTheDocument()
  })

  it('a failing test keeps Add off and says what to do', async () => {
    stubFetch([failing])
    render(<SshLibraryForm />)
    fill('Host', 'me@box')
    fill('Index directory', '/data/index')
    fireEvent.click(button('Test connection'))
    await screen.findByText(/asked for a password/)
    expect(button('Add library')).toBeDisabled()
    expect(screen.getByText('Fix the failing step and test again.')).toBeInTheDocument()
  })

  it('an error from the server is shown', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'destination: user@host, or a Host alias' }), { status: 400 })))
    render(<SshLibraryForm />)
    fill('Host', 'me@box')
    fill('Index directory', 'relative')
    fireEvent.click(button('Test connection'))
    expect(await screen.findByText('destination: user@host, or a Host alias')).toBeInTheDocument()
  })
})

const sys = (name: string, running = true) => ({ name, label: name, status: running ? 'active' : 'off', running, type: null })
const systems = (list: unknown[], extra: Record<string, unknown> = {}) => ({ usable: true, why: null, systems: list, error: null, ...extra })

/** A fetch that answers the systems list with `list`, and records the rest. */
function stubSystems(list: unknown) {
  const calls: { url: string; body: unknown }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, body: init?.body ? JSON.parse(String(init.body)) : undefined })
    const b = url === '/api/user-libraries/pw/systems' ? list : { ok: false, steps: [{ name: 'ssh', ok: false, detail: 'not reached' }] }
    return new Response(JSON.stringify(b), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

describe('connecting an index on a cluster over pw ssh', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('asks to sign in with ACTIVATE when pw ssh has no credential, with a way to Accounts, and no fields', async () => {
    stubSystems({ usable: false, why: 'The pw CLI on this computer is not signed in to a platform. Sign it in under Settings, Accounts.', systems: [], error: null })
    const onOpenAccess = vi.fn()
    render(<SshLibraryForm transport="pw" onOpenAccess={onOpenAccess} />)
    expect(await screen.findByText(/is not signed in to a platform/)).toBeInTheDocument()
    expect(screen.getByText('Sign in with ACTIVATE to use this')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Test connection' })).toBeNull()
    expect(screen.queryByLabelText('Index directory')).toBeNull()
    fireEvent.click(button('Open Accounts'))
    expect(onOpenAccess).toHaveBeenCalled()
  })

  it('marks a system that is off, and takes another system by name', async () => {
    const calls = stubSystems(systems([sys('cluster-a'), { ...sys('cluster-c', false), label: 'Cluster C' }]))
    render(<SshLibraryForm transport="pw" />)
    await waitFor(() => expect(screen.getByRole('combobox')).toHaveTextContent('cluster-a'))
    fireEvent.click(screen.getByRole('combobox'))
    // The status is read after the name; the name pw ssh takes is shown under a display name.
    const off = await screen.findByRole('option', { name: 'Cluster C off' })
    expect(off).toHaveTextContent('cluster-c')
    expect(screen.getByRole('option', { name: 'cluster-a running' })).toBeInTheDocument()
    const another = screen.getByRole('option', { name: 'Another remote computer…' })
    fireEvent.pointerDown(another)
    fireEvent.click(another)
    fill('Its name', 'my-cluster')
    fill('Index directory', '~/idx')
    fireEvent.click(button('Test connection'))
    await screen.findByText('not reached')
    expect(calls.find(c => c.url === '/api/user-libraries/pw/test')?.body).toEqual({ cluster: 'my-cluster', indexRoot: '~/idx' })
  })

  it('a list that cannot be had says why, and the system is typed', async () => {
    const calls = stubSystems(systems([], { error: 'unauthorized: the token has expired' }))
    render(<SshLibraryForm transport="pw" />)
    expect(await screen.findByText(/could not be listed: unauthorized: the token has expired/)).toBeInTheDocument()
    expect(screen.queryByRole('combobox')).toBeNull()
    fill('Remote computer', 'my-cluster')
    fill('Index directory', '~/idx')
    fireEvent.click(button('Test connection'))
    await screen.findByText('not reached')
    expect(calls.find(c => c.url === '/api/user-libraries/pw/test')?.body).toEqual({ cluster: 'my-cluster', indexRoot: '~/idx' })
  })

  it('picks a running cluster, tests the same steps, and adds through the pw routes', async () => {
    const calls: { url: string; body: unknown }[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      const body = init?.body ? JSON.parse(String(init.body)) : undefined
      calls.push({ url, body })
      const reply = (b: unknown) => new Response(JSON.stringify(b), { status: 200, headers: { 'Content-Type': 'application/json' } })
      if (url === '/api/user-libraries/pw/systems') return reply(systems([sys('cluster-a'), sys('cluster-b'), sys('cluster-c', false)]))
      if (url === '/api/user-libraries/pw/test') {
        return reply({ ...passing, steps: [{ name: 'ssh', ok: true, detail: 'Connected to cluster-a, a Linux host.' }, ...passing.steps.slice(1)], gufiBin: '/home/me/.activate-studio/gufi/abc/bin' })
      }
      return reply({ id: 'u-cluster-a-idx', label: 'idx' })
    }))
    const onAdded = vi.fn()
    render(<SshLibraryForm transport="pw" onAdded={onAdded} />)
    expect(screen.queryByLabelText('Host')).toBeNull()
    // The first running system is chosen.
    await waitFor(() => expect(button('Test connection')).toBeDisabled())
    fill('Index directory', '~/idx')
    await waitFor(() => expect(button('Test connection')).toBeEnabled())
    fireEvent.click(button('Test connection'))
    await screen.findByText('Connected to cluster-a, a Linux host.')
    expect(calls.find(c => c.url === '/api/user-libraries/pw/test')?.body).toEqual({ cluster: 'cluster-a', indexRoot: '~/idx' })
    expect(screen.getByLabelText('GUFI bin directory')).toHaveValue('/home/me/.activate-studio/gufi/abc/bin')
    expect(screen.getByLabelText('Label')).toHaveAttribute('placeholder', 'idx')
    await waitFor(() => expect(button('Add library')).toBeEnabled())
    fireEvent.click(button('Add library'))
    await waitFor(() => expect(onAdded).toHaveBeenCalledWith({ id: 'u-cluster-a-idx', label: 'idx' }))
    expect(calls.find(c => c.url === '/api/user-libraries/pw')?.body).toEqual({ cluster: 'cluster-a', indexRoot: '~/idx', gufiBin: '/home/me/.activate-studio/gufi/abc/bin' })
  })
})
