import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it, vi } from 'vitest'
import { fillShell, shellOf } from '../shell/build'

// web/shell/sw.js run in a fake service worker scope: its caches, the
// network, the window clients and the events are stand-ins, and the
// worker's own code is the file the build ships.

const ORIGIN = 'https://studio.platform.example'
const HTML = '<!doctype html><script type="module" crossorigin src="/assets/index-AAAAAAAA.js"></script><link rel="stylesheet" crossorigin href="/assets/index-BBBBBBBB.css">'
const SHELL = shellOf(HTML, ['favicon.svg'])
const SRC = fillShell(readFileSync(join(__dirname, '..', 'shell', 'sw.js'), 'utf8'), SHELL)

type Res = Response & { tag?: string }
function res(body: string, o: { status?: number; type?: string; ctype?: string; redirected?: boolean; tag?: string } = {}): Res {
  const status = o.status ?? 200
  const r = new Response(status === 0 ? null : body, status === 0 ? undefined : { status, headers: { 'Content-Type': o.ctype ?? 'text/html; charset=utf-8' } }) as Res
  Object.defineProperty(r, 'type', { value: o.type ?? 'basic' })
  Object.defineProperty(r, 'redirected', { value: !!o.redirected })
  if (status === 0) Object.defineProperty(r, 'status', { value: 0 })
  r.tag = o.tag ?? body
  return r
}
const redirect = () => res('', { status: 0, type: 'opaqueredirect', tag: 'redirect' })

function worker(net: (url: string) => Promise<Res>) {
  const handlers: Record<string, ((e: unknown) => void)[]> = {}
  const stores = new Map<string, Map<string, Res>>()
  const caches = {
    open: async (n: string) => {
      if (!stores.has(n)) stores.set(n, new Map())
      const m = stores.get(n)!
      return {
        match: async (u: string) => m.get(u),
        put: async (u: string, r: Res) => { m.set(u, r) },
      }
    },
    keys: async () => [...stores.keys()],
    delete: async (n: string) => stores.delete(n),
  }
  const navigated: string[] = []
  const client = { id: 'c1', url: ORIGIN + '/#/chat', navigate: vi.fn(async (u: string) => { navigated.push(u); return client }) }
  const self = {
    registration: { scope: ORIGIN + '/', navigationPreload: { disable: vi.fn(async () => {}) } },
    clients: { get: async (id: string) => (id === 'c1' ? client : undefined), claim: vi.fn(async () => {}) },
    skipWaiting: vi.fn(),
    addEventListener: (t: string, fn: (e: unknown) => void) => { (handlers[t] ??= []).push(fn) },
  }
  const fetch = vi.fn((input: string | { url: string; mode?: string }) => {
    if (typeof input !== 'string' && input.mode === 'navigate' && nextAnswer) { const a = nextAnswer; nextAnswer = null; return a }
    return net(typeof input === 'string' ? input : input.url)
  })
  new Function('self', 'caches', 'fetch', SRC)(self, caches, fetch)

  const fire = (type: string, extra: object = {}) => {
    const waits: Promise<unknown>[] = []
    let responded: Promise<Res> | undefined
    const e = { ...extra, waitUntil: (p: Promise<unknown>) => { waits.push(p) }, respondWith: (p: Promise<Res>) => { responded = p } }
    for (const fn of handlers[type] ?? []) fn(e)
    return { responded: () => responded, settled: () => Promise.allSettled(waits) }
  }
  const request = (path: string, o: { mode?: string; method?: string; range?: boolean } = {}) =>
    ({ url: ORIGIN + path, method: o.method ?? 'GET', mode: o.mode ?? 'cors', headers: new Headers(o.range ? { range: 'bytes=0-1' } : {}) })
  /** A navigation; `answer` is the network's answer to it, else the server's. */
  let nextAnswer: Promise<Res> | null = null
  const navigate = (path = '/', answer?: Promise<Res>) => {
    nextAnswer = answer ?? null
    return fire('fetch', { request: request(path, { mode: 'navigate' }), resultingClientId: 'c1' })
  }
  const install = async () => { const ev = fire('install'); const [r] = await ev.settled(); return r }
  const activate = async () => { await fire('activate').settled() }
  return { stores, fetch, navigate, request, fire, install, activate, navigated, self }
}

/** A server that answers the shell's files as this build wrote them. */
const server = (over: Record<string, () => Promise<Res>> = {}) => async (url: string): Promise<Res> => {
  const path = new URL(url).pathname
  if (over[path]) return over[path]()
  if (path === '/') return res(HTML, { tag: 'network page' }) // also the copy install keeps
  if (path.endsWith('.js')) return res('js', { ctype: 'text/javascript', tag: 'network ' + path })
  if (path.endsWith('.css')) return res('css', { ctype: 'text/css', tag: 'network ' + path })
  if (path.endsWith('.svg')) return res('svg', { ctype: 'image/svg+xml' })
  return res(HTML)
}
const later = <T,>(ms: number, v: T) => new Promise<T>(r => setTimeout(() => r(v), ms))

describe('the app shell worker', () => {
  it('keeps the page, its entry files and the icons on install, under its build', async () => {
    const w = worker(server())
    expect((await w.install()).status).toBe('fulfilled')
    const kept = w.stores.get('studio-shell-' + SHELL.build)!
    expect([...kept.keys()].sort()).toEqual([ORIGIN + '/', ORIGIN + '/assets/index-AAAAAAAA.js', ORIGIN + '/assets/index-BBBBBBBB.css', ORIGIN + '/favicon.svg'].sort())
  })

  it('does not install on a sign-in redirect, an error, or a page from another build', async () => {
    for (const answer of [redirect, () => res(HTML, { redirected: true }), () => res('no', { status: 502 }), () => res('<html>another build</html>')]) {
      const w = worker(server({ '/': async () => answer() }))
      expect((await w.install()).status).toBe('rejected')
    }
  })

  it('drops older builds’ caches when it takes over', async () => {
    const w = worker(server())
    await w.install()
    w.stores.set('studio-shell-older', new Map())
    w.stores.set('someone-else', new Map())
    await w.activate()
    expect([...w.stores.keys()].sort()).toEqual(['someone-else', 'studio-shell-' + SHELL.build])
    expect(w.self.clients.claim).toHaveBeenCalled()
    expect(w.self.registration.navigationPreload.disable).toHaveBeenCalled()
  })

  it('never answers the API, streams, other methods, ranges or unhashed files', () => {
    const w = worker(server())
    for (const [path, o] of [['/api/config', {}], ['/api/chat/stream', {}], ['/v1/chat/completions', {}], ['/internal/x', {}],
      ['/api/kb/file?path=a.pdf', {}], ['/assets/index-AAAAAAAA.js', { method: 'POST' }], ['/assets/index-AAAAAAAA.js', { range: true }],
      ['/pdfjs/cmaps/x.bcmap', {}], ['/manifest.webmanifest', {}], ['/assets/index-AAAAAAAA.js?x=1', {}]] as const) {
      const ev = w.fire('fetch', { request: w.request(path, o) })
      expect(ev.responded(), path).toBeUndefined()
    }
  })

  it('serves hashed files from the cache, and keeps one on first use but never the page served in its place', async () => {
    const w = worker(server({ '/assets/Missing-CCCCCCCC.js': async () => res(HTML) }))
    await w.install()
    w.fetch.mockClear()
    expect((await w.fire('fetch', { request: w.request('/assets/index-AAAAAAAA.js') }).responded()!).status).toBe(200)
    expect(w.fetch).not.toHaveBeenCalled()

    const lazy = w.fire('fetch', { request: w.request('/assets/Lazy-DDDDDDDD.js') })
    await lazy.responded(); await lazy.settled()
    expect(w.stores.get('studio-shell-' + SHELL.build)!.has(ORIGIN + '/assets/Lazy-DDDDDDDD.js')).toBe(true)

    const gone = w.fire('fetch', { request: w.request('/assets/Missing-CCCCCCCC.js') })
    await gone.responded(); await gone.settled()
    expect(w.stores.get('studio-shell-' + SHELL.build)!.has(ORIGIN + '/assets/Missing-CCCCCCCC.js')).toBe(false)
  })

  it('opens from the network before anything is kept', async () => {
    const w = worker(server())
    const r = await w.navigate().responded()!
    expect(r.tag).toBe('network page')
  })

  it('uses the server’s page when it answers in time, and follows its sign-in redirect', async () => {
    const w = worker(server())
    await w.install()
    expect((await w.navigate('/', later(50, res(HTML, { tag: 'fresh' }))).responded()!).tag).toBe('fresh')
    expect((await w.navigate('/', later(50, redirect())).responded()!).tag).toBe('redirect')
  })

  it('shows the kept page when the network is slow, and the network’s page when that turns out to be a sign-in', async () => {
    const w = worker(server())
    await w.install()
    const ok = w.navigate('/', later(900, res(HTML, { tag: 'fresh' })))
    const t0 = Date.now()
    expect((await ok.responded()!).tag).toBe('network page')
    expect(Date.now() - t0).toBeLessThan(800)
    await ok.settled()
    expect(w.navigated).toEqual([])

    const expired = w.navigate('/', later(900, redirect()))
    expect((await expired.responded()!).tag).toBe('network page')
    await expired.settled()
    expect(w.navigated).toEqual([ORIGIN + '/#/chat'])
    // That navigation goes to the network, untouched, once.
    expect(w.navigate('/').responded()).toBeUndefined()
    expect(w.navigate('/').responded()).toBeDefined()
  })

  it('shows the kept page offline', async () => {
    const w = worker(server())
    await w.install()
    const off = w.navigate('/', Promise.reject(new TypeError('offline')))
    expect((await off.responded()!).tag).toBe('network page')
  })

  it('leaves a navigation with a query, or to another path, to the network', async () => {
    const w = worker(server())
    await w.install()
    for (const p of ['/?sessionToken=x', '/?theme=dark', '/api/auth/login?returnTo=%2F', '/other']) expect(w.navigate(p).responded(), p).toBeUndefined()
    expect(w.navigate('/index.html').responded()).toBeDefined()
  })

  it('answers the page with the build it carries, and takes over when asked', async () => {
    const w = worker(server())
    const port = { postMessage: vi.fn() }
    w.fire('message', { data: { type: 'build' }, ports: [port] })
    expect(port.postMessage).toHaveBeenCalledWith({ build: SHELL.build, entry: SHELL.entry })
    w.fire('message', { data: { type: 'skip-waiting' } })
    expect(w.self.skipWaiting).toHaveBeenCalled()
  })
})
