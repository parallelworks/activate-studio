import type { ReactNode } from 'react'
import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { ChatProvider, ChatThread, type ChatAdapter, type ChatMessage } from '@parallelworks/ui/ai'
import { Markdown } from '@parallelworks/ui'
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

// The session view's client takes the page's fetch as it loads.
vi.hoisted(() => {
  const real = globalThis.fetch
  const current = ((...a: Parameters<typeof fetch>) => (globalThis.fetch === current ? real(...a) : globalThis.fetch(...a))) as typeof fetch
  globalThis.fetch = current
})

import { ChatMarkdownLink } from '../src/chatLinks'
import { CitedPaths, citedMarkdown, codePath, stepFiles, textPaths, type CitedWhere } from '../src/citedPaths'
import { CodePage } from '../src/code/CodePage'
import { PWCodeSession } from '../src/pwcode/PWCodeSession'

const now = new Date().toISOString()
const navigation = { toConversation: () => {}, toNewChat: () => {} }
const user = { id: 'u', username: 'alex', name: 'Alex' } as never

interface Asked { machine?: { id: string }; cwd?: string; libraries?: string[]; paths: { path: string; also?: { path: string; lib?: string }[] }[] }

/** The Studio, whose libraries hold files. */
function studio(files: () => Record<string, { path: string; lib?: string }>, routes: Record<string, unknown> = {}) {
  const asked: Asked[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(new URL(String(input), 'http://studio.example'), init)
    const path = new URL(req.url, 'http://studio.example').pathname
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } })
    if (path === '/api/libraries/cited') {
      const body = JSON.parse(await req.text()) as Asked
      asked.push(body)
      const has = files()
      return json({ files: Object.fromEntries(body.paths.flatMap(p => (has[p.path] ? [[p.path, has[p.path]]] : []))) })
    }
    return json(routes[path] ?? {})
  }))
  return asked
}

beforeAll(() => {
  if (!Element.prototype.scrollTo) Element.prototype.scrollTo = function scrollTo() {}
  if (!globalThis.ResizeObserver) globalThis.ResizeObserver = class { observe() {} unobserve() {} disconnect() {} } as never
})
beforeEach(() => { location.hash = ''; localStorage.clear() })
afterEach(() => { cleanup(); vi.useRealTimers(); vi.unstubAllGlobals(); location.hash = '' })

const links = () => [...document.querySelectorAll<HTMLAnchorElement>('a.cited-path')].map(a => [a.textContent, a.getAttribute('href')])
const tick = (ms: number) => act(async () => { await new Promise(r => setTimeout(r, ms)) })

describe('what may be a path', () => {
  it('takes inline code that is one, and leaves commands and code', () => {
    expect(codePath('reports/2026/site survey.pdf')).toBe('reports/2026/site survey.pdf')
    expect(codePath('notes.md')).toBe('notes.md')
    expect(codePath('src/solver.py:42')).toBe('src/solver.py')
    expect(codePath('/data/proj/Makefile')).toBe('/data/proj/Makefile')
    for (const no of ['npm run build', 'https://example.com/a.pdf', 'x', 'reports/', '--out=a.md', 'a | b.md', '$HOME/a.md', 'cd /data/proj', 'x = y', '#open=file:a.md']) expect(codePath(no), no).toBeNull()
  })

  it('finds paths in text, with the words before a name that has spaces', () => {
    const paths = (t: string) => textPaths(t).map(ways => ways.map(w => w.path))
    expect(paths('See reports/plan.docx, and /data/proj/run1/out.csv.')).toEqual([['See reports/plan.docx', 'reports/plan.docx'], ['/data/proj/run1/out.csv']])
    expect(paths('In reports/2026/site survey.pdf it says')[0]).toContain('reports/2026/site survey.pdf')
    expect(paths('(Field Notes 2026.md)')[0][0]).toBe('Field Notes 2026.md')
    for (const no of ['https://example.com/a/b.pdf', 'e.g. this', 'version 1.2.3', 'me@example.com', 'either/or', 'nothing here']) expect(paths(no), no).toEqual([])
  })

  it("knows a file by its name from the chat's steps and links", () => {
    const m = (parts: unknown[]) => [{ id: 'a', role: 'assistant', content: '', parts }] as unknown as ChatMessage[]
    const steps = stepFiles(m([
      { kind: 'tool_call', id: '1', name: 'search_kb', args: '{"query":"x"}', result: '[library docs] plan/one.md\n  a snippet\nreports/plan.docx\n' },
      { kind: 'tool_call', id: '2', name: 'ReadFile', args: '{"path":"agents/writer/draft.md"}', result: 'not/a/step.md' },
      { kind: 'text', text: 'Wrote [q3](#open=file:reports/q3%20final.md&lib=u-a).' },
    ]))
    expect(steps.get('one.md')).toEqual([{ path: 'plan/one.md', lib: 'docs' }])
    expect(steps.get('plan.docx')).toEqual([{ path: 'reports/plan.docx' }])
    expect(steps.get('draft.md')).toEqual([{ path: 'agents/writer/draft.md' }])
    expect(steps.get('q3 final.md')).toEqual([{ path: 'reports/q3 final.md', lib: 'u-a' }])
    expect(steps.has('step.md')).toBe(false)
  })
})

const adapterWith = (conversation: unknown) => ({
  conversations: {
    list: async () => [{ id: 'c1', title: 't', createdAt: now, updatedAt: now, isOwner: true, canCollaborate: true }],
    get: async () => conversation, create: async () => ({ id: 'c2' }), rename: async () => {}, remove: async () => {},
  },
  models: { list: async () => ({ models: [{ id: 'm', name: 'Model' }], unreachableSessions: [], providerIssues: [] }) },
  streamCompletion: async () => ({ content: '', messageId: 'x', toolCalls: [], finishReason: 'stop', model: 'm', reasoning: '', responsesOutput: [] }),
}) as unknown as ChatAdapter
const adapter = adapterWith(null)
function Chat({ children, where = {}, busy = false, messages }: { children: ReactNode; where?: CitedWhere | null; busy?: boolean; messages?: ChatMessage[] }) {
  return (
    <ChatProvider adapter={adapter} currentUser={user} navigation={navigation} config={{ markdownComponents: { ...citedMarkdown, a: ChatMarkdownLink } }}>
      <CitedPaths where={where} busy={busy} messages={messages}>{children}</CitedPaths>
    </ChatProvider>
  )
}
const reply = (text: string) => <Markdown components={{ ...citedMarkdown, a: ChatMarkdownLink }}>{text}</Markdown>

describe('the library files a reply names', () => {
  it('link from inline code, text, tables, lists and headings, asked for once and together, with everything else left as written', async () => {
    const asked = studio(() => ({ 'notes.md': { path: 'notes.md' }, 'reports/2026/site survey.pdf': { path: 'reports/2026/site survey.pdf' }, 'Field docs/one.md': { path: 'one.md', lib: 'docs' }, 'plan.md': { path: 'plan.md' }, '/data/kb/run.csv': { path: 'run.csv' } }))
    render(<Chat>{reply([
      '# About plan.md', '',
      'Read `notes.md`, `missing.md`, reports/2026/site survey.pdf and **/data/kb/run.csv**.', '',
      '- `Field docs/one.md`', '- [the notes](#open=file:notes.md) and [notes.md](https://example.com/notes.md)', '',
      '| File | What |', '|---|---|', '| notes.md | notes |', '',
      '```', 'cat notes.md', '```', '',
    ].join('\n'))}</Chat>)
    await waitFor(() => expect(links()).toEqual([
      ['plan.md', '#open=file:plan.md'],
      ['notes.md', '#open=file:notes.md'],
      ['reports/2026/site survey.pdf', '#open=file:reports/2026/site%20survey.pdf'],
      ['/data/kb/run.csv', '#open=file:run.csv'],
      ['Field docs/one.md', '#open=file:one.md&lib=docs'],
      ['notes.md', '#open=file:notes.md'],
    ]))
    expect(asked).toHaveLength(1)
    // Links as they were, and no link in one.
    expect(screen.getByRole('link', { name: 'the notes' })).toHaveAttribute('href', '#open=file:notes.md')
    expect(document.querySelector('a a')).toBeNull()
    expect(document.querySelector('pre a, [data-block] a')).toBeNull()
    expect(screen.getByText('missing.md').closest('a')).toBeNull()
    expect(document.querySelector('td a')?.closest('td')).toHaveClass('px-4', 'py-2')
    // Nothing more while it is only shown.
    await tick(2500)
    expect(asked).toHaveLength(1)
  })

  it('wait for the session to be known, and a file written by the end of the turn links then', async () => {
    let has: Record<string, { path: string }> = {}
    const asked = studio(() => has)
    const text = reply('Writing `out/summary.md` now.')
    const view = render(<Chat where={null} busy>{text}</Chat>)
    await tick(400)
    expect(asked).toHaveLength(0)
    view.rerender(<Chat where={{ machine: { id: 'studio' }, cwd: '/w' }} busy>{text}</Chat>)
    await waitFor(() => expect(asked).toHaveLength(1))
    expect(asked[0]).toMatchObject({ machine: { id: 'studio' }, cwd: '/w', paths: [{ path: 'out/summary.md' }] })
    expect(links()).toEqual([])
    has = { 'out/summary.md': { path: 'out/summary.md' } }
    view.rerender(<Chat where={{ machine: { id: 'studio' }, cwd: '/w' }}>{text}</Chat>)
    await waitFor(() => expect(links()).toEqual([['out/summary.md', '#open=file:out/summary.md']]))
    expect(asked).toHaveLength(2)
  })

  it('ask nothing for a reply that names none, and stay as written when the Studio does not answer', async () => {
    const asked = studio(() => ({}))
    const view = render(<Chat>{reply('All done, e.g. version 1.2.3 with `npm run build`.')}</Chat>)
    await tick(500)
    expect(asked).toHaveLength(0)
    view.unmount()
    vi.stubGlobal('fetch', vi.fn(async () => new Response('', { status: 502 })))
    render(<Chat>{reply('See `notes.md`.')}</Chat>)
    await tick(1500)
    expect(links()).toEqual([])
    expect(fetch).toHaveBeenCalledTimes(1)
  })
})

const machine = (id: string) => ({ id, name: 'box', kind: 'existing', status: 'connected', remoteStart: true, hostname: 'box.example', daemonId: 'd1' })
const listed = (machineId: string) => ({ machineId, id: 's1', name: 'two files', status: 'idle', workspace: '/home/alex/kb', createdAt: now, updatedAt: now, messageCount: 1 })
const detail = {
  id: 's1', machineId: 'studio', workspace: '/home/alex/kb', model: 'm', status: 'idle', permissionMode: 'default', createdAt: now, updatedAt: now, messages: 1, lastSeq: 1,
  history: [
    { kind: 'user', text: 'What is here?' },
    { kind: 'tool_call', toolName: 'ReadFile', toolArgs: '{"path":"reports/plan.docx"}', callId: 'c1' },
    { kind: 'tool_result', text: 'text', callId: 'c1' },
    { kind: 'assistant', text: 'Two files: `plan.docx`, and /home/alex/kb/notes.md.' },
    { kind: 'turn_end', turnEnd: { durationMs: 3000, endedAt: now } },
  ],
}
const routes = {
  '/api/code/sessions': { machines: [machine('studio')], sessions: [listed('studio')] },
  '/api/code-page/sessions': { machines: [machine('m1')], sessions: [listed('m1')] },
  '/api/code-page/where': { local: false, codeUrl: 'https://platform.example/code' },
  '/api/code-page/openai/models': { object: 'list', data: [] },
  '/api/delegated': { sessions: [] },
  '/api/mcp/bridge/machines': { available: true, machines: [] },
  '/api/chat/pwcode/here': { available: true, daemonId: 'd1', hostname: 'box.example', chats: [] },
  '/api/code/machines/studio/sessions/s1': detail,
  '/api/code/machines/studio/sessions/s1/transcript': { tail: detail, from: 0, total: detail.history.length },
}

describe('in the chats', () => {
  const files = () => ({ 'plan.docx': { path: 'reports/plan.docx' }, '/home/alex/kb/notes.md': { path: 'notes.md' } })
  const want = [['plan.docx', '#open=file:reports/plan.docx'], ['/home/alex/kb/notes.md', '#open=file:notes.md']]

  it.each<[string, ReactNode]>([
    ['a pw code chat', <ChatProvider key="c" adapter={adapter} currentUser={user} navigation={navigation} config={{ markdownComponents: { ...citedMarkdown, a: ChatMarkdownLink } }}>
      <PWCodeSession conversationId="c1" machine="studio" sessionId="s1" username="alex" />
    </ChatProvider>],
    ["the Code page's pane", <CodePage key="p" />],
  ])("%s links its replies' files, from the session's folder and the files its steps named", async (_where, view) => {
    location.hash = '#view=code?machine=m1&session=s1'
    const asked = studio(files, routes)
    render(view)
    await waitFor(() => expect(links()).toEqual(want), { timeout: 4000 })
    expect(asked[0]).toMatchObject({ machine: { id: 'studio' }, cwd: '/home/alex/kb' })
    expect(asked.flatMap(a => a.paths).find(p => p.path === 'plan.docx')?.also).toEqual([{ path: 'reports/plan.docx' }])
    fireEvent.click(screen.getByRole('link', { name: '/home/alex/kb/notes.md' }))
    await waitFor(() => expect(location.hash).toBe('#open=file:notes.md'))
  })

  it('a Studio assistant chat does too', async () => {
    const asked = studio(files)
    const messages = [
      { id: 'u1', role: 'user', content: 'What is here? notes.md', parentId: null, timestamp: now },
      { id: 'a1', role: 'assistant', content: 'Two files: `plan.docx`, and /home/alex/kb/notes.md.', parentId: 'u1', timestamp: now, model: 'm', parts: [] },
    ] as unknown as ChatMessage[]
    const c = { id: 'c1', title: 't', activeBranchId: 'a1', createdAt: now, isOwner: true, canCollaborate: true, messages }
    render(
      <ChatProvider adapter={adapterWith(c)} currentUser={user} navigation={navigation} activeConversationId="c1" config={{ markdownComponents: { ...citedMarkdown, a: ChatMarkdownLink } }}>
        <CitedPaths where={{ libraries: ['kb'] }} busy={false} messages={messages}><ChatThread conversationId="c1" /></CitedPaths>
      </ChatProvider>,
    )
    await waitFor(() => expect(links()).toEqual(want), { timeout: 5000 })
    expect(asked).toHaveLength(1)
    expect(asked[0].libraries).toEqual(['kb'])
  })
})
