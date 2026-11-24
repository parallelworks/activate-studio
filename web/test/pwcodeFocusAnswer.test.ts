import { afterEach, describe, expect, it } from 'vitest'
import { focusComposer, focusWaiting } from '../src/pwcode/session/focusAnswer'

function pane(html: string): HTMLElement {
  const el = document.createElement('div')
  el.innerHTML = html
  document.body.appendChild(el)
  return el
}
afterEach(() => { document.body.innerHTML = '' })

const thread = `
  <button>Copy</button><button>Read 3 files</button>
  <div data-testid="approval-part"><button disabled>Allowed</button></div>
  <div data-testid="approval-part"><button>Yes</button><button>No</button></div>
  <textarea disabled></textarea>`

describe('a keyboard user, when the session waits on them', () => {
  it('is taken to the first answer of the open card, past every earlier message', () => {
    const p = pane(thread)
    expect(focusWaiting(p)).toBe(true)
    expect(document.activeElement?.textContent).toBe('Yes')
  })
  it('is taken to the plan answer under the transcript first', () => {
    const p = pane(thread + '<div data-testid="pwcode-plan-answer"><button>Approve plan</button></div>')
    focusWaiting(p)
    expect(document.activeElement?.textContent).toBe('Approve plan')
  })
  it('keeps focus they put somewhere else', () => {
    const p = pane(thread)
    const other = document.createElement('input')
    document.body.appendChild(other); other.focus()
    expect(focusWaiting(p)).toBe(false)
    expect(document.activeElement).toBe(other)
  })
  it('is taken from the conversations list to a card waiting as the chat opens, and only then', () => {
    const list = document.createElement('div')
    list.className = 'chat-canvas'
    list.innerHTML = '<aside><ul><li><a href="#c1">Plan for notes</a></li></ul></aside>'
    document.body.appendChild(list)
    const p = pane(thread)
    const row = list.querySelector('a')!
    row.focus()
    expect(focusWaiting(p)).toBe(false)
    expect(document.activeElement).toBe(row)
    expect(focusWaiting(p, true)).toBe(true)
    expect(document.activeElement?.textContent).toBe('Yes')
  })
  it('goes back to the message box once answered', () => {
    const p = pane('<button>Copy</button><textarea aria-label="Send a message"></textarea>')
    expect(focusComposer(p)).toBe(true)
    expect(document.activeElement?.getAttribute('aria-label')).toBe('Send a message')
  })
})
