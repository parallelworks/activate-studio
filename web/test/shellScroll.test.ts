import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { isShellBox, layoutSize, onShellScroll, settleAfterKeyboard } from '../src/shellScroll'

// The chat as the package nests it in its drawer layout: a box that only
// hides its overflow, around the thread, with the composer inside the thread.
const page = `
  <div class="app"><div class="app-body"><main class="content"><div class="view view-chat"><div class="chat-wrap">
    <div class="chat-canvas card">
      <div id="drawer" class="flex h-full relative overflow-hidden"><main id="col"><div id="inner">
        <div id="thread" class="chat-thread"><pre id="code"></pre><div id="composer" class="chat-composer-dock"></div></div>
      </div></main></div>
    </div>
  </div></div></main></div></div>
  <nav id="rail" class="sidenav"></nav>`

const el = (id: string) => document.getElementById(id)!
const scrolled = (e: Element, top: number, left = 0) => {
  e.scrollTop = top
  e.scrollLeft = left
  onShellScroll({ target: e } as unknown as Event)
}

describe('the shell stays in place', () => {
  beforeEach(() => { document.body.innerHTML = page })
  afterEach(() => { document.body.innerHTML = '' })

  it('puts back every box around the thread, whatever the package calls it', () => {
    for (const id of ['drawer', 'col', 'inner']) {
      expect(isShellBox(el(id)), id).toBe(true)
      scrolled(el(id), 36, 4)
      expect([el(id).scrollTop, el(id).scrollLeft], id).toEqual([0, 0])
    }
    for (const sel of ['.app', '.app-body', 'main.content', '.view', '.chat-wrap', '.chat-canvas']) {
      scrolled(document.querySelector(sel)!, 36)
      expect(document.querySelector(sel)!.scrollTop, sel).toBe(0)
    }
  })

  it('leaves the thread and what scrolls inside it alone', () => {
    scrolled(el('thread'), 900)
    expect(el('thread').scrollTop).toBe(900)
    scrolled(el('code'), 40, 12)
    expect([el('code').scrollTop, el('code').scrollLeft]).toEqual([40, 12])
  })

  it('leaves a pane outside the chat alone, and only stops the rail going sideways', () => {
    scrolled(el('rail'), 50, 8)
    expect([el('rail').scrollTop, el('rail').scrollLeft]).toEqual([50, 0])
  })
})

describe('the rules that keep the chat from lifting', () => {
  const css = readFileSync('src/styles.css', 'utf8')
  const rule = (selector: string) => css.split('\n').find(l => l.startsWith(`${selector} {`)) ?? ''

  it('holds the thread\'s absolutely placed children in it, and stops it bouncing with the composer inside', () => {
    expect(rule('.chat-canvas .chat-thread')).toContain('position: relative')
    expect(rule('.chat-canvas .chat-thread')).toContain('overscroll-behavior: none')
  })

  it('clips the drawer layout\'s box, which a script could otherwise scroll', () => {
    expect(rule('.chat-canvas .overflow-hidden:has(.chat-thread)')).toContain('overflow: clip')
  })
})

/** Safari's visual viewport, moved by hand: a pinch changes its scale, a
 *  keyboard only its height and offset. */
function viewport() {
  const vv = Object.assign(new EventTarget(), { scale: 1, height: 1194, width: 834, offsetTop: 0, offsetLeft: 0 })
  const move = (to: Partial<typeof vv>) => { Object.assign(vv, to); vv.dispatchEvent(new Event('resize')) }
  return { vv, move }
}

describe('a page pinched in or out on an iPad', () => {
  afterEach(() => { vi.unstubAllGlobals(); document.body.innerHTML = '' })

  it('goes back to the top when the keyboard closes', () => {
    const { vv, move } = viewport(), toTop = vi.fn()
    settleAfterKeyboard(vv as unknown as VisualViewport, toTop)
    move({ height: 800, offsetTop: 380 }) // the keyboard opens and Safari pans the page
    move({ height: 1194, offsetTop: 380 }) // and closes
    expect(toTop).toHaveBeenCalledTimes(1)
  })

  it('is left where the pinch puts it while zooming back out', () => {
    const { vv, move } = viewport(), toTop = vi.fn()
    settleAfterKeyboard(vv as unknown as VisualViewport, toTop)
    move({ scale: 2, height: 597, width: 417, offsetTop: 300, offsetLeft: 200 })
    move({ scale: 1.5, height: 796, width: 556, offsetTop: 250, offsetLeft: 150 })
    move({ scale: 1.1, height: 1085, width: 758, offsetTop: 60, offsetLeft: 40 })
    move({ scale: 1, height: 1194, width: 834, offsetTop: 0, offsetLeft: 0 })
    expect(toTop).not.toHaveBeenCalled()
  })

  it('does not put the page itself back while it is pinched, and does once it is not', () => {
    document.body.innerHTML = page
    vi.stubGlobal('visualViewport', { scale: 2 })
    scrolled(document.body, 120)
    expect(document.body.scrollTop).toBe(120)
    // A box inside the shell is still put back.
    scrolled(el('drawer'), 36)
    expect(el('drawer').scrollTop).toBe(0)
    vi.stubGlobal('visualViewport', { scale: 1 })
    scrolled(document.body, 120)
    expect(document.body.scrollTop).toBe(0)
  })

  it('places menus and tips by the layout, which a pinch does not shrink', () => {
    vi.spyOn(document.documentElement, 'clientWidth', 'get').mockReturnValue(834)
    vi.spyOn(document.documentElement, 'clientHeight', 'get').mockReturnValue(1194)
    vi.stubGlobal('innerWidth', 417) // what Safari reports pinched in to 2x
    vi.stubGlobal('innerHeight', 597)
    expect(layoutSize()).toEqual({ width: 834, height: 1194 })
    vi.restoreAllMocks()
  })
})
