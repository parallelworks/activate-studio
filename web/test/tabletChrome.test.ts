import { describe, expect, it } from 'vitest'
import { chromeWatch, EDGE_TRAVEL, HIDE_AFTER, SHOW_AFTER, TAP_ZONE, type ChromeState } from '../src/tabletChrome'

// The decisions behind the touch screen's chrome: a person's finger and
// fling hide and show it, a script's scrolling does not, a tap at the top
// brings it back and a swipe at the left edge opens the navigation.
function setup() {
  let t = 0
  const seen: ChromeState[] = []
  const w = chromeWatch(s => seen.push(s), () => t)
  const el = {}
  /** A finger from y0 moving by dy (negative: up the screen, reading on). */
  const drag = (y0: number, dy: number, x = 400) => { w.touchStart(x, y0, false); for (let i = 1; i <= 4; i++) w.touchMove(x, y0 + dy * i / 4); w.touchEnd() }
  return { w, el, at: (top: number) => w.scroll(el, top), drag, seen, later: (ms: number) => { t += ms } }
}

describe('a touch screen\'s chrome', () => {
  it('hides when a finger reads on down a page and shows when it reads back up', () => {
    const { w, at, drag, later } = setup()
    at(400)
    drag(600, -HIDE_AFTER / 2)
    expect(w.state().hidden).toBe(false)
    later(2000); drag(600, -HIDE_AFTER - 10)
    expect(w.state().hidden).toBe(true)
    later(2000); drag(600, SHOW_AFTER + 4)
    expect(w.state().hidden).toBe(false)
  })

  it('leaves the page\'s own scrolling alone while the finger is down', () => {
    const { w, at } = setup()
    at(4600)
    w.touchStart(400, 600, false)
    for (const top of [4400, 4200, 4000, 3800]) at(top) // the finger reading back
    w.touchMove(400, 640); w.touchMove(400, 600) // net: nothing
    at(3700); at(3500); at(3600) // the chat's rows loading above
    expect(w.state().hidden).toBe(false)
    w.touchEnd()
  })

  it('carries on through a fling after the finger lifts, and not a script\'s scrolling later', () => {
    const { w, at, later } = setup()
    at(400)
    w.touchStart(400, 600, false); w.touchEnd()
    later(100); at(460); at(520)
    expect(w.state().hidden).toBe(true)
    w.reset(); at(520)
    later(5000); at(900) // a reply streaming in moves the chat
    expect(w.state().hidden).toBe(false)
  })

  it('shows at the top of a page and on a tap near the top edge', () => {
    const { w, at, drag, later } = setup()
    at(400); drag(600, -200)
    expect(w.state().hidden).toBe(true)
    later(2000); w.touchStart(400, TAP_ZONE - 4, false); w.touchEnd()
    expect(w.state().hidden).toBe(false)
    later(2000); drag(600, -200)
    expect(w.state().hidden).toBe(true)
    at(4)
    expect(w.state().hidden).toBe(false)
  })

  it('opens the navigation from a swipe at the left edge and closes it on a tap outside', () => {
    const { w, at, drag, later } = setup()
    at(400); drag(600, -200); later(2000)
    w.touchStart(8, 500, false)
    w.touchMove(8 + EDGE_TRAVEL / 2, 505)
    expect(w.state().navOpen).toBe(false)
    w.touchMove(8 + EDGE_TRAVEL + 4, 510)
    expect(w.state().navOpen).toBe(true)
    w.touchEnd()
    w.touchStart(100, 300, true) // inside the navigation
    expect(w.state().navOpen).toBe(true)
    w.touchEnd(); w.touchStart(600, 300, false)
    expect(w.state().navOpen).toBe(false)
  })

  it('does not take a vertical drag at the edge, or an edge swipe while the navigation shows', () => {
    const { w, at, drag, later } = setup()
    w.touchStart(8, 500, false); w.touchMove(8 + EDGE_TRAVEL + 4, 510); w.touchEnd()
    expect(w.state().navOpen).toBe(false)
    at(400); drag(600, -200); later(2000)
    w.touchStart(8, 300, false); w.touchMove(60, 500); w.touchEnd()
    expect(w.state().navOpen).toBe(false)
  })
})
