import { describe, expect, it } from 'vitest'
import { inkOn } from '../src/components/CorpusMaps'

describe('treemap label ink', () => {
  it('is white on a dark cell and dark on a light one, brightness included', () => {
    expect(inkOn('hsl(150 60% 30%)')).toBe('#fff')
    expect(inkOn('hsl(205 68% 44%)')).toBe('#fff')
    expect(inkOn('hsl(164 68% 62%)', 1.18)).toBe('#0f172a')
    expect(inkOn('hsl(60 90% 70%)')).toBe('#0f172a')
  })
})
