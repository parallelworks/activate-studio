import { afterEach, describe, expect, it, vi } from 'vitest'
import type { IndexStatus } from '../src/api'
import { refreshIndexStatus, resetIndexStatusForTests } from '../src/indexActivity'

// A re-index after a transfer can start and end between two idle reads, so
// the status never shows it running. Its change count still grows, and the
// views that read the index are told, whatever page is open.

const status = (changes: number | undefined, over: Partial<IndexStatus> = {}): IndexStatus => ({
  running: false, lastSweepAt: null, lastSweepMs: null, lastChanges: [], lastError: null, sweepIntervalSec: 300,
  job: null, queued: [], last: null, lastBuild: null, runs: [], indexed: true, incomplete: false, threads: 8, changes, ...over,
})

afterEach(() => { vi.unstubAllGlobals(); resetIndexStatusForTests() })

describe('the index changing between two reads', () => {
  it('tells the views when the change count moves, not on the first read or when it holds', async () => {
    resetIndexStatusForTests()
    const told = vi.fn()
    window.addEventListener('ade-kb-changed', told)
    const reads = [status(3), status(3), status(5), status(0), status(undefined)]
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(reads.shift()), { status: 200 })))
    await refreshIndexStatus()
    expect(told).not.toHaveBeenCalled()
    await refreshIndexStatus()
    expect(told).not.toHaveBeenCalled()
    await refreshIndexStatus()
    expect(told).toHaveBeenCalledTimes(1)
    // A restarted server counts from 0 again: a change too.
    await refreshIndexStatus()
    expect(told).toHaveBeenCalledTimes(2)
    // An older server without the count: nothing from it.
    await refreshIndexStatus()
    expect(told).toHaveBeenCalledTimes(2)
    window.removeEventListener('ade-kb-changed', told)
  })
})
