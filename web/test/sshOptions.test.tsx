import { fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SshLibraryForm } from '../src/components/SshLibraryForm'
import { sshOptionsProblem } from '../src/sshOptions'
import { warmRemoteHosts } from '../src/api'

const passing = {
  ok: true,
  steps: [
    { name: 'ssh', ok: true, detail: 'Connected to me@box through me@jump, a Linux host.' },
    { name: 'gufi', ok: true, detail: 'gufi_query 0.7.0 in /usr/local/bin.' },
    { name: 'index', ok: true, detail: 'A GUFI index of 12 directories and 40 files.' },
  ],
  gufiBin: '/usr/local/bin', gufiVersion: '0.7.0', directories: 12,
}

function stubFetch(answers: unknown[]) {
  const calls: { url: string; body: Record<string, unknown> }[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push({ url, body: JSON.parse(String(init?.body ?? '{}')) })
    return new Response(JSON.stringify(answers.shift() ?? {}), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}
const fill = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } })
const button = (name: string) => screen.getByRole('button', { name })

describe('ssh options as they are typed', () => {
  it('passes what the server takes and names what it refuses', () => {
    expect(sshOptionsProblem('')).toBeNull()
    expect(sshOptionsProblem('-J me@jump -p 2222 -o ConnectTimeout=20 -i ~/.ssh/id_lab -4')).toBeNull()
    expect(sshOptionsProblem('-o ProxyCommand=nc')).toMatch(/ProxyCommand is not allowed here: .*use -J/)
    expect(sshOptionsProblem('-o BatchMode=no')).toMatch(/without prompts/)
    expect(sshOptionsProblem('-L 8080:x:80')).toMatch(/-L is not one of -J, -p, -i, -l, -F, -o, -4, -6 or -C/)
    expect(sshOptionsProblem('-J')).toMatch(/-J needs a value/)
    expect(sshOptionsProblem('-o User')).toMatch(/Keyword=value/)
    expect(sshOptionsProblem('-o "User=me"')).toMatch(/No quotes/)
  })
})

describe('the Over SSH form', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('says a Host from ~/.ssh/config works, and sends the options with the test and the library', async () => {
    const calls = stubFetch([passing, { id: 'u-box-index', label: 'index on box' }])
    render(<SshLibraryForm />)
    expect(screen.getByText(/A Host from your ~\/.ssh\/config works, including ProxyJump/)).toBeInTheDocument()
    fill('Host', 'me@box')
    fill('SSH options', ' -J me@jump -o ConnectTimeout=20 ')
    fill('Index directory', '/data/index')
    fireEvent.click(button('Test connection'))
    await screen.findByText('Connected to me@box through me@jump, a Linux host.')
    expect(calls[0]).toEqual({ url: '/api/user-libraries/ssh/test', body: { destination: 'me@box', sshOptions: '-J me@jump -o ConnectTimeout=20', indexRoot: '/data/index' } })
    fireEvent.click(button('Add library'))
    await screen.findByLabelText('Host')
    expect(calls[1].body).toMatchObject({ sshOptions: '-J me@jump -o ConnectTimeout=20' })
  })

  it('shows what is wrong with the options and holds the test until it is fixed', () => {
    const calls = stubFetch([])
    render(<SshLibraryForm />)
    fill('Host', 'me@box')
    fill('Index directory', '/data/index')
    fill('SSH options', '-o ProxyCommand=nc %h %p')
    expect(screen.getByText(/ProxyCommand is not allowed here/)).toBeInTheDocument()
    expect(button('Test connection')).toBeDisabled()
    fill('SSH options', '-J me@jump')
    expect(screen.queryByText(/is not allowed here/)).toBeNull()
    expect(button('Test connection')).toBeEnabled()
    expect(calls).toHaveLength(0)
  })

  it('has no options field over pw ssh', () => {
    stubFetch([{ usable: true, why: null, systems: [], error: null }])
    render(<SshLibraryForm transport="pw" />)
    expect(screen.queryByLabelText('SSH options')).toBeNull()
  })
})

describe('warming the hosts of remote libraries', () => {
  afterEach(() => vi.unstubAllGlobals())
  it('asks the server at most every two minutes', () => {
    const calls = stubFetch([{ hosts: 1 }, { hosts: 1 }])
    const t = 10_000_000
    warmRemoteHosts(t)
    warmRemoteHosts(t + 60_000)
    warmRemoteHosts(t + 121_000)
    expect(calls.map(c => c.url)).toEqual(['/api/user-libraries/warm', '/api/user-libraries/warm'])
  })
})
