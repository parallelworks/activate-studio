import { afterEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { StudioToolsSetup, type BridgeReach } from '../src/components/StudioToolsSetup'

const command = "pw code mcp add --scope user --transport http --header 'Authorization: Bearer <your ACTIVATE API key>' studio https://platform.example/me/session/ada/studio/api/mcp"

function serve(reach: () => BridgeReach) {
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    if (url !== '/api/mcp/bridge') return new Response('{}', { status: 404 })
    return new Response(JSON.stringify(reach()), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
}

describe('the setup card for the Studio\'s tools on another machine', () => {
  afterEach(() => { cleanup(); vi.unstubAllGlobals() })

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
