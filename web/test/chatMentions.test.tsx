import { render } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { mentionQuery, mentionToken } from '../src/components/MentionPalette'
import { parseMention } from '../src/components/MentionLinks'
import { breakable } from '../src/components/breakable'
import { copyWithTextarea, installClipboardFallback } from '../src/clipboard'
import { EFFORTS, getEffort, hasEffort, setEffort } from '../src/labelScope'

describe('@ file picker', () => {
  it('reads the query the caret is in', () => {
    expect(mentionQuery('Summarize @', 11)).toEqual({ start: 10, query: '' })
    expect(mentionQuery('Summarize @rep', 14)).toEqual({ start: 10, query: 'rep' })
    expect(mentionQuery('@reports/', 9)).toEqual({ start: 0, query: 'reports/' })
    // Not mid word (an email), not past a space, not inside a written token.
    expect(mentionQuery('mail me@example.org', 19)).toBeNull()
    expect(mentionQuery('@rep and', 8)).toBeNull()
    expect(mentionQuery('@[reports/a.md]', 15)).toBeNull()
  })
  it('writes the token, with the library when it is not the main one', () => {
    expect(mentionToken('reports/a b.md', 'kb', 'kb')).toBe('@[reports/a b.md]')
    expect(mentionToken('specs/pump.pdf', 'archive', 'kb')).toBe('@[archive:specs/pump.pdf]')
  })
  it('reads a token back to its file', () => {
    expect(parseMention('archive:specs/pump.pdf', ['kb', 'archive'])).toEqual({ path: 'specs/pump.pdf', lib: 'archive' })
    // A colon that names no library is part of the path.
    expect(parseMention('notes:plan.md', ['kb'])).toEqual({ path: 'notes:plan.md' })
    expect(parseMention(' reports/a.md ', ['kb'])).toEqual({ path: 'reports/a.md' })
  })
})

describe('paths in table cells', () => {
  it('may break after separators and nowhere else, with the text unchanged', () => {
    const { container } = render(<code>{breakable('structures/aft_section_fem_v3.inp')}</code>)
    expect(container.textContent).toBe('structures/aft_section_fem_v3.inp')
    expect(container.querySelectorAll('wbr').length).toBe(5)
    expect(container.innerHTML).toContain('structures/<wbr>aft_<wbr>section_<wbr>fem_<wbr>v3.<wbr>inp')
  })
  it('leaves short words and nested elements whole', () => {
    const { container } = render(<a>{breakable(['Under review', <span key="s">reports/site-survey-notes.md</span>])}</a>)
    expect(container.textContent).toBe('Under reviewreports/site-survey-notes.md')
    expect(container.querySelector('span')?.querySelectorAll('wbr').length).toBe(4)
  })
})

describe('reasoning effort', () => {
  afterEach(() => localStorage.clear())
  it('offers the model default and four levels', () => {
    expect(EFFORTS.map(e => e.value)).toEqual(['', 'low', 'medium', 'high', 'max'])
  })
  it('keeps one per conversation, and a new chat starts from the last chosen', () => {
    expect(getEffort('c1')).toBe('')
    setEffort('high', 'c1')
    expect(getEffort('c1')).toBe('high')
    expect(hasEffort('c1')).toBe(true)
    setEffort('low', 'c2')
    expect(getEffort('c1')).toBe('high')
    expect(getEffort('c3')).toBe('low')
    expect(hasEffort('c3')).toBe(false)
    setEffort('bogus', 'c1')
    expect(getEffort('c1')).toBe('')
  })
})

describe('copy where the clipboard is refused', () => {
  afterEach(() => vi.restoreAllMocks())
  it('falls back to a textarea copy', async () => {
    const refused = vi.fn().mockRejectedValue(new DOMException('no', 'NotAllowedError'))
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: refused }, configurable: true })
    const exec = vi.fn().mockReturnValue(true)
    Object.defineProperty(document, 'execCommand', { value: exec, configurable: true })
    installClipboardFallback()
    await expect(navigator.clipboard.writeText('pw code mcp add')).resolves.toBeUndefined()
    expect(refused).toHaveBeenCalledWith('pw code mcp add')
    expect(exec).toHaveBeenCalledWith('copy')
    expect(document.querySelector('textarea')).toBeNull()
  })
  it('still reports a copy that fails both ways', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: vi.fn().mockRejectedValue(new Error('no')) }, configurable: true })
    Object.defineProperty(document, 'execCommand', { value: vi.fn().mockReturnValue(false), configurable: true })
    installClipboardFallback()
    await expect(navigator.clipboard.writeText('x')).rejects.toThrow()
    expect(copyWithTextarea('x')).toBe(false)
  })
})
