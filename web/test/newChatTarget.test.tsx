import { useRef } from 'react'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const chat = { isStreaming: false }
vi.mock('@parallelworks/ui/ai', async original => ({ ...(await original<object>()), useChat: () => chat }))

import { createStudioAdapter } from '../src/adapter'
import { NewChatTargetSlot } from '../src/pwcode/NewChatTarget'
import { pickModel, pickedModel } from '../src/pwcode/newChatPick'
import { bindLibraryStart, firstMessageStart, forgetStarts, setStart, startOf } from '../src/pwcode/newChatStart'

// A checkbox forwards its press to its hidden input as a PointerEvent, which
// jsdom does not define.
if (!('PointerEvent' in window)) {
  class PointerEventShim extends MouseEvent {
    pointerType: string
    constructor(type: string, init: PointerEventInit = {}) { super(type, init); this.pointerType = init.pointerType ?? '' }
  }
  Object.assign(window, { PointerEvent: PointerEventShim })
}

const base = { status: 'connected', remoteStart: true, directories: true, claudeCodeAllowed: false, claudeInstalled: false }
const own = { ...base, id: 'studio', name: 'box', kind: 'existing' }
const gpu = { ...base, id: 'm2', name: 'gpu', kind: 'existing', permissionModes: ['read-only', 'accept-edits', 'auto'], remoteMaxPermissionMode: 'accept-edits' }
const recent = (machineId: string, workspace: string) => ({ machineId, id: workspace, workspace, updatedAt: '2026-11-01T00:00:00Z' })

/** The Studio's routes, answered as it would. */
function studio(over: { machines?: unknown[]; sessions?: unknown[]; available?: boolean; tools?: unknown[] } = {}) {
  const asked: string[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(new URL(String(input), 'http://studio.example'), init)
    const url = new URL(req.url, 'http://studio.example')
    asked.push(`${req.method} ${url.pathname}${url.search}`)
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } })
    switch (url.pathname) {
      case '/api/chat/pwcode/start':
        return json({ available: over.available ?? true, machine: 'studio', machines: over.machines ?? [own, gpu], sessions: over.sessions ?? [recent('studio', '/home/me/solver'), recent('m2', '/data/runs')],
          folder: '/home/me/kb' + (url.searchParams.get('persona') ? `/agents/${url.searchParams.get('persona')}` : '') })
      case '/api/chat/pwcode/start/machines/studio/directories': return json({ path: '/home/me/kb', home: '/home/me', entries: [{ name: 'notes' }, { name: 'reports' }] })
      case '/api/chat/pwcode/start/machines/m2/directories': return json({ path: '/home/them', home: '/home/them', entries: [] })
      case '/api/mcp/bridge/machines': return json({ available: true, machines: over.tools ?? [{ state: 'offer', machine: 'gpu', machineId: 'm2', available: true }] })
      case '/api/mcp/bridge/setup': return json({ done: true, worked: true, changed: true, tools: 40, live: 'next' })
      default: return json({})
    }
  }))
  return asked
}

/** The chat as the package draws it: a composer, with or without the row it
 *  keeps over it. */
function Chat({ engine = 'pwcode', kept = false, ...p }: { engine?: string; kept?: boolean; id?: string | null; hasSession?: boolean; persona?: string | null }) {
  const canvas = useRef<HTMLDivElement>(null)
  return (
    <div ref={canvas} className="chat-canvas" data-engine={engine}>
      <div className="chat-composer-dock"><div>{kept && <div className="min-h-7" data-testid="kept" />}<div className="relative"><div className="chat-composer"><textarea /></div></div></div></div>
      <NewChatTargetSlot conversationId={p.id ?? null} hasSession={!!p.hasSession} persona={p.persona ?? null} canvas={canvas} />
    </div>
  )
}
/** The open menu's rows, in order. */
const menuRows = () => waitFor(() => {
  const rows = [...document.querySelectorAll<HTMLElement>('[role^="menuitem"]')]
  expect(rows.length).toBeGreaterThan(0)
  return rows
})
const machineChip = () => screen.findByTestId('agents-machine-chip', {}, { timeout: 5000 })
const folderChip = () => screen.findByTestId('agents-directory-chip', {}, { timeout: 5000 })
async function chooseMachine(name: RegExp) {
  fireEvent.click(await machineChip())
  fireEvent.click(await screen.findByRole('menuitemcheckbox', { name }).catch(() => screen.findByRole('menuitemradio', { name })).catch(() => screen.findByRole('menuitem', { name })))
}

beforeEach(() => { forgetStarts(); localStorage.clear(); chat.isStreaming = false })
afterEach(() => { vi.unstubAllGlobals() })

describe('where a new pw code chat starts', () => {
  it('shows the machine and folder the Studio starts it in, and sends nothing for them', async () => {
    const asked = studio()
    render(<Chat kept />)
    expect(await machineChip()).toHaveTextContent('box')
    expect(await folderChip()).toHaveTextContent('kb')
    // In the row the package keeps over an empty chat's composer.
    expect(screen.getByTestId('kept')).toContainElement(screen.getByTestId('pwcode-start'))
    expect(screen.queryByTestId('machine-tools-choice')).toBeNull()
    expect(screen.queryByRole('button', { name: /permission/i })).toBeNull()
    expect(firstMessageStart(null)).toEqual({})
    await act(async () => { await new Promise(r => setTimeout(r, 100)) })
    expect(asked.filter(a => a.includes('/api/chat/pwcode/start')).length).toBe(1)
  })

  it('names the Studio\'s own machine as this Studio\'s, first and chosen ahead of the workspace, and says it runs as the person', async () => {
    const workspace = { ...base, id: 'workspace', name: 'workspace', kind: 'workspace' }
    studio({ machines: [{ ...own, owner: 'alex', runsAs: 'alex' }, workspace, gpu] })
    render(<Chat />)
    const chip = await machineChip()
    expect(chip).toHaveTextContent('box (this Studio)')
    expect(chip.getAttribute('title')).toMatch(/runs as you \(alex\)/)
    fireEvent.click(chip)
    const rows = await menuRows()
    expect(rows[0]).toHaveTextContent(/^box \(this Studio\)/)
    expect(rows[0]).toHaveAttribute('aria-checked', 'true')
    expect(rows[1]).toHaveTextContent(/^workspace/)
    expect(firstMessageStart(null)).toEqual({})
  })

  it('shows a command that wraps, with Copy beside it, for a machine the Studio does not reach', async () => {
    const lap = { ...base, id: 'm3', name: 'lap', kind: 'local' }
    const command = 'sh -c "$(pw auth token --print | tail -n 1 | sed \'s/^/Authorization: Bearer /\' | curl -fsS -H @- \'https://platform.example/me/session/ada/studio/api/mcp/bridge/setup.sh?machine=m3\')"'
    studio({ machines: [own, lap], tools: [{ state: 'command', machine: 'lap', machineId: 'm3', command, uncommand: 'pw code mcp remove --scope user studio' }] })
    render(<Chat />)
    await chooseMachine(/^lap/)
    fireEvent.click(await screen.findByTestId('machine-tools-button'))
    const shown = await screen.findByTestId('machine-tools-command')
    expect(shown.textContent).toBe(command)
    expect(within(screen.getByTestId('machine-tools-popover')).getByRole('button', { name: 'Copy command' })).toBeInTheDocument()
  })

  it('is there only while pw code answers a chat with no session, right over the composer', async () => {
    studio()
    const { rerender, container } = render(<Chat id="c1" />)
    await machineChip()
    const row = container.querySelector('.pwcode-start-host')!
    expect(row.nextElementSibling).toHaveClass('relative')
    rerender(<Chat id="c1" hasSession />)
    await waitFor(() => expect(screen.queryByTestId('pwcode-start')).toBeNull())
    expect(container.querySelector('.pwcode-start-host')).toBeNull()
    rerender(<Chat id="c1" engine="studio" />)
    await act(async () => { await new Promise(r => setTimeout(r, 50)) })
    expect(screen.queryByTestId('pwcode-start')).toBeNull()
  })

  it('is not there where the Studio has no pw code for the person', async () => {
    studio({ available: false, machines: [] })
    render(<Chat />)
    await act(async () => { await new Promise(r => setTimeout(r, 200)) })
    expect(screen.queryByTestId('pwcode-start')).toBeNull()
  })

  it('sends a folder chosen on the Studio\'s machine, and nothing once it is the default again', async () => {
    const asked = studio()
    render(<Chat />)
    fireEvent.click(await folderChip())
    const field = await screen.findByRole('combobox')
    await waitFor(() => expect(asked.some(a => a.startsWith('GET /api/chat/pwcode/start/machines/studio/directories'))).toBe(true))
    fireEvent.change(field, { target: { value: '/home/me/solver' } })
    fireEvent.keyDown(field, { key: 'Enter' })
    await waitFor(async () => expect(await folderChip()).toHaveTextContent('solver'))
    // With the first message, for the conversation it makes.
    expect(firstMessageStart('c9')).toEqual({ pwcodeFolder: '/home/me/solver' })
    expect(startOf(null)).toEqual({})
  })

  it('starts on another machine in the folder used there last, within its ceiling, and drops the model picked for the first', async () => {
    localStorage.setItem('studio.code.agents:startMode', JSON.stringify('auto'))
    studio()
    pickModel(null, { model: 'claude-code/opus' })
    render(<Chat />)
    await chooseMachine(/gpu/)
    await waitFor(async () => expect(await machineChip()).toHaveTextContent('gpu'))
    expect(await folderChip()).toHaveTextContent('runs')
    expect(pickedModel(null)).toBeUndefined()
    await waitFor(() => expect(startOf(null)).toMatchObject({ machine: { id: 'm2' }, folder: '/data/runs' }))
    // The mode chosen last is wider than the machine takes: held to that.
    expect(startOf(null).mode).toBe('accept-edits')
    let sent: Record<string, unknown> = {}
    const answer = fetch
    vi.stubGlobal('fetch', vi.fn(async (u: RequestInfo, init?: RequestInit) => {
      if (String(u).includes('/api/chat/stream')) { sent = JSON.parse(String(init?.body)); return new Response('', { status: 500 }) }
      return answer(u, init)
    }))
    await createStudioAdapter().streamCompletion!({ conversationId: 'c5', model: 'm', messages: [{ role: 'user', content: 'Hi' }] } as never, { onToken: () => {}, onDone: () => {}, onError: () => {} } as never, new AbortController().signal).catch(() => {})
    expect(sent).toMatchObject({ conversationId: 'c5', pwcodeMachine: 'm2', pwcodeFolder: '/data/runs', pwcodeMode: 'accept-edits' })
    expect(sent.pwcodeModel).toBeUndefined()
    // And back.
    forgetStarts()
    await waitFor(async () => expect(await machineChip()).toHaveTextContent('box'))
    expect(firstMessageStart(null)).toEqual({})
  })

  it('starts in the home folder of a machine no session has worked on', async () => {
    studio({ sessions: [] })
    render(<Chat />)
    await chooseMachine(/gpu/)
    await waitFor(() => expect(startOf(null).folder).toBe('/home/them'))
  })

  it('asks, ticked, to add the Studio\'s tools to a machine that lacks them, and sends the choice', async () => {
    const asked = studio()
    render(<Chat />)
    await machineChip()
    expect(screen.queryByTestId('machine-tools-choice')).toBeNull()
    await chooseMachine(/gpu/)
    const choice = await screen.findByTestId('machine-tools-choice')
    expect(choice).toHaveTextContent("Add the Studio's tools to gpu")
    await waitFor(() => expect(startOf(null)).toMatchObject({ machine: { id: 'm2' }, tools: true }))
    expect(firstMessageStart(null)).toMatchObject({ pwcodeMachine: 'm2', pwcodeTools: true })
    fireEvent.click(within(choice).getByRole('checkbox'))
    await waitFor(() => expect(firstMessageStart(null)).toMatchObject({ pwcodeMachine: 'm2', pwcodeTools: false }))
    // The Studio adds them as the chat's session starts, not the page.
    expect(asked).not.toContain('POST /api/mcp/bridge/setup')
  })

  it('marks whether the machine has them, with no sentence in the chat', async () => {
    for (const [tools, on] of [[{ state: 'unavailable', why: 'pw ssh does not reach gpu.' }, 'false'], [{ state: 'set' }, 'true']] as const) {
      forgetStarts()
      studio({ tools: [{ machine: 'gpu', machineId: 'm2', ...tools }] })
      const { unmount } = render(<Chat />)
      await chooseMachine(/gpu/)
      await waitFor(() => expect(startOf(null).machine?.id).toBe('m2'))
      await waitFor(() => expect(within(screen.getByTestId('pwcode-start')).getByTestId('tools-mark')).toHaveAttribute('data-on', on))
      expect(screen.getByTestId('pwcode-start').textContent).not.toMatch(/Studio's tools|pw ssh/)
      expect(firstMessageStart(null)).not.toHaveProperty('pwcodeTools')
      unmount()
    }
  })

  it('shows a persona\'s own folder, and says a persona does not go to another machine', async () => {
    studio()
    const { rerender } = render(<Chat persona="reviewer" />)
    expect(await folderChip()).toHaveTextContent('reviewer')
    expect(screen.queryByTestId('pwcode-start-persona')).toBeNull()
    await chooseMachine(/gpu/)
    expect(await screen.findByTestId('pwcode-start-persona')).toHaveTextContent("The reviewer persona works only on this Studio's own pw code machine")
    rerender(<Chat />)
    await waitFor(() => expect(screen.queryByTestId('pwcode-start-persona')).toBeNull())
  })

  it('goes back to the default when the machine chosen no longer takes sessions', async () => {
    studio()
    render(<Chat id="c2" />)
    await chooseMachine(/gpu/)
    await waitFor(() => expect(startOf('c2').machine?.id).toBe('m2'))
    studio({ machines: [own, { ...gpu, status: 'unreachable' }] })
    act(() => { window.dispatchEvent(new Event('ade:viewer-caches-cleared')) })
    await waitFor(() => expect(startOf('c2')).toEqual({}))
    expect(await machineChip()).toHaveTextContent('box')
  })

  it('holds while a message is on its way', async () => {
    studio()
    chat.isStreaming = true
    render(<Chat />)
    await machineChip()
    expect(screen.getByTestId('pwcode-start')).toBeDisabled()
  })

  it('is forgotten when someone else is the viewer', async () => {
    const { clearViewerCaches } = await import('../src/viewerCache')
    studio()
    render(<Chat />)
    await chooseMachine(/gpu/)
    await waitFor(() => expect(startOf(null).machine?.id).toBe('m2'))
    act(() => clearViewerCaches())
    expect(startOf(null)).toEqual({})
  })
})

// New pw code chat here, in the Library (pwcode/chatHere.ts).
describe('where a chat started from the Library starts', () => {
  const start = { machine: { id: 'm2' }, folder: '/data/runs', library: { id: 'u-work', path: 'runs', machineName: 'gpu' } }

  it('is the machine and folder found for it, until the person chooses others', async () => {
    studio()
    setStart(null, start)
    render(<Chat />)
    expect(await machineChip()).toHaveTextContent('gpu')
    expect(await folderChip()).toHaveTextContent('runs')
    await waitFor(() => expect(startOf(null)).toMatchObject({ ...start, mode: 'accept-edits' }))
    await chooseMachine(/box/)
    await waitFor(() => expect(startOf(null)).toEqual({}))
    // Another folder is no longer the Library's.
    act(() => setStart(null, { folder: '/home/me/kb/plans', library: { id: 'kb', path: 'plans', machineName: 'box' } }))
    await waitFor(async () => expect(await folderChip()).toHaveTextContent('plans'))
    fireEvent.click(await folderChip())
    const field = await screen.findByRole('combobox')
    fireEvent.change(field, { target: { value: '/home/me/other' } })
    fireEvent.keyDown(field, { key: 'Enter' })
    await waitFor(() => expect(startOf(null)).toEqual({ folder: '/home/me/other' }))
  })

  it('takes no other machine when that one is not there, and says so', async () => {
    studio({ machines: [own] })
    setStart(null, start)
    render(<Chat />)
    expect(await screen.findByTestId('pwcode-start-lost', {}, { timeout: 5000 })).toHaveTextContent('/data/runs is on gpu, which takes no new sessions now.')
    await act(async () => { await new Promise(r => setTimeout(r, 100)) })
    expect(startOf(null)).toEqual(start)
    expect(await machineChip()).not.toHaveTextContent('box')
    expect(screen.queryByTestId('agents-directory-chip')).toBeNull()
    expect(firstMessageStart(null)).toMatchObject({ pwcodeMachine: 'm2', pwcodeFolder: '/data/runs', pwcodeLibrary: 'u-work', pwcodePath: 'runs' })
  })

  it('is the new chat\'s alone', () => {
    setStart(null, start)
    expect(startOf('c-old')).toEqual({})
    expect(firstMessageStart('c-old')).toEqual({})
    expect(bindLibraryStart('c-new')).toBe(true)
    expect(startOf('c-new')).toEqual(start)
    expect(startOf(null)).toEqual({})
    expect(bindLibraryStart('c-next')).toBe(false)
  })
})
