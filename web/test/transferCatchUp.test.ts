import { describe, expect, it, vi } from 'vitest'

// An upload into My library that lands after the person switched to another
// library still has My library's index watched, not the one now selected.
const sel = vi.hoisted(() => ({ lib: 'u-space-ada' }))
const api = vi.hoisted(() => ({
  uploadFiles: vi.fn(),
  sendListed: vi.fn(),
  startIndexJob: vi.fn(async () => ({ id: 1 })),
  indexJob: vi.fn(async () => ({ state: 'done', ms: 5 })),
  job: vi.fn(),
}))
vi.mock('../src/api', () => ({ api, currentLibrary: () => sel.lib }))
const libraryWritten = vi.hoisted(() => vi.fn())
vi.mock('../src/libraryCatchUp', () => ({ libraryWritten }))

import { startUpload } from '../src/transfers'

describe('the catch-up after an upload', () => {
  it('watches the library written to, not the one selected when it lands', async () => {
    let land!: () => void
    api.uploadFiles.mockImplementation(async (batch: { rel: string }[]) => {
      await new Promise<void>(r => { land = r })
      return { saved: batch.map(b => b.rel), deferred: true, reindexing: true }
    })
    const done = startUpload([{ file: new File(['x'], 'a.md'), rel: 'a.md' }], 'uploads')
    await vi.waitFor(() => expect(land).toBeTypeOf('function'))
    sel.lib = 'kb'
    land()
    await done
    expect(libraryWritten).toHaveBeenCalled()
    for (const c of libraryWritten.mock.calls) expect(c[0]).toBe('u-space-ada')
  }, 8000)
})
