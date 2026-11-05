import { afterEach, describe, expect, it, vi } from 'vitest'

const api = vi.hoisted(() => ({
  uploadFiles: vi.fn(),
  sendListed: vi.fn(),
  startIndexJob: vi.fn(async () => ({ id: 1 })),
  indexJob: vi.fn(async () => ({ state: 'done', ms: 5 })),
  job: vi.fn(),
}))
vi.mock('../src/api', () => ({ api }))

import { act, renderHook } from '@testing-library/react'
import { collectChosen, collectListed } from '../src/upload'
import { startPathJob, startUpload, useTransfers } from '../src/transfers'

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

  it('turn away a file over the limit by name and send the rest', async () => {
    api.uploadFiles.mockImplementation(async (batch: { rel: string }[]) => ({ saved: batch.map(b => b.rel) }))
    const large = { ...item('large.bin'), size: 300 * 1024 * 1024 }
    const r = await startUpload([item('small.md'), large], 'd')
    expect(api.uploadFiles.mock.calls[0][0].map((b: { rel: string }) => b.rel)).toEqual(['small.md'])
    expect(r.done).toBe(1)
    expect(r.failed.map(f => f.name)).toEqual(['large.bin'])
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
})
