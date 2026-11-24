import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

// A stand-in for noVNC's RFB client: it records what the viewer asks of it,
// and the test plays the desktop's side with its events.
const clients: FakeRFB[] = []
class FakeRFB extends EventTarget {
  url: string; options: unknown
  scaleViewport = false; clipViewport = false; resizeSession = false; showDotCursor = false
  focusOnClick = true; viewOnly = false; qualityLevel = 6; compressionLevel = 2; background = ''
  disconnected = false; ctrlAltDel = 0; pasted: string[] = []
  constructor(_target: HTMLElement, url: string, options?: unknown) { super(); this.url = url; this.options = options; clients.push(this) }
  disconnect() { this.disconnected = true }
  sendCtrlAltDel() { this.ctrlAltDel++ }
  clipboardPasteFrom(text: string) { this.pasted.push(text) }
  focus() {}
  blur() {}
  emit(type: string, detail: unknown = {}) { this.dispatchEvent(new CustomEvent(type, { detail })) }
}
vi.mock('@novnc/novnc', () => ({ default: FakeRFB }))

import { DesktopView, MISSING_TRIES, desktopSocketUrl, isDesktop, retryDelay } from '../src/components/DesktopView'
import { showsInStudio } from '../src/components/SessionView'
import { sessionItem } from '../src/components/RunPane'

type Answer = { status: number; body: unknown }
let session: Answer
let logs: Answer
const asked: string[] = []

beforeEach(() => {
  clients.length = 0; asked.length = 0
  session = { status: 200, body: { name: 'desk', user: 'alex', type: 'desktop', status: 'running' } }
  logs = { status: 200, body: { xstartup: 'xstartup line', xvnc: 'Xvnc line' } }
  vi.stubGlobal('fetch', vi.fn(async (url: string) => {
    asked.push(url)
    const a = url.startsWith('/api/sessions/item') ? session : url.includes('/logs') ? logs : { status: 404, body: {} }
    return new Response(JSON.stringify(a.body), { status: a.status, headers: { 'Content-Type': 'application/json' } })
  }))
})
afterEach(() => { vi.unstubAllGlobals(); vi.useRealTimers(); vi.restoreAllMocks(); localStorage.clear() })

const client = async (n = 1) => { await waitFor(() => expect(clients.length).toBe(n)); return clients[n - 1] }

describe('DesktopView', () => {
  it('connects through the relay as a binary socket and draws the desktop', async () => {
    render(<DesktopView user="alex" name="desk" />)
    const c = await client()
    expect(c.url).toBe(`ws://${window.location.host}/api/session-desktop/alex/desk/websockify`)
    expect(c.options).toMatchObject({ wsProtocols: ['binary'], shared: true })
    // Settings are on the client before it connects, so Fit's first resize
    // is asked for as soon as the desktop takes one.
    expect(c.scaleViewport).toBe(true)
    expect(c.qualityLevel).toBe(9)
    expect(screen.getByText('Connecting…')).toBeTruthy()
    act(() => c.emit('connect'))
    expect(await screen.findByText('Connected')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Ctrl+Alt+Del' }))
    expect(c.ctrlAltDel).toBe(1)
  })

  it('shows what the desktop copied and sends text to it', async () => {
    render(<DesktopView user="alex" name="desk" />)
    const c = await client()
    act(() => c.emit('connect'))
    act(() => c.emit('clipboard', { text: 'from the desktop' }))
    fireEvent.click(screen.getByRole('button', { name: 'Clipboard' }))
    expect((await screen.findByLabelText('Copied on the desktop') as HTMLTextAreaElement).value).toBe('from the desktop')
    fireEvent.change(screen.getByLabelText('Send to the desktop'), { target: { value: 'to the desktop' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    expect(c.pasted).toEqual(['to the desktop'])
  })

  it('waits for a desktop that is not running, without a socket', async () => {
    session = { status: 200, body: { name: 'desk', user: 'alex', type: 'desktop', status: 'starting' } }
    render(<DesktopView user="alex" name="desk" />)
    expect(await screen.findByText('Desktop desk is starting. It shows here once it is running.')).toBeTruthy()
    expect(clients.length).toBe(0)
  })

  it('says why the Studio refused it and does not try again by itself', async () => {
    render(<DesktopView user="alex" name="desk" />)
    const c = await client()
    logs = { status: 409, body: { error: 'A desktop opens with your own ACTIVATE sign-in.' } }
    act(() => c.emit('disconnect', { clean: false }))
    expect(await screen.findByText('A desktop opens with your own ACTIVATE sign-in.')).toBeTruthy()
    expect(screen.getByText('Not opened')).toBeTruthy()
    await new Promise(r => setTimeout(r, 50))
    expect(clients.length).toBe(1)
    // Reconnect is the person's to press.
    logs = { status: 200, body: { xstartup: '', xvnc: '' } }
    fireEvent.click(screen.getByRole('button', { name: 'Reconnect' }))
    await client(2)
  })

  it('refused for want of an ACTIVATE sign-in, asks to sign in and opens Accounts', async () => {
    render(<DesktopView user="alex" name="desk" />)
    const c = await client()
    logs = { status: 409, body: { error: 'A desktop opens with your own ACTIVATE sign-in. Sign in with ACTIVATE under Settings, Accounts, then try again.' } }
    act(() => c.emit('disconnect', { clean: false }))
    expect(await screen.findByText('Sign in with ACTIVATE to use this')).toBeTruthy()
    expect(screen.getByText(/A desktop opens with your own ACTIVATE sign-in/)).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Open Accounts' }))
    expect(location.hash).toBe('#view=settings:accounts?open=activate')
    history.replaceState(null, '', location.pathname)
  })

  it('a desktop the relay cannot find is tried again after pauses, then it says so and stops', async () => {
    render(<DesktopView user="alex" name="desk" />)
    const c = await client()
    // The platform still lists the desktop as running, but its socket is gone.
    logs = { status: 404, body: { error: 'The platform has no desktop at that address; it may have stopped.' } }
    act(() => c.emit('disconnect', { clean: false }))
    // No new connection at once: the next one waits.
    await new Promise(r => setTimeout(r, 100))
    expect(clients.length).toBe(1)
    vi.useFakeTimers({ shouldAdvanceTime: true })
    for (let n = 0; n < MISSING_TRIES; n++) {
      await act(async () => { await vi.advanceTimersByTimeAsync(retryDelay(n) + 50) })
      const next = await client(n + 2)
      act(() => next.emit('disconnect', { clean: false }))
    }
    expect(await screen.findByText('Could not connect')).toBeTruthy()
    expect(screen.getByText('The platform has no desktop at that address; it may have stopped.')).toBeTruthy()
    const made = clients.length
    await act(async () => { await vi.advanceTimersByTimeAsync(120_000) })
    expect(clients.length).toBe(made)
    // Reconnect is the person's to press.
    logs = { status: 200, body: { xstartup: '', xvnc: '' } }
    fireEvent.click(screen.getByRole('button', { name: 'Reconnect' }))
    await client(made + 1)
  })

  it('tries a dropped connection again after a pause', async () => {
    render(<DesktopView user="alex" name="desk" />)
    const c = await client()
    act(() => c.emit('connect'))
    vi.useFakeTimers({ shouldAdvanceTime: true })
    act(() => c.emit('disconnect', { clean: false }))
    await waitFor(() => expect(screen.getByText('Reconnecting…')).toBeTruthy())
    await act(async () => { await vi.advanceTimersByTimeAsync(retryDelay(0) + 50) })
    await client(2)
  })

  it('a clean close waits for Reconnect', async () => {
    render(<DesktopView user="alex" name="desk" />)
    const c = await client()
    act(() => c.emit('connect'))
    act(() => c.emit('disconnect', { clean: true }))
    expect(await screen.findByText('The desktop closed the connection.')).toBeTruthy()
    fireEvent.click(screen.getByRole('button', { name: 'Reconnect' }))
    await client(2)
  })

  it('tells its card when the desktop closes the connection', async () => {
    const onClosed = vi.fn()
    render(<DesktopView user="alex" name="desk" onClosed={onClosed} />)
    const c = await client()
    act(() => c.emit('connect'))
    expect(onClosed).not.toHaveBeenCalled()
    act(() => c.emit('disconnect', { clean: true }))
    await waitFor(() => expect(onClosed).toHaveBeenCalledTimes(1))
  })

  it('closes its connection when it goes away', async () => {
    const { unmount } = render(<DesktopView user="alex" name="desk" />)
    const c = await client()
    act(() => c.emit('connect'))
    unmount()
    expect(c.disconnected).toBe(true)
  })

  it('only the view used last resizes the desktop, across frames too', async () => {
    // The desktop app's window can say it has no focus while it takes keys,
    // so the focus does not decide.
    vi.spyOn(document, 'hasFocus').mockReturnValue(false)
    const { container } = render(<><DesktopView user="alex" name="desk" /><DesktopView user="alex" name="desk" /></>)
    const b = await client(2), a = clients[0]
    expect([a.resizeSession, b.resizeSession]).toEqual([false, true])
    // A chat card is another page in a frame: its claim arrives on the channel.
    const other = new BroadcastChannel('studio.desktop.resize')
    other.postMessage('another-frame-1')
    await waitFor(() => expect([a.resizeSession, b.resizeSession]).toEqual([false, false]))
    // A click in a view takes it back.
    fireEvent.pointerDown(container.querySelectorAll('.desktop-screen')[0])
    await waitFor(() => expect([a.resizeSession, b.resizeSession]).toEqual([true, false]))
    other.close()
  })

  it('when the view that resizes goes away, the one used before it takes over', async () => {
    const two = (both: boolean) => <><DesktopView key="a" user="alex" name="desk" />{both && <DesktopView key="b" user="alex" name="desk" />}</>
    const { rerender } = render(two(true))
    const b = await client(2), a = clients[0]
    expect([a.resizeSession, b.resizeSession]).toEqual([false, true])
    // Full screen closes.
    rerender(two(false))
    expect(b.disconnected).toBe(true)
    await waitFor(() => expect(a.resizeSession).toBe(true))
  })

  it('Actual size shows the desktop unscaled, with scroll bars for the rest', async () => {
    localStorage.setItem('studio.desktop.fit', 'actual')
    const { container } = render(<DesktopView user="alex" name="desk" />)
    const c = await client()
    act(() => c.emit('connect'))
    expect(container.querySelector('.desktop-view')?.getAttribute('data-fit')).toBe('actual')
    // Unclipped, so the page scrolls it, and never resized.
    expect([c.scaleViewport, c.clipViewport, c.resizeSession]).toEqual([false, false, false])
    // Pan: a drag moves the view, and the desktop takes no input meanwhile.
    expect(c.viewOnly).toBe(false)
    fireEvent.click(screen.getByRole('button', { name: 'Pan' }))
    await waitFor(() => expect(c.viewOnly).toBe(true))
    // jsdom has no PointerEvent; a MouseEvent carries the same position.
    if (typeof window.PointerEvent !== 'function') vi.stubGlobal('PointerEvent', class extends MouseEvent {})
    const scroller = document.createElement('div')
    const view = container.querySelector('.desktop-screen') as HTMLElement
    view.appendChild(scroller)
    scroller.scrollLeft = 300; scroller.scrollTop = 200
    fireEvent.pointerDown(view, { button: 0, clientX: 100, clientY: 100 })
    fireEvent.pointerMove(window, { clientX: 60, clientY: 70 })
    fireEvent.pointerUp(window)
    expect([scroller.scrollLeft, scroller.scrollTop]).toEqual([340, 230])
    fireEvent.click(screen.getByRole('button', { name: 'Pan' }))
    await waitFor(() => expect(c.viewOnly).toBe(false))
  })

  it('Pan is offered only in Actual size', async () => {
    render(<DesktopView user="alex" name="desk" />)
    const c = await client()
    act(() => c.emit('connect'))
    expect(screen.queryByRole('button', { name: 'Pan' })).toBeNull()
  })

  it('shows the start-up logs while it connects', async () => {
    render(<DesktopView user="alex" name="desk" />)
    await client()
    expect(await screen.findByLabelText('Start-up logs')).toBeTruthy()
    expect(screen.getByLabelText('Start-up logs').textContent).toContain('Xvnc line')
    expect(asked.some(u => u === '/api/session-desktop/alex/desk/logs')).toBe(true)
  })
})

describe('desktop sessions elsewhere', () => {
  it('builds the relay address on the page origin', () => {
    expect(desktopSocketUrl('a b', 'd/e', { protocol: 'https:', host: 'studio.example.com' }))
      .toBe('wss://studio.example.com/api/session-desktop/a%20b/d%2Fe/websockify')
  })
  it('a desktop shows in the Studio, as a relayed session does', () => {
    expect(isDesktop({ desktop: true })).toBe(true)
    expect(showsInStudio({ desktop: true, ownHost: false, url: 'https://platform.example.com/sessions/alex/desk/vnc' })).toBe(true)
    // A desktop the relay would not open (software other than VNC) is
    // marked so by the server, and is not drawn.
    expect(isDesktop({ type: 'desktop', desktop: false } as { desktop: boolean })).toBe(false)
    expect(showsInStudio({ desktop: false, ownHost: false, url: 'https://platform.example.com/sessions/alex/other/x' })).toBe(false)
    expect(showsInStudio({ desktop: false, ownHost: false, url: 'https://platform.example.com/me/session/alex/t' })).toBe(false)
    expect(showsInStudio({ desktop: false, ownHost: true, url: 'https://e.sessions.example.com/' })).toBe(true)
  })
  it('a side view tab for a desktop names it for the viewer', () => {
    expect(sessionItem({ name: 'desk', user: 'alex', type: 'desktop', desktop: true, url: 'https://platform.example.com/sessions/alex/desk/vnc', ownHost: false }))
      .toMatchObject({ kind: 'session', host: null, desktop: { user: 'alex', name: 'desk' } })
    expect(sessionItem({ name: 'web', type: 'endpoint', url: 'https://web.sessions.example.com/', ownHost: true }))
      .toMatchObject({ host: 'web.sessions.example.com', desktop: null })
  })
})
