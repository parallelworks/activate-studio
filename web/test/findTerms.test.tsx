import { describe, expect, it } from 'vitest'
import { findTerms } from '../src/components/Viewer'

describe('findTerms', () => {
  it('drops quotes, operators, and one-letter fragments', () => {
    expect(findTerms('"tax identification" OR TIN -x')).toEqual(['tax', 'identification', 'TIN', '-x'.replace(/^["'(]+|["')]+$/g, '')].filter(t => t.length >= 2))
    expect(findTerms('a AND b')).toEqual([])
  })
})
