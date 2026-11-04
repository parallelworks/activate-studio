import { readFileSync } from 'node:fs'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'
import { isShellBox, onShellScroll } from '../src/shellScroll'

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
