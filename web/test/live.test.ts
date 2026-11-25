import { renderHook } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { feedSocket, readNow, resetLiveForTests, shownInterval, shownTimeout, startLive, useWatch, watch, type LiveError } from '../src/live'

// What a page shows is read over one socket: no requests while nothing
// happens, nothing while the tab is hidden, nothing for what is not shown,
// and every reading reaches those who show it as when each asked for itself.

class Sock {
  static all: Sock[] = []
  sent: Record<string, unknown>[] = []
  readyState = 0
  onopen: (() => void) | null = null
  onmessage: ((m: { data: string }) => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  constructor(readonly path: string) { Sock.all.push(this) }
  send(s: string) { this.sent.push(JSON.parse(s) as Record<string, unknown>) }
  close() { if (this.readyState === 3) return; this.readyState = 3; this.onclose?.() }
  open() { this.readyState = 1; this.onopen?.() }
  say(v: unknown) { this.onmessage?.({ data: JSON.stringify(v) }) }
  get want() { return this.sent.filter(s => s.want).at(-1)?.want as Record<string, [number, number]> | undefined }
}
const opener = (p: string) => new Sock(p) as unknown as WebSocket
const sock = () => Sock.all.at(-1)!

/** Time passes on a socket that is there. */
async function beat(ws: Sock, ms: number) { for (let t = 0; t < ms; t += 4_000) { ws.say({ beat: true }); await tick(4_000) } }

let asked: string[]
let answer: (u: string) => Response | Promise<Response>
let hide = false
const tick = (ms: number) => vi.advanceTimersByTimeAsync(ms)
const setHidden = (h: boolean) => { hide = h; document.dispatchEvent(new Event('visibilitychange')) }

beforeEach(() => {
  vi.useFakeTimers()
  Sock.all = []
  asked = []
  hide = false
  answer = u => new Response(JSON.stringify({ from: u, n: asked.length }))
  vi.spyOn(document, 'visibilityState', 'get').mockImplementation(() => (hide ? 'hidden' : 'visible'))
  vi.stubGlobal('fetch', vi.fn(async (u: RequestInfo | URL) => { asked.push(String(u)); return answer(String(u)) }))
})
afterEach(() => { resetLiveForTests(); vi.useRealTimers(); vi.unstubAllGlobals(); vi.restoreAllMocks() })

describe('over the socket', () => {
  it('makes no requests while the page sits open, and gives every reading', async () => {
    const stop = startLive(opener)
    const a = vi.fn()
    const b = vi.fn()
    watch('/api/fleet', 10_000, a)
    watch('/api/fleet', 30_000, b)
    watch('/api/notices', 30_000, () => {})
    await tick(100)
    // The first is not waited for.
    expect(asked).toEqual(['/api/fleet', '/api/notices'])
    expect(sock().path).toBe('/api/live')
    sock().open()
    await tick(0)
    // Each once, at the fastest pace asked, when it is next due.
    expect(Object.keys(sock().want!)).toEqual(['/api/fleet', '/api/notices'])
    expect(sock().want!['/api/fleet'][0]).toBe(10_000)
    expect(sock().want!['/api/fleet'][1]).toBeGreaterThan(9_000)
    sock().say({ path: '/api/fleet', status: 200, body: { agents: [1] } })
    expect(a).toHaveBeenLastCalledWith({ agents: [1] })
    expect(b).toHaveBeenLastCalledWith({ agents: [1] })
    const n = a.mock.calls.length
    // Unchanged is a reading too.
    sock().say({ same: ['/api/fleet', '/api/elsewhere'] })
    expect(a.mock.calls.length).toBe(n + 1)
    expect(a).toHaveBeenLastCalledWith({ agents: [1] })
    asked.length = 0
    await beat(sock(), 600_000)
    expect(asked).toEqual([])
    expect(Sock.all).toHaveLength(1)
    stop()
  })

  it('does not take an unchanged reading of a socket that has closed for this one\'s', async () => {
    const stop = startLive(opener)
    const on = vi.fn()
    watch('/api/fleet', 10_000, on)
    await tick(0)
    sock().open()
    sock().say({ path: '/api/fleet', status: 200, body: { agents: [1] } })
    sock().close()
    await tick(1_000)
    sock().open()
    const n = on.mock.calls.length
    sock().say({ same: ['/api/fleet'] })
    expect(on.mock.calls.length).toBe(n)
    stop()
  })

  it('follows what is shown, and no longer what is not', async () => {
    const stop = startLive(opener)
    const { rerender } = renderHook(({ on }) => useWatch('/api/runs', 15_000, on, () => {}), { initialProps: { on: false } })
    watch('/api/fleet', 10_000, () => {})
    await tick(0)
    sock().open()
    await tick(0)
    expect(Object.keys(sock().want!)).toEqual(['/api/fleet'])
    expect(asked).not.toContain('/api/runs')
    rerender({ on: true })
    await tick(0)
    expect(sock().want!['/api/runs']).toEqual([15_000, 0])
    rerender({ on: false })
    await tick(0)
    expect(Object.keys(sock().want!)).toEqual(['/api/fleet'])
    stop()
  })

  it('reads at once what a page asks for, by request when the socket does not answer', async () => {
    const stop = startLive(opener)
    const on = vi.fn()
    watch('/api/fleet', 10_000, on)
    await tick(0)
    sock().open()
    await tick(0)
    asked.length = 0
    const one = readNow('/api/fleet')
    await tick(0)
    expect(sock().sent.at(-1)).toEqual({ read: '/api/fleet', n: 1 })
    sock().say({ path: '/api/fleet', status: 200, body: { agents: [2] }, n: [1] })
    expect(await one).toEqual({ agents: [2] })
    expect(asked).toEqual([])
    const two = readNow('/api/fleet')
    await tick(8_000)
    await two
    expect(asked).toEqual(['/api/fleet'])
    // Nobody shows it: a plain request.
    await readNow('/api/runs')
    expect(asked).toEqual(['/api/fleet', '/api/runs'])
    stop()
  })

  it('says what went wrong to those who show it', async () => {
    const stop = startLive(opener)
    const bad = vi.fn<(e: LiveError) => void>()
    watch('/api/chat/pwcode/here', 20_000, () => {}, bad)
    await tick(0)
    sock().open()
    sock().say({ path: '/api/chat/pwcode/here', status: 502, body: { error: 'pw code is not running' } })
    expect(bad.mock.lastCall![0]).toMatchObject({ message: 'pw code is not running', status: 502 })
    stop()
  })

  it('goes back to requests when the socket closes or says nothing, and opens another', async () => {
    const stop = startLive(opener)
    watch('/api/fleet', 10_000, () => {})
    await tick(0)
    sock().open()
    await tick(0)
    asked.length = 0
    sock().close()
    await tick(900)
    expect(Sock.all).toHaveLength(1)
    await tick(200)
    expect(Sock.all).toHaveLength(2)
    // Until it is open: every 10 seconds.
    await tick(20_000)
    expect(asked).toEqual(['/api/fleet', '/api/fleet'])
    sock().open()
    await tick(0)
    // Nothing from it, not even its beat.
    asked.length = 0
    await tick(50_000)
    expect(Sock.all.length).toBeGreaterThan(2)
    expect(asked.length).toBeGreaterThanOrEqual(1)
    stop()
  })

  it('tries less and less often where sockets open and are cut at once', async () => {
    const stop = startLive(opener)
    watch('/api/fleet', 10_000, () => {})
    await tick(0)
    for (let s = 0; s < 120; s++) {
      if (sock().readyState === 0) { sock().open(); sock().say({ path: '/api/fleet', status: 200, body: {} }); sock().close() }
      await tick(1_000)
    }
    expect(Sock.all.length).toBeLessThanOrEqual(8)
    // One that lasts, and then ends, is opened again at once.
    await tick(60_000)
    sock().open()
    await beat(sock(), 60_000)
    const n = Sock.all.length
    sock().close()
    await tick(1_000)
    expect(Sock.all.length).toBe(n + 1)
    stop()
  })
})

describe('a socket that goes silent', () => {
  it('is noticed within 15 seconds, and what is due is read at once', async () => {
    const stop = startLive(opener)
    const on = vi.fn()
    watch('/api/index/status', 15_000, on)
    await tick(0)
    sock().open()
    sock().say({ path: '/api/index/status', status: 200, body: {} })
    await beat(sock(), 12_000)
    // Silent from here, with the next reading due soon.
    asked.length = 0
    const n = Sock.all.length
    let late = 0
    while (!asked.length && late < 60_000) { await tick(500); late += 500 }
    expect(asked).toEqual(['/api/index/status'])
    expect(late).toBeLessThanOrEqual(7_000)
    // One that comes back is kept; one that does not is gone within 15 s.
    expect(Sock.all).toHaveLength(n)
    await tick(15_000 - late)
    expect(Sock.all.length).toBe(n + 1)
    stop()
  })

  it('that comes back is kept, and its readings are taken as before', async () => {
    const stop = startLive(opener)
    const on = vi.fn()
    watch('/api/fleet', 10_000, on)
    watch('/api/delegated', 10_000, () => {})
    watch('/api/whoami', 60_000, () => {})
    await tick(0)
    sock().open()
    await beat(sock(), 8_000)
    asked.length = 0
    await tick(5_000)
    expect(asked).toHaveLength(1)
    expect(asked[0]).toMatch(/^\/api\/live\/read\?p=%2Fapi%2Ffleet&p=%2Fapi%2Fdelegated$/)
    sock().say({ path: '/api/fleet', status: 200, body: { agents: [3] } })
    expect(on).toHaveBeenLastCalledWith({ agents: [3] })
    await beat(sock(), 4_000)
    expect(Sock.all).toHaveLength(1)
    stop()
  })

  it('is not taken for gone by a page that was busy', async () => {
    const stop = startLive(opener)
    watch('/api/fleet', 10_000, () => {})
    await tick(0)
    sock().open()
    await beat(sock(), 8_000)
    // Nothing ran for 20 seconds; what came meanwhile is read after.
    vi.setSystemTime(Date.now() + 20_000)
    await tick(1_000)
    sock().say({ beat: true })
    await beat(sock(), 20_000)
    expect(Sock.all).toHaveLength(1)
    stop()
  })

  it('a feed is closed for its package, which reads for itself', async () => {
    class Feed extends EventTarget {
      static OPEN = 1
      readyState = 1
      closed = 0
      constructor(readonly url: string) { super() }
      close() { this.closed++ }
    }
    vi.stubGlobal('WebSocket', Feed)
    const ws = feedSocket('ws://studio.test/api/code-page/feed?heartbeat=10') as unknown as Feed
    expect(ws.url).toBe('ws://studio.test/api/code-page/feed?heartbeat=4')
    const gone = vi.fn()
    ws.addEventListener('close', gone)
    for (let i = 0; i < 20; i++) { ws.dispatchEvent(new MessageEvent('message', { data: '{"type":"heartbeat"}' })); await tick(4_000) }
    expect(gone).not.toHaveBeenCalled()
    await tick(15_000)
    expect(gone).toHaveBeenCalledTimes(1)
    expect(ws.closed).toBe(1)
    await tick(60_000)
    expect(gone).toHaveBeenCalledTimes(1)
  })
})

describe('a socket that ends', () => {
  const follow = () => { for (const [p, ms] of [['/api/fleet', 10_000], ['/api/delegated', 10_000], ['/api/whoami', 60_000], ['/healthz', 60_000]] as const) watch(p, ms, () => {}) }

  it('is taken over by another before, with no request and no reading missed', async () => {
    const stop = startLive(opener)
    const on = vi.fn()
    watch('/api/notices', 30_000, on)
    follow()
    await tick(0)
    const first = sock()
    first.open()
    first.say({ until: 300_000 })
    asked.length = 0
    await beat(first, 276_000)
    expect(Sock.all).toHaveLength(1)
    await beat(first, 8_000)
    expect(Sock.all).toHaveLength(2)
    const second = sock()
    // While it opens, the first goes on.
    first.say({ path: '/api/notices', status: 200, body: { items: [1] } })
    expect(on).toHaveBeenLastCalledWith({ items: [1] })
    expect(first.readyState).toBe(1)
    const late = readNow('/api/notices')
    await tick(0)
    second.open()
    await tick(0)
    expect(first.readyState).toBe(3)
    expect(Object.keys(second.want!).sort()).toEqual(['/api/delegated', '/api/fleet', '/api/notices', '/api/whoami', '/healthz'].sort())
    // Due when they would have been, and a read under way is asked again.
    expect(second.want!['/api/notices'][1]).toBeGreaterThan(20_000)
    expect(second.sent.at(-1)).toEqual({ read: '/api/notices', n: 1 })
    second.say({ path: '/api/notices', status: 200, body: { items: [2] }, n: [1] })
    expect(await late).toEqual({ items: [2] })
    second.say({ until: 300_000 })
    await beat(second, 60_000)
    expect(asked).toEqual([])
    expect(Sock.all).toHaveLength(2)
    stop()
  })

  it('tries the next one again while it is there, and goes on with one that is opening', async () => {
    const stop = startLive(opener)
    follow()
    await tick(0)
    const first = sock()
    first.open()
    first.say({ until: 60_000 })
    await beat(first, 44_000)
    expect(Sock.all).toHaveLength(2)
    sock().close()
    await beat(first, 8_000)
    expect(Sock.all).toHaveLength(3)
    for (const path of ['/api/fleet', '/api/delegated']) first.say({ path, status: 200, body: {} })
    asked.length = 0
    first.close()
    await tick(0)
    expect(Sock.all).toHaveLength(3)
    sock().open()
    await tick(0)
    expect(Object.keys(sock().want!)).toHaveLength(4)
    expect(asked).toEqual([])
    stop()
  })

  it('is not renewed with a sign-in that ends as soon', async () => {
    const stop = startLive(opener)
    follow()
    await tick(0)
    sock().open()
    sock().say({ until: 18_000 })
    await beat(sock(), 16_000)
    expect(Sock.all).toHaveLength(1)
    stop()
  })

  it('without warning: what is due meanwhile goes together, and what is read once a minute waits', async () => {
    const stop = startLive(opener)
    follow()
    await tick(0)
    sock().open()
    await beat(sock(), 60_000)
    asked.length = 0
    sock().close()
    await tick(100)
    expect(asked).toHaveLength(1)
    expect(new URLSearchParams(asked[0].split('?')[1]).getAll('p').sort()).toEqual(['/api/delegated', '/api/fleet'])
    // The next one is open within half a second or so.
    await tick(500)
    expect(Sock.all).toHaveLength(2)
    sock().open()
    await tick(0)
    expect(sock().want!['/api/whoami'][1]).toBe(0)
    await beat(sock(), 20_000)
    expect(asked).toHaveLength(1)
    // Where none opens, they are read in a few seconds.
    sock().close()
    asked.length = 0
    answer = () => new Response('{}', { status: 404 })
    await tick(60_000)
    expect(asked.filter(u => u === '/api/whoami')).toHaveLength(1)
    // The server was away: it has the combined read when it is back.
    sock().open()
    await tick(0)
    sock().close()
    asked.length = 0
    await tick(20_000)
    expect(asked.some(u => u.startsWith('/api/live/read'))).toBe(true)
    stop()
  })
})

describe('a hidden tab', () => {
  it('reads nothing, lets its socket go, and reads everything when it shows', async () => {
    const stop = startLive(opener)
    watch('/api/fleet', 10_000, () => {})
    watch('/api/whoami', 60_000, () => {})
    await tick(0)
    sock().open()
    await tick(0)
    asked.length = 0
    setHidden(true)
    await tick(10_000)
    expect(sock().readyState).toBe(3)
    await tick(600_000)
    expect(asked).toEqual([])
    expect(Sock.all).toHaveLength(1)
    setHidden(false)
    await tick(0)
    expect(asked).toEqual(['/api/fleet', '/api/whoami'])
    expect(Sock.all).toHaveLength(2)
    // A glance at another tab keeps the socket; what it shows is read over it.
    sock().open()
    await tick(0)
    asked.length = 0
    setHidden(true)
    await tick(3_000)
    setHidden(false)
    await tick(0)
    expect(asked).toEqual([])
    expect(sock().sent.filter(s => s.read).map(s => s.read)).toEqual(['/api/fleet', '/api/whoami'])
    stop()
  })

  it('starts hidden with no requests and no socket', async () => {
    hide = true
    const stop = startLive(opener)
    watch('/api/fleet', 10_000, () => {})
    await tick(60_000)
    expect(asked).toEqual([])
    expect(Sock.all).toHaveLength(0)
    setHidden(false)
    await tick(0)
    expect(asked).toEqual(['/api/fleet'])
    stop()
  })

  it('holds a page\'s own timers until it shows', async () => {
    const once = vi.fn()
    const each = vi.fn()
    shownTimeout(once, 4_000)
    const stop = shownInterval(each, 5_000)
    await tick(10_000)
    expect([once.mock.calls.length, each.mock.calls.length]).toEqual([1, 2])
    shownTimeout(once, 4_000)
    setHidden(true)
    await tick(60_000)
    expect([once.mock.calls.length, each.mock.calls.length]).toEqual([1, 2])
    setHidden(false)
    expect([once.mock.calls.length, each.mock.calls.length]).toEqual([2, 3])
    stop()
    await tick(60_000)
    expect(each.mock.calls.length).toBe(3)
  })
})

describe('where no socket opens', () => {
  it('reads at the same pace, those due together in one request', async () => {
    const combined = { reads: ['/api/fleet', '/api/delegated', '/api/index/status'].map(path => ({ path, status: 200, body: { ok: path } })) }
    answer = u => new Response(JSON.stringify(u.startsWith('/api/live/read') ? combined : {}))
    const stop = startLive(opener)
    const fleet = vi.fn()
    watch('/api/fleet', 10_000, fleet)
    watch('/api/delegated', 10_000, () => {})
    watch('/api/index/status', 10_000, () => {})
    watch('/api/whoami', 60_000, () => {})
    for (let i = 0; i < 12; i++) { sock().close(); await tick(1_000) }
    await tick(30_000)
    for (let i = 0; i < 4; i++) { sock().close(); await tick(300_000) }
    asked.length = 0
    await tick(60_000)
    const together = asked.filter(u => u.startsWith('/api/live/read'))
    expect(together.length).toBeGreaterThanOrEqual(5)
    expect(together.length).toBeLessThanOrEqual(7)
    expect(new URLSearchParams(together[0].split('?')[1]).getAll('p').sort()).toEqual(['/api/delegated', '/api/fleet', '/api/index/status'])
    expect(asked.filter(u => !u.startsWith('/api/live/read'))).toEqual(['/api/whoami'])
    expect(fleet).toHaveBeenLastCalledWith({ ok: '/api/fleet' })
    // It tries again now and then.
    const n = Sock.all.length
    sock().close()
    await tick(300_000)
    expect(Sock.all.length).toBe(n + 1)
    stop()
  })

  it('reads each by itself on a server without the combined read', async () => {
    answer = u => (u.startsWith('/api/live/read') ? new Response('{}', { status: 404 }) : new Response('{}'))
    const stop = startLive(opener)
    watch('/api/fleet', 10_000, () => {})
    watch('/api/delegated', 10_000, () => {})
    for (let i = 0; i < 6; i++) { sock().close(); await tick(10_000) }
    asked.length = 0
    await tick(20_000)
    expect(asked.filter(u => u.startsWith('/api/live/read'))).toEqual([])
    expect(asked).toEqual(['/api/fleet', '/api/delegated', '/api/fleet', '/api/delegated'])
    stop()
  })
})

describe('what is read', () => {
  it('gives what another part follows, once, and reads it again only when due', async () => {
    const a = vi.fn()
    const b = vi.fn()
    watch('/api/fleet', 10_000, a)
    await tick(2_000)
    watch('/api/fleet', 10_000, b)
    await tick(0)
    expect(asked).toHaveLength(1)
    expect(b).toHaveBeenCalledTimes(1)
    await tick(8_000)
    expect(asked).toHaveLength(2)
  })

  it('keeps the later of two readings', async () => {
    const on = vi.fn()
    let first: (r: Response) => void = () => {}
    answer = () => (asked.length === 1 ? new Promise<Response>(f => { first = f }) : new Response('{"n":2}'))
    watch('/api/fleet', 10_000, on)
    await readNow('/api/fleet')
    first(new Response('{"n":1}'))
    await tick(0)
    expect(on.mock.calls).toEqual([[{ n: 2 }]])
  })

  it('is read again when shown again, unless it was read lately and said to keep', async () => {
    watch('/api/fleet', 10_000, () => {}).stop()
    await tick(100)
    watch('/api/fleet', 10_000, () => {}).stop()
    await tick(100)
    expect(asked).toHaveLength(2)
    const on = vi.fn()
    watch('/api/fleet', 10_000, on, undefined, true)
    await tick(100)
    expect(asked).toHaveLength(2)
    expect(on).toHaveBeenCalledTimes(1)
  })
})
