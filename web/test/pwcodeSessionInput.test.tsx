import { act, cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatProvider, type ChatAdapter } from '@parallelworks/ui/ai'
import { PWCodeSession } from '../src/pwcode/PWCodeSession'

// The session view's client takes the page's fetch as it loads.
vi.hoisted(() => {
  const real = globalThis.fetch
  const current = ((...a: Parameters<typeof fetch>) => (globalThis.fetch === current ? real(...a) : globalThis.fetch(...a))) as typeof fetch
  globalThis.fetch = current
})

const now = new Date().toISOString()
const machine = { id: 'studio', name: 'box', kind: 'existing', status: 'connected', remoteStart: true, hostname: 'box.example', daemonId: 'd1' }
const listed = { machineId: 'studio', id: 's1', name: 'mesh study', status: 'idle', workspace: '/w', createdAt: now, updatedAt: now, messageCount: 1 }
const idle = {
  id: 's1', machineId: 'studio', workspace: '/w', model: 'm', status: 'idle', permissionMode: 'default', createdAt: now, updatedAt: now, messages: 1, lastSeq: 1,
  history: [
    { kind: 'user', text: 'Look at the mesh.' },
    { kind: 'assistant', text: 'It is coarse near the inlet.' },
    { kind: 'turn_end', turnEnd: { durationMs: 3000, endedAt: now } },
  ],
}
const asking = {
  ...idle, status: 'awaiting_approval',
  history: idle.history.slice(0, 2),
  approvals: [{ id: 'ap1', kind: 'ask', questions: [{ question: 'Which mesh?', options: [{ label: 'Coarse' }, { label: 'Fine' }] }] }],
}

function studio(detail: typeof idle, on: Record<string, unknown> = {}) {
  const posts: { path: string; body: Record<string, unknown> }[] = []
  vi.stubGlobal('fetch', vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(new URL(String(input), 'http://studio.example'), init)
    const json = (v: unknown) => new Response(JSON.stringify(v), { status: 200, headers: { 'Content-Type': 'application/json' } })
    const path = new URL(req.url, 'http://studio.example').pathname
    if (req.method === 'POST') posts.push({ path, body: await req.clone().json().catch(() => ({})) as Record<string, unknown> })
    switch (path) {
      case '/api/code/sessions': return json({ machines: [{ ...machine, ...on }], sessions: [{ ...listed, status: detail.status }] })
      case '/api/chat/pwcode/here': return json({ available: true, daemonId: 'd1', hostname: 'box.example', chats: [] })
      case '/api/code/machines/studio/sessions/s1': return json(detail)
      case '/api/code/machines/studio/sessions/s1/transcript': return json({ tail: detail, from: 0, total: detail.history.length })
      case '/api/code/machines/studio/sessions/s1/images': return json({ path: '/cache/s1/image-1.png' })
      case '/api/code/machines/studio/sessions/s1/commands': return json({ output: 'Installing pw 7.143.0.' })
      default: return json({})
    }
  }))
  return posts
}

const adapter = { listConversations: async () => [], getConversation: async () => null } as unknown as ChatAdapter
const navigation = { push: () => {}, replace: () => {} } as never
const session = (
  <ChatProvider adapter={adapter} currentUser={{ username: 'alex' }} navigation={navigation}>
    <PWCodeSession conversationId="c1" machine="studio" sessionId="s1" username="alex" />
  </ChatProvider>
)

const png = () => new File(['png'], 'shot.png', { type: 'image/png' })
const images = (posts: { path: string }[]) => posts.filter(p => p.path.endsWith('/images')).length

beforeEach(() => {
  localStorage.clear()
  vi.stubGlobal('createImageBitmap', async () => ({ width: 10, height: 10, close: () => {} }))
  URL.createObjectURL = () => 'blob:preview'
  URL.revokeObjectURL = () => {}
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

describe("a session's composer", () => {
  it('takes images picked with +, dropped on the conversation and pasted, and says what it does not take', async () => {
    const posts = studio(idle)
    const { container } = render(session)
    // The composer is drawn anew once the transcript is read.
    const composer = () => screen.getByRole('textbox', { name: 'Message to this session' })
    expect(await screen.findByText('It is coarse near the inlet.', {}, { timeout: 5000 })).toBeInTheDocument()
    await waitFor(() => expect(composer()).toBeEnabled(), { timeout: 5000 })

    const picker = container.querySelector<HTMLInputElement>('input[type="file"]')!
    expect(picker.accept).toBe('image/png,image/jpeg,image/gif,image/webp')
    const opened = vi.spyOn(picker, 'click')
    fireEvent.click(screen.getByRole('button', { name: 'Attach files' }))
    expect(opened).toHaveBeenCalled()
    fireEvent.change(picker, { target: { files: [png()] } })
    await waitFor(() => expect(images(posts)).toBe(1))

    fireEvent.drop(screen.getByText('It is coarse near the inlet.'), { dataTransfer: { files: [png()], types: ['Files'] } })
    await waitFor(() => expect(images(posts)).toBe(2))

    fireEvent.paste(composer(), { clipboardData: { files: [png()], getData: () => '' } })
    await waitFor(() => expect(images(posts)).toBe(3))
    expect(await screen.findAllByRole('img', { name: /\[Image #\d\]/ })).toHaveLength(3)

    act(() => { fireEvent.change(picker, { target: { files: [new File(['x'], 'notes.txt', { type: 'text/plain' })] } }) })
    expect(await screen.findByText(/Only PNG, JPEG, GIF, and WebP images/)).toBeInTheDocument()
    expect(images(posts)).toBe(3)
  }, 20_000)

  it('keeps its height while the box is measured, so typing does not move the transcript', async () => {
    studio(idle)
    render(session)
    expect(await screen.findByText('It is coarse near the inlet.', {}, { timeout: 5000 })).toBeInTheDocument()
    const box = screen.getByRole('textbox', { name: 'Message to this session' })
    const holder = box.parentElement!
    Object.defineProperty(holder, 'offsetHeight', { configurable: true, value: 96 })
    const measured: string[] = []
    Object.defineProperty(box, 'scrollHeight', { configurable: true, get: () => { measured.push(holder.style.minHeight); return 70 } })
    fireEvent.change(box, { target: { value: 'a line\nand another' } })
    expect(measured.length).toBeGreaterThan(0)
    expect(new Set(measured)).toEqual(new Set(['96px']))
    expect(holder.style.minHeight).toBe('')
  }, 20_000)

  it('reads a message that starts with ! as a shell command, as the terminal does', async () => {
    const posts = studio(idle)
    render(session)
    expect(await screen.findByText('It is coarse near the inlet.', {}, { timeout: 5000 })).toBeInTheDocument()
    const box = screen.getByRole('textbox', { name: 'Message to this session' })
    fireEvent.change(box, { target: { value: '!ls -l' } })
    await waitFor(() => expect(screen.getByRole('textbox', { name: 'Message to this session' })).toHaveValue('ls -l'))
    expect(screen.getAllByText(/shell mode/i).length).toBeGreaterThan(0)
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(posts.find(p => p.path.endsWith('/shell'))?.body).toMatchObject({ command: 'ls -l' }))
    expect(posts.some(p => p.path.endsWith('/messages'))).toBe(false)
  }, 20_000)

  it("says a newer pw is out for the session's machine, and installs it", async () => {
    const posts = studio(idle, { pwVersion: '7.142.0', pwUpgrade: true, pwUpdate: { version: '7.143.0', state: 'available' } })
    render(session)
    const notice = await screen.findByTestId('pw-update-notice', {}, { timeout: 5000 })
    expect(notice).toHaveTextContent('7.143.0')
    fireEvent.click(within(notice).getByRole('button', { name: 'Update pw' }))
    await waitFor(() => expect(posts.find(p => p.path.endsWith('/commands'))?.body).toEqual({ name: 'upgrade' }))
    expect(await screen.findByText('Installing pw 7.143.0.')).toBeInTheDocument()
  }, 20_000)

  it("sends notes on a question's pick, with or without option previews", async () => {
    const posts = studio(asking as unknown as typeof idle)
    render(session)
    fireEvent.click(await screen.findByRole('button', { name: /Fine/ }, { timeout: 5000 }))
    fireEvent.click(await screen.findByRole('button', { name: /Add notes/ }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Notes' }), { target: { value: 'only near the inlet' } })
    fireEvent.click(screen.getByRole('button', { name: 'Send' }))
    await waitFor(() => expect(posts.find(p => p.path.endsWith('/approvals/ap1'))?.body).toEqual({ answers: [{ picked: ['Fine'], notes: 'only near the inlet' }] }))
  }, 20_000)
})
