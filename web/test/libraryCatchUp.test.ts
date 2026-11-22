import { afterEach, describe, expect, it, vi } from 'vitest'

// After a change in a person's own library, the views are told at once and
// again when its index, rebuilt on its system a little later, is done.
const lib = { id: 'u-space-ada', state: 'ready', builtAt: '2026-11-22T10:00:00Z' as string | null }
const api = vi.hoisted(() => ({ userLibraries: vi.fn() }))
vi.mock('./../src/api', () => ({ api, currentLibrary: () => 'u-space-ada' }))

import { libraryWritten, resetCatchUpForTests, setCatchUpPollingForTests } from '../src/libraryCatchUp'
import { forgetSelectionUnder, librarySelection, renameSelection, selectLibraryEntry } from '../src/components/LibraryDetails'

api.userLibraries.mockImplementation(async () => ({ libraries: [{ ...lib }] }))
const told = vi.fn()
window.addEventListener('ade-kb-changed', told)
afterEach(() => { resetCatchUpForTests(); told.mockClear(); api.userLibraries.mockClear(); lib.state = 'ready'; lib.builtAt = '2026-11-22T10:00:00Z' })

describe('a change in a person\'s own library', () => {
  it('tells the views at once, and again once its index is rebuilt', async () => {
    setCatchUpPollingForTests(10)
    libraryWritten()
    expect(told).toHaveBeenCalledTimes(1)
    await new Promise(r => setTimeout(r, 40))
    expect(told).toHaveBeenCalledTimes(1)
    lib.state = 'updating'
    await new Promise(r => setTimeout(r, 30))
    expect(told).toHaveBeenCalledTimes(1)
    lib.state = 'ready'; lib.builtAt = '2026-11-22T10:00:25Z'
    await vi.waitFor(() => expect(told).toHaveBeenCalledTimes(2))
  })

  it('does not watch the main library, whose index status already says', async () => {
    setCatchUpPollingForTests(10)
    libraryWritten('kb')
    expect(told).toHaveBeenCalledTimes(1)
    await new Promise(r => setTimeout(r, 30))
    expect(api.userLibraries).not.toHaveBeenCalled()
  })
})

describe('the Details card\'s selection', () => {
  it('is cleared when what it shows is deleted, and follows a rename', () => {
    selectLibraryEntry({ path: 'notes/a.txt', isDir: false })
    renameSelection('notes/a.txt', 'notes/b.txt')
    expect(librarySelection()).toEqual({ path: 'notes/b.txt', isDir: false })
    renameSelection('notes', 'docs')
    expect(librarySelection()).toEqual({ path: 'docs/b.txt', isDir: false })
    forgetSelectionUnder('other')
    expect(librarySelection()?.path).toBe('docs/b.txt')
    forgetSelectionUnder('docs')
    expect(librarySelection()).toBeNull()
  })
})
