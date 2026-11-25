import { describe, expect, it } from 'vitest'
import { copyName, textBytes, TEXT_MAX, toEdit, toFile } from '../src/textFiles'

describe('a text file in the editor', () => {
  it('comes back as it was: line endings, byte order mark, last line', () => {
    for (const s of ['', 'one', 'one\n', 'one\r\ntwo\r\n', '﻿one\r\ntwo', '﻿', '\n\n', '\r\n']) {
      const e = toEdit(s)!
      expect(e.text).not.toMatch(/[\r﻿]/)
      expect(toFile(e.text, e.form)).toBe(s)
    }
  })

  it('is not edited when its lines end in more than one way', () => {
    for (const s of ['a\r\nb\n', 'a\nb\r\n', 'a\rb', 'a\r\n\nb', '\n\r\n']) expect(toEdit(s), JSON.stringify(s)).toBeNull()
  })

  it('counts bytes, not characters', () => {
    expect(textBytes('a'.repeat(TEXT_MAX))).toBe(TEXT_MAX)
    expect(textBytes('é'.repeat(TEXT_MAX / 2 + 1))).toBe(TEXT_MAX + 2)
  })

  it('is copied beside itself', () => {
    expect(copyName('notes/plan.md', 1)).toBe('notes/plan (copy).md')
    expect(copyName('notes/plan.md', 2)).toBe('notes/plan (copy 2).md')
    expect(copyName('a.b/Makefile', 1)).toBe('a.b/Makefile (copy)')
  })
})
