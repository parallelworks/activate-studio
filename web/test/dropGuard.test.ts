import { afterEach, describe, expect, it } from 'vitest'
import { guardDrops } from '../src/dropGuard'

guardDrops()
afterEach(() => { document.body.innerHTML = '' })

/** Whether the page took the drag over `el` from the browser. */
function taken(el: Element, type: 'dragover' | 'drop', types: string[]): boolean {
  const e = new Event(type, { bubbles: true, cancelable: true })
  Object.defineProperty(e, 'dataTransfer', { value: { types } })
  el.dispatchEvent(e)
  return e.defaultPrevented
}
function put<T extends HTMLElement>(html: string): T {
  document.body.innerHTML = html
  return document.body.firstElementChild as T
}

describe('drops no drop zone takes', () => {
  it.each(['dragover', 'drop'] as const)('a file or a link never replaces the page (%s)', type => {
    expect(taken(put('<div></div>'), type, ['Files'])).toBe(true)
    expect(taken(put('<textarea></textarea>'), type, ['Files'])).toBe(true)
    expect(taken(put('<div></div>'), type, ['text/uri-list', 'text/plain'])).toBe(true)
  })

  it.each(['dragover', 'drop'] as const)('text dragged into a field goes in as the browser does it (%s)', type => {
    expect(taken(put('<textarea></textarea>'), type, ['text/plain'])).toBe(false)
    expect(taken(put('<input>'), type, ['text/plain'])).toBe(false)
    expect(taken(put('<div contenteditable="true"></div>'), type, ['text/plain'])).toBe(false)
    expect(taken(put('<textarea disabled></textarea>'), type, ['text/plain'])).toBe(true)
  })
})
