import { describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  uploadFiles: vi.fn(),
  sendListed: vi.fn(),
  uploadTarget: () => ({ query: '', back: (p: string) => p }),
  startIndexJob: vi.fn(async () => ({ id: 1 })),
  indexJob: vi.fn(async () => ({ state: 'done', ms: 5 })),
  job: vi.fn(),
}))
vi.mock('../src/api', async () => ({ ...(await vi.importActual<object>('../src/api')), api }))
const libraryWritten = vi.hoisted(() => vi.fn())
vi.mock('../src/libraryCatchUp', () => ({ libraryWritten }))

import { setLibrary } from '../src/api'
import { startUpload } from '../src/transfers'

describe('the catch-up after an upload', () => {
  // An upload into My library that lands after the person switched to
  // another library still has My library's index watched, not the one now
  // selected.
  it('watches the library written to, not the one selected when it lands', async () => {
    libraryWritten.mockClear()
    setLibrary('u-space-ada')
    let land!: () => void
    api.uploadFiles.mockImplementation(async (batch: { rel: string }[]) => {
      await new Promise<void>(r => { land = r })
      return { saved: batch.map(b => b.rel), deferred: true, reindexing: true }
    })
    const done = startUpload([{ file: new File(['x'], 'a.md'), rel: 'a.md' }], 'uploads')
    await vi.waitFor(() => expect(land).toBeTypeOf('function'))
    setLibrary('kb')
    land()
    await done
    expect(libraryWritten).toHaveBeenCalled()
    for (const c of libraryWritten.mock.calls) expect(c[0]).toBe('u-space-ada')
  }, 8000)

  // With All libraries showing, the folder's path names its library: that
  // library's index is watched, not "all" (which is watched by nothing, so
  // the Details card kept the new file at 0 B; phase 4, C2).
  it('in All libraries, watches the library the folder is in', async () => {
    libraryWritten.mockClear()
    setLibrary('all')
    api.uploadFiles.mockImplementation(async (batch: { rel: string }[]) => ({ saved: batch.map(b => b.rel), deferred: true, reindexing: true }))
    await startUpload([{ file: new File(['x'], 'b.md'), rel: 'b.md' }], 'u-space-ada/uploads')
    expect(libraryWritten).toHaveBeenCalled()
    for (const c of libraryWritten.mock.calls) expect(c[0]).toBe('u-space-ada')
  }, 8000)
})
