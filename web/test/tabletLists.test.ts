import { describe, expect, it } from 'vitest'
import { chatListPlacement, CODE_DRAWER_QUERY, TOUCH_QUERY } from '../src/phoneLayout'

// An upright iPad: the conversations and sessions lists move to their drawers
// so the conversation is not left about 270px wide. A desktop window keeps
// them beside the thread down to a phone's width.
describe('the lists beside a conversation on a tablet', () => {
  it('puts the Assistant\'s list in the drawer on a touch screen when the chat area is narrow', () => {
    expect(chatListPlacement(false, true)).toBe('auto')
  })

  it('keeps the list beside the thread in a desktop window, and in the drawer on a phone', () => {
    expect(chatListPlacement(false, false)).toBe('inline')
    expect(chatListPlacement(true, false)).toBe('drawer')
    expect(chatListPlacement(true, true)).toBe('drawer')
  })

  it('opens the Code page\'s sessions over the page on a touch screen under a tablet held sideways', () => {
    const [phone, tablet] = CODE_DRAWER_QUERY.split(', ')
    expect(phone).toBe('(max-width: 720px)')
    expect(tablet).toBe(`${TOUCH_QUERY} and (max-width: 1099px)`)
  })
})
