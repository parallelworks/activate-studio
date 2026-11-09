import { describe, expect, it } from 'vitest'
import { findPattern, findTerms } from '../src/components/Viewer'

describe('findTerms', () => {
  it('drops quotes, operators, and one-letter fragments', () => {
    expect(findTerms('"tax identification" OR TIN -x')).toEqual(['tax', 'identification', 'TIN', '-x'.replace(/^["'(]+|["')]+$/g, '')].filter(t => t.length >= 2))
    expect(findTerms('a AND b')).toEqual([])
  })
})

describe('findPattern', () => {
  const hits = (q: string, text: string) => text.match(findPattern(findTerms(q))) ?? []

  it('matches a term across any punctuation or space, as search reads words', () => {
    expect(hits('NIST_SP811', 'a printing of NIST SP811 corrects; see nist-sp811 and NIST_SP811.pdf')).toEqual(['NIST SP811', 'nist-sp811', 'NIST_SP811'])
  })

  it('keeps a plain word, and a term with one part, as typed', () => {
    expect(hits('mesh C++', 'Mesh files in C++ and C')).toEqual(['Mesh', 'C++'])
    // Two parts with an operator between: the operator is punctuation.
    expect(hits('(a+b)*', 'x (a+b)* y')).toEqual(['a+b'])
  })
})
