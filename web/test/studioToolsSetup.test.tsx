import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { StudioToolsSetup, type BridgeReach } from '../src/components/StudioToolsSetup'

const command = "pw code mcp add --scope user --transport http --header 'Authorization: Bearer <your ACTIVATE API key>' studio https://platform.example/me/session/ada/studio/api/mcp"

function serve(reach: () => BridgeReach, setup?: (body: Record<string, unknown>) => [number, unknown]) {
  const posted: Record<string, unknown>[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } })
    if (url === '/api/mcp/bridge/setup' && setup) {
      const body = JSON.parse(String(init?.body ?? '{}')) as Record<string, unknown>
      posted.push(body)
      const [status, out] = setup(body)
      return json(out, status)
    }
    if (url !== '/api/mcp/bridge' && !url.startsWith('/api/mcp/bridge?')) return new Response('{}', { status: 404 })
    return json(reach())
  }))
  return posted
}

const offer = { state: 'offer' as const, machine: 'build-box', machineId: 'm2', available: true }

describe('the setup card for the Studio\'s tools on another machine', () => {
  afterEach(() => { cleanup(); vi.unstubAllGlobals(); sessionStorage.clear() })

  it('sets it up for the person on the chat\'s machine, and says what the session in front has', async () => {
    const reach: BridgeReach = { available: true, command, keyHole: '<your ACTIVATE API key>', tools: 44, ownKey: true, platformTools: true, seen: null, setup: offer }
    const posted = serve(() => reach, () => [200, { done: true, worked: true, changed: true, tools: 44, live: 'next' }])
    render(<StudioToolsSetup conversationId="c1" />)
    // The manual steps wait behind Do it myself.
    expect(await screen.findByRole('button', { name: 'Do it myself' })).toBeInTheDocument()
    expect(screen.queryByText(command)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Set up for me' }))
    const done = await screen.findByTestId('studio-tools-done')
    expect(done).toHaveTextContent("pw code on build-box now has this Studio's tools (44 of them), as you, through its own ACTIVATE sign-in; no key was made.")
    expect(done).toHaveTextContent('one already open, this one included, keeps the tools it started with')
    expect(posted).toEqual([{ conversationId: 'c1', check: false }])
  })

  it('runs as the chat opens when asked for as the session was continued, and the session takes them at once where it can', async () => {
    const posted = serve(() => ({ available: true, command, tools: 44, seen: null, setup: offer }), () => [200, { done: true, worked: true, changed: true, tools: 44, live: 'now' }])
    sessionStorage.setItem('ade-studio-tools-auto:c1', '1')
    render(<StudioToolsSetup conversationId="c1" />)
    expect(await screen.findByTestId('studio-tools-done')).toHaveTextContent('This session has them now.')
    expect(posted).toHaveLength(1)
    expect(sessionStorage.getItem('ade-studio-tools-auto:c1')).toBeNull()
  })

  it('asks before replacing another Studio\'s entry, and shows the steps when the machine is out of reach', async () => {
    const posted = serve(() => ({ available: true, command, keyHole: '<your ACTIVATE API key>', tools: 44, seen: null, setup: offer }), b =>
      (b.replace ? [200, { done: true, worked: true, changed: true, replaced: true, tools: 44, live: 'next' }]
        : [200, { done: false, ask: true, current: 'https://platform.example/me/session/cy/old/api/mcp', why: 'pw code on build-box already has a studio entry, for https://platform.example/me/session/cy/old/api/mcp. Replace it with this Studio\'s?' }]))
    render(<StudioToolsSetup conversationId="c1" />)
    fireEvent.click(await screen.findByRole('button', { name: 'Set up for me' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Replace it' }))
    await screen.findByTestId('studio-tools-done')
    expect(posted.map(b => b.replace)).toEqual([undefined, true])
    cleanup()
    serve(() => ({ available: true, command, keyHole: '<your ACTIVATE API key>', tools: 44, seen: null, setup: offer }), () => [502, { error: 'The Studio could not reach build-box with pw ssh as you (timed out). Do it yourself in a terminal there.' }])
    render(<StudioToolsSetup conversationId="c1" />)
    fireEvent.click(await screen.findByRole('button', { name: 'Set up for me' }))
    expect(await screen.findByTestId('studio-tools-failed')).toHaveTextContent('could not reach build-box with pw ssh')
    expect(screen.getByText(command, { selector: '.studio-tools-command' })).toBeInTheDocument()
  })

  it('when pw ssh does not reach the machine, says the site it tried and gives the command to paste there, no key needed', async () => {
    const paste = "pw code mcp add --scope user --force studio -- sh -c 'u=$1; p=$2' studio-mcp 'https://platform.example/me/session/ada/studio/api/mcp' \"$(command -v pw)\""
    serve(() => ({ available: true, command, paste, keyHole: '<your ACTIVATE API key>', tools: 44, seen: null, setup: { ...offer, machine: 'labcluster' } }),
      () => [502, { error: 'The Studio could not reach labcluster with pw ssh as you, at pw://bob/labcluster ([ERROR] Cluster not found). Do it yourself in a terminal there.' }])
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    render(<StudioToolsSetup conversationId="c1" />)
    fireEvent.click(await screen.findByRole('button', { name: 'Set up for me' }))
    expect(await screen.findByTestId('studio-tools-failed')).toHaveTextContent('at pw://bob/labcluster')
    expect(screen.getByTestId('studio-tools-paste')).toHaveTextContent(paste)
    expect(screen.getByText(/In a terminal on labcluster, paste this once/)).toBeInTheDocument()
    // The key steps wait behind their own button.
    expect(screen.queryByText(command)).toBeNull()
    fireEvent.click(screen.getByRole('button', { name: 'Copy the command' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(paste))
    fireEvent.click(screen.getByRole('button', { name: 'No curl there? Use an ACTIVATE API key' }))
    expect(screen.getByText(command, { selector: '.studio-tools-command' })).toBeInTheDocument()
  })

  it('offers only the steps where the Studio cannot reach the machine, saying why', async () => {
    serve(() => ({ available: true, command, tools: 44, seen: null, setup: { state: 'unavailable', machine: 'laptop', machineId: 'm9', why: 'laptop is a computer that connected itself to ACTIVATE, and the Studio cannot open a terminal on it: do it yourself in a terminal there.' } }))
    render(<StudioToolsSetup conversationId="c1" />)
    expect(await screen.findByText(/cannot open a terminal on it/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Set up for me' })).toBeNull()
    expect(screen.getByRole('button', { name: 'Set them up once' })).toBeInTheDocument()
  })

  it('shows the command for this Studio, copies it, and checks that it worked', async () => {
    let reach: BridgeReach = { available: true, command, keyHole: '<your ACTIVATE API key>', keyPage: 'https://platform.example/settings/keys', tools: 44, ownKey: true, platformTools: true, seen: null }
    serve(() => reach)
    const writeText = vi.fn(async () => {})
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } })
    render(<StudioToolsSetup machineName="build-box" />)
    fireEvent.click(await screen.findByRole('button', { name: 'Set them up once' }))
    expect(screen.getByText(command, { selector: '.studio-tools-command' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'API keys in ACTIVATE' })).toHaveAttribute('href', 'https://platform.example/settings/keys')
    expect(screen.getByText(/In a terminal on build-box/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Copy the command' }))
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(command))
    fireEvent.click(screen.getByRole('button', { name: 'Check that it worked' }))
    expect(await screen.findByText(/Not yet: no pw code session of yours/)).toBeInTheDocument()
    reach = { ...reach, seen: { at: '2026-11-23T10:42:00Z', method: 'tools/list' } }
    fireEvent.click(screen.getByRole('button', { name: 'Check that it worked' }))
    expect(await screen.findByText(/pw code reaches this Studio's tools as you/)).toBeInTheDocument()
  })

  it('says why when the tools cannot reach another machine, and what a missing sign-in leaves out', async () => {
    serve(() => ({ available: false, why: 'MCP access is off for this Studio. An administrator turns it on under Settings, External access.' }))
    render(<StudioToolsSetup defaultOpen />)
    expect(await screen.findByText(/MCP access is off for this Studio/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Copy the command' })).toBeNull()
    cleanup()
    serve(() => ({ available: true, command, keyHole: '<your ACTIVATE API key>', tools: 30, ownKey: false, platformTools: false, seen: null }))
    render(<StudioToolsSetup defaultOpen />)
    expect(await screen.findByText(/The platform's tools come once you sign in with ACTIVATE/)).toBeInTheDocument()
    expect(screen.getByText(/Account, Authentication, API Keys/)).toBeInTheDocument()
  })
})
