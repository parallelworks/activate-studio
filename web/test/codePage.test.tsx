import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { pagePath } from '../src/code/api'
import { CodePage } from '../src/code/CodePage'
import { continuable, sessionInHash, type Here } from '../src/code/openInStudio'

const machine = { id: 'm1', name: 'box', kind: 'existing', status: 'connected', remoteStart: true, directories: true, claudeCodeAllowed: false, hostname: 'box.example', daemonId: 'd1' }
const other = { ...machine, id: 'm2', name: 'gpu', hostname: 'gpu.example', daemonId: 'd2' }
const now = new Date().toISOString()
const session = (o: Record<string, unknown>) => ({ machineId: 'm1', id: 'x', name: 't', status: 'idle', workspace: '/home/alex/solver', createdAt: now, updatedAt: now, messageCount: 1, ...o })

interface Sent { method: string; path: string; body: Record<string, unknown> | null }

/** The Studio's routes the page calls, answered as the Studio would. */
function studio(over: { here?: Partial<Here>; sessions?: unknown[]; machines?: unknown[]; where?: unknown; delegated?: unknown[]; continueAnswer?: (body: Record<string, unknown>) => unknown; toolsMachines?: unknown[]; setup?: unknown; setupStatus?: number } = {}) {
  const sent: Sent[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(new URL(String(input), 'http://studio.example'), init)
    const url = new URL(req.url, 'http://studio.example')
    const text = req.method === 'GET' ? '' : await req.text()
    const body = text ? JSON.parse(text) as Record<string, unknown> : null
    sent.push({ method: req.method, path: url.pathname + url.search, body })
    const json = (v: unknown, status = 200) => new Response(JSON.stringify(v), { status, headers: { 'Content-Type': 'application/json' } })
    switch (url.pathname) {
      case '/api/code-page/sessions':
        return json({ machines: over.machines ?? [machine], sessions: over.sessions ?? [session({ id: 's1', name: 'mesh refinement' }), session({ id: 's2', name: 'pump curves' })] })
      case '/api/code-page/where': return json(over.where ?? { local: false, codeUrl: 'https://platform.example/code' })
      case '/api/code-page/openai/models': return json({ object: 'list', data: [] })
      case '/api/delegated': return json({ sessions: over.delegated ?? [] })
      case '/api/chat/pwcode/here': return json({ available: true, daemonId: 'd1', hostname: 'box.example', chats: [], ...over.here })
      case '/api/chat/pwcode/continue':
        return json(over.continueAnswer ? over.continueAnswer(body ?? {}) : body?.confirmed ? { conversationId: 'c1' } : { confirm: 'Your messages go to mesh refinement on box.' })
      case '/api/mcp/bridge/machines': return json({ available: true, machines: over.toolsMachines ?? [] })
      case '/api/mcp/bridge/setup': return json(over.setup ?? { done: true, worked: true, changed: true, tools: 40, live: 'next' }, over.setupStatus)
      case '/api/mcp/bridge/prefs': return json({ ok: true })
      case '/api/code-page/machines/m1/directories': return json({ path: '/home/alex', home: '/home/alex', entries: [] })
      case '/api/code-page/machines/m1/sessions':
        return json({ id: 'new1', machineId: 'm1', name: '', status: 'working', workspace: body?.workspace, createdAt: now, updatedAt: now })
      default:
        if (url.pathname.startsWith('/api/code-page/machines/')) return json({})
        return json({})
    }
  }))
  return sent
}

/** A session's row in the list beside the start. */
const listed = async (name: string) => {
  await screen.findAllByText(name)
  const row = screen.getAllByTestId('agent-session-row').find(r => r.textContent?.includes(name))
  if (!row) throw new Error(`no row for ${name}`)
  return row
}

beforeEach(() => {
  location.hash = '#view=code'
  localStorage.clear()
})
afterEach(() => { vi.unstubAllGlobals(); location.hash = '' })

describe('the Code page client', () => {
  it('calls the platform routes PW Code on the web calls, at the Studio', () => {
    expect(pagePath('/api/code/sessions')).toBe('/api/code-page/sessions')
    expect(pagePath('/api/code/machines/m1/sessions/s1/rename')).toBe('/api/code-page/machines/m1/sessions/s1/rename')
    expect(pagePath('/api/openai/v1/models')).toBe('/api/code-page/openai/models')
    expect(pagePath('/api/chat/pwcode/here')).toBe('/api/chat/pwcode/here')
  })

  it('reads the session a link names', () => {
    expect(sessionInHash('#view=code?machine=m1&session=s1')).toEqual({ machine: 'm1', session: 's1' })
    expect(sessionInHash('#view=code?session=s1')).toEqual({ machine: '', session: 's1' })
    expect(sessionInHash('#view=code')).toBeNull()
  })

  it('continues a session on the machine the chat reaches, and through ACTIVATE on the others', () => {
    const here: Here = { available: true, daemonId: 'd1', hostname: 'box.example', chats: [] }
    expect(continuable({ machineId: 'm1', id: 's1' }, [machine, other], here)).toEqual({ ok: true, why: '' })
    expect(continuable({ machineId: 'm2', id: 's1' }, [machine, other], { ...here, relay: true }).relay).toBe(true)
    const no = continuable({ machineId: 'm2', id: 's1' }, [machine, other], { ...here, relayReason: 'the platform does not relay chats.' })
    expect(no.ok).toBe(false)
    expect(no.why).toMatch(/runs on gpu.*the platform does not relay chats\)\. You can still answer/)
    expect(continuable({ machineId: 'm1', id: 's1', archivedAt: now }, [machine], here).why).toMatch(/archived/)
  })
})

describe('the Code page', () => {
  it('lists the sessions as PW Code on the web lists them and opens one in a Studio chat, asking first', async () => {
    const sent = studio()
    render(<CodePage />)
    const row = await listed('mesh refinement')
    await listed('pump curves')
    fireEvent.click(row)
    const dialog = await screen.findByRole('dialog')
    expect(dialog).toHaveTextContent('Your messages go to mesh refinement on box.')
    // Asked, not yet made.
    expect(sent.filter(s => s.path === '/api/chat/pwcode/continue').map(s => s.body)).toEqual([{ machine: 'm1', session: 's1' }])
    fireEvent.click(within(dialog).getByRole('button', { name: 'Continue in Studio' }))
    await waitFor(() => expect(location.hash).toBe('#chat=c1'))
    expect(sent.filter(s => s.path === '/api/chat/pwcode/continue').at(-1)?.body).toEqual({ machine: 'm1', session: 's1', confirmed: true })
  })

  it('offers to add the Studio\'s tools on the session\'s machine as it continues, ticked, and remembers an untick', async () => {
    const tools = { state: 'offer', machine: 'gpu', machineId: 'm2', available: true }
    let sent = studio({ continueAnswer: b => (b.confirmed ? { conversationId: 'c1' } : { confirm: 'This session runs on gpu.', studioTools: tools }) })
    render(<CodePage />)
    fireEvent.click(await listed('mesh refinement'))
    let dialog = await screen.findByRole('dialog')
    const tick = within(dialog).getByTestId('studio-tools-tick') as HTMLInputElement
    expect(tick.checked).toBe(true)
    expect(dialog).toHaveTextContent("Add this Studio's tools to pw code on gpu")
    fireEvent.click(within(dialog).getByRole('button', { name: 'Continue in Studio' }))
    await waitFor(() => expect(location.hash).toBe('#chat=c1'))
    // The chat's card runs the setup as it opens; nothing else is recorded.
    expect(sessionStorage.getItem('ade-studio-tools-auto:c1')).toBe('1')
    expect(sent.some(s => s.path === '/api/mcp/bridge/prefs')).toBe(false)
    sessionStorage.clear()

    // Unticked: not run, and the machine is not offered again.
    cleanup()
    location.hash = '#view=code'
    sent = studio({ continueAnswer: b => (b.confirmed ? { conversationId: 'c2' } : { confirm: 'This session runs on gpu.', studioTools: tools }) })
    render(<CodePage />)
    fireEvent.click(await listed('mesh refinement'))
    dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByTestId('studio-tools-tick'))
    fireEvent.click(within(dialog).getByRole('button', { name: 'Continue in Studio' }))
    await waitFor(() => expect(location.hash).toBe('#chat=c2'))
    await waitFor(() => expect(sent.find(s => s.path === '/api/mcp/bridge/prefs')?.body).toEqual({ machineId: 'm2', add: false }))
    expect(sessionStorage.getItem('ade-studio-tools-auto:c2')).toBeNull()
  })

  it('does not ask again for a machine already set up, and still gives the session in front the tools', async () => {
    studio({ continueAnswer: b => (b.confirmed ? { conversationId: 'c3' } : { confirm: 'This session runs on gpu.', studioTools: { state: 'set', machine: 'gpu', machineId: 'm2' } }) })
    render(<CodePage />)
    fireEvent.click(await listed('mesh refinement'))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).queryByTestId('studio-tools-tick')).toBeNull()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Continue in Studio' }))
    await waitFor(() => expect(location.hash).toBe('#chat=c3'))
    expect(sessionStorage.getItem('ade-studio-tools-auto:c3')).toBe('1')
    sessionStorage.clear()
  })

  it('offers the Studio\'s tools once for a machine that lacks them', async () => {
    const cpu = { ...other, id: 'm3', name: 'cpu', hostname: 'cpu.example', daemonId: 'd3' }
    let sent = studio({ machines: [machine, other, cpu], toolsMachines: [{ state: 'offer', machine: 'gpu', machineId: 'm2' }, { state: 'set', machine: 'box', machineId: 'm1' }] })
    render(<CodePage />)
    const offer = await screen.findByTestId('machine-tools-offer')
    expect(offer).toHaveTextContent('pw code on gpu')
    fireEvent.click(within(offer).getByRole('button', { name: 'Add' }))
    expect(await screen.findByTestId('machine-tools-said')).toHaveTextContent("pw code on gpu now has this Studio's tools (40 of them)")
    expect(sent.find(s => s.path === '/api/mcp/bridge/setup')?.body).toEqual({ machineId: 'm2' })
    // Offered once: not again on the next visit.
    cleanup()
    sent = studio({ machines: [machine, other, cpu], toolsMachines: [{ state: 'offer', machine: 'gpu', machineId: 'm2' }, { state: 'offer', machine: 'cpu', machineId: 'm3' }] })
    render(<CodePage />)
    const next = await screen.findByTestId('machine-tools-offer')
    expect(next).toHaveTextContent('pw code on cpu')
    fireEvent.click(within(next).getByRole('button', { name: 'Not on cpu' }))
    await waitFor(() => expect(sent.find(s => s.path === '/api/mcp/bridge/prefs')?.body).toEqual({ machineId: 'm3', add: false }))
    expect(screen.queryByTestId('machine-tools-offer')).toBeNull()
  })

  // Matthew saw the offer for a cluster that was not connected. Only a
  // machine the page lists as connected is offered, whatever the Studio's
  // own answer says.
  it('offers the tools only on a machine the page shows online', async () => {
    const stopped = { ...other, status: 'unreachable' }
    const noDaemon = { ...other, id: 'm3', name: 'cpu', hostname: 'cpu.example', daemonId: 'd3', status: 'no_daemon' }
    const tpu = { ...other, id: 'm4', name: 'tpu', hostname: 'tpu.example', daemonId: 'd4' }
    studio({ machines: [machine, stopped, noDaemon, tpu], toolsMachines: [
      { state: 'offer', machine: 'gpu', machineId: 'm2' }, { state: 'offer', machine: 'cpu', machineId: 'm3' },
      { state: 'set', machine: 'box', machineId: 'm1' }, { state: 'set', machine: 'tpu', machineId: 'm4' }] })
    render(<CodePage />)
    await listed('mesh refinement')
    await new Promise(r => setTimeout(r, 50))
    expect(screen.queryByTestId('machine-tools-offer')).toBeNull()
  })

  it('offers the tools on the machine chosen on the page, not another', async () => {
    // box is the one machine to start on, so it is chosen; gpu is online too.
    const watchOnly = { ...other, remoteStart: false }
    studio({ machines: [machine, watchOnly], toolsMachines: [
      { state: 'offer', machine: 'gpu', machineId: 'm2' }, { state: 'offer', machine: 'box', machineId: 'm1' }] })
    render(<CodePage />)
    expect(await screen.findByTestId('machine-tools-offer')).toHaveTextContent('pw code on box')
  })

  // A session on another machine never starts silently without the tools:
  // said each time one is chosen to start on, with Add or why not.
  it('says each time that a session on another machine would lack the tools', async () => {
    const own = { ...machine, remoteStart: false }
    const at = (toolsMachines: unknown[]) => { cleanup(); studio({ machines: [own, other], toolsMachines }); render(<CodePage />) }
    for (let visit = 0; visit < 2; visit++) {
      at([{ state: 'offer', machine: 'gpu', machineId: 'm2' }])
      const offer = await screen.findByTestId('machine-tools-offer')
      expect(offer).toHaveTextContent("pw code on gpu does not have this Studio's tools, so a session there works with its own.")
      within(offer).getByRole('button', { name: 'Add' })
    }
    at([{ state: 'unavailable', machine: 'gpu', machineId: 'm2', why: 'pw ssh does not reach gpu.' }])
    expect(await screen.findByTestId('machine-tools-none')).toHaveTextContent('They cannot be added there now: pw ssh does not reach gpu.')
    // Set up there, or attached as a session starts: only that read-only
    // mode, the widest gpu takes here, does not run them.
    for (const has of [{ state: 'set' }, { state: 'offer', attach: true }]) {
      cleanup()
      studio({ machines: [own, { ...other, remoteMaxPermissionMode: 'read-only' }], toolsMachines: [{ ...has, machine: 'gpu', machineId: 'm2' }] })
      render(<CodePage />)
      expect(await screen.findByTestId('machine-tools-mode')).toHaveTextContent("In read-only mode pw code on gpu does not run this Studio's tools for a chat here. In accept edits it asks you before each one.")
      expect(screen.queryByTestId('machine-tools-offer')).toBeNull()
      expect(screen.queryByTestId('machine-tools-none')).toBeNull()
    }
  })

  it('checks and removes the tools on a machine chosen to start on that has them, and says what Add allows', async () => {
    const own = { ...machine, remoteStart: false }
    const sent = studio({ machines: [own, other], toolsMachines: [{ state: 'set', machine: 'gpu', machineId: 'm2' }] })
    render(<CodePage />)
    const line = await screen.findByTestId('machine-tools-set')
    expect(line).toHaveTextContent("pw code on gpu has this Studio's tools.")
    fireEvent.click(within(line).getByRole('button', { name: 'Remove' }))
    within(line).getByRole('button', { name: 'Keep' })
    expect(sent.some(s => s.path === '/api/mcp/bridge/remove')).toBe(false)
    fireEvent.click(within(line).getByRole('button', { name: 'Check' }))
    await screen.findByTestId('machine-tools-said')
    expect(sent.find(s => s.path === '/api/mcp/bridge/setup')?.body).toEqual({ machineId: 'm2', check: true })
    cleanup()
    studio({ machines: [own, other], toolsMachines: [{ state: 'offer', machine: 'gpu', machineId: 'm2', allows: ['list_kb_dir', 'search_kb'] }] })
    render(<CodePage />)
    expect(await screen.findByTestId('studio-tools-allows')).toHaveTextContent('as they only read: list_kb_dir, search_kb.')
  })

  it('says plainly that a machine gone offline is not connected', async () => {
    const cpu = { ...other, id: 'm3', name: 'cpu', hostname: 'cpu.example', daemonId: 'd3' }
    studio({ machines: [machine, other, cpu], toolsMachines: [{ state: 'offer', machine: 'gpu', machineId: 'm2' }],
      setupStatus: 409, setup: { error: 'gpu is not connected. Start it, then add the tools.' } })
    render(<CodePage />)
    fireEvent.click(within(await screen.findByTestId('machine-tools-offer')).getByRole('button', { name: 'Add' }))
    expect(await screen.findByTestId('machine-tools-said')).toHaveTextContent('gpu is not connected. Start it, then add the tools.')
  })

  it('renames and archives from a row menu, as the session actions of the package do', async () => {
    const sent = studio()
    render(<CodePage />)
    const row = await listed('mesh refinement')
    fireEvent.contextMenu(row)
    fireEvent.click(await screen.findByRole('menuitem', { name: /Rename/ }))
    const field = await screen.findByRole('textbox', { name: 'Session name' })
    fireEvent.change(field, { target: { value: 'inlet mesh' } })
    fireEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Rename' }))
    await waitFor(() => expect(sent.find(x => x.path === '/api/code-page/machines/m1/sessions/s1/rename')?.body).toEqual({ name: 'inlet mesh' }))
    fireEvent.contextMenu(await listed('pump curves'))
    fireEvent.click(await screen.findByRole('menuitem', { name: /Archive/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(within(dialog).getByRole('button', { name: /Archive/ }))
    await waitFor(() => expect(sent.some(x => x.method === 'POST' && x.path === '/api/code-page/machines/m1/sessions/s2/archive')).toBe(true))
    // The Studio's own entries lead the menu.
    fireEvent.contextMenu(await listed('pump curves'))
    expect((await screen.findAllByRole('menuitem')).map(m => m.textContent)).toEqual(expect.arrayContaining(['Continue in Studio', 'Open in ACTIVATE']))
  })

  it('opens the chat that already continues a session without asking', async () => {
    studio({ here: { chats: [{ session: 's2', conversationId: 'c9' }] } })
    render(<CodePage />)
    expect(await screen.findByText('in a chat')).toBeInTheDocument()
    fireEvent.click(await listed('pump curves'))
    await waitFor(() => expect(location.hash).toBe('#chat=c9'))
  })

  it('shows why a session cannot be continued here, with what still works, rather than asking', async () => {
    const sent = studio({ sessions: [session({ id: 's3', machineId: 'm2', name: 'far away' })], machines: [machine, other], here: { relayReason: 'no relay for you' } })
    render(<CodePage />)
    fireEvent.click(await listed('far away'))
    const card = await screen.findByTestId('code-linked-session')
    expect(card).toHaveTextContent(/runs on gpu.*no relay for you/)
    expect(within(card).getByRole('button', { name: 'Continue in Studio' })).toBeDisabled()
    expect(sent.some(s => s.path === '/api/chat/pwcode/continue')).toBe(false)
  })

  it('shows the session a link names, with its delegated work and the ways to open it', async () => {
    location.hash = '#view=code?session=s1'
    studio({
      delegated: [{ site: 'local', session: 's1', workspace: '/kb/agents/reviewer/q3', folder: 'agents/reviewer/q3', instructions: 'reviewer', objective: 'Summarize', started: now, state: 'finished', files: ['agents/reviewer/q3/summary.md'] }],
    })
    render(<CodePage />)
    const card = await screen.findByTestId('code-linked-session')
    expect(card).toHaveTextContent('mesh refinement')
    const note = within(card).getByRole('region', { name: 'Delegated work' })
    expect(note).toHaveTextContent('Delegated from a chat, finished, with the instructions reviewer in its AGENTS.md, in the library folder agents/reviewer/q3.')
    expect(within(note).getByRole('link', { name: 'summary.md' })).toHaveAttribute('href', '#open=file:agents/reviewer/q3/summary.md')
    expect(within(card).getByText('Open in ACTIVATE').closest('a')).toHaveAttribute('href', 'https://platform.example/code/m1/s1')
    // The list filters to delegated work.
    fireEvent.click(screen.getByTestId('code-only-delegated'))
    await waitFor(() => expect(screen.getAllByTestId('agent-session-row').map(r => r.textContent)).toEqual([expect.stringContaining('mesh refinement')]))
  })

  it('sends a person with no ACTIVATE sign-in of their own to Settings, Accounts', async () => {
    studio({ where: { local: false, codeUrl: 'https://platform.example/code', signIn: true } })
    render(<CodePage />)
    const notice = await screen.findByTestId('code-sign-in')
    expect(notice).toHaveTextContent('Sign in with ACTIVATE to use this')
    fireEvent.click(within(notice).getByRole('button', { name: 'Open Accounts' }))
    expect(location.hash).toBe('#view=settings:accounts?open=activate')
  })

  it('says a linked session is gone', async () => {
    location.hash = '#view=code?session=gone1'
    studio()
    render(<CodePage />)
    expect(await screen.findByText(/not among your pw code sessions/)).toBeInTheDocument()
  })

  it('offers nothing to open in ACTIVATE where it lists this computer\'s sessions', async () => {
    studio({ where: { local: true, codeUrl: '' }, machines: [{ ...machine, kind: 'local' }] })
    render(<CodePage />)
    await listed('mesh refinement')
    await act(async () => { await new Promise(r => setTimeout(r, 0)) })
    expect(screen.queryByText('Open in ACTIVATE')).toBeNull()
  })

  it('starts a session on the machine and folder chosen, held to read-only by default, and opens it in a chat', async () => {
    const sent = studio()
    render(<CodePage />)
    const input = await screen.findByRole('textbox', { name: /what should it work on|describe|start/i })
    fireEvent.change(input, { target: { value: 'refine the inlet mesh' } })
    const start = await screen.findByTestId('agents-start-button')
    await waitFor(() => expect(start).not.toBeDisabled())
    fireEvent.click(start)
    await waitFor(() => expect(sent.some(s => s.method === 'POST' && s.path === '/api/code-page/machines/m1/sessions')).toBe(true))
    const create = sent.find(s => s.method === 'POST' && s.path === '/api/code-page/machines/m1/sessions')!
    expect(create.body).toMatchObject({ workspace: '/home/alex/solver' })
    // A session started here is the person's: it opens without asking again.
    await waitFor(() => expect(location.hash).toBe('#chat=c1'))
    expect(sent.filter(s => s.path === '/api/chat/pwcode/continue').map(s => s.body)).toEqual([{ machine: 'm1', session: 'new1', confirmed: true }])
  })
})

describe('the Code entry in the menu', () => {
  it('counts the sessions waiting on the person, as PW Code on the web does, leaving out archived ones', async () => {
    const { waitsOnYou } = await import('../src/code/waiting')
    expect(waitsOnYou({ status: 'awaiting_approval' })).toBe(true)
    expect(waitsOnYou({ status: 'idle', approvals: [{}] })).toBe(true)
    expect(waitsOnYou({ status: 'running' })).toBe(false)
    expect(waitsOnYou({ status: 'awaiting_approval', archivedAt: now })).toBe(false)
  })
})
