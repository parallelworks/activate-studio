import { describe, expect, it } from 'vitest'
import { fitControls, noticePad } from '../src/chatTop'

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
