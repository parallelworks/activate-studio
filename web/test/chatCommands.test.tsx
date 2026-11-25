import { act, cleanup, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SlashPalette } from '../src/components/SlashPalette'
import { withEarlier } from '../src/pwcode/carried'
import { forgetCommands } from '../src/pwcode/chatCommands'
import { clearModelPick, pickModel } from '../src/pwcode/newChatPick'
import { forgetStarts, setStart } from '../src/pwcode/newChatStart'
import { clearViewerCaches } from '../src/viewerCache'

// "/" in a pw code chat listed the Studio assistant's skills, agents and
// tools, which its session does not know.

const OWN = { commands: [{ name: 'compact' }, { name: 'plan', usage: '/plan [open|<description>]', description: 'Enable plan mode' }], prompts: [], mcpPrompts: [] }
const FULL = {
  commands: [...OWN.commands, { name: 'search-files', description: 'a description with rc in it' }, { name: 'harbor:tide', description: 'A skill' }],
  mcpPrompts: [{ name: 'mcp__harbor__ebb', mcp: true }], prompts: ['harbor:tide'],
}
let asked: string[]
let answers: (url: URL) => unknown
let Switch: (engine: string) => void

function Harness(props: { conversationId?: string; persona?: string; machine?: string; session?: string }) {
  const [canvas] = useState(() => ({ current: null as HTMLDivElement | null }))
  const [engine, setEngine] = useState('pwcode')
  Switch = setEngine
  return (
    <div ref={el => { canvas.current = el }} className="chat-canvas" data-engine={engine} style={{ position: 'relative' }}>
      <textarea aria-label="composer" />
      <SlashPalette canvas={canvas} {...props} />
    </div>
  )
}

beforeEach(() => {
  asked = []
  answers = url => (url.searchParams.get('own') ? OWN : FULL)
  vi.stubGlobal('fetch', vi.fn(async (input: string) => {
    const url = new URL(input, 'http://studio.example')
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200 })
    if (url.pathname === '/api/extensions') return json({ skills: [{ name: 'deep_research', description: 'Research' }], agents: [{ name: 'reviewer' }] })
    if (url.pathname === '/api/chat/tools') return json({ tools: [{ name: 'search_kb', description: 'Search.' }] })
    asked.push(url.pathname + url.search)
    const v = await answers(url)
    return v === null ? new Response('{}', { status: 502 }) : json(v)
  }))
})
afterEach(() => { cleanup(); forgetCommands(); forgetStarts(); clearModelPick(null); vi.unstubAllGlobals() })

const names = () => screen.queryAllByRole('option').map(o => /^\/([\w:-]+)/.exec(o.querySelector('.slash-name')!.textContent!)![1])
const box = () => screen.getByLabelText<HTMLTextAreaElement>('composer')
const type = async (text: string) => {
  const user = userEvent.setup()
  await act(async () => { await new Promise(r => setTimeout(r, 0)) })
  await user.click(box())
  await user.type(box(), text)
  return user
}
const START = '/api/chat/pwcode/start/machines/studio/palette'

describe('"/" in a pw code chat', () => {
  it('lists what pw code has, and none of the Studio’s own', async () => {
    render(<Harness />)
    await type('/')
    await waitFor(() => expect(names()).toEqual(['compact', 'plan', 'search-files', 'harbor:tide', 'mcp__harbor__ebb']))
    expect(screen.getAllByRole('option').map(o => o.children[1].textContent)).toEqual(['command', 'command', 'command', 'skill', 'prompt'])
    expect(screen.getByText('[open|<description>]')).toBeInTheDocument()
    expect(asked).toEqual([`${START}?own=1`, START])
  })

  it('has pw code’s own commands until the machine answers, and where it does not', async () => {
    let answer = (_: unknown) => {}
    answers = url => (url.searchParams.get('own') ? OWN : new Promise(r => { answer = r }))
    render(<Harness />)
    await type('/')
    await waitFor(() => expect(names()).toEqual(['compact', 'plan']))
    await act(async () => { answer(null); await new Promise(r => setTimeout(r, 20)) })
    expect(names()).toEqual(['compact', 'plan'])
  })

  it('is read where the chat starts, for its folder, model and persona', async () => {
    setStart(null, { machine: { id: 'm2' }, folder: '/home/me/solver' })
    pickModel(null, { model: 'm9' })
    render(<Harness persona="reviewer" />)
    await type('/')
    await waitFor(() => expect(asked).toEqual(['/api/chat/pwcode/start/machines/m2/palette?own=1', '/api/chat/pwcode/start/machines/m2/palette?workspace=%2Fhome%2Fme%2Fsolver&model=m9&persona=reviewer']))
    // Another machine chosen has its own.
    answers = () => ({ commands: [{ name: 'theirs' }], prompts: [] })
    await act(async () => { setStart(null, {}) })
    await waitFor(() => expect(names()).toEqual(['theirs']))
  })

  it('follows what answers the chat', async () => {
    render(<Harness />)
    const user = await type('/')
    await waitFor(() => expect(names()).toContain('compact'))
    await act(async () => { Switch('studio') })
    await waitFor(() => expect(names()).toEqual(['help', 'tools', 'skills', 'agents', 'deep_research', 'reviewer', 'search_kb']))
    await act(async () => { Switch('pwcode') })
    await waitFor(() => expect(names()).toContain('harbor:tide'))
    expect(names()).not.toContain('search_kb')
    await user.clear(box())
  })

  it('is its session’s own where it has one, less what the Studio does not send', async () => {
    answers = url => (url.pathname.startsWith('/api/code/') ? { commands: [{ name: 'help' }, { name: 'rename' }, { name: 'rewind' }], prompts: [], notListed: ['rename', 'rewind'] } : OWN)
    render(<Harness conversationId="c1" machine="m2" session="s1" />)
    await type('/')
    await waitFor(() => expect(names()).toEqual(['help']))
    expect(asked).toEqual(['/api/chat/pwcode/start/machines/m2/palette?own=1', '/api/code/machines/m2/sessions/s1/palette'])
  })

  it('leaves a line typed whole, or one it does not have, to be sent', async () => {
    const sent = vi.fn()
    const key = (e: KeyboardEvent) => { if (e.key === 'Enter' && !e.defaultPrevented) sent(box().value) }
    document.addEventListener('keydown', key)
    render(<Harness />)
    const user = await type('/pl')
    await waitFor(() => expect(names()).toEqual(['plan']))
    await user.keyboard('{Enter}')
    expect(box().value).toBe('/plan ')
    for (const line of ['/compact', '/rc', '/nosuch']) {
      await user.clear(box())
      await user.type(box(), line)
      await user.keyboard('{Enter}')
    }
    document.removeEventListener('keydown', key)
    expect(sent.mock.calls.map(c => c[0])).toEqual(['/compact', '/rc', '/nosuch'])
  })

  it('is not kept for another viewer', async () => {
    render(<Harness />)
    const user = await type('/')
    await waitFor(() => expect(names()).toContain('harbor:tide'))
    clearViewerCaches()
    answers = url => (url.searchParams.get('own') ? OWN : null)
    await user.clear(box())
    await user.type(box(), '/')
    await waitFor(() => expect(names()).toEqual(['compact', 'plan']))
  })
})

describe('the turns a session is owed', () => {
  const at = (id: string, role: string, content: string) => ({ id, role, content, timestamp: '' }) as never
  const record = [at('u0', 'user', 'What is the budget?'), at('a0', 'assistant', 'Nine.')]
  const ids = (m: readonly { id: string }[] | undefined) => m?.map(x => x.id)

  it('are drawn before what it did meanwhile', () => {
    expect(ids(withEarlier([], record, { upto: 'a0' }))).toEqual(['earlier-u0', 'earlier-studio-a0'])
    expect(ids(withEarlier([at('s1', 'assistant', 'On it.')], record, { upto: 'a0' }))).toEqual(['earlier-u0', 'earlier-studio-a0', 's1'])
    expect(ids(withEarlier([], record))).toEqual([])
  })

  it('come after the turns it has', () => {
    const more = [at('u1', 'user', 'hello'), at('pwc-1', 'assistant', 'Hello'), ...record]
    const session = [at('s1', 'user', 'hello'), at('s2', 'assistant', 'Hello')]
    const goal = [at('s3', 'user', 'the goal'), at('s4', 'assistant', 'On it.')]
    expect(ids(withEarlier([...session, ...goal], more, { after: 'pwc-1', upto: 'a0' }))).toEqual(['s1', 's2', 'earlier-u0', 'earlier-studio-a0', 's3', 's4'])
    expect(ids(withEarlier(session, more, { after: 'pwc-1', upto: 'a0' }))).toEqual(['s1', 's2', 'earlier-u0', 'earlier-studio-a0'])
  })

  it('are drawn once, as they go with a message', () => {
    const told = at('s1', 'user', 'Earlier in this conversation, before you joined it:\n\nThe person: What is the budget?\n\nThe person\'s new message:\n\nspend it')
    expect(ids(withEarlier([told], [...record, at('u1', 'user', 'spend it')]))).toEqual(['earlier-u0', 'earlier-studio-a0', 's1'])
    // Read before the message was kept.
    expect(ids(withEarlier([told], record, { upto: 'a0' }))).toEqual(['earlier-u0', 'earlier-studio-a0', 's1'])
  })
})
