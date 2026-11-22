import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import { CONTINUED_NOTE_MARK, markNotes } from '../src/adapter'

// Report H (retest phase 4): the note that opens a chat continued from a
// pw code session offered Regenerate response.
describe('the note opening a continued chat', () => {
  const note = () => ({
    messages: [
      { id: 'note-continued-1', content: 'Continued from the pw code session **x** on box. [See the session on the Code page](#view=code?machine=m1&session=s1)' },
      { id: 'pwh-0-a', content: 'A reply that links [the session](#view=code?machine=m1&session=s1).' },
    ],
  })

  it('marks the note\'s link, and only the note\'s, once', () => {
    const c = markNotes(markNotes(note()))
    expect(c.messages[0].content).toContain(`](#view=code?${CONTINUED_NOTE_MARK}&machine=m1&session=s1)`)
    expect(c.messages[0].content.split(CONTINUED_NOTE_MARK)).toHaveLength(2)
    expect(c.messages[1].content).not.toContain(CONTINUED_NOTE_MARK)
  })

  it('hides Regenerate in the message holding the marked link, not in a reply', () => {
    const css = readFileSync('src/styles.css', 'utf8')
    const rule = css.split('\n').find(l => l.includes(CONTINUED_NOTE_MARK) && l.includes('Regenerate response'))
    expect(rule).toBeTruthy()
    const selector = rule!.slice(0, rule!.indexOf('{')).trim()
    const c = markNotes(note())
    document.body.innerHTML = `<div class="group-list">${c.messages.map(m => `<div class="group"><div><a href="${m.content.match(/\]\(([^)]+)\)/)![1]}">link</a></div><div class="chat-actions"><button aria-label="Regenerate response"></button></div></div>`).join('')}</div>`
    const [first, second] = document.querySelectorAll('button[aria-label="Regenerate response"]')
    expect(first.matches(selector)).toBe(true)
    expect(second.matches(selector)).toBe(false)
  })
})
