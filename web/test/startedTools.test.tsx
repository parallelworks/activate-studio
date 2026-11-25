import { afterEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { typedPart, withoutCarried } from '../src/pwcode/carried'
import { PWCodeToolsNote } from '../src/pwcode/ToolsNote'
import { markAutoSetup, sessionToolsSaid } from '../src/components/studioTools'

afterEach(() => { vi.unstubAllGlobals(); localStorage.clear() })

const TOLD = '\n\nFrom ACTIVATE Studio, for this session (the person did not type this):\n\nYou are answering in Studio.'

// A session on another machine is given the Studio's instructions after its
// first message, which the chat draws as the person typed it.
describe('a first message that brought the instructions', () => {
  it('is drawn as typed', () => {
    expect(typedPart(`Which folders are largest?${TOLD}`)).toBe('Which folders are largest?')
    expect(typedPart(`Earlier in this conversation, before you joined it:\n\nThe person: Hi\n\nThe person's new message:\n\nGo on${TOLD}`)).toBe('Go on')
    expect(withoutCarried([{ id: 'm1', role: 'user', content: `Hello${TOLD}` }])[0].content).toBe('Hello')
  })
})

describe('what a chat says of the tools its session started with', () => {
  const note = (link: Parameters<typeof PWCodeToolsNote>[0]['link']) => render(<PWCodeToolsNote conversationId="c1" link={link} />)

  it('says nothing for a session on another machine that has them', async () => {
    const fetched = vi.fn(async () => new Response('{}'))
    vi.stubGlobal('fetch', fetched)
    const { container } = note({ session: 's1', relay: true, hasTools: true, machineName: 'gpu' })
    expect(container).toBeEmptyDOMElement()
    expect(fetched).not.toHaveBeenCalled()
    expect(sessionToolsSaid({ tools: { connected: false, count: 0, has: true }, continued: true, relay: true, machine: 'gpu' }))
      .toEqual({ ok: true, offerNew: false, text: "This chat's session on gpu started with this Studio's tools." })
  })

  it('says why a session has none', () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{}', { status: 404 })))
    note({ session: 's1', continued: true, picked: true, toolsError: 'connection refused' })
    expect(screen.getByText(/started without the Studio's tools, which pw code could not connect to \(connection refused\)/)).toBeInTheDocument()
  })

  // One short line; the setup card and the reason are behind it.
  it('says in one line that a session on another machine works without them', async () => {
    const reach = { available: true, url: 'u', paste: 'p', tools: 40, setup: { state: 'offer', machine: 'gpu', machineId: 'm2', available: true } }
    const fetched = vi.fn(async () => new Response(JSON.stringify(reach)))
    vi.stubGlobal('fetch', fetched)
    const { container, unmount } = note({ session: 's2', relay: true, toolsError: 'gpu did not answer', machineName: 'gpu' })
    const line = screen.getByTestId('pwcode-tools-line')
    expect(line).toHaveTextContent("This session on gpu works without the Studio's tools. Add them")
    expect(container.textContent!.length).toBeLessThan(90)
    expect(fetched).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Add them' }))
    expect(screen.getByText(/started without the Studio's tools: gpu did not answer\./)).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Set up for me' })).toBeInTheDocument()
    unmount()
    // Continued with the box ticked: the card opens, and runs the setup.
    markAutoSetup('c1')
    note({ session: 's2', relay: true, machineName: 'gpu' })
    await waitFor(() => expect(fetched).toHaveBeenCalledWith('/api/mcp/bridge/setup', expect.anything()))
    expect(screen.queryByTestId('pwcode-tools-line')).toBeNull()
  })

  it('keeps the line away once it is dismissed', () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('{}')))
    const { unmount } = note({ session: 's2', relay: true, machineName: 'gpu' })
    fireEvent.click(within(screen.getByTestId('pwcode-tools-line')).getByRole('button', { name: /dismiss|close/i }))
    expect(screen.queryByTestId('pwcode-tools-line')).toBeNull()
    unmount()
    note({ session: 's2', relay: true, machineName: 'gpu' })
    expect(screen.queryByTestId('pwcode-tools-line')).toBeNull()
  })
})
