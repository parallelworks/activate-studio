import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { ChatProvider, ChatThread, type ChatAdapter } from '@parallelworks/ui/ai'
import { GUFI_TOOL_RENDERERS, GufiCommands, GufiToolCommands, commandMeta, errorsLabel, where, type GufiCommand } from '../src/components/GufiCommands'
import { createStudioAdapter } from '../src/adapter'

const at = '2026-10-28T09:30:05.000Z'
const ran: GufiCommand = { engine: 'gufi_query', command: "gufi_query -E 'SELECT 1;' /ix", ms: 18, at, exit: 0, stderr: '' }

afterEach(() => vi.restoreAllMocks())

describe('a GUFI command\'s record', () => {
  it('says where, when, how long and how it ended', () => {
    const time = new Date(at).toLocaleTimeString()
    expect(commandMeta(ran)).toBe(`on the Studio host, at ${time}, 18 ms, exit 0`)
    expect(commandMeta({ ...ran, exit: undefined, failed: 'ended by SIGKILL' })).toBe(`on the Studio host, at ${time}, 18 ms, ended by SIGKILL`)
    expect(where({ engine: 'gufi_query', command: 'x', site: 'cluster-a' })).toBe('on cluster-a, after pw ssh cluster-a')
    expect(where({ engine: 'gufi_query', command: 'x', site: 'alex@hpc.example:2222', shell: 'ssh -p 2222 alex@hpc.example' })).toBe('on alex@hpc.example:2222, after ssh -p 2222 alex@hpc.example')
    // A command shown before it runs (a saved query's) says only where.
    expect(commandMeta({ engine: 'gufi_query', command: 'x' })).toBe('on the Studio host')
  })

  it('counts its error output, the expected lines apart', () => {
    expect(errorsLabel(ran)).toBeNull()
    expect(errorsLabel({ ...ran, expected: 12 })).toBe('Error output: 12 expected lines')
    expect(errorsLabel({ ...ran, stderr: 'Error: a\nError: b', expected: 1 })).toBe('Error output: 2 lines, 1 expected')
    expect(errorsLabel({ ...ran, stderr: 'Error: a' })).toBe('Error output: 1 line')
  })
})

describe('the GUFI commands panel', () => {
  it('shows each command, and its error output a click away: the rest as it came, the expected lines named', () => {
    render(<GufiCommands commands={[
      { ...ran, stderr: 'Error: disk I/O error: /ix/a/db.db', stderrCut: true, expected: 3, expectedExample: 'Error: no such table: words: /ix/b/db.db' },
      { ...ran, command: 'gufi_query /ix/c', exit: undefined, failed: 'the ssh session ended (255)', stderr: undefined, site: 'cluster-a' },
      { ...ran, command: 'gufi_query /ix/d' },
    ]} />)
    fireEvent.click(screen.getByRole('button', { name: /GUFI commands \(3\)/ }))
    expect(screen.getByText("gufi_query -E 'SELECT 1;' /ix")).toBeInTheDocument()
    expect(screen.getByText('Its error output is not kept.')).toBeInTheDocument()
    expect(screen.getByText('No error output.')).toBeInTheDocument()
    expect(screen.getByText(/on cluster-a, after pw ssh cluster-a, .*the ssh session ended \(255\)/)).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /Error output: 1 line, 3 expected/ }))
    expect(screen.getByText('Error: disk I/O error: /ix/a/db.db')).toBeInTheDocument()
    expect(screen.getByText('Only the end of its error output is kept.')).toBeInTheDocument()
    expect(screen.getByText(/3 lines say .no such table. for the text or vector tables. That is expected/)).toBeInTheDocument()
    expect(screen.getByText('Error: no such table: words: /ix/b/db.db')).toBeInTheDocument()
  })
})

// A chat adapter that serves one stored conversation.
const adapterWith = (conversation: unknown) => ({
  conversations: {
    list: async () => [{ id: 'c1', title: 'storage', createdAt: at, updatedAt: at, isOwner: true, canCollaborate: true }],
    get: async () => conversation, create: async () => ({ id: 'c2' }), rename: async () => {}, remove: async () => {},
  },
  models: { list: async () => ({ models: [{ id: 'm', name: 'Model' }], unreachableSessions: [], providerIssues: [] }) },
  streamCompletion: async () => ({ content: '', messageId: 'x', toolCalls: [], finishReason: 'stop', model: 'm', reasoning: '', responsesOutput: [] }),
}) as unknown as ChatAdapter
const user = { id: 'u-alex', username: 'alex', name: 'Alex' } as never
const nav = { toConversation: () => {}, toNewChat: () => {} }

describe('in the chat', () => {
  beforeAll(() => {
    if (!Element.prototype.scrollTo) Element.prototype.scrollTo = function scrollTo() {}
    if (!globalThis.ResizeObserver) globalThis.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} } as never
  })

  it('the adapter keeps a result\'s commands for its block, and passes the result on as before', async () => {
    const gufi = [{ ...ran, command: 'gufi_query /streamed' }]
    const sse = [
      `event: tool\ndata: ${JSON.stringify({ phase: 'call', id: 'call_9', name: 'search_kb', args: '{"query":"x"}' })}`,
      `event: tool\ndata: ${JSON.stringify({ phase: 'result', id: 'call_9', name: 'search_kb', summary: '4 hits', error: false, gufi })}`,
      `event: done\ndata: ${JSON.stringify({ finishReason: 'stop', messageId: 'srv-9' })}`,
    ].join('\n\n') + '\n\n'
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(sse, { status: 200 }))
    const parts: unknown[] = []
    await createStudioAdapter().streamCompletion({ model: 'm', messages: [], conversationId: 'c1', userMessageId: 'u9' }, { onContent: () => {}, onPart: d => parts.push(d) })
    expect(parts[1]).toEqual({ type: 'tool_end', id: 'call_9', result: '4 hits', isError: false })
    const part = { kind: 'tool_call' as const, id: 'call_9', name: 'search_kb', args: '{"query":"x"}', status: 'ok' as const, result: '4 hits' }
    const message = { id: 'srv-9', role: 'assistant' as const, content: '', parentId: null, timestamp: at, parts: [part] }
    render(<ChatProvider adapter={adapterWith(null)} currentUser={user} navigation={nav}><GufiToolCommands part={part} message={message as never} /></ChatProvider>)
    expect(screen.getByText('4 hits')).toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: /GUFI command/ }))
    expect(screen.getByText('gufi_query /streamed')).toBeInTheDocument()
  })

  it('a stored call shows its result summary and its commands under its row; one without commands, its summary', async () => {
    const conversation = {
      id: 'c1', title: 'storage', activeBranchId: 'srv-1', createdAt: at, isOwner: true, canCollaborate: true,
      messages: [
        { id: 'u1', role: 'user', content: 'find storage notes', parentId: null, timestamp: at },
        { id: 'srv-1', role: 'assistant', content: 'Found them.', parentId: 'u1', timestamp: at, model: 'm', parts: [
          { kind: 'tool_call', id: 'call_1', name: 'search_kb', args: '{"query":"storage"}', status: 'ok', result: '3 hits', gufi: [{ ...ran, command: 'gufi_query /stored' }] },
          { kind: 'tool_call', id: 'call_2', name: 'list_kb_dir', args: '{"path":"notes"}', status: 'ok', result: '2 entries' },
        ] },
      ],
    }
    render(
      <ChatProvider adapter={adapterWith(conversation)} currentUser={user} navigation={nav} activeConversationId="c1"
        config={{ slots: { toolRenderers: GUFI_TOOL_RENDERERS } }}>
        <ChatThread conversationId="c1" />
      </ChatProvider>,
    )
    const toggle = await screen.findByRole('button', { name: /GUFI command/ }, { timeout: 5000 })
    expect(screen.getByText('3 hits')).toBeInTheDocument()
    expect(screen.getByText('2 entries')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /GUFI command/ })).toHaveLength(1)
    await act(async () => { fireEvent.click(toggle) })
    await waitFor(() => expect(screen.getByText('gufi_query /stored')).toBeInTheDocument())
  })
})
