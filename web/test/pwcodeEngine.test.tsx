import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { PWCodeApprovals, PWCodeEngine, PWCodeModelNotice, PWCodeNotice, PWCodeWatcher, shortModel } from '../src/components/PWCodeEngine'
import { getLastEngine, knownEngine } from '../src/labelScope'
import { forgetEngines } from '../src/chatEngine'
import { announceModel } from '../src/pwcode/liveModel'

const chat = vi.hoisted(() => ({ loadConversation: vi.fn(async () => {}), streaming: false }))
vi.mock('@parallelworks/ui/ai', () => ({
  useChat: () => ({ currentConversation: { id: 'c9' }, isStreaming: chat.streaming, loadConversation: chat.loadConversation }),
}))

type Engine = { setting: string; default: string; current?: string; pwcode: boolean; reason: string | null; toolsDown?: string | null }

function serve(engine: Engine, conversation: Record<string, unknown> = {}, pwState: Record<string, unknown> = { session: 's1', status: 'idle', tools: { connected: true, count: 31 } }) {
  const calls: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    calls.push(`${init?.method ?? 'GET'} ${url}${init?.body ? ' ' + String(init.body) : ''}`)
    const body = url.startsWith('/api/chat/engine') ? (init?.method === 'PUT' ? { engine: 'x' } : engine)
      : url.endsWith('/engine') ? { engine: JSON.parse(String(init?.body)).engine }
      : url.startsWith('/api/chat/conversations/') ? conversation
      : url.startsWith('/api/chat/pwcode/') ? pwState
      : { ok: true }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  return calls
}

beforeEach(() => { localStorage.clear(); forgetEngines(); chat.streaming = false })
afterEach(() => { vi.unstubAllGlobals() })

describe('What answers a conversation', () => {
  it('shows pw code as the default where it is offered, links the session, and keeps a change for the chat', async () => {
    const calls = serve({ setting: 'auto', default: 'pwcode', pwcode: true, reason: null }, { pwcode: { url: 'https://platform.example/code?session=s1', session: 's1' } })
    render(<PWCodeEngine conversationId="c1" />)
    const button = await screen.findByRole('button', { name: /pw code/ })
    fireEvent.click(button)
    expect(await screen.findByRole('link', { name: /Open this chat in PW Code on the web/ })).toHaveAttribute('href', 'https://platform.example/code?session=s1')
    expect(await screen.findByText(/has the Studio's tools: 31 of them, the index included/)).toBeInTheDocument()
    expect(screen.getByText('default')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Studio assistant/ }))
    // The switch is the conversation's, kept by the server, and the last
    // choice here, which new chats start with.
    await waitFor(() => expect(calls).toContain('PUT /api/chat/conversations/c1/engine {"engine":"studio"}'))
    await waitFor(() => expect(knownEngine('c1')).toBe('studio'))
    expect(getLastEngine()).toBe('studio')
  })

  it("reads for its own conversation, each time it opens, and marks current apart from a new chat's default", async () => {
    const calls = serve({ setting: 'auto', default: 'studio', current: 'pwcode', pwcode: true, reason: null }, { pwcode: { session: 's1', continued: true, machine: 'm1' } })
    localStorage.setItem('ade-chat-engine', 'studio')
    render(<PWCodeEngine conversationId="c1" />)
    const button = await screen.findByRole('button', { name: /pw code/ })
    expect(calls).toContain('GET /api/chat/engine?conversation=c1&last=studio')
    const before = calls.filter(c => c.startsWith('GET /api/chat/engine')).length
    fireEvent.click(button)
    await waitFor(() => expect(calls.filter(c => c.startsWith('GET /api/chat/engine')).length).toBeGreaterThan(before))
    const pw = await screen.findByRole('button', { name: /^pw code/, pressed: true })
    expect(pw).toHaveClass('on')
    const studio = screen.getByRole('button', { name: /Studio assistant/ })
    expect(studio).not.toHaveClass('on')
    expect(studio).toHaveTextContent('default')
    expect(pw).not.toHaveTextContent('default')
  })

  it('does not switch while a reply is being written, and says why', async () => {
    chat.streaming = true
    const calls = serve({ setting: 'auto', default: 'pwcode', current: 'pwcode', pwcode: true, reason: null }, { pwcode: { session: 's1' } })
    render(<PWCodeEngine conversationId="c1" />)
    fireEvent.click(await screen.findByRole('button', { name: /pw code/ }))
    expect(await screen.findByText(/A reply is being written; switch what answers when it ends/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Studio assistant/ })).toBeDisabled()
    expect(calls.some(c => c.startsWith('PUT'))).toBe(false)
  })

  it("says what is known of a relayed session's tools, and offers a new session there that has them", async () => {
    const calls = serve({ setting: 'auto', default: 'pwcode', current: 'pwcode', pwcode: true, reason: null },
      { pwcode: { session: 's1', continued: true, relay: true, machine: 'm1', machineName: 'box-a' } },
      { session: 's1', status: 'idle', tools: { connected: false, count: 0, relay: true, machineHas: true }, continued: true, relay: true })
    render(<PWCodeEngine conversationId="c1" />)
    fireEvent.click(await screen.findByRole('button', { name: /pw code/ }))
    expect(await screen.findByText(/pw code on box-a has this Studio's tools, as you\. Sessions started there since it was set up have them; one open from before keeps the tools it started with\. This chat's session has them only if it started after that\./)).toBeInTheDocument()
    expect(screen.queryByText(/tries to reconnect them/)).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Start a new session there with them' }))
    await waitFor(() => expect(calls).toContain('POST /api/chat/pwcode/c1/new-session'))
  })

  it('links a chat that continues a session back to it on the Code page', async () => {
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      const body = url.startsWith('/api/chat/engine') ? { setting: 'auto', default: 'studio', current: 'pwcode', pwcode: true, reason: null }
        : url.startsWith('/api/chat/conversations/') ? { pwcode: { url: 'https://platform.example/code/m1/s1', session: 's1', continued: true, machine: 'm1' } }
        : url.startsWith('/api/chat/pwcode/') ? { session: 's1', status: 'idle', tools: null, continued: true }
        : {}
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
    render(<PWCodeEngine conversationId="c1" />)
    fireEvent.click(await screen.findByRole('button', { name: /pw code/ }))
    expect(await screen.findByRole('link', { name: 'See it on the Code page' })).toHaveAttribute('href', '#view=code?machine=m1&session=s1')
    expect(await screen.findByText(/began outside the Studio, so it works with its own tools/)).toBeInTheDocument()
  })

  it('says why pw code is not offered when the Studio is set to it', async () => {
    serve({ setting: 'pwcode', default: 'studio', pwcode: false, reason: 'pw code runs as you: sign in with ACTIVATE under Settings, Accounts.' })
    render(<PWCodeEngine conversationId="c1" />)
    fireEvent.click(await screen.findByRole('button', { name: /Studio/ }))
    expect(await screen.findByText(/sign in with ACTIVATE/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /pw code/ })).toBeDisabled()
  })

  it('where pw code waits only for an ACTIVATE sign-in, says so and links to Accounts', async () => {
    serve({ setting: 'auto', default: 'studio', pwcode: false, reason: 'pw code needs ACTIVATE: sign the pw CLI in, or add your ACTIVATE API key under Settings, Accounts.' })
    render(<PWCodeEngine conversationId={null} />)
    fireEvent.click(await screen.findByRole('button', { name: /Studio/ }))
    expect(await screen.findByText('Sign in with ACTIVATE to use this.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /pw code/ })).toBeDisabled()
    expect(screen.getByRole('link', { name: 'Open Accounts' })).toHaveAttribute('href', '#view=settings:accounts?open=activate')
  })

  it('stays out of the way where pw code is not offered', async () => {
    const calls = serve({ setting: 'auto', default: 'studio', pwcode: false, reason: 'pw code needs ACTIVATE' })
    const { container } = render(<PWCodeEngine conversationId={null} />)
    await waitFor(() => expect(calls).toContain('GET /api/chat/engine?'))
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

  it('answers a question with the options picked or its own words, or talks it through', async () => {
    const calls = serve({ setting: 'auto', default: 'pwcode', pwcode: true, reason: null })
    render(<PWCodeApprovals conversationId="c1" />)
    const questions = [
      { question: 'Which mesh?', header: 'Mesh', options: [{ label: 'Coarse' }, { label: 'Fine', description: 'slower' }] },
      { question: 'Anything else?' },
    ]
    act(() => { window.dispatchEvent(new CustomEvent('ade-pwcode-approval', { detail: { conversationId: 'c1', approval: { id: 'ap-q', kind: 'ask', questions } } })) })
    expect(await screen.findByText('pw code asks you some questions')).toBeInTheDocument()
    const sendBtn = screen.getByRole('button', { name: 'Send' })
    expect(sendBtn).toBeDisabled()
    fireEvent.click(screen.getByRole('radio', { name: /Fine/ }))
    expect(sendBtn).toBeDisabled()
    fireEvent.change(screen.getByRole('textbox', { name: 'Your own answer to: Anything else?' }), { target: { value: ' keep the old output ' } })
    fireEvent.click(sendBtn)
    await waitFor(() => expect(screen.queryByText('pw code asks you some questions')).not.toBeInTheDocument())
    expect(calls).toContain('POST /api/chat/pwcode/c1/approvals/ap-q {"allowed":true,"kind":"ask","answers":[{"question":"Which mesh?","picked":["Fine"],"text":""},{"question":"Anything else?","picked":[],"text":"keep the old output"}]}')

    act(() => { window.dispatchEvent(new CustomEvent('ade-pwcode-approval', { detail: { conversationId: 'c1', approval: { id: 'ap-r', kind: 'ask', questions: [questions[1]] } } })) })
    fireEvent.click(await screen.findByRole('button', { name: 'Talk it through' }))
    await waitFor(() => expect(calls).toContain('POST /api/chat/pwcode/c1/approvals/ap-r {"allowed":false,"kind":"ask","chat":true}'))
  })

  it('approves or rejects a plan', async () => {
    const calls = serve({ setting: 'auto', default: 'pwcode', pwcode: true, reason: null })
    render(<PWCodeApprovals conversationId="c1" />)
    act(() => { window.dispatchEvent(new CustomEvent('ade-pwcode-approval', { detail: { conversationId: 'c1', approval: { id: 'ap-p', kind: 'plan', plan: '1. mesh\n2. run' } } })) })
    fireEvent.click(await screen.findByRole('button', { name: 'Reject' }))
    await waitFor(() => expect(calls).toContain('POST /api/chat/pwcode/c1/approvals/ap-p {"allowed":false,"kind":"plan"}'))
  })

  it("rejects a plan with what to change, and shows a long plan whole before it is approved", async () => {
    const calls = serve({ setting: 'auto', default: 'pwcode', pwcode: true, reason: null })
    render(<PWCodeApprovals conversationId="c1" />)
    act(() => { window.dispatchEvent(new CustomEvent('ade-pwcode-approval', { detail: { conversationId: 'c1', approval: { id: 'ap-f', kind: 'plan', plan: '1. mesh' } } })) })
    fireEvent.change(await screen.findByRole('textbox', { name: 'What to change in the plan' }), { target: { value: ' split step 1 ' } })
    fireEvent.click(screen.getByRole('button', { name: 'Reject' }))
    await waitFor(() => expect(calls).toContain('POST /api/chat/pwcode/c1/approvals/ap-f {"allowed":false,"kind":"plan","message":"split step 1"}'))

    const long = 'Step one. ' + 'x'.repeat(700) + ' The last step deletes results.'
    act(() => { window.dispatchEvent(new CustomEvent('ade-pwcode-approval', { detail: { conversationId: 'c1', approval: { id: 'ap-l', kind: 'plan', plan: long } } })) })
    const approve = await screen.findByRole('button', { name: 'Approve' })
    expect(screen.queryByText(/The last step deletes results/)).not.toBeInTheDocument()
    expect(approve).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: /Show all/ }))
    expect(screen.getByText(/The last step deletes results/)).toBeInTheDocument()
    expect(approve).toBeEnabled()
    fireEvent.click(approve)
    await waitFor(() => expect(calls).toContain('POST /api/chat/pwcode/c1/approvals/ap-l {"allowed":true,"kind":"plan"}'))
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

describe('When pw code cannot reach the Studio tools', () => {
  it('says the Studio assistant answered, in that chat only, until pw code answers again', async () => {
    serve({ setting: 'auto', default: 'studio', pwcode: true, reason: null, toolsDown: 'down' } as Engine)
    render(<PWCodeNotice conversationId="c1" />)
    act(() => {
      window.dispatchEvent(new CustomEvent('ade-pwcode-fallback', { detail: { conversationId: 'other', reason: 'not this one' } }))
      window.dispatchEvent(new CustomEvent('ade-pwcode-fallback', { detail: { conversationId: 'c1', reason: 'pw code could not reach this Studio\'s tools (refused)' } }))
    })
    expect(await screen.findByText(/could not reach this Studio's tools \(refused\)/)).toBeInTheDocument()
    expect(screen.queryByText('not this one')).not.toBeInTheDocument()
    act(() => { window.dispatchEvent(new CustomEvent('ade-pwcode-session', { detail: { conversationId: 'c1', session: 's2' } })) })
    await waitFor(() => expect(screen.queryByText(/could not reach/)).not.toBeInTheDocument())
  })
})

describe("The model pw code answers with", () => {
  it('is named on the button and in the menu, and the composer is told pw code answers', async () => {
    expect(shortModel('acct:codex/gpt-6.2-sol')).toBe('gpt-6.2-sol')
    expect(shortModel('claude-code/haiku')).toBe('haiku')
    const note = 'Runs on gpt-b because sonnet-x is not enabled for you. Choose another under Settings, External access, pw code model.'
    serve({ setting: 'auto', default: 'pwcode', pwcode: true, reason: null }, { pwcode: { session: 's1', model: 'acct:codex/gpt-b', modelFrom: 'acct:anthropic/sonnet-x', modelNote: note } })
    const canvas = document.createElement('div'); canvas.className = 'chat-canvas'; document.body.appendChild(canvas)
    render(<PWCodeEngine conversationId="c1" />)
    fireEvent.click(await screen.findByRole('button', { name: 'pw code · gpt-b' }))
    // Short names in the text, the full ids on hover, and the setting a link.
    const link = await screen.findByRole('link', { name: 'Settings, External access' })
    expect(link).toHaveAttribute('href', '#view=settings:rag')
    expect(link.parentElement).toHaveTextContent(note)
    expect(link.parentElement).toHaveAttribute('title', 'acct:anthropic/sonnet-x → acct:codex/gpt-b')
    expect(canvas.getAttribute('data-engine')).toBe('pwcode')
    // A new session has no note of its own: the old one's does not stay.
    act(() => { window.dispatchEvent(new CustomEvent('ade-pwcode-session', { detail: { conversationId: 'c1', session: 's2', model: 'acct:codex/gpt-b', modelNote: '' } })) })
    await waitFor(() => expect(screen.queryByRole('link', { name: 'Settings, External access' })).toBeNull())
    canvas.remove()
  })
  it('follows the model the session runs on now, picked after the chat was linked', async () => {
    serve({ setting: 'auto', default: 'pwcode', pwcode: true, reason: null }, { pwcode: { session: 's1', model: 'claude-code/haiku' } })
    // The session view said its model before the button mounted.
    announceModel('c7', 'claude-code/haiku')
    const { unmount } = render(<PWCodeEngine conversationId="c7" />)
    expect(await screen.findByRole('button', { name: 'pw code · haiku' })).toBeInTheDocument()
    act(() => announceModel('c7', 'claude-code/sonnet'))
    expect(await screen.findByRole('button', { name: 'pw code · sonnet' })).toBeInTheDocument()
    // Another chat's session changing model leaves this one's name alone.
    act(() => announceModel('c8', 'claude-code/opus'))
    expect(screen.getByRole('button', { name: 'pw code · sonnet' })).toBeInTheDocument()
    unmount()
    // Opened again, it names the model last said, not the one linked.
    render(<PWCodeEngine conversationId="c7" />)
    expect(await screen.findByRole('button', { name: 'pw code · sonnet' })).toBeInTheDocument()
  })
  it('says once in the chat when the Studio moved the session to another model', async () => {
    render(<PWCodeModelNotice conversationId="c1" />)
    act(() => {
      window.dispatchEvent(new CustomEvent('ade-pwcode-session', { detail: { conversationId: 'c1', modelMoved: false, modelNote: 'old note' } }))
    })
    expect(screen.queryByText('old note')).toBeNull()
    act(() => {
      window.dispatchEvent(new CustomEvent('ade-pwcode-session', { detail: { conversationId: 'c1', modelMoved: true, modelNote: 'This chat runs on acct:codex/gpt-b.' } }))
    })
    expect(screen.getByText('pw code runs this chat on another model')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }))
    expect(screen.queryByText('This chat runs on acct:codex/gpt-b.')).toBeNull()
  })
  it('says so when the Studio could not move the session', () => {
    render(<PWCodeModelNotice conversationId="c1" />)
    act(() => {
      window.dispatchEvent(new CustomEvent('ade-pwcode-session', { detail: { conversationId: 'c1', modelMoved: false, modelStuck: true, modelNote: 'could not move: the session is busy.' } }))
    })
    expect(screen.getByText('pw code could not move this chat to another model')).toBeInTheDocument()
    expect(screen.getByText('could not move: the session is busy.')).toBeInTheDocument()
  })
})
