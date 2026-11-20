import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import * as ui from '@parallelworks/ui'
import { downloadPath, zipNotice } from '../src/libraryDownload'

let success: ReturnType<typeof vi.spyOn>
let error: ReturnType<typeof vi.spyOn>
let clicked: { href: string; download: string }[]
beforeEach(() => {
  success = vi.spyOn(ui.toast, 'success').mockImplementation(() => '' as never)
  error = vi.spyOn(ui.toast, 'error').mockImplementation(() => '' as never)
  clicked = []
  // jsdom does not navigate; record each link the page clicks instead.
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
    clicked.push({ href: this.getAttribute('href') ?? '', download: this.download })
  })
})
afterEach(() => vi.restoreAllMocks())

const answer = (status: number, body: unknown) =>
  vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))

describe('Download from the right-click menu', () => {
  it('downloads a file as itself, with no check first', async () => {
    const f = answer(200, {})
    await downloadPath('reports/plan.md', false)
    expect(f).not.toHaveBeenCalled()
    expect(clicked).toEqual([{ href: '/api/kb/download?path=reports%2Fplan.md', download: 'plan.md' }])
  })

  it('sizes a folder, then downloads it as a zip', async () => {
    const f = answer(200, { name: 'reports.zip', files: 12, bytes: 3_500_000 })
    await downloadPath('reports', true)
    expect(f).toHaveBeenCalledWith('/api/kb/download?path=reports&folder=1&check=1')
    expect(clicked).toEqual([{ href: '/api/kb/download?path=reports&folder=1', download: 'reports.zip' }])
    expect(success).toHaveBeenCalledWith('Downloading reports.zip: 12 files, 3.3 MB.')
  })

  it('says why a folder cannot be zipped, and saves nothing', async () => {
    answer(413, { error: 'this folder is over 50000 files or 8 GB, too large to download as a zip; download the folders inside it one at a time' })
    await downloadPath('archive', true)
    expect(clicked).toEqual([])
    expect(error).toHaveBeenCalledWith('Could not download archive: this folder is over 50000 files or 8 GB, too large to download as a zip; download the folders inside it one at a time')
  })

  it('says when a folder has nothing to download', async () => {
    answer(200, { name: 'empty.zip', files: 0, bytes: 0 })
    await downloadPath('empty', true)
    expect(clicked).toEqual([])
    expect(error).toHaveBeenCalledWith('empty has no files to download.')
  })

  it('counts one file as one file', () => {
    expect(zipNotice({ name: 'a.zip', files: 1, bytes: 900 })).toBe('Downloading a.zip: 1 file, 900 B.')
  })
})
