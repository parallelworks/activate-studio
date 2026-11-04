import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it, vi } from 'vitest'
import { ChatProvider, ChatThread, type ChatAdapter } from '@parallelworks/ui/ai'
import { GUFI_TOOL_RENDERERS, GufiCommands, GufiToolCommands, commandMeta, errorsLabel, gufiToolPresentation, shownCommand, shownNote, where, type GufiCommand } from '../src/components/GufiCommands'
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

  const slots = { toolRenderers: GUFI_TOOL_RENDERERS, toolPresentation: gufiToolPresentation }
  const stored = (parts: unknown[]) => ({
    id: 'c1', title: 'storage', activeBranchId: 'srv-1', createdAt: at, isOwner: true, canCollaborate: true,
    messages: [
      { id: 'u1', role: 'user', content: 'find storage notes', parentId: null, timestamp: at },
      { id: 'srv-1', role: 'assistant', content: 'Found them.', parentId: 'u1', timestamp: at, model: 'm', parts },
    ],
  })
  const thread = (conversation: unknown) => render(
    <ChatProvider adapter={adapterWith(conversation)} currentUser={user} navigation={nav} activeConversationId="c1" config={{ slots }}>
      <ChatThread conversationId="c1" />
    </ChatProvider>,
  )
  const call = (id: string, name: string, args: string, extra: object = {}) => ({ kind: 'tool_call', id, name, args, status: 'ok', result: '3 hits', ...extra })

  it('one stored call keeps its summary on its row and its commands inside it', async () => {
    thread(stored([call('call_1', 'search_kb', '{"query":"storage"}', { gufi: [{ ...ran, command: 'gufi_query /stored' }] })]))
    const row = await screen.findByRole('button', { name: /Searched for storage/ }, { timeout: 5000 })
    expect(screen.getByText('3 hits')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /GUFI command/ })).toBeNull()
    await act(async () => { fireEvent.click(row) })
    await act(async () => { fireEvent.click(await screen.findByRole('button', { name: /GUFI command/ })) })
    await waitFor(() => expect(screen.getByText('gufi_query /stored')).toBeInTheDocument())
  })

  it('a reply\'s finished calls fold into one row, GUFI tools with the rest, and their commands stay reachable', async () => {
    const parts = [
      ...Array.from({ length: 6 }, (_, i) => call(`s${i}`, 'search_kb', `{"query":"q${i}"}`, { gufi: [{ ...ran, command: `gufi_query /s${i}` }] })),
      ...Array.from({ length: 5 }, (_, i) => call(`r${i}`, 'read_kb_file', `{"path":"f${i}.md"}`)),
      call('l0', 'list_kb_dir', '{"path":"notes"}'),
      call('o0', 'read_original', '{"path":"f.pdf"}'),
    ]
    thread(stored(parts))
    const fold = await screen.findByRole('button', { name: /Ran 6 searches, read 5 files, listed 1 folder, ran 1 tool/ }, { timeout: 5000 })
    expect(screen.getAllByTestId('tool-rollup')).toHaveLength(1)
    // Nothing of the calls is drawn beside the folded row.
    expect(screen.queryByTestId('tool-call-group')).toBeNull()
    expect(screen.queryByRole('button', { name: /GUFI command/ })).toBeNull()
    await act(async () => { fireEvent.click(fold) })
    await act(async () => { fireEvent.click(await screen.findByRole('button', { name: /Searched for q3/ })) })
    await act(async () => { fireEvent.click(await screen.findByRole('button', { name: /GUFI command/ })) })
    await waitFor(() => expect(screen.getByText('gufi_query /s3')).toBeInTheDocument())
  })

  it('a running call and a failed one keep their own rows between folded ones', async () => {
    const ok = (id: string) => call(id, 'read_kb_file', `{"path":"${id}.md"}`)
    thread(stored([
      ok('a'), ok('b'),
      call('bad', 'search_kb', '{"query":"broken"}', { status: 'error', result: 'the index is locked', gufi: [{ ...ran, command: 'gufi_query /failed', exit: 1 }] }),
      ok('c'), ok('d'),
      call('live', 'query_corpus', '{"canned":"largest"}', { status: 'running', result: undefined }),
    ]))
    const failed = await screen.findByRole('button', { name: /Searched for broken/ }, { timeout: 5000 })
    expect(screen.getAllByTestId('tool-rollup')).toHaveLength(2)
    expect(screen.getByText(/the index is locked/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /Queried the index: largest/ })).toBeInTheDocument()
    await act(async () => { fireEvent.click(failed) })
    // Open, the reason is still there, with the commands behind the failure.
    expect(await screen.findByText(/the index is locked/)).toBeInTheDocument()
    await act(async () => { fireEvent.click(await screen.findByRole('button', { name: /GUFI command/ })) })
    await waitFor(() => expect(screen.getByText('gufi_query /failed')).toBeInTheDocument())
  })

  it('names the GUFI tools only, and copes with arguments still streaming', () => {
    const part = (name: string, args: string) => ({ kind: 'tool_call' as const, id: 'x', name, args, status: 'running' as const })
    expect(gufiToolPresentation(part('search_kb', '{"qu'))?.label).toBe('Searched')
    expect(gufiToolPresentation(part('search_kb', '{"text":"pumps"}'))?.label).toBe('Searched for pumps')
    expect(gufiToolPresentation(part('list_kb_dir', '{}'))?.label).toBe('Listed the top folder')
    expect(gufiToolPresentation(part('search_kb', '{}'))?.group?.(2)).toBe('Ran 2 searches')
    expect(gufiToolPresentation(part('read_original', '{}'))).toBeUndefined()
  })
})

describe('a command as the page shows it', () => {
  const hex = 'ab12'.repeat(768)
  const q = "'\\''"
  const quoted = `gufi_query -I 'CREATE TABLE qv (pat TEXT, qe BLOB);' -E 'INSERT INTO qv VALUES (${q}m${q}, X${q}${hex}${q}), (${q}n${q}, X${q}${hex}${q});' /ix`
  const heredoc = `gufi_sqlite3 <<'SQL'\nCREATE TABLE temp.qo AS SELECT X'${hex}' AS qe;\nSQL`

  it('shortens each query vector, inside shell quotes or a here-document, and reads the quotes as SQL', () => {
    const shown = shownCommand(quoted)
    expect(shown.vectors).toBe(2)
    expect(shown.quotes).toBe(true)
    expect(shown.text).toBe(`gufi_query -I 'CREATE TABLE qv (pat TEXT, qe BLOB);' -E 'INSERT INTO qv VALUES ('m', X'ab12ab12…' (384-value query vector)), ('n', X'ab12ab12…' (384-value query vector));' /ix`)
    expect(shownCommand(heredoc).text).toBe(`gufi_sqlite3 <<'SQL'\nCREATE TABLE temp.qo AS SELECT X'ab12ab12…' (384-value query vector) AS qe;\nSQL`)
  })

  it('leaves a command with nothing to shorten alone, and a short blob too', () => {
    expect(shownCommand("gufi_query -E 'SELECT 1;' /ix")).toEqual({ text: "gufi_query -E 'SELECT 1;' /ix", vectors: 0, quotes: false })
    expect(shownCommand("gufi_query -E 'SELECT X;' X'ab12'").vectors).toBe(0)
    expect(shownCommand(`x X'${'ab'.repeat(33)}'`).text).toBe(`x X'abababab…' (33-byte value)`)
  })

  it('says what it changed, once, and nothing where it changed nothing', () => {
    expect(shownNote([quoted, heredoc])).toBe('Query vectors are shortened on screen and shell quote escapes left out. Copy has the full command.')
    expect(shownNote([heredoc])).toBe('Query vectors are shortened on screen. Copy has the full command.')
    expect(shownNote([`a 'b${q}c'`])).toBe('Shell quote escapes are left out on screen. Copy has the command as it runs.')
    expect(shownNote(['gufi_query /ix'])).toBeNull()
  })

  it('shows the short form with one note, and Copy gives the whole command', async () => {
    const writeText = vi.fn(async () => {})
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    render(<GufiCommands commands={[{ ...ran, command: quoted }, { ...ran, command: heredoc }]} />)
    fireEvent.click(screen.getByRole('button', { name: /GUFI commands \(2\)/ }))
    expect(document.body.textContent).not.toContain(hex)
    expect(screen.getAllByText(/384-value query vector/)).toHaveLength(2)
    expect(screen.getAllByText(/Copy has the full command/)).toHaveLength(1)
    await act(async () => { fireEvent.click(screen.getAllByRole('button', { name: /Copy GUFI command/ })[0]) })
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(quoted))
  })
})
