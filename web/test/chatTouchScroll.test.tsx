import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ChatMessageList, ChatProvider, type ChatAdapter, type ChatMessage } from '@parallelworks/ui/ai'

// A chat on a touch screen (iPad Safari): a finger on the thread at the latest
// turn must not bring up "Jump to latest", and the thread follows the latest
// turn when it changes height itself (turning the iPad, Split View). The list
// is the package's, patched in patches/@parallelworks__ui@0.151.1.patch.

type Observed = { cb: ResizeObserverCallback; targets: Element[] }
let observers: Observed[]
vi.hoisted(() => {
  globalThis.matchMedia ??= ((q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {} })) as never
  Element.prototype.scrollTo ??= (() => {}) as never
})

beforeEach(() => {
  observers = []
  vi.stubGlobal('ResizeObserver', class {
    o: Observed
    constructor(cb: ResizeObserverCallback) { this.o = { cb, targets: [] }; observers.push(this.o) }
    observe(t: Element) { this.o.targets.push(t) }
    unobserve() {}
    disconnect() { this.o.targets = [] }
  })
})
afterEach(() => { cleanup(); vi.unstubAllGlobals() })

const adapter = { listConversations: async () => [], getConversation: async () => null } as unknown as ChatAdapter
const navigation = { toConversation() {}, toNewChat() {} } as never
const now = new Date().toISOString()
const turns = (n: number): ChatMessage[] => Array.from({ length: n }, (_, i) => [
  { id: `u${i}`, role: 'user', content: `Question ${i + 1}`, timestamp: now, parentId: i ? `a${i - 1}` : null },
  { id: `a${i}`, role: 'assistant', content: `Answer ${i + 1}`, timestamp: now, parentId: `u${i}` },
] as ChatMessage[]).flat()

/** The list, with the thread given a size jsdom does not lay out. */
function open(box: { height: number; client: number }) {
  render(
    <ChatProvider adapter={adapter} currentUser={{ username: 'alex' } as never} navigation={navigation}>
      <ChatMessageList messages={turns(3)} inputElement={<textarea aria-label="Message" />} />
    </ChatProvider>,
  )
  const thread = screen.getByRole('log')
  let top = Math.max(0, box.height - box.client)
  Object.defineProperty(thread, 'scrollHeight', { configurable: true, get: () => box.height })
  Object.defineProperty(thread, 'clientHeight', { configurable: true, get: () => box.client })
  Object.defineProperty(thread, 'scrollTop', { configurable: true, get: () => top, set: v => { top = Math.max(0, Math.min(v, box.height - box.client)) } })
  return { thread, top: () => top, setTop: (v: number) => { top = v } }
}

/** A finger from one height on the screen to another, moving the thread by
 *  `scroll` as the browser would (nothing, where it has no room). */
function drag(thread: HTMLElement, from: number, to: number, scroll: (dy: number) => void) {
  const fire = (type: string, y: number) => {
    const e = new Event(type, { bubbles: true })
    Object.defineProperty(e, 'touches', { value: type === 'touchend' ? [] : [{ clientY: y }] })
    thread.dispatchEvent(e)
  }
  act(() => {
    fire('touchstart', from)
    for (let i = 1; i <= 4; i++) {
      const y = from + (to - from) * i / 4
      fire('touchmove', y)
      const before = thread.scrollTop
      scroll(-(to - from) / 4)
      // A browser fires scroll only when the thread moved.
      if (thread.scrollTop !== before) thread.dispatchEvent(new Event('scroll'))
    }
    fire('touchend', to)
  })
}

const jump = () => screen.queryByRole('button', { name: 'Jump to latest' })

describe('chat thread on a touch screen', () => {
  it('a drag on toward the end, at the latest turn, keeps Jump to latest hidden', () => {
    const t = open({ height: 3000, client: 800 })
    drag(t.thread, 600, 480, () => {}) // at the end there is no room to move
    expect(jump()).toBeNull()
  })

  it('a drag in a chat too short to scroll keeps Jump to latest hidden', () => {
    const t = open({ height: 800, client: 800 })
    drag(t.thread, 300, 460, () => {})
    drag(t.thread, 460, 300, () => {})
    expect(jump()).toBeNull()
  })

  it('a drag back through the chat lets go of the latest turn', () => {
    const t = open({ height: 3000, client: 800 })
    drag(t.thread, 300, 700, dy => t.setTop(t.top() + dy))
    expect(t.top()).toBe(1800)
    expect(jump()).not.toBeNull()
  })

  it('a thread that changes height keeps its latest turn in view', () => {
    const box = { height: 3000, client: 1100 }
    const t = open(box)
    const watching = observers.find(o => o.targets.includes(t.thread))
    expect(watching).toBeDefined()
    box.client = 700 // the iPad turned to landscape, or Split View narrowed it
    act(() => watching!.cb([], watching as never))
    expect(t.top()).toBe(box.height - box.client)
    expect(jump()).toBeNull()
  })
})
