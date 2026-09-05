import { describe, expect, it } from 'vitest'
import { selectMessageBlocks } from '../src/components/ConversationScrubber'

const block = (text: string, className = '', queued = false) => ({
  textContent: text, className, querySelector: (sel: string) => (queued && sel.includes('queued') ? {} : null),
})

describe('selectMessageBlocks', () => {
  it('keeps messages and drops the empty spacer and queued blocks', () => {
    const rows = [block('hello'), block(''), block('   '), block('queued draft', 'group/queued'), block('nested', '', true), block('reply')]
    expect(selectMessageBlocks(rows).map(b => b.textContent)).toEqual(['hello', 'reply'])
  })
})
