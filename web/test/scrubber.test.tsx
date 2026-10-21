import { describe, expect, it } from 'vitest'
import { currentTick, selectMessageBlocks } from '../src/components/ConversationScrubber'

const block = (text: string, className = '', queued = false) => ({
  textContent: text, className, querySelector: (sel: string) => (queued && sel.includes('queued') ? {} : null),
})

describe('selectMessageBlocks', () => {
  it('keeps messages and drops the empty spacer and queued blocks', () => {
    const rows = [block('hello'), block(''), block('   '), block('queued draft', 'animate-chat-enter chat-queued'), block('nested', '', true), block('reply')]
    expect(selectMessageBlocks(rows).map(b => b.textContent)).toEqual(['hello', 'reply'])
  })
})

describe('the current tick', () => {
  const tops = [-900, -400, -20, 6, 120, 300, 520]
  it('is the last message whose top reached the top of the thread', () => {
    expect(currentTick(tops, false, null)).toBe(3)
    expect(currentTick([40, 300], false, null)).toBe(0)
  })
  it('is the last message at the end of the thread, where the last few never reach the top', () => {
    expect(currentTick(tops, true, null)).toBe(6)
  })
  it('is the one clicked at the end, while the thread stays there', () => {
    expect(currentTick(tops, true, 5)).toBe(5)
    expect(currentTick(tops, false, 5)).toBe(3)
    expect(currentTick(tops, true, 99)).toBe(6)
    expect(currentTick([], true, null)).toBe(0)
  })
})
