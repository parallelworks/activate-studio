import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ago, argSummary, CodeView, continuable, groupSessions, isAtEnd, sessionInHash, shortPath, Transcript, type CodeSession, type Here } from '../src/views/CodeView'
import { getEngine } from '../src/labelScope'

const row = (o: Partial<CodeSession>): CodeSession => ({ machineId: 'm', machine: 'm1', id: 'x', title: 't', messages: 1, status: 'idle', url: '', ...o })

describe('Code sessions', () => {
  it('groups what needs an answer first and archived last, pinned first within a group', () => {
    const g = groupSessions([
      row({ id: 'a', status: 'idle', updatedAt: '2026-11-12T10:00:00Z' }),
      row({ id: 'b', status: 'running', updatedAt: '2026-11-12T09:00:00Z' }),
      row({ id: 'c', status: 'awaiting_approval' }),
      row({ id: 'd', status: 'idle', archived: true }),
      row({ id: 'e', status: 'idle', pinned: true, updatedAt: '2026-11-01T00:00:00Z' }),
    ])
    expect(g.map(x => [x.key, x.rows.map(r => r.id).join('')])).toEqual([['needs', 'c'], ['working', 'b'], ['recent', 'ea'], ['archived', 'd']])
  })
  it('names times, paths and tool arguments briefly', () => {
    const now = Date.parse('2026-11-12T12:00:00Z')
    expect(ago('2026-11-12T11:58:00Z', now)).toBe('2m')
    expect(ago('2026-11-10T12:00:00Z', now)).toBe('2d')
    expect(shortPath('/home/u/work/project/src')).toBe('…/project/src')
    expect(argSummary('{"path":"a/b.go","limit":5}')).toBe('a/b.go')
    expect(argSummary('{"x":1}')).toBe('')
  })
  it('reads tool arguments the server cut short, and never shows them raw', () => {
    // The server keeps the first 600 characters, so long arguments are not JSON.
    const cut = '{"command":"ssh host \'python3 -c \\"\\n# Check the coordinates\\nimport os\\nprint(1)'
    expect(argSummary(cut, 'Bash')).toBe("ssh host 'python3 -c \" …")
    expect(argSummary('{"todos":[{"activeForm":"Checking the mesh","content":"Check the mesh","status":"completed"},{"activeForm":"Validating the domain","status":"in_progress"', 'TodoWrite'))
      .toBe('Validating the domain')
    expect(argSummary('{"todos":[{"activeForm":"Checking the mesh","content":"Check the mesh","status":"in_progress"}]}', 'TodoWrite')).toBe('Checking the mesh')
    expect(argSummary('{"pattern":"radarAxes","path":"app/explorer.html"}', 'GrepSearch')).toBe('radarAxes in app/explorer.html')
    expect(argSummary('{"file_path":"app/explorer.html","old_string":"a","new_string":" .d-actions { display: flex', 'EditFile')).toBe('app/explorer.html')
    expect(argSummary('{"new_string":" .d-actions { display: flex; }\\n .d-note { margin: 0', 'EditFile')).toBe('.d-actions { display: flex; } …')
  })
  it('folds a run of tool calls into one line that opens to each', () => {
    const call = (n: string, a: string) => ({ kind: 'tool_call', toolName: n, toolArgs: a })
    const { container } = render(<Transcript earlier={0} items={[
      { kind: 'user', text: 'Tidy the explorer' },
      call('ReadFile', '{"path":"a.html"}'), { kind: 'tool_result', text: '<html>' },
      call('ReadFile', '{"path":"a.html"}'),
      call('EditFile', '{"path":"a.html"}'),
      call('Bash', '{"command":"git status -s"}'),
      { kind: 'assistant', text: 'Done.' },
      call('ReadFile', '{"path":"b.html"}'),
    ]} />)
    const runs = container.querySelectorAll('details.code-tool-run')
    expect(runs.length).toBe(1)
    expect(runs[0].querySelector('summary')?.textContent).toBe('4 tool calls ReadFile 2, EditFile, Bash')
    expect(runs[0].querySelectorAll('.code-tool-run-body details.code-tool').length).toBe(4)
    expect((runs[0] as HTMLDetailsElement).open).toBe(false)
    // A lone call after the reply stays a line of its own.
    expect(container.querySelectorAll('.code-transcript > details.code-tool:not(.code-tool-run)').length).toBe(1)
  })
  it('folds each tool result under its call', () => {
    const { container } = render(<Transcript earlier={2} items={[
      { kind: 'user', text: 'Fix the build' },
      { kind: 'tool_call', toolName: 'ReadFile', toolArgs: '{"path":"go.mod"}' },
      { kind: 'tool_result', text: 'module x' },
      { kind: 'assistant', text: 'Done: **fixed**.' },
    ]} />)
    expect(container.querySelectorAll('details.code-tool').length).toBe(1)
    expect(container.querySelector('details pre')?.textContent).toBe('module x')
    expect(container.querySelector('.code-msg.user')?.textContent).toBe('Fix the build')
    expect(container.textContent).toContain('2 earlier items')
  })
})

describe('Continue in Studio', () => {
  const here: Here = { available: true, daemonId: 'd1', hostname: 'box.example', chats: [] }
  const machines = [
    { id: 'm1', name: 'box', kind: 'existing', status: 'connected', daemonId: 'd1', hostname: 'box.example' },
    { id: 'm2', name: 'laptop', kind: 'existing', status: 'connected', daemonId: 'd2' },
    { id: 'm3', name: 'old', kind: 'existing', status: 'connected', hostname: 'BOX.example' },
  ]
  it('is offered on the machine whose daemon the chat reaches, and says why not elsewhere', () => {
    expect(continuable(row({ machineId: 'm1' }), machines, here)).toEqual({ ok: true, why: '' })
    expect(continuable(row({ machineId: 'm2', machine: 'laptop' }), machines, here).why).toMatch(/on laptop, and this Studio's pw code runs on box.example, so it is read only here/)
    expect(continuable(row({ machineId: 'm3' }), machines, here).ok).toBe(true)
    expect(continuable(row({ machineId: 'm3' }), machines, { ...here, daemonId: undefined }).ok).toBe(false)
    expect(continuable(row({ machineId: 'm1', archived: true }), machines, here).why).toMatch(/archived/)
    expect(continuable(row({ machineId: 'm1' }), machines, { available: false, reason: 'Sign in to use pw code.', chats: [] }).why).toBe('Sign in to use pw code.')
  })

  let calls: string[] = []
  beforeEach(() => {
    localStorage.clear(); location.hash = ''; calls = []
  })
  afterEach(() => { vi.unstubAllGlobals() })
  function serve(chats: Here['chats'] = [], refuse = '') {
    vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
      calls.push(`${init?.method ?? 'GET'} ${url}${init?.body ? ' ' + String(init.body) : ''}`)
      const confirmed = String(init?.body ?? '').includes('"confirmed":true')
      const body = url === '/api/code-sessions' ? { machines, codeUrl: 'https://platform.example/code', sessions: [
        row({ machineId: 'm1', machine: 'box', id: 's1', title: 'Fix the build' }),
        row({ machineId: 'm2', machine: 'laptop', id: 's2', title: 'On the laptop' }),
      ] }
        : url === '/api/chat/pwcode/here' ? { ...here, chats }
        : url.startsWith('/api/code-sessions/item') ? { session: row({ machineId: url.includes('m2') ? 'm2' : 'm1', machine: url.includes('m2') ? 'laptop' : 'box', id: url.includes('s2') ? 's2' : 's1', title: 't' }), history: [], earlier: 0 }
        : url === '/api/chat/pwcode/continue' ? (confirmed ? { conversationId: 'c-new' } : { confirm: 'This session is open on laptop.example. Continuing it here takes it over from there.' })
        : {}
      if (refuse && url === '/api/chat/pwcode/continue' && confirmed) {
        return new Response(JSON.stringify({ error: refuse }), { status: 409, headers: { 'Content-Type': 'application/json' } })
      }
      return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }))
  }

  it('asks first, then opens the new chat, answered by pw code', async () => {
    serve()
    render(<CodeView />)
    fireEvent.click(await screen.findByText('Fix the build'))
    const go = await screen.findByRole('button', { name: 'Continue in Studio' })
    await waitFor(() => expect(go).toBeEnabled())
    fireEvent.click(go)
    expect(await screen.findByText(/takes it over from there/)).toBeInTheDocument()
    expect(calls).toContain('POST /api/chat/pwcode/continue {"machine":"m1","session":"s1"}')
    fireEvent.click(screen.getAllByRole('button', { name: 'Continue in Studio' }).at(-1)!)
    await waitFor(() => expect(location.hash).toBe('#chat=c-new'))
    expect(calls).toContain('POST /api/chat/pwcode/continue {"machine":"m1","session":"s1","confirmed":true}')
    expect(getEngine('c-new')).toBe('pwcode')
  })

  it('says in the dialog why the Studio could not continue, and stays on the session', async () => {
    location.hash = '#view=code'
    serve([], 'Your pw code on box did not answer.')
    render(<CodeView />)
    fireEvent.click(await screen.findByText('Fix the build'))
    const go = await screen.findByRole('button', { name: 'Continue in Studio' })
    await waitFor(() => expect(go).toBeEnabled())
    fireEvent.click(go)
    expect(await screen.findByText(/takes it over from there/)).toBeInTheDocument()
    fireEvent.click(screen.getAllByRole('button', { name: 'Continue in Studio' }).at(-1)!)
    expect(await screen.findByText('Your pw code on box did not answer.')).toBeInTheDocument()
    expect(location.hash).toBe('#view=code')
    expect(screen.getByText(/takes it over from there/)).toBeInTheDocument()
  })

  it('opens the session a chat links back to', async () => {
    expect(sessionInHash('#view=code?machine=m1&session=s1')).toEqual({ machine: 'm1', session: 's1' })
    expect(sessionInHash('#view=code')).toBeNull()
    expect(sessionInHash('#view=search?machine=m1&session=s1')).toBeNull()
    serve([{ session: 's1', conversationId: 'c-old', continued: true }])
    location.hash = '#view=code?machine=m1&session=s1'
    render(<CodeView />)
    expect(await screen.findByRole('button', { name: 'Open the Studio chat' })).toBeInTheDocument()
    expect(calls.some(c => c.startsWith('GET /api/code-sessions/item?machine=m1&id=s1'))).toBe(true)
  })

  it('keeps a session on another machine read only, and opens a chat that continues one', async () => {
    serve([{ session: 's1', conversationId: 'c-old', continued: true }])
    render(<CodeView />)
    expect(await screen.findByText(/box · in a Studio chat/)).toBeInTheDocument()
    fireEvent.click(screen.getByText('On the laptop'))
    const go = await screen.findByRole('button', { name: 'Continue in Studio' })
    await waitFor(() => expect(screen.getByText(/so it is read only here/)).toBeInTheDocument())
    expect(go).toBeDisabled()
    fireEvent.click(screen.getByText('Fix the build'))
    fireEvent.click(await screen.findByRole('button', { name: 'Open the Studio chat' }))
    expect(location.hash).toBe('#chat=c-old')
    expect(calls.some(c => c.startsWith('POST'))).toBe(false)
  })
})

describe('The open session', () => {
  afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers() })
  const json = (b: unknown) => new Response(JSON.stringify(b), { status: 200, headers: { 'Content-Type': 'application/json' } })
  const said = (text: string) => [{ kind: 'assistant', text }]

  it('shows the session last chosen when an earlier, slower read comes back after it', async () => {
    let slow: (r: Response) => void = () => {}
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url === '/api/code-sessions') return json({ machines: [], codeUrl: '', sessions: [row({ id: 'big', title: 'Big one' }), row({ id: 'small', title: 'Small one' })] })
      if (url.includes('id=big')) return new Promise<Response>(res => { slow = res })
      if (url.includes('id=small')) return json({ session: row({ id: 'small', title: 'Small one' }), history: said('From the small one'), earlier: 0 })
      return json({ available: false, chats: [] })
    }))
    render(<CodeView />)
    fireEvent.click(await screen.findByText('Big one'))
    fireEvent.click(screen.getByText('Small one'))
    expect(await screen.findByText('From the small one')).toBeInTheDocument()
    slow(json({ session: row({ id: 'big', title: 'Big one' }), history: said('From the big one'), earlier: 0 }))
    await new Promise(r => setTimeout(r, 20))
    expect(screen.queryByText('From the big one')).toBeNull()
    expect(screen.getByText('From the small one')).toBeInTheDocument()
  })

  it('reads a session opened while ready again once the list says it moved on', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true })
    let status = 'idle', reply = 'First reply'
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      if (url === '/api/code-sessions') return json({ machines: [], codeUrl: '', sessions: [row({ id: 's', title: 'Session', status, updatedAt: status === 'idle' ? '2026-11-21T10:00:00Z' : '2026-11-21T10:05:00Z' })] })
      if (url.startsWith('/api/code-sessions/item')) return json({ session: row({ id: 's', title: 'Session', status }), history: said(reply), earlier: 0 })
      return json({ available: false, chats: [] })
    }))
    const { container } = render(<CodeView />)
    fireEvent.click(await screen.findByText('Session'))
    expect(await screen.findByText('First reply')).toBeInTheDocument()
    // A message from somewhere else: the list sees it working.
    status = 'running'; reply = 'Working on it'
    await vi.advanceTimersByTimeAsync(20_000)
    expect(await screen.findByText('Working on it')).toBeInTheDocument()
    expect(container.querySelector('.code-status')?.textContent).toBe('Working')
    // It keeps being read while it works, and its last turn shows once done.
    status = 'idle'; reply = 'All done'
    await vi.advanceTimersByTimeAsync(5_000)
    expect(await screen.findByText('All done')).toBeInTheDocument()
  })

  it('knows when the reader is at the end', () => {
    const el = { scrollHeight: 1000, clientHeight: 400, scrollTop: 600 } as HTMLElement
    expect(isAtEnd(el)).toBe(true)
    expect(isAtEnd({ ...el, scrollTop: 300 } as HTMLElement)).toBe(false)
  })
})
