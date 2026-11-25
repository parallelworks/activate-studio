import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { sessionGroupsPreference } from '@parallelworks/code'
import { pagePath } from '../src/code/api'
import { CodePage } from '../src/code/CodePage'
import { PageHeadSlot } from '../src/pageHead'
import { continuable, sessionInHash, type Here } from '../src/code/openInStudio'

// The session view's client takes the page's fetch as it loads.
vi.hoisted(() => {
  const real = globalThis.fetch
  const current = ((...a: Parameters<typeof fetch>) => (globalThis.fetch === current ? real(...a) : globalThis.fetch(...a))) as typeof fetch
  globalThis.fetch = current
})

// A checkbox forwards its press to its hidden input as a PointerEvent, which
// jsdom does not define.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const machine = { id: 'm1', name: 'box', kind: 'existing', status: 'connected', remoteStart: true, directories: true, claudeCodeAllowed: false, hostname: 'box.example', daemonId: 'd1' }
const other = { ...machine, id: 'm2', name: 'gpu', hostname: 'gpu.example', daemonId: 'd2' }
const now = new Date().toISOString()
const session = (o: Record<string, unknown>) => ({ machineId: 'm1', id: 'x', name: 't', status: 'idle', workspace: '/home/alex/solver', createdAt: now, updatedAt: now, messageCount: 1, ...o })

interface Sent { method: string; path: string; body: Record<string, unknown> | null }

/** The Studio's routes the page calls, answered as the Studio would. */
function studio(over: { here?: Partial<Here>; sessions?: unknown[]; machines?: unknown[]; where?: unknown; delegated?: unknown[]; continueAnswer?: (body: Record<string, unknown>) => unknown; toolsMachines?: unknown[]; setup?: unknown; setupStatus?: number; create?: () => Promise<Response>; createOther?: () => Promise<Response>; goal?: string } = {}) {
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
      case '/api/mcp/bridge/remove': return json({ removed: true, rules: 2 })
      case '/api/mcp/bridge/prefs': return json({ ok: true })
      case '/api/code-page/machines/m1/directories': return json({ path: '/home/alex', home: '/home/alex', entries: [] })
      case '/api/code-page/machines/m2/sessions':
        if (over.createOther) return over.createOther()
        return json({ id: 'new2', machineId: 'm2', name: '', status: 'working', workspace: body?.workspace, createdAt: now, updatedAt: now }, 201)
      case '/api/code-page/machines/m1/sessions':
        if (over.create) return over.create()
        return json({ id: 'new1', machineId: 'm1', name: '', status: 'working', workspace: body?.workspace, createdAt: now, updatedAt: now, conversationId: 'c1', shownOn: 'studio' }, 201)
      case '/api/code/machines/studio/sessions/new1/plan': return json({ action: 'turn' })
      case '/api/code/machines/studio/sessions/new1/goal': return over.goal ? json({ detail: over.goal }, 409) : json({ output: 'Goal set.' })
      case '/api/code/machines/studio/sessions/new1/images': return json({ path: '/cache/image-1.png' })
      case '/api/code/sessions': return json({ machines: [], sessions: [] })
      default:
        if (url.pathname.startsWith('/api/code-page/machines/')) return json({})
        return json({})
    }
  }))
  return sent
}

/** Something a test lets go of. */
const hold = () => {
  let release = () => {}
  const held = new Promise<void>(r => { release = r })
  return { release, held }
}

/** A session's row in the list beside the start. */
const listed = async (name: string) => {
  await screen.findAllByText(name)
  const row = screen.getAllByTestId('agent-session-row').find(r => r.textContent?.includes(name))
  if (!row) throw new Error(`no row for ${name}`)
  return row
}

/** Continues a session in a Studio chat, from its row's menu. */
const continueFrom = async (name: string, item = 'Continue in a Studio chat') => {
  fireEvent.contextMenu(await listed(name))
  fireEvent.click(await screen.findByRole('menuitem', { name: item }))
}
const pane = () => screen.findByTestId('code-session')

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
  it('opens a session from the list in the page, with the list beside it, and goes to no chat', async () => {
    const sent = studio()
    render(<CodePage />)
    fireEvent.click(await listed('mesh refinement'))
    const open = await pane()
    expect(open).toHaveTextContent('mesh refinement')
    expect(open).toHaveTextContent('box')
    expect(location.hash).toBe('#view=code?machine=m1&session=s1')
    expect(sent.some(s => s.path === '/api/chat/pwcode/continue')).toBe(false)
    await waitFor(() => expect(sent.some(s => s.path === '/api/code/sessions?open=studio%2Fs1')).toBe(true))
    await waitFor(() => expect(sent.some(s => s.path.startsWith('/api/code/machines/studio/sessions/s1'))).toBe(true))
    // Another from the list, which is still there; then back.
    fireEvent.click(await listed('pump curves'))
    await waitFor(() => expect(screen.getByTestId('code-session')).toHaveTextContent('pump curves'))
    expect(location.hash).toBe('#view=code?machine=m1&session=s2')
    act(() => { location.hash = '#view=code?machine=m1&session=s1'; window.dispatchEvent(new HashChangeEvent('hashchange')) })
    await waitFor(() => expect(screen.getByTestId('code-session')).toHaveTextContent('mesh refinement'))
    fireEvent.click(screen.getByTestId('code-session-close'))
    await waitFor(() => expect(screen.queryByTestId('code-session')).toBeNull())
    expect(location.hash).toBe('#view=code')
  })

  it('opens a session on another machine through ACTIVATE, and one a chat holds where the chat has it', async () => {
    const sent = studio({ machines: [machine, other], sessions: [session({ id: 's4', machineId: 'm2', name: 'far one' }), session({ id: 's5', machineId: 'm2', name: 'far chat' })],
      here: { relay: true, chats: [{ session: 's5', conversationId: 'c5', machine: 'studio' }] } })
    render(<CodePage />)
    fireEvent.click(await listed('far one'))
    expect(within(await pane()).getByTestId('code-continue')).toHaveTextContent('Continue in a Studio chat')
    await waitFor(() => expect(sent.some(s => s.path.startsWith('/api/code/machines/m2/sessions/s4'))).toBe(true))
    fireEvent.click(await listed('far chat'))
    await waitFor(() => expect(screen.getByTestId('code-continue')).toHaveTextContent('Open in its Studio chat'))
    await waitFor(() => expect(sent.some(s => s.path.startsWith('/api/code/machines/studio/sessions/s5'))).toBe(true))
  })

  it('shows an archived session and says how to go on with it', async () => {
    location.hash = '#view=code?machine=m1&session=s6'
    studio({ sessions: [session({ id: 's6', name: 'old one', archivedAt: now })] })
    render(<CodePage />)
    const open = await pane()
    expect(open).toHaveTextContent('This session is archived.')
    expect(within(open).getByTestId('code-continue')).toBeDisabled()
  })

  it('lists the sessions as PW Code on the web lists them and continues one in a Studio chat, asking first', async () => {
    const sent = studio()
    render(<CodePage />)
    await listed('pump curves')
    fireEvent.click(await listed('mesh refinement'))
    fireEvent.click(await screen.findByTestId('code-continue'))
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
    await continueFrom('mesh refinement')
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
    await continueFrom('mesh refinement')
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
    await continueFrom('mesh refinement')
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).queryByTestId('studio-tools-tick')).toBeNull()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Continue in Studio' }))
    await waitFor(() => expect(location.hash).toBe('#chat=c3'))
    expect(sessionStorage.getItem('ade-studio-tools-auto:c3')).toBe('1')
    sessionStorage.clear()
  })

  const own = { ...machine, remoteStart: false }
  const START = '/api/code-page/machines/m2/sessions'
  /** The page with gpu chosen to start on, and what the Studio knows of its tools. */
  const onGpu = (tools: Record<string, unknown>, over: Parameters<typeof studio>[0] = {}, gpu: Record<string, unknown> = {}) => {
    cleanup()
    location.hash = '#view=code'
    const sent = studio({ machines: [own, { ...other, ...gpu }], sessions: [session({ id: 's1', name: 'mesh refinement' }), session({ id: 's4', machineId: 'm2', name: 'far one', workspace: '/data/runs' })], toolsMachines: [{ machine: 'gpu', machineId: 'm2', ...tools }], ...over })
    render(<CodePage />)
    return sent
  }
  const startOne = async () => {
    const box = await screen.findByRole('textbox', { name: /message|task|describe/i }).catch(() => document.querySelector('textarea')!)
    fireEvent.change(box, { target: { value: 'go' } })
    await waitFor(() => expect(screen.getByTestId('agents-start-button')).toBeEnabled())
    fireEvent.click(screen.getByTestId('agents-start-button'))
  }
  const toolsMark = () => screen.findByTestId('machine-tools-button')

  it('has no line of text or row of buttons over the start: they are in the header and on the machine', async () => {
    for (const tools of [{ state: 'set', available: true }, { state: 'command', command: 'pw code mcp add x', why: 'the Studio cannot open a terminal on it.' }, { state: 'unavailable', why: 'pw ssh does not reach gpu.' }]) {
      onGpu(tools)
      await toolsMark()
      const main = document.querySelector('.code-start')!
      expect(main.textContent).not.toMatch(/Studio's tools|pw ssh|terminal/)
    }
    const head = document.querySelector('.page-head-actions')!
    for (const name of ['New session', 'How sessions appear']) expect(within(head as HTMLElement).getByRole('button', { name })).toHaveTextContent('')
    expect(await within(head as HTMLElement).findByRole('link', { name: 'Open in ACTIVATE' })).toHaveAttribute('href', 'https://platform.example/code')
  })

  it('draws the controls in the page header where there is one', async () => {
    studio()
    render(<><header data-testid="head"><PageHeadSlot /></header><CodePage /></>)
    expect(await within(screen.getByTestId('head')).findByTestId('agents-new-session')).toBeInTheDocument()
    expect(screen.getAllByTestId('agents-new-session').length).toBe(1)
  })

  it('marks each machine in the menu and beside the one chosen', async () => {
    const cpu = { ...other, id: 'm3', name: 'cpu', hostname: 'cpu.example', daemonId: 'd3' }
    studio({ machines: [machine, other, cpu], toolsMachines: [{ state: 'offer', machine: 'gpu', machineId: 'm2', available: true }, { state: 'set', machine: 'cpu', machineId: 'm3' }] })
    render(<CodePage />)
    // The Studio's own always has them.
    fireEvent.click(await screen.findByTestId('agents-machine-chip'))
    fireEvent.click(await screen.findByRole('menuitemcheckbox', { name: /^box/ }).catch(() => screen.findByRole('menuitem', { name: /^box/ })))
    expect((await toolsMark())).toHaveAccessibleName("pw code on box has this Studio's tools")
    fireEvent.click(screen.getByTestId('agents-machine-chip'))
    const row = async (name: RegExp) => (await screen.findByText(name, { selector: '[role^="menuitem"] *' })).closest('[role^="menuitem"]') as HTMLElement
    for (const [name, on] of [[/^box$/, 'true'], [/^gpu$/, 'false'], [/^cpu$/, 'true']] as const) {
      const r = await row(name)
      expect(within(r).getByTestId('tools-mark')).toHaveAttribute('data-on', on)
      expect(r).toHaveTextContent(on === 'true' ? "with this Studio's tools" : "without this Studio's tools")
    }
  })

  it('gives a computer that connected itself its one command, and nothing more', async () => {
    onGpu({ state: 'command', command: "pw code mcp add --scope user studio -- sh -c 'x'", uncommand: 'pw code mcp remove --scope user studio', why: 'gpu is a computer that connected itself to ACTIVATE.' }, {}, { kind: 'local' })
    fireEvent.click(await toolsMark())
    const tip = await screen.findByTestId('machine-tools-popover')
    expect(tip.querySelectorAll('p').length).toBe(1)
    expect(tip).toHaveTextContent("Run this once on gpu to give its pw code this Studio's tools:pw code mcp add --scope user studio -- sh -c 'x'")
    expect(within(tip).getAllByRole('button').map(b => b.getAttribute('aria-label'))).toEqual(['Copy command'])
    expect(screen.queryByTestId('machine-tools-choice')).toBeNull()
  })

  it('asks once, ticked, as the first session starts on a machine it can add them to', async () => {
    const reply = () => new Response(JSON.stringify({ id: 'new2', machineId: 'm2', status: 'working', createdAt: now, updatedAt: now, conversationId: 'c2', shownOn: 'm2' }), { status: 201, headers: { 'Content-Type': 'application/json' } })
    let sent = onGpu({ state: 'offer', available: true, allows: ['list_kb_dir', 'search_kb'] }, { createOther: async () => reply() })
    const choice = await screen.findByTestId('machine-tools-choice')
    expect(choice).toHaveTextContent("Add the Studio's tools to gpu")
    expect(within(choice).getByRole('checkbox')).toBeChecked()
    await startOne()
    await waitFor(() => expect(sent.find(s => s.path === START)?.body).toMatchObject({ prompt: 'go', studioTools: true }))
    expect(sent.some(s => s.path === '/api/mcp/bridge/setup')).toBe(false)
    // Unticked: kept, and sent with the start.
    sent = onGpu({ state: 'offer', available: true }, { createOther: async () => reply() })
    fireEvent.click(within(await screen.findByTestId('machine-tools-choice')).getByRole('checkbox'))
    await startOne()
    await waitFor(() => expect(sent.find(s => s.path === START)?.body).toMatchObject({ studioTools: false }))
    // Chosen before, said no to, there already, or given by the machine: not asked.
    for (const tools of [{ state: 'offer', available: true, chosen: true }, { state: 'declined', available: true }, { state: 'set', available: true }, { state: 'offer', attach: true }, { state: 'unavailable', why: 'x' }]) {
      sent = onGpu(tools, { createOther: async () => reply() })
      await toolsMark()
      expect(screen.queryByTestId('machine-tools-choice')).toBeNull()
      await startOne()
      await waitFor(() => expect(sent.find(s => s.path === START)).toBeTruthy())
      expect(sent.find(s => s.path === START)!.body).not.toHaveProperty('studioTools')
    }
  })

  it('never asks about a machine the page does not list as connected', async () => {
    onGpu({ state: 'offer', available: true }, {}, { status: 'unreachable' })
    await listed('mesh refinement')
    await new Promise(r => setTimeout(r, 50))
    expect(screen.queryByTestId('machine-tools-choice')).toBeNull()
  })

  it('says it is adding them while the session starts', async () => {
    onGpu({ state: 'offer', available: true, chosen: true }, { createOther: () => new Promise(() => {}) })
    await toolsMark()
    await startOne()
    expect(await screen.findByTestId('code-starting')).toHaveTextContent("Adding the Studio's tools, then starting on gpu")
  })

  it('checks and removes them from the machine\'s menu, once asked', async () => {
    const sent = onGpu({ state: 'set', available: true, allows: ['search_kb'] })
    await toolsMark()
    fireEvent.click(screen.getByTestId('agents-machine-chip'))
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Check' }))
    await waitFor(() => expect(sent.find(s => s.path === '/api/mcp/bridge/setup')?.body).toEqual({ machineId: 'm2', check: true }))
    fireEvent.click(screen.getByTestId('agents-machine-chip'))
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Remove' }))
    const dialog = await screen.findByRole('dialog').catch(() => screen.findByRole('alertdialog'))
    expect(dialog).toHaveTextContent('with the rules that let the ones that only read run without asking')
    expect(sent.some(s => s.path === '/api/mcp/bridge/remove')).toBe(false)
    fireEvent.click(within(dialog).getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(sent.find(s => s.path === '/api/mcp/bridge/remove')?.body).toEqual({ machineId: 'm2' }))
  })

  it('adds them from the menu, saying what that allows, and says why it could not at a start', async () => {
    const sent = onGpu({ state: 'offer', available: true, chosen: true, held: 'pw there is signed in as someone else.', allows: ['list_kb_dir', 'search_kb'] })
    fireEvent.click(await toolsMark())
    expect(await screen.findByTestId('machine-tools-popover')).toHaveTextContent("pw code on gpu does not have this Studio's tools: pw there is signed in as someone else.")
    fireEvent.click(within(screen.getByTestId('machine-tools-popover')).getByRole('button', { name: 'Add' }))
    expect(await screen.findByTestId('studio-tools-allows')).toHaveTextContent('as they only read: list_kb_dir, search_kb.')
    const dialog = screen.getByTestId('studio-tools-allows').closest('[role$="dialog"]') as HTMLElement
    fireEvent.click(within(dialog).getByRole('button', { name: 'Add' }))
    await waitFor(() => expect(sent.find(s => s.path === '/api/mcp/bridge/setup')?.body).toEqual({ machineId: 'm2' }))
  })

  it('says read-only mode does not run them on another machine, where the mark says how it stands', async () => {
    for (const has of [{ state: 'set' }, { state: 'offer', attach: true }]) {
      onGpu(has, {}, { remoteMaxPermissionMode: 'read-only' })
      fireEvent.click(await toolsMark())
      expect(await screen.findByTestId('machine-tools-mode')).toHaveTextContent('In read-only mode it does not run them for a chat here.')
    }
  })

  it('shows each machine\'s own icon in the menu, the list and the start', async () => {
    const linux = { ...own, kind: 'local', os: 'linux' }
    studio({ machines: [linux, { ...other, schedulerType: 'slurm' }], sessions: [session({ id: 's1', name: 'mesh refinement' }), session({ id: 's4', machineId: 'm2', name: 'far one' })] })
    render(<CodePage />)
    const src = (el: Element | null | undefined) => [...(el?.querySelectorAll('img') ?? [])].map(i => decodeURIComponent(i.getAttribute('src') ?? '').replace('/api/code-page/icon?path=/assets/images/icons/', ''))
    expect(src(await listed('mesh refinement'))).toEqual(['compute/penguin.png'])
    expect(src(await listed('far one'))).toEqual(['compute/slurm.png', 'compute/slurm-dark.png'])
    expect(src(await screen.findByTestId('agents-machine-chip'))).toEqual(['compute/slurm.png', 'compute/slurm-dark.png'])
    // One that does not load becomes the plain glyph.
    fireEvent.error(screen.getByTestId('agents-machine-chip').querySelector('img')!)
    expect(screen.getByTestId('agents-machine-chip').querySelector('img')).toBeNull()
    expect(screen.getByTestId('agents-machine-chip').querySelector('svg.code-machine-icon')).not.toBeNull()
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
    expect((await screen.findAllByRole('menuitem')).map(m => m.textContent)).toEqual(expect.arrayContaining(['Open', 'Continue in a Studio chat', 'Open in ACTIVATE']))
  })

  it('opens the chat that already continues a session without asking', async () => {
    studio({ here: { chats: [{ session: 's2', conversationId: 'c9' }] } })
    render(<CodePage />)
    expect(await screen.findByText('in a chat')).toBeInTheDocument()
    await continueFrom('pump curves', 'Open in its Studio chat')
    await waitFor(() => expect(location.hash).toBe('#chat=c9'))
  })

  it('shows why a session cannot be continued here, with what still works, rather than asking', async () => {
    const sent = studio({ sessions: [session({ id: 's3', machineId: 'm2', name: 'far away' })], machines: [machine, other], here: { relayReason: 'no relay for you' } })
    render(<CodePage />)
    fireEvent.click(await listed('far away'))
    const card = await screen.findByTestId('code-linked-session')
    expect(card).toHaveTextContent(/runs on gpu.*no relay for you/)
    expect(within(card).getByRole('button', { name: 'Continue in a Studio chat' })).toBeDisabled()
    expect(sent.some(s => s.path === '/api/chat/pwcode/continue')).toBe(false)
  })

  it('shows the session a link names, with its delegated work and the ways to open it', async () => {
    location.hash = '#view=code?session=s1'
    studio({
      delegated: [{ site: 'local', session: 's1', workspace: '/kb/agents/reviewer/q3', folder: 'agents/reviewer/q3', instructions: 'reviewer', objective: 'Summarize', started: now, state: 'finished', files: ['agents/reviewer/q3/summary.md'] }],
    })
    render(<CodePage />)
    const card = await pane()
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
    for (const id of ['agents-machine-chip', 'machine-tools-button', 'agents-new-session', 'agents-how-button', 'code-sidebar']) expect(screen.queryByTestId(id)).toBeNull()
    expect(screen.queryByRole('link', { name: 'Open in ACTIVATE' })).toBeNull()
  })

  it('shows no start or controls before it knows whose sessions they are', async () => {
    const { release, held } = hold()
    const sent = studio()
    const answer = globalThis.fetch
    vi.stubGlobal('fetch', async (input: RequestInfo | URL, init?: RequestInit) => {
      if (String(input).endsWith('/api/code-page/where')) await held
      return answer(input, init)
    })
    render(<CodePage />)
    await waitFor(() => expect(sent.some(x => x.path.startsWith('/api/chat/pwcode/here'))).toBe(true))
    await act(async () => { await new Promise(r => setTimeout(r, 30)) })
    for (const id of ['agents-machine-chip', 'agents-new-session', 'agents-start-button']) expect(screen.queryByTestId(id)).toBeNull()
    release()
    expect(await screen.findByTestId('agents-new-session')).toBeInTheDocument()
  })

  it('with no platform, says nothing of tools on this computer, and why a session is not continued', async () => {
    location.hash = '#view=code?session=s1'
    studio({
      where: { local: true, codeUrl: '' },
      machines: [{ ...machine, id: 'local', name: 'This computer', kind: 'local' }],
      sessions: [session({ id: 's1', name: 'mesh refinement', machineId: 'local' })],
      here: { available: false, reason: 'pw code needs ACTIVATE.', relay: false, relayReason: 'The Studio is not connected to an ACTIVATE platform.', hostname: undefined, daemonId: undefined },
    })
    render(<CodePage />)
    const card = await screen.findByTestId('code-linked-session')
    await waitFor(() => expect(card).toHaveTextContent('The Studio is not connected to an ACTIVATE platform. You can still answer what it waits on here, and open it in a terminal.'))
    expect(card).not.toHaveTextContent('open it in ACTIVATE')
    expect(await screen.findByTestId('agents-machine-chip')).toHaveTextContent('This computer')
    expect(screen.queryByTestId('machine-tools-button')).toBeNull()
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

  const start = async (text: string) => {
    fireEvent.change(await screen.findByRole('textbox', { name: /what should it work on|describe|start/i }), { target: { value: text } })
    const button = await screen.findByTestId('agents-start-button')
    await waitFor(() => expect(button).not.toBeDisabled())
    fireEvent.click(button)
  }

  it('starts a session on the machine and folder chosen, held to read-only by default, and opens it in the page', async () => {
    const sent = studio()
    render(<CodePage />)
    await start('refine the inlet mesh')
    await waitFor(() => expect(sent.some(s => s.method === 'POST' && s.path === '/api/code-page/machines/m1/sessions')).toBe(true))
    const create = sent.find(s => s.method === 'POST' && s.path === '/api/code-page/machines/m1/sessions')!
    expect(create.body).toMatchObject({ workspace: '/home/alex/solver' })
    // Open before any listing has it, with its message, where the Studio
    // said its view is; and no chat is gone to, though one was made for it.
    const open = await pane()
    expect(open).toHaveTextContent('refine the inlet mesh')
    expect(location.hash).toBe('#view=code?machine=m1&session=new1')
    await waitFor(() => expect(sent.some(s => s.path.startsWith('/api/code/machines/studio/sessions/new1'))).toBe(true))
    expect(sent.some(s => s.path === '/api/chat/pwcode/continue')).toBe(false)
    fireEvent.click(within(open).getByRole('button', { name: 'Open in its Studio chat' }))
    await waitFor(() => expect(location.hash).toBe('#chat=c1'))
  })

  it('says where a session is starting from the first moment, and another can be opened meanwhile', async () => {
    let made: (r: Response) => void = () => undefined
    studio({ create: () => new Promise<Response>(r => { made = r }) })
    render(<CodePage />)
    await listed('pump curves')
    await start('refine the inlet mesh')
    const starting = await screen.findByTestId('code-starting')
    expect(starting).toHaveTextContent('Starting on box, in /home/alex/solver')
    expect(starting).toHaveTextContent('refine the inlet mesh')
    fireEvent.click(await listed('pump curves'))
    expect(await pane()).toHaveTextContent('pump curves')
    expect(screen.queryByTestId('code-starting')).toBeNull()
    // It starts where the person is not looking: they stay where they are.
    await act(async () => { made(new Response(JSON.stringify({ id: 'new2', machineId: 'm1', name: '', status: 'working', workspace: '/home/alex/solver', createdAt: now, updatedAt: now, conversationId: 'c2', shownOn: 'studio' }), { status: 201, headers: { 'Content-Type': 'application/json' } })); await new Promise(r => setTimeout(r, 20)) })
    expect(screen.getByTestId('code-session')).toHaveTextContent('pump curves')
  })

  // Sent as a message, the machine refuses /goal and /plan: the package makes
  // the session empty and runs the command, then sends what /plan hands back.
  it('starts a session with /plan, sent as its chat sends it', async () => {
    const sent = studio()
    render(<CodePage />)
    await start('/plan refine the inlet mesh')
    expect(await pane()).toBeInTheDocument()
    const posts = sent.filter(s => s.method === 'POST' && /sessions/.test(s.path))
    expect(posts.map(s => s.path)).toEqual(['/api/code-page/machines/m1/sessions', '/api/code/machines/studio/sessions/new1/plan', '/api/code/machines/studio/sessions/new1/messages'])
    expect(posts[0].body).not.toHaveProperty('prompt')
    expect(posts[2].body).toMatchObject({ text: 'refine the inlet mesh' })
  })

  it('leaves no session and no chat behind a command that was refused', async () => {
    const sent = studio({ goal: 'A goal needs a newer pw code.' })
    render(<CodePage />)
    await start('/goal finish the sweep')
    expect(await screen.findByText(/A goal needs a newer pw code/)).toBeInTheDocument()
    await waitFor(() => expect(sent.filter(s => s.method === 'DELETE').map(s => s.path)).toEqual(['/api/code-page/machines/m1/sessions/new1', '/api/chat/conversations/c1']))
    expect(await screen.findByDisplayValue('/goal finish the sweep')).toBeVisible()
    expect(screen.queryByTestId('code-session')).toBeNull()
  })

  it('takes images with a first message, saved with the session it makes', async () => {
    vi.stubGlobal('createImageBitmap', async () => ({ width: 10, height: 10, close: () => {} }))
    URL.createObjectURL = () => 'blob:preview'
    URL.revokeObjectURL = () => {}
    const sent = studio()
    const { container } = render(<CodePage />)
    await listed('pump curves')
    const picker = await waitFor(() => { const f = container.querySelector<HTMLInputElement>('.code-start input[type="file"]'); expect(f).toBeTruthy(); return f! })
    expect(picker.accept).toBe('image/png,image/jpeg,image/gif,image/webp')
    fireEvent.change(picker, { target: { files: [new File(['png'], 'shot.png', { type: 'image/png' })] } })
    expect(await screen.findByRole('img', { name: '[Image #1]' })).toBeInTheDocument()
    await start('what is wrong here')
    expect(await pane()).toBeInTheDocument()
    const posts = sent.filter(s => s.method === 'POST' && /sessions/.test(s.path))
    expect(posts.map(s => s.path)).toEqual(['/api/code-page/machines/m1/sessions', '/api/code/machines/studio/sessions/new1/images', '/api/code/machines/studio/sessions/new1/messages'])
    expect(posts[2].body).toMatchObject({ text: 'what is wrong here [Image #1]', images: [{ placeholder: '[Image #1]', path: '/cache/image-1.png' }] })
  })

  it('puts a new session in the group chosen', async () => {
    localStorage.setItem(`studio.code.${sessionGroupsPreference('studio').key}`, JSON.stringify([{ id: 'g1', name: 'inlet work' }]))
    const sent = studio({ machines: [{ ...machine, sessionGroups: true }] })
    render(<CodePage />)
    fireEvent.click(await screen.findByTestId('agents-group-chip'))
    fireEvent.click(await screen.findByRole('menuitemcheckbox', { name: 'inlet work' }))
    await start('refine the inlet mesh')
    expect(await pane()).toBeInTheDocument()
    await waitFor(() => expect(sent.find(s => s.method === 'PUT')).toMatchObject({ path: '/api/code-page/machines/m1/sessions/new1/group', body: { group: 'g1' } }))
  })

  it('holds a message for a workspace that is starting, and gives it back', async () => {
    const workspace = { ...machine, id: 'w1', name: 'workspace', kind: 'workspace', status: 'starting', home: '/home/alex' }
    const sent = studio({ machines: [workspace], sessions: [] })
    render(<CodePage />)
    const box = await screen.findByRole('textbox', { name: 'Describe a task or ask a question' })
    fireEvent.change(box, { target: { value: 'refine the inlet mesh' } })
    fireEvent.click(await screen.findByTestId('agents-start-button'))
    const held = await screen.findByTestId('agents-start-held')
    expect(sent.some(s => s.method === 'POST' && /sessions$/.test(s.path))).toBe(false)
    expect(screen.queryByTestId('code-starting')).toBeNull()
    fireEvent.click(within(held).getByRole('button'))
    expect(await screen.findByDisplayValue('refine the inlet mesh')).toBeVisible()
  })

  it('starts a held message as the workspace answers, and adds nothing there that was not asked about', async () => {
    const workspace = { ...other, name: 'workspace', kind: 'workspace', status: 'starting', home: '/home/alex' }
    const over = { machines: [workspace], sessions: [], toolsMachines: [{ state: 'offer', available: true, machine: 'workspace', machineId: 'm2' }] }
    const sent = studio(over)
    render(<CodePage />)
    const box = await screen.findByRole('textbox', { name: 'Describe a task or ask a question' })
    fireEvent.change(box, { target: { value: 'refine the inlet mesh' } })
    fireEvent.click(await screen.findByTestId('agents-start-button'))
    await screen.findByTestId('agents-start-held')
    over.machines = [{ ...workspace, status: 'connected' }]
    const create = await waitFor(() => {
      const c = sent.find(s => s.method === 'POST' && s.path === '/api/code-page/machines/m2/sessions')
      expect(c).toBeTruthy()
      return c!
    }, { timeout: 12_000 })
    expect(create.body).toMatchObject({ prompt: 'refine the inlet mesh' })
    expect(create.body).not.toHaveProperty('studioTools')
  }, 15_000)

  it('comes back to the message with the reason when a start fails', async () => {
    studio({ create: async () => new Response(JSON.stringify({ detail: 'box did not answer.' }), { status: 502, headers: { 'Content-Type': 'application/json' } }) })
    render(<CodePage />)
    await start('refine the inlet mesh')
    expect(await screen.findByText(/box did not answer/)).toBeInTheDocument()
    expect(screen.queryByTestId('code-starting')).toBeNull()
    expect(screen.queryByTestId('code-session')).toBeNull()
    expect(await screen.findByDisplayValue('refine the inlet mesh')).toBeVisible()
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
