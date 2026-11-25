import { act, render } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { SemanticMap } from '../src/components/CorpusMaps'
import { StatusFooter } from '../src/components/StatusFooter'
import { AgentsView } from '../src/views/AgentsView'

// Every page stays mounted once opened. One that is not shown, or a tab that
// is hidden, asks the Studio for nothing.

let asked: string[]
let hide = false
const tick = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms) })
const setHidden = (h: boolean) => act(async () => { hide = h; document.dispatchEvent(new Event('visibilitychange')) })
const reply = (u: string): unknown => {
  if (u.startsWith('/api/kb/semantic-map')) return { available: false, reason: 'none' }
  if (u.startsWith('/api/kb/stats')) return { available: true, files: 3, dirs: 1, totalBytes: 10 }
  if (u.startsWith('/api/index/status')) return { running: false, lastSweepAt: null, lastSweepMs: null, lastChanges: [], lastError: null, sweepIntervalSec: 60 }
  if (u.startsWith('/api/whoami')) return { authEnabled: false, header: '', verified: false, user: null, token: null, claims: null }
  if (u.startsWith('/healthz')) return { ok: true, gufi: true }
  if (u.startsWith('/api/version')) return { version: 'v1.0', commit: 'a', builtAt: null, startedAt: null, source: 'bundle' }
  if (u.startsWith('/api/fleet')) return { agents: [], recent: [] }
  if (u.startsWith('/api/extensions')) return { dir: '/kb/.agents', tools: [], personas: [], skills: [] }
  return { runs: [], tasks: [], agents: [] }
}

beforeEach(() => {
  vi.useFakeTimers()
  localStorage.clear()
  asked = []
  hide = false
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => (hide ? 'hidden' : 'visible'))
  vi.stubGlobal('matchMedia', (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} }))
  vi.stubGlobal('fetch', vi.fn(async (u: RequestInfo | URL) => { asked.push(String(u)); return new Response(JSON.stringify(reply(String(u))), { status: 200 }) }))
})
afterEach(() => { vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks(); localStorage.clear() })

describe('a page that is not shown', () => {
  it('the Agents page reads its runs and agents as it is opened', async () => {
    const props = { onOpen: () => {} }
    const { rerender } = render(<AgentsView active={false} {...props} />)
    await tick(1_000)
    asked.length = 0
    await tick(120_000)
    expect(asked).toEqual([])
    rerender(<AgentsView active {...props} />)
    await tick(0)
    expect(asked).toContain('/api/runs')
    expect(asked.some(u => u.startsWith('/api/fleet'))).toBe(true)
    asked.length = 0
    await tick(30_000)
    // As often as before while it shows.
    expect(asked.filter(u => u === '/api/runs')).toHaveLength(2)
    expect(asked.filter(u => u.startsWith('/api/fleet'))).toHaveLength(3)
  })

  it('the semantic map waits for its files as long as Statistics shows', async () => {
    const maps = () => asked.filter(u => u.startsWith('/api/kb/semantic-map')).length
    const { rerender } = render(<SemanticMap active={false} onOpen={() => {}} />)
    await tick(120_000)
    expect(maps()).toBe(1)
    rerender(<SemanticMap active onOpen={() => {}} />)
    await tick(0)
    expect(maps()).toBe(2)
    await tick(60_000)
    expect(maps()).toBe(4)
  })
})

describe('a hidden tab', () => {
  it('asks for nothing, and for what the footer shows when it shows again', async () => {
    render(<StatusFooter />)
    await tick(1_000)
    await setHidden(true)
    asked.length = 0
    await tick(600_000)
    expect(asked).toEqual([])
    await setHidden(false)
    await tick(0)
    for (const u of ['/healthz', '/api/whoami', '/api/version']) expect(asked).toContain(u)
  })
})
