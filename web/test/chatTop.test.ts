import { afterEach, describe, expect, it, vi } from 'vitest'
import { fitControls, noticePad, watchNotices } from '../src/chatTop'

// A row whose width shrinks as its labels drop: 560 with every label,
// 380 with the engine's alone, 300 with none.
function row() {
  const el = document.createElement('div')
  el.getBoundingClientRect = () => ({ width: el.classList.contains('tighter') ? 300 : el.classList.contains('tight') ? 380 : 560 }) as DOMRect
  return el
}

describe('the header buttons over the thread', () => {
  it('keep their labels where they fit', () => {
    const el = row(); fitControls(el, 600)
    expect(el.className).toBe('')
  })
  it('drop the labels, the engine\'s last, until the row fits', () => {
    const el = row()
    fitControls(el, 460); expect(el.classList.contains('tight')).toBe(true); expect(el.classList.contains('tighter')).toBe(false)
    fitControls(el, 320); expect(el.classList.contains('tighter')).toBe(true)
  })
  it('get their labels back when the room comes back, and on a phone', () => {
    const el = row()
    fitControls(el, 320); fitControls(el, 600); expect(el.className).toBe('')
    fitControls(el, 320); fitControls(el, null); expect(el.className).toBe('')
  })
})

describe('a notice over the thread', () => {
  it('pushes the conversation below its foot', () => {
    expect(noticePad(100, [271])).toBe(181)
    expect(noticePad(100, [180, 271.4])).toBe(182)
  })
  it('leaves the thread alone when there is none, or it ends above the thread', () => {
    expect(noticePad(100, [])).toBe(0)
    expect(noticePad(100, [90])).toBe(0)
  })
})

// A ResizeObserver as browsers have it: an element is reported once when
// it is observed, and again when observed after being let go.
class BrowserRO {
  static calls = 0
  private seen = new Set<Element>()
  constructor(private cb: () => void) {}
  observe(el: Element) {
    if (this.seen.has(el)) return
    this.seen.add(el)
    setTimeout(() => { if (this.seen.has(el)) { BrowserRO.calls++; this.cb() } }, 0)
  }
  unobserve(el: Element) { this.seen.delete(el) }
  disconnect() { this.seen.clear() }
}

describe('watching the notices over the thread', () => {
  afterEach(() => { vi.unstubAllGlobals(); document.body.innerHTML = '' })
  const canvas = () => {
    const c = document.createElement('div')
    c.innerHTML = '<main></main>'
    document.body.appendChild(c)
    return c
  }
  const settle = () => new Promise(r => setTimeout(r, 30))

  it('settles: the observer is not set off by its own callback', async () => {
    vi.stubGlobal('ResizeObserver', BrowserRO)
    BrowserRO.calls = 0
    const stop = watchNotices(canvas())
    await settle()
    const first = BrowserRO.calls
    await settle()
    expect(first).toBeLessThanOrEqual(2)
    expect(BrowserRO.calls).toBe(first)
    stop()
  })

  it('watches a notice that comes and lets go of one that leaves, and settles again', async () => {
    vi.stubGlobal('ResizeObserver', BrowserRO)
    BrowserRO.calls = 0
    const c = canvas()
    const stop = watchNotices(c)
    await settle()
    const note = document.createElement('div'); note.className = 'cred-banner'
    c.appendChild(note)
    await settle()
    const after = BrowserRO.calls
    expect(after).toBeGreaterThan(0)
    await settle()
    expect(BrowserRO.calls).toBe(after)
    note.remove()
    await settle(); await settle()
    expect(c.style.getPropertyValue('--chat-notice-pad')).toBe('0px')
    stop()
  })
})
