import { describe, expect, it } from 'vitest'
import { plural } from '../src/plural'
import { indexSummary } from '../src/indexActivity'
import { countedIn } from '../src/components/StatusFooter'
import { libraryLines } from '../src/components/StatusPanel'
import type { IndexStatus } from '../src/api'

const idx = { indexed: true, running: false } as unknown as IndexStatus
const libs = [{ id: 'kb', label: 'Projects', primary: true }, { id: 'u-space-ada', label: 'My library', primary: false }]

describe('file counts', () => {
  it('say one file, not 1 files', () => {
    expect(plural(1, 'file')).toBe('1 file')
    expect(plural(2, 'file')).toBe('2 files')
    expect(plural(1200, 'directory', 'directories')).toBe('1,200 directories')
    expect(indexSummary(idx, 1).text).toBe('1 file indexed')
    const line = libraryLines([{ id: 'u', label: 'Mine', primary: false, writable: true, caps: { index: true, fullText: true, vectors: false } } as never], null,
      { files: 1, libraries: [{ id: 'u', label: 'Mine', available: true, files: 1 }] } as never)[0]
    expect(line.count).toBe('1 file')
  })

  it('name the library the footer counts when it is not the main one', () => {
    expect(countedIn(libs, 'kb')).toBeUndefined()
    expect(countedIn(libs, 'u-space-ada')).toBe('My library')
    expect(countedIn(libs, 'all')).toBe('all libraries')
    expect(indexSummary(idx, 3, countedIn(libs, 'u-space-ada')).text).toBe('3 files indexed in My library')
    expect(indexSummary(idx, 3, countedIn(libs, 'kb')).text).toBe('3 files indexed')
  })
})
