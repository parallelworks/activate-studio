import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatProvider, SessionView, type ChatAdapter, type Message } from '@parallelworks/ui/ai'

/*
 * Find in a long session. The chat stays mounted under the Studio's other
 * pages, hidden with `visibility` (styles.css, .view-chat), and turns go on
 * arriving: what the list lets go of then, and what find reads then, was
 * kept as empty text, so nothing in it was found afterwards
 * (patches/@parallelworks__ui).
 */

const ROW = 100
const VIEW = 600
const ALL: Message[] = Array.from({ length: 60 }, (_, i) => i).flatMap(i => [
  { id: `u${i}`, conversationId: 'c', role: 'user', content: `Read gauge${i} at the pier.`, createdAt: '' },
  { id: `a${i}`, conversationId: 'c', role: 'assistant', content: `Answer${i}: the tide is in.`, createdAt: '' },
] as unknown as Message[])
/** The session's first n turns. */
const turns = (n: number): Message[] => ALL.slice(0, 2 * n)

const shown = (el: Element | null): boolean => {
  for (let e = el as HTMLElement | null; e; e = e.parentElement) if (e.style.visibility === 'hidden' || e.style.display === 'none') return false
  return true
}

// A list as laid out: every row ROW high, in a view VIEW high.
const proto = HTMLElement.prototype
const keep = new Map<string, [object, PropertyDescriptor | undefined]>()
const stub = (on: object, name: string, d: PropertyDescriptor) => {
  keep.set(name, [on, Object.getOwnPropertyDescriptor(on, name)])
  Object.defineProperty(on, name, { configurable: true, ...d })
}
// The list and its rows, found once for each change to the page: the list
// measures every row it has, and a search of the page for each was nearly
// all of these tests' time.
let laid: { log: HTMLElement; rows: HTMLElement[]; at: Map<HTMLElement, number> } | null = null
const changes = new MutationObserver(() => { laid = null })
const layout = () => {
  if (changes.takeRecords().length) laid = null
  if (laid) return laid
  const found = [...document.querySelectorAll<HTMLElement>('[role="log"] [data-transcript] [data-row]')]
  return (laid = { log: document.querySelector<HTMLElement>('[role="log"]')!, rows: found, at: new Map(found.map((r, i) => [r, i])) })
}
const log = () => layout().log
const rows = () => layout().rows
beforeEach(() => {
  laid = null
  changes.observe(document, { subtree: true, childList: true, attributes: true, attributeFilter: ['role', 'data-transcript', 'data-row'] })
  stub(Element.prototype, 'checkVisibility', { value(this: Element) { return shown(this) } })
  stub(proto, 'clientHeight', { get(this: HTMLElement) { return this.getAttribute('role') === 'log' ? VIEW : 0 } })
  stub(proto, 'scrollHeight', { get(this: HTMLElement) { return this.getAttribute('role') === 'log' ? Math.max(VIEW, rows().length * ROW) : 0 } })
  stub(proto, 'getBoundingClientRect', {
    value(this: HTMLElement) {
      const at = this.dataset['row'] ? layout().at.get(this) ?? -1 : -1
      const top = at < 0 ? 0 : at * ROW - (log()?.scrollTop ?? 0)
      const height = at < 0 ? VIEW : ROW
      return { top, bottom: top + height, left: 0, right: 0, width: 0, height, x: 0, y: top, toJSON: () => ({}) }
    },
  })
  stub(proto, 'scrollIntoView', { value: () => {}, writable: true })
})
afterEach(() => {
  cleanup()
  changes.disconnect()
  vi.useRealTimers()
  for (const [name, [on, d]] of keep) { if (d) Object.defineProperty(on, name, d); else delete (on as Record<string, unknown>)[name] }
  keep.clear()
})

const adapter = { listConversations: async () => [], getConversation: async () => null } as unknown as ChatAdapter
const page = (messages: Message[], away: boolean) => (
  <ChatProvider adapter={adapter} currentUser={{ username: 'alex' }} navigation={{ push: () => {}, replace: () => {} } as never}>
    <div style={{ visibility: away ? 'hidden' : 'visible' }}>
      <SessionView sessionKey="s" messages={messages} streamingMessage={null} turn={{ running: false, onStop: () => {} }} onSend={async () => {}} find workingIndicator={false} label="session" />
    </div>
  </ChatProvider>
)
const openFind = async () => {
  fireEvent.keyDown(document.body, { key: 'f', ctrlKey: true })
  return screen.findByRole('textbox', { name: /find/i })
}
const count = () => screen.getByTestId('transcript-find-count')
const settle = (ms = 50) => act(() => new Promise<void>(r => { setTimeout(r, ms) }))

describe('find in a session that went on while another page showed', () => {
  it('finds turns the list let go of while it was hidden', async () => {
    const { rerender } = render(page(turns(4), false))
    await waitFor(() => expect(rows().length).toBeGreaterThan(0), { timeout: 8000 })
    for (let n = 6; n <= 60; n += 6) rerender(page(turns(n), true))
    await settle()
    rerender(page(turns(60), false))
    await settle()
    expect(rows().some(r => r.dataset['row'] === 'u5')).toBe(false)

    const box = await openFind()
    for (const q of ['gauge5 at', 'gauge30 at', 'gauge0 at', 'Answer50:', 'gauge59 at']) {
      fireEvent.change(box, { target: { value: q } })
      await waitFor(() => expect(count()).toHaveTextContent('1 of 1'), { timeout: 5000 })
    }
    fireEvent.change(box, { target: { value: 'gauge999' } })
    await waitFor(() => expect(count()).toHaveTextContent('No results'), { timeout: 5000 })
  }, 30_000)

  it('reads them for a find left open, once the session shows again', async () => {
    const { rerender } = render(page(turns(4), false))
    await waitFor(() => expect(rows().length).toBeGreaterThan(0), { timeout: 8000 })
    const box = await openFind()
    for (let n = 10; n <= 60; n += 10) {
      rerender(page(turns(n), true))
      await settle()
    }
    await settle(700)
    rerender(page(turns(60), false))
    for (const q of ['gauge7 at', 'gauge30 at', 'gauge59 at']) {
      fireEvent.change(box, { target: { value: q } })
      await waitFor(() => expect(count()).toHaveTextContent('1 of 1'), { timeout: 5000 })
    }
  }, 30_000)

  it('keeps what a find left open had found', async () => {
    const { rerender } = render(page(turns(4), false))
    await waitFor(() => expect(rows().length).toBeGreaterThan(0), { timeout: 8000 })
    fireEvent.change(await openFind(), { target: { value: 'gauge3 at' } })
    await waitFor(() => expect(count()).toHaveTextContent('1 of 1'))
    rerender(page(turns(4), true))
    rerender(page(turns(8), true))
    await settle(600)
    expect(count()).toHaveTextContent('1 of 1')
    rerender(page(turns(8), false))
    await settle(1000)
    expect(count()).toHaveTextContent('1 of 1')
  }, 30_000)
})
