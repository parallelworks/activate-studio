import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { IndexJob, IndexStatus } from '../src/api'
import { fmtDuration, indexSummary, jobPercent } from '../src/indexActivity'

// The footer's index line, the status panel, Settings, Libraries and
// Statistics read one status, once per poll, so they say the same thing,
// follow a running job, and say done when it is.

const phase = (id: string, state: string, done: number, total: number, ms: number | null = null) => ({
  id, label: id, state, done, total, unit: 'items', step: null, ms, note: null,
})
const job = (over: Partial<IndexJob> = {}): IndexJob => ({
  id: 4, kind: 'build', dir: '', label: 'Building the index', state: 'running', startedAt: new Date(Date.now() - 65_000).toISOString(),
  runningAt: new Date(Date.now() - 65_000).toISOString(), finishedAt: null, ms: null, error: null, threads: 8, searchableAt: new Date().toISOString(),
  warnings: [], warningCount: 0,
  phases: [phase('names', 'done', 40, 40, 300), phase('documents', 'done', 12, 12, 9000), phase('text', 'running', 450, 1000), phase('meaning', 'pending', 0, 0)] as IndexJob['phases'],
  ...over,
})
const status = (over: Partial<IndexStatus> = {}): IndexStatus => ({
  running: false, lastSweepAt: new Date(Date.now() - 120_000).toISOString(), lastSweepMs: 40, lastChanges: [], lastError: null, sweepIntervalSec: 300,
  job: null, queued: [], last: null, lastBuild: null, runs: [], indexed: true, incomplete: false, threads: 8, ...over,
})

describe('what is said about the index', () => {
  it('a running job: the phase it is in, and how far the whole job has come', () => {
    const s = status({ running: true, job: job() })
    expect(indexSummary(s)).toMatchObject({ tone: 'info', pulse: true, running: true, text: 'Indexing: text 45%', detail: 'Building the index' })
    // Two phases done and one 45% through, of four: 61%.
    expect(jobPercent(s.job)).toBe(61)
    // A skipped phase does not count against it.
    expect(jobPercent(job({ phases: [phase('names', 'done', 1, 1), phase('text', 'running', 1, 2), phase('meaning', 'skipped', 0, 0)] as IndexJob['phases'] }))).toBe(75)
  })

  it('done, stopped, failed, never built', () => {
    expect(indexSummary(status(), 1234)).toMatchObject({ tone: 'success', text: '1,234 files indexed', running: false })
    expect(indexSummary(status({ incomplete: true, lastBuild: job({ state: 'cancelled' }) })).text).toBe('index incomplete')
    expect(indexSummary(status({ lastError: 'Building the index: enrich.py: disk full' }))).toMatchObject({ tone: 'warning', detail: 'Building the index: enrich.py: disk full' })
    expect(indexSummary(status({ indexed: false })).text).toBe('not indexed')
    expect(indexSummary(null).text).toBe('index loading')
  })

  it('durations as people read them', () => {
    expect(fmtDuration(850)).toBe('850 ms')
    expect(fmtDuration(2500)).toBe('2.5 s')
    expect(fmtDuration(252_000)).toBe('4 min 12 s')
    expect(fmtDuration(3_780_000)).toBe('1 h 3 min')
  })
})

describe('one status for every view', () => {
  let current: IndexStatus
  let asked: string[]
  beforeEach(() => {
    asked = []
    current = status({ running: true, job: job() })
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const url = String(input)
      asked.push(url)
      const path = url.split('?')[0]
      const json = (b: unknown) => new Response(JSON.stringify(b), { status: 200, headers: { 'Content-Type': 'application/json' } })
      if (path === '/api/index/status') return json(current)
      if (path === '/api/kb/stats') return json({ available: true, files: current.running ? 500 : 1234, dirs: 40, totalBytes: 1e9, byExt: [] })
      if (path === '/api/index/extractors') return json({ extractors: [], formats: [], missing: [{ formats: '.ppt, .xls, .odt, .odp, .ods', readers: [], ok: false, note: 'These index by name only until LibreOffice is installed.' }] })
      if (path === '/api/tags') return json({ tags: [] })
      if (path === '/api/version') return json({ version: 'v1.0', commit: 'abc', builtAt: null, startedAt: 'x', source: 'git' })
      if (path === '/healthz') return json({ ok: true, gufi: true })
      if (path === '/api/whoami') return json({ authEnabled: false, header: 'x', verified: false, user: null, token: null, claims: null })
      if (path === '/api/index/log') return new Response('2026-10-22 01:00:00 [4 build] Building the index: started on 8 threads\n', { status: 200 })
      return new Response('{}', { status: 404 })
    }))
  })
  afterEach(() => { vi.unstubAllGlobals() })

  it('the footer and Statistics agree while a build runs, and both say done when it ends', async () => {
    vi.resetModules()
    const ia = await import('../src/indexActivity')
    ia.setIndexPollingForTests(30, 60_000)
    const { StatusFooter } = await import('../src/components/StatusFooter')
    const { OverviewView } = await import('../src/views/OverviewView')
    render(<><div data-testid="footer"><StatusFooter /></div><div data-testid="stats"><OverviewView onOpen={() => {}} /></div></>)

    const footer = await screen.findByTestId('footer')
    expect(await within(footer).findByText('Indexing: text 45%', {}, { timeout: 5000 })).toBeInTheDocument()
    const stats = screen.getByTestId('stats')
    // The tile reads the same job; the page shows its phases with counts.
    expect(stats.querySelector('.ov-index-state')?.textContent).toBe('61%')
    expect(within(stats).getByText('450 of 1,000 items')).toBeInTheDocument()
    expect(within(stats).getByText('Search by name works now; text and meaning are added as they are read.')).toBeInTheDocument()
    expect(within(stats).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    // Formats nothing here reads are listed; readers that are optional are not.
    expect(within(stats).getByText(/until LibreOffice is installed/)).toBeInTheDocument()
    // Read once per poll for both views, not once per view.
    const reads = asked.filter(u => u === '/api/index/status').length
    expect(reads).toBeGreaterThanOrEqual(1)

    // The build ends: both say done, and the counts are read again.
    const statsBefore = asked.filter(u => u.startsWith('/api/kb/stats')).length
    const done = job({ state: 'done', ms: 252_000, finishedAt: new Date().toISOString(), phases: job().phases!.map(p => ({ ...p, state: 'done', done: p.total, ms: 1000 })) })
    current = status({ last: done, lastBuild: done, runs: [done] })
    await waitFor(() => expect(within(footer).getByText('1,234 files indexed')).toBeInTheDocument(), { timeout: 5000 })
    expect(within(stats).queryByText('450 of 1,000 items')).toBeNull()
    expect(stats.querySelector('.ov-index-state')?.textContent).toBe('healthy')
    expect(asked.filter(u => u.startsWith('/api/kb/stats')).length).toBeGreaterThan(statsBefore)
    expect(within(stats).getAllByText(/Built the index in 4 min 12 s/).length).toBeGreaterThan(0)
    await act(async () => { await new Promise(r => setTimeout(r, 0)) })
  })
})

describe('the footer\'s index panel', () => {
  afterEach(() => { vi.unstubAllGlobals() })

  it('shows the running job with Stop, and opens the log, which stays open as the panel closes', async () => {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
      const path = String(input).split('?')[0]
      const json = (b: unknown) => new Response(JSON.stringify(b), { status: 200, headers: { 'Content-Type': 'application/json' } })
      if (path === '/api/index/status') return json(status({ running: true, job: job(), log: { path: '/data/logs/indexing.log', bytes: 80, reveal: false } }))
      if (path === '/api/index/log') return new Response('2026-10-22 01:00:00 [4 build] Building the index: started on 8 threads\n', { status: 200 })
      if (path === '/api/kb/stats') return json({ available: true, files: 500, dirs: 40, totalBytes: 1e9 })
      if (path === '/api/version') return json({ version: 'v1.0', commit: 'abc', builtAt: null, startedAt: 'x', source: 'git' })
      if (path === '/healthz') return json({ ok: true, gufi: true })
      if (path === '/api/whoami') return json({ authEnabled: false, header: 'x', verified: false, user: null, token: null, claims: null })
      return new Response('{}', { status: 404 })
    }))
    vi.resetModules()
    ;(await import('../src/indexActivity')).setIndexPollingForTests(30, 60_000)
    const { StatusFooter } = await import('../src/components/StatusFooter')
    render(<StatusFooter />)
    fireEvent.click(await screen.findByRole('button', { name: /Indexing: text 45%/ }, { timeout: 5000 }))
    const panel = await screen.findByRole('dialog', { name: 'Index health' }, { timeout: 5000 })
    expect(within(panel).getByText('450 of 1,000 items')).toBeInTheDocument()
    expect(within(panel).getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    fireEvent.click(within(panel).getByRole('button', { name: 'View log' }))
    const log = await screen.findByRole('dialog', { name: 'Index log' }, { timeout: 5000 })
    expect(await within(log).findByText(/Building the index: started on 8 threads/, {}, { timeout: 5000 })).toBeInTheDocument()
    expect(within(log).getByText('/data/logs/indexing.log')).toBeInTheDocument()
  })
})
