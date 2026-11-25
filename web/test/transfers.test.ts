import { afterEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  uploadFiles: vi.fn(),
  sendListed: vi.fn(),
  uploadTarget: vi.fn((dir: string) => ({ query: `?dir=${dir}`, back: (p: string) => p })),
  startParts: vi.fn(),
  sendPart: vi.fn(),
  sendListedPart: vi.fn(),
  finishParts: vi.fn(),
  dropParts: vi.fn(async () => {}),
  startIndexJob: vi.fn(async () => ({ id: 1 })),
  indexJob: vi.fn(async () => ({ state: 'done', ms: 5 })),
  job: vi.fn(),
}))
vi.mock('../src/api', () => ({ api, currentLibrary: () => 'kb', libraryOf: () => 'kb' }))

import { act, renderHook } from '@testing-library/react'
import { collectChosen, collectListed, RETRY_MS, uploadBatched } from '../src/upload'
import { startPathJob, startUpload, useTransfers } from '../src/transfers'

const MB = 1024 * 1024
/** A file of a size, with no bytes to hold. */
const large = (rel: string, size: number, linked?: string) => {
  const file = new File(['x'], rel.split('/').pop()!)
  Object.defineProperty(file, 'size', { value: size })
  file.slice = (a = 0, b = 0) => ({ from: a, size: b - a }) as unknown as Blob
  return { file, rel, ...(linked ? { linked, size } : {}) }
}
const failed = (status: number, message = 'no') => Object.assign(new Error(message), { status })
const PART = 16 * MB
const serveParts = () => {
  api.startParts.mockImplementation(async (rel: string, size: number) => ({ id: `id-${rel}`, part: PART, parts: Math.ceil(size / PART), path: rel }))
  api.sendPart.mockResolvedValue(undefined)
  api.sendListedPart.mockResolvedValue({})
  api.finishParts.mockImplementation(async (id: string) => ({ saved: [id], deferred: true }))
}

const item = (rel: string) => ({ file: new File(['x'], rel.split('/').pop()!), rel })

afterEach(() => { vi.clearAllMocks() })

describe('uploads', () => {
  it('keep a chosen folder\'s paths and leave out dot names', () => {
    const f = (name: string, rel: string) => Object.defineProperty(new File(['x'], name), 'webkitRelativePath', { value: rel })
    const got = collectChosen([f('a.md', 'top/sub/a.md'), f('.DS_Store', 'top/.DS_Store'), f('b.md', 'top/.git/b.md')])
    expect(got.map(i => i.rel)).toEqual(['top/sub/a.md'])
  })

  it('take turns: the second sends once the first has', async () => {
    let finishFirst!: () => void
    const order: string[] = []
    api.uploadFiles.mockImplementation(async (batch: { rel: string }[]) => {
      order.push(batch[0].rel)
      if (batch[0].rel === 'first.md') await new Promise<void>(r => { finishFirst = r })
      return { saved: batch.map(b => b.rel) }
    })
    const one = startUpload([item('first.md')], 'd')
    const two = startUpload([item('second.md')], 'd')
    await vi.waitFor(() => expect(order).toEqual(['first.md']))
    await new Promise(r => setTimeout(r, 20))
    expect(order).toEqual(['first.md'])
    finishFirst()
    const [a, b] = await Promise.all([one, two])
    expect(order).toEqual(['first.md', 'second.md'])
    expect([a.done, b.done]).toEqual([1, 1])
  }, 8000)

  it('leave a library that keeps its own index to it: no main library index job', async () => {
    // My library on a system: the upload reply says its index is being
    // updated, and the main library's job would index its folder of that name.
    api.uploadFiles.mockImplementation(async (batch: { rel: string }[]) => ({ saved: batch.map(b => b.rel), deferred: true, reindexing: true }))
    const r = await startUpload([item('notes.md')], 'uploads')
    expect(api.startIndexJob).not.toHaveBeenCalled()
    expect(r.done).toBe(1)
    expect(r.indexError).toBeUndefined()
    expect(r.reindexing).toBe(true)
  }, 8000)

  it('ask for one index pass after an upload to the main library', async () => {
    api.uploadFiles.mockImplementation(async (batch: { rel: string }[]) => ({ saved: batch.map(b => b.rel), deferred: true }))
    const r = await startUpload([item('notes.md')], 'uploads')
    expect(api.startIndexJob).toHaveBeenCalledWith('uploads', expect.objectContaining({ query: '?dir=uploads' }))
    expect(r.indexMs).toBe(5)
  }, 8000)

  it('send a large file in parts, each its own request, and the small ones as before', async () => {
    serveParts()
    api.uploadFiles.mockImplementation(async (batch: { rel: string }[]) => ({ saved: batch.map(b => b.rel) }))
    const size = 3 * 1024 * MB + 5
    const r = await startUpload([large('sub/run.tar', size), item('small.md')], 'd')
    expect(api.uploadFiles.mock.calls[0][0].map((b: { rel: string }) => b.rel)).toEqual(['small.md'])
    expect(api.startParts).toHaveBeenCalledTimes(1)
    expect(api.startParts.mock.calls[0].slice(0, 2)).toEqual(['sub/run.tar', size])
    const sent = api.sendPart.mock.calls.map(c => [c[0], c[1], c[2].from, c[2].size]).sort((a, b) => a[1] - b[1])
    expect(sent.length).toBe(193)
    expect(sent[0]).toEqual(['id-sub/run.tar', 0, 0, PART])
    expect(sent[192]).toEqual(['id-sub/run.tar', 192, 192 * PART, 5])
    expect(api.finishParts).toHaveBeenCalledTimes(1)
    expect(api.dropParts).not.toHaveBeenCalled()
    expect([r.done, r.failed, r.bytes]).toEqual([2, [], size + 1])
  }, 8000)

  it('move the bar by what each part has sent', async () => {
    vi.useFakeTimers()
    serveParts()
    const seen: number[] = []
    let tries = 0
    api.sendPart.mockImplementation(async (_id: string, n: number, _b: Blob, o: { onBytes?: (n: number) => void }) => {
      o.onBytes?.(PART / 2)
      if (n === 1 && tries++ === 0) throw failed(0, 'connection failed')
    })
    const up = uploadBatched([large('a.tar', 4 * PART)], 'd', p => { seen.push(p.bytes) })
    await vi.advanceTimersByTimeAsync(RETRY_MS[0] + 2000)
    const r = await up
    expect(tries).toBe(2)
    expect(seen).toContain(PART / 2)
    expect(seen.filter(b => b % PART === 0 && b > 0).length).toBeGreaterThan(2)
    expect(Math.max(...seen)).toBe(4 * PART)
    expect([r.done, r.bytes]).toEqual([1, 4 * PART])
    vi.useRealTimers()
  }, 8000)

  it('send a part again a few times, then take the file back and go on to the next', async () => {
    vi.useFakeTimers()
    serveParts()
    api.sendPart.mockImplementation(async (id: string, n: number) => { if (id === 'id-a.tar' && n === 1) throw failed(502, 'bad gateway') })
    const up = startUpload([large('a.tar', 3 * PART), large('b.tar', 2 * PART)], 'd')
    await vi.advanceTimersByTimeAsync(RETRY_MS.reduce((a, b) => a + b, 0) + 3000)
    const r = await up
    expect(api.sendPart.mock.calls.filter(c => c[0] === 'id-a.tar' && c[1] === 1).length).toBe(RETRY_MS.length + 1)
    expect(api.dropParts.mock.calls).toEqual([['id-a.tar']])
    expect(r.failed).toEqual([{ name: 'a.tar', error: 'bad gateway' }])
    expect(api.finishParts.mock.calls.map(c => c[0])).toEqual(['id-b.tar'])
    expect(r.done).toBe(1)
    vi.useRealTimers()
  }, 8000)

  it('do not send again what was refused: no room, no rights', async () => {
    serveParts()
    api.startParts.mockRejectedValueOnce(failed(507, 'not enough free space: it needs 3.0 GB and 1.0 GB is free'))
    const full = await startUpload([large('a.tar', 3 * PART)], 'd')
    expect(full.failed).toEqual([{ name: 'a.tar', error: 'not enough free space: it needs 3.0 GB and 1.0 GB is free' }])
    expect(api.startParts).toHaveBeenCalledTimes(1)
    api.sendPart.mockRejectedValue(failed(403, 'read only'))
    const refused = await startUpload([large('a.tar', PART + 1)], 'd')
    expect(refused.failed[0].error).toBe('read only')
    expect(api.dropParts).toHaveBeenCalledWith('id-a.tar')
    for (const n of [0, 1]) expect(api.sendPart.mock.calls.filter(c => c[1] === n).length).toBeLessThanOrEqual(1)
  }, 8000)

  it('take a large file back when it is cancelled, and when the page goes', async () => {
    serveParts()
    api.sendPart.mockImplementation((_i: string, _n: number, _b: Blob, o: { signal: AbortSignal }) => new Promise((_, no) => {
      o.signal.addEventListener('abort', () => no(new DOMException('cancelled', 'AbortError')))
    }))
    const { result } = renderHook(() => useTransfers())
    const up = startUpload([large('a.tar', 5 * PART)], 'd')
    await vi.waitFor(() => expect(api.sendPart).toHaveBeenCalledTimes(3))
    window.dispatchEvent(new Event('pagehide'))
    expect(api.dropParts).toHaveBeenCalledTimes(1)
    act(() => { result.current.at(-1)!.cancel!() })
    const r = await up
    expect(r.cancelled).toBe(true)
    expect(r.failed).toEqual([])
    expect(api.dropParts).toHaveBeenLastCalledWith('id-a.tar')
    expect(api.finishParts).not.toHaveBeenCalled()
  }, 8000)

  it('ask the window for the parts of a file it listed, where it can send them', async () => {
    serveParts()
    api.sendListed.mockImplementation(async (batch: { rel: string }[]) => ({ saved: batch.map(b => b.rel) }))
    const w = window as unknown as { __studioShell?: unknown }
    w.__studioShell = { sendsParts: true }
    const r = await startUpload([large('a.tar', PART + 3, 'abc')], 'd')
    expect(api.sendListedPart.mock.calls.map(c => c.slice(0, 5)).sort((a, b) => a[1] - b[1]))
      .toEqual([['id-a.tar', 0, 'abc', 0, PART], ['id-a.tar', 1, 'abc', PART, 3]])
    expect(r.done).toBe(1)
    // A window from before that sends it whole.
    w.__studioShell = {}
    await startUpload([large('b.tar', PART + 3, 'def')], 'd')
    expect(api.sendListed.mock.calls[0][0][0]).toMatchObject({ rel: 'b.tar', linked: 'def' })
    const o = await startUpload([large('c.tar', 201 * 1024 * 1024, 'ghi'), large('d.tar', PART + 3, 'jkl')], 'd')
    expect([o.done, o.failed.map(f => f.name)]).toEqual([1, ['c.tar']])
    delete w.__studioShell
  }, 8000)

  it('go to the library chosen when the files were added, whatever is chosen since', async () => {
    serveParts()
    let finishFirst!: () => void
    api.uploadFiles.mockImplementation(async (batch: { rel: string }[]) => {
      if (batch[0].rel === 'first.md') await new Promise<void>(r => { finishFirst = r })
      return { saved: batch.map(b => b.rel) }
    })
    const one = startUpload([item('first.md')], 'd')
    const two = startUpload([large('a.tar', PART + 1), item('b.md')], 'mine')
    await vi.waitFor(() => expect(api.uploadFiles).toHaveBeenCalledTimes(1))
    expect(api.uploadTarget.mock.calls.map(c => c[0])).toEqual(['d', 'mine'])
    api.uploadTarget.mockReturnValue({ query: '?library=other', back: (p: string) => p })
    finishFirst()
    await Promise.all([one, two])
    expect(api.startParts.mock.calls[0][2].query).toBe('?dir=mine')
    expect(api.uploadFiles.mock.calls[1][2].to.query).toBe('?dir=mine')
    api.uploadTarget.mockImplementation((dir: string) => ({ query: `?dir=${dir}`, back: (p: string) => p }))
  }, 8000)

  it('leave what the window listed to the window', async () => {
    api.sendListed.mockImplementation(async (batch: { rel: string }[]) => ({ saved: batch.map(b => b.rel) }))
    const r = await startUpload(collectListed([{ rel: 'top/sub/a.md', size: 3, id: 'abc' }]), 'd')
    expect(api.uploadFiles).not.toHaveBeenCalled()
    expect(api.sendListed.mock.calls[0][0][0]).toMatchObject({ rel: 'top/sub/a.md', linked: 'abc' })
    expect(r.done).toBe(1)
  }, 8000)

  it('cancelling one still queued sends nothing and leaves the first alone', async () => {
    let finishFirst!: () => void
    const sent: string[] = []
    api.uploadFiles.mockImplementation(async (batch: { rel: string }[]) => {
      sent.push(batch[0].rel)
      if (batch[0].rel === 'running.md') await new Promise<void>(r => { finishFirst = r })
      return { saved: batch.map(b => b.rel) }
    })
    const { result } = renderHook(() => useTransfers())
    const one = startUpload([item('running.md')], 'd')
    const two = startUpload([item('queued.md')], 'd')
    await vi.waitFor(() => expect(sent).toEqual(['running.md']))
    act(() => { result.current.find(t => t.phase === 'queued')!.cancel!() })
    const b = await two
    expect(b.cancelled).toBe(true)
    expect(b.done).toBe(0)
    finishFirst()
    expect((await one).done).toBe(1)
    expect(sent).toEqual(['running.md'])
  }, 8000)
})

describe('path jobs', () => {
  it('show the tree the change before indexing ends, and split long lists', async () => {
    const changed = vi.fn()
    window.addEventListener('ade-kb-changed', changed)
    api.job
      .mockResolvedValueOnce({ state: 'running', phase: 'indexing', done: 500, total: 500, current: '' })
      .mockResolvedValueOnce({ state: 'done', phase: 'done', done: 500, total: 500, current: '', result: { skipped: [] }, ms: 1 })
      .mockResolvedValueOnce({ state: 'done', phase: 'done', done: 3, total: 3, current: '', result: { skipped: [{ path: 'x', reason: 'not found' }] }, ms: 1 })
    const start = vi.fn(async () => ({ id: 7 }) as never)
    const paths = Array.from({ length: 503 }, (_, i) => `f${i}`)
    const r = await startPathJob('delete', paths, '', start)
    window.removeEventListener('ade-kb-changed', changed)
    expect(start).toHaveBeenCalledTimes(2)
    expect(r.done).toBe(502)
    expect(r.failed).toEqual([{ name: 'x', error: 'not found' }])
    expect(changed.mock.calls.length).toBeGreaterThanOrEqual(2)
  }, 8000)

  it('take an answer of what was done (no job) as done, and never poll a job without an id', async () => {
    const start = vi.fn(async () => ({ moved: [{ from: 'a.md', to: 'd/a.md' }], skipped: [{ path: 'b.md', reason: 'already there' }], indexMs: 0 }) as never)
    const r = await startPathJob('move', ['a.md', 'b.md'], 'd', start)
    expect(api.job).not.toHaveBeenCalled()
    expect(r.indexError).toBeUndefined()
    expect(r.done).toBe(1)
    expect(r.failed).toEqual([{ name: 'b.md', error: 'already there' }])
  })

  it('say why a move did not start, not "no such job"', async () => {
    const refused = await startPathJob('move', ['a.md'], 'd', vi.fn(async () => { throw new Error('Your library is read only here') }))
    expect(refused.indexError).toBe('Your library is read only here')
    const odd = await startPathJob('move', ['a.md'], 'd', vi.fn(async () => ({}) as never))
    expect(odd.indexError).toBe('the move did not start')
    expect(api.job).not.toHaveBeenCalled()
  })
})
