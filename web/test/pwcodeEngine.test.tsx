import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PWCodeApprovals, PWCodeEngine, PWCodeWatcher } from '../src/components/PWCodeEngine'
import { getEngine } from '../src/labelScope'

const chat = vi.hoisted(() => ({ loadConversation: vi.fn(async () => {}) }))
vi.mock('@parallelworks/ui/ai', () => ({
  useChat: () => ({ currentConversation: { id: 'c9' }, isStreaming: false, loadConversation: chat.loadConversation }),
}))

type Engine = { setting: string; default: string; pwcode: boolean; reason: string | null }

function serve(engine: Engine, conversation: Record<string, unknown> = {}) {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push(`${init?.method ?? 'GET'} ${url}${init?.body ? ' ' + String(init.body) : ''}`)
    const body = url === '/api/chat/engine' ? engine : url.startsWith('/api/chat/conversations/') ? conversation : { ok: true }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

beforeEach(() => { localStorage.clear() })
afterEach(() => { vi.unstubAllGlobals() })

describe('What answers a conversation', () => {
  it('shows pw code as the default where it is offered, links the session, and keeps a change per chat', async () => {
    serve({ setting: 'auto', default: 'pwcode', pwcode: true, reason: null }, { pwcode: { url: 'https://platform.example/code?session=s1', session: 's1' } })
    render(<PWCodeEngine conversationId="c1" />)
    const button = await screen.findByRole('button', { name: /pw code/ })
    fireEvent.click(button)
    expect(await screen.findByRole('link', { name: /Open this chat in PW Code on the web/ })).toHaveAttribute('href', 'https://platform.example/code?session=s1')
    expect(screen.getByText('default')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Studio assistant/ }))
    expect(getEngine('c1')).toBe('studio')
    expect(await screen.findByRole('button', { name: /Studio/ })).toBeInTheDocument()
  })

  it('says why pw code is not offered when the Studio is set to it', async () => {
    serve({ setting: 'pwcode', default: 'studio', pwcode: false, reason: 'pw code runs as you: sign in with ACTIVATE under Settings, Model access.' })
    render(<PWCodeEngine conversationId="c1" />)
    fireEvent.click(await screen.findByRole('button', { name: /Studio/ }))
    expect(await screen.findByText(/sign in with ACTIVATE/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /pw code/ })).toBeDisabled()
  })

  it('stays out of the way where pw code is not offered', async () => {
    const calls = serve({ setting: 'auto', default: 'studio', pwcode: false, reason: 'pw code needs ACTIVATE' })
    const { container } = render(<PWCodeEngine conversationId={null} />)
    await waitFor(() => expect(calls).toContain('GET /api/chat/engine'))
    expect(container).toBeEmptyDOMElement()
  })
})

describe("pw code's approvals", () => {
  it('shows a request in its conversation and sends the answer', async () => {
    const calls = serve({ setting: 'auto', default: 'pwcode', pwcode: true, reason: null })
    render(<PWCodeApprovals conversationId="c1" />)
    act(() => {
      window.dispatchEvent(new CustomEvent('ade-pwcode-approval', { detail: { conversationId: 'other', approval: { id: 'ap-0', tool: 'Bash', command: 'ls' } } }))
      window.dispatchEvent(new CustomEvent('ade-pwcode-approval', { detail: { conversationId: 'c1', url: 'https://platform.example/code?session=s1', approval: { id: 'ap-1', kind: 'confirm', tool: 'Bash', command: 'date' } } }))
    })
    expect(await screen.findByText('pw code asks to run Bash')).toBeInTheDocument()
    expect(screen.getByText('date')).toBeInTheDocument()
    expect(screen.queryByText('ls')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Deny' }))
    await waitFor(() => expect(screen.queryByText('pw code asks to run Bash')).not.toBeInTheDocument())
    expect(calls).toContain('POST /api/chat/pwcode/c1/approvals/ap-1 {"allowed":false}')
  })

  it('leaves when pw code says the request was answered elsewhere', async () => {
    serve({ setting: 'auto', default: 'pwcode', pwcode: true, reason: null })
    render(<PWCodeApprovals conversationId="c1" />)
    act(() => { window.dispatchEvent(new CustomEvent('ade-pwcode-approval', { detail: { conversationId: 'c1', approval: { id: 'ap-2', tool: 'Bash', command: 'make' } } })) })
    expect(await screen.findByText('make')).toBeInTheDocument()
    act(() => { window.dispatchEvent(new CustomEvent('ade-pwcode-approval-resolved', { detail: { approvalId: 'ap-2' } })) })
    await waitFor(() => expect(screen.queryByText('make')).not.toBeInTheDocument())
  })
})

describe('A pw code turn that outlived its stream', () => {
  it('shows what it waits on, then reads the chat again when it ends', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    const states = [
      { session: 's1', status: 'awaiting_approval', answering: true, url: 'https://platform.example/code?session=s1', approvals: [{ id: 'ap-9', kind: 'confirm', tool: 'Bash', command: 'make test' }] },
      { session: 's1', status: 'idle', answering: false, approvals: [] },
    ]
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(states.length > 1 ? states.shift() : states[0]), { status: 200 })))
    render(<><PWCodeWatcher /><PWCodeApprovals conversationId="c9" /></>)
    expect(await screen.findByText('make test')).toBeInTheDocument()
    await act(async () => { await vi.advanceTimersByTimeAsync(3100) })
    await waitFor(() => expect(chat.loadConversation).toHaveBeenCalledWith('c9'))
    expect(screen.queryByText('make test')).not.toBeInTheDocument()
    vi.useRealTimers()
  })
})
