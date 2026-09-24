import { afterEach, describe, expect, it, vi } from 'vitest'

describe('attachments paging under the cursor contract', () => {
  afterEach(() => vi.unstubAllGlobals())
  it('turns the server offset into a cursor and reports whether there is more', async () => {
    const calls: string[] = []
    vi.stubGlobal('fetch', vi.fn(async (url: string) => {
      calls.push(url)
      const offset = Number(new URL(url, 'http://x').searchParams.get('offset'))
      const all = Array.from({ length: 5 }, (_, i) => ({ id: `a${i}` }))
      return { ok: true, json: async () => ({ attachments: all.slice(offset, offset + 2), total: 5 }) }
    }))
    const mod = await import('../src/adapter')
    const adapter = mod.createStudioAdapter() as any
    const att = adapter.attachments
    const p1 = await att.list({ limit: 2 })
    expect(p1.hasMore).toBe(true)
    expect(p1.nextCursor).toBe('2')
    const p3 = await att.list({ limit: 2, cursor: '4' })
    expect(p3.attachments.length).toBe(1)
    expect(p3.hasMore).toBe(false)
    expect(p3.nextCursor).toBeUndefined()
    expect(calls[1]).toContain('offset=4')
  })
})
