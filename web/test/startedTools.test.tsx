import { afterEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { typedPart, withoutCarried } from '../src/pwcode/carried'
import { PWCodeToolsNote } from '../src/pwcode/ToolsNote'
import { sessionToolsSaid } from '../src/components/studioTools'

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
    note({ session: 's2', relay: true, toolsError: 'no attach', machineName: 'gpu' })
    expect(screen.getByText(/ACTIVATE could not attach them \(no attach\)/)).toBeInTheDocument()
  })
})
