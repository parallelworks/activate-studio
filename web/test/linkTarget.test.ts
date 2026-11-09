import { afterEach, describe, expect, it } from 'vitest'
import { linkTarget, setLibrary, settleLibraries } from '../src/api'

afterEach(() => setLibrary('kb'))

describe('a file link opened in the open library', () => {
  it('keeps its path in one library', () => {
    setLibrary('kb')
    expect(linkTarget('ihelp-docs/COMBINE.pdf')).toBe('ihelp-docs/COMBINE.pdf')
  })

  it('with All libraries open, a chat link with no library opens in the main one', () => {
    settleLibraries(['kb', 'far'])
    setLibrary('all')
    expect(linkTarget('ihelp-docs/COMBINE.pdf')).toBe('kb/ihelp-docs/COMBINE.pdf')
    expect(linkTarget('specs/pump.pdf', 'far')).toBe('far/specs/pump.pdf')
  })

  it('with All libraries open, a path that already starts with a library is left as it is', () => {
    settleLibraries(['kb', 'far'])
    setLibrary('all')
    expect(linkTarget('far/ai-papers/a.pdf')).toBe('far/ai-papers/a.pdf')
  })
})
