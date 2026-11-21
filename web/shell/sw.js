/*
 * The Studio's service worker: it keeps the app shell (index.html, the
 * entry script and stylesheet, the icons) on the device, so an installed
 * app or a returning tab paints the Studio's own loading screen and starts
 * the app without waiting on the network. web/vite.config.ts (shellWorker)
 * writes this file into the build as sw.js with SHELL filled in, and
 * web/src/shellWorker.ts registers it. docs/REFERENCE.md, "The app shell
 * on the device", says what it does and does not do.
 *
 * - index.html is network first, with a short wait: the server's answer is
 *   used when it comes within NETWORK_WAIT_MS, so a reachable server keeps
 *   the page and its scripts on one build and a sign-in redirect is followed
 *   as before. When the network is slower, the copy kept with this build is
 *   shown, and the network's answer is still read: anything but the page
 *   (a redirect to sign-in, an error page) is then shown in its place.
 * - Files whose names carry their content hash (/assets/) never change, so
 *   they are served from the cache and kept on first use.
 * - Nothing else is touched: /api, /v1, /internal, event streams, downloads
 *   and every other request go to the network as if there were no worker.
 * - Each build has its own cache, which holds only that build's files. A
 *   new build's worker waits until the page asks it to take over (the
 *   page's update path) or every window of the Studio has closed, then
 *   removes the older caches.
 */
const SHELL = /* SHELL */ { build: 'dev', entry: [], files: [] } /* END SHELL */
const PREFIX = 'studio-shell-'
const CACHE = PREFIX + SHELL.build
const NETWORK_WAIT_MS = 500
const SCOPE = new URL(self.registration.scope)
// A file name with its content hash, as server-go's static.go reads it.
const HASHED = /-[A-Za-z0-9_-]{8,}\.[a-z0-9]+$/
const KEPT = new Set(SHELL.files.map(f => new URL(f, SCOPE).pathname))

/** The shell's own address: the scope, which start_url names. */
const shellKey = () => SCOPE.href

/** A navigation the shell answers: the scope itself, or its index.html,
 *  with no query. A query (the platform's sign-in return, an embed's
 *  theme) and every other path go to the network. */
function isShellNavigation(url) {
  return url.origin === SCOPE.origin && !url.search &&
    (url.pathname === SCOPE.pathname || url.pathname === SCOPE.pathname + 'index.html')
}

/** A file the cache may answer: one of the shell's, or a hashed file under
 *  the scope's assets. */
function isShellFile(url) {
  if (url.origin !== SCOPE.origin || url.search) return false
  if (KEPT.has(url.pathname)) return true
  return url.pathname.startsWith(SCOPE.pathname + 'assets/') && HASHED.test(url.pathname)
}

/** A response worth keeping: a whole, plain answer from this origin. Never
 *  the page served in place of a missing file (text/html under assets/). */
function keepable(res, html) {
  if (!res || res.status !== 200 || res.type !== 'basic' || res.redirected) return false
  const type = res.headers.get('Content-Type') || ''
  return html ? type.startsWith('text/html') : !type.startsWith('text/html')
}

/** The page as this build wrote it: it names this build's entry files. */
async function isThisBuild(res) {
  const text = await res.clone().text()
  return SHELL.entry.every(f => text.includes(f))
}

self.addEventListener('install', event => {
  event.waitUntil((async () => {
    const cache = await caches.open(CACHE)
    await Promise.all(SHELL.files.map(async f => {
      const url = new URL(f, SCOPE).href
      const html = url === shellKey()
      // The page itself from the server, never the HTTP cache; hashed files
      // may come from it, since they never change.
      const res = await fetch(url, { cache: html ? 'no-store' : 'default', credentials: 'same-origin' })
      // A sign-in redirect, an error, or a server already on another build:
      // no install now; the browser tries again on a later visit.
      if (!keepable(res, html)) throw new Error(`${f}: not kept (${res.status} ${res.type})`)
      if (html && !(await isThisBuild(res))) throw new Error('index.html is from another build')
      await cache.put(url, res)
    }))
  })())
})

self.addEventListener('activate', event => {
  event.waitUntil((async () => {
    for (const k of await caches.keys()) if (k.startsWith(PREFIX) && k !== CACHE) await caches.delete(k)
    // No navigation preload: it sends every navigation in scope to the
    // server, and one this worker leaves alone (the platform's sign-in
    // return, which may carry a single-use token) would reach it twice.
    if (self.registration.navigationPreload) await self.registration.navigationPreload.disable().catch(() => {})
    await self.clients.claim()
  })())
})

self.addEventListener('message', event => {
  const d = event.data || {}
  if (d.type === 'skip-waiting') self.skipWaiting()
  // The page asks which build a waiting worker carries, to tell whether it
  // already runs it (web/src/shellWorker.ts).
  if (d.type === 'build' && event.ports && event.ports[0]) event.ports[0].postMessage({ build: SHELL.build, entry: SHELL.entry })
})

/** Navigations to pass to the network once, after the cached shell was
 *  shown and the network then answered with something else. */
let passUntil = 0

self.addEventListener('fetch', event => {
  const req = event.request
  if (req.method !== 'GET') return
  const url = new URL(req.url)
  if (req.mode === 'navigate') {
    if (!isShellNavigation(url)) return
    if (Date.now() < passUntil) { passUntil = 0; return }
    event.respondWith(openShell(event))
    return
  }
  if (req.headers.has('range') || !isShellFile(url)) return
  event.respondWith(shellFile(event, req, url))
})

async function shellFile(event, req, url) {
  const cache = await caches.open(CACHE)
  const hit = await cache.match(url.href, { ignoreVary: true })
  if (hit) return hit
  const res = await fetch(req)
  if (keepable(res, false)) event.waitUntil(cache.put(url.href, res.clone()).catch(() => {}))
  return res
}

const wait = ms => new Promise(r => setTimeout(r, ms))

async function openShell(event) {
  // A redirect comes back unfollowed (opaqueredirect), and the browser
  // follows it when it is the answer.
  const network = fetch(event.request)
  network.catch(() => {})
  const cached = await caches.open(CACHE).then(c => c.match(shellKey(), { ignoreVary: true })).catch(() => undefined)
  if (!cached) return network.catch(() => Response.error())
  const first = await Promise.race([network.then(r => ({ r }), () => ({ offline: true })), wait(NETWORK_WAIT_MS).then(() => null)])
  if (first && first.r) {
    event.waitUntil(checkBuild(first.r))
    return first.r
  }
  if (!first) event.waitUntil(followUp(event, network))
  return cached
}

const isPage = res => res.status === 200 && (res.headers.get('Content-Type') || '').startsWith('text/html')

/** The server's page is another build's. Its worker replaces this one when
 *  the browser looks for it; a server with none (a roll back to a release
 *  from before the worker) gets this one removed, so it does not show a
 *  newer page against an older server. */
async function checkBuild(res) {
  if (!isPage(res) || await isThisBuild(res).catch(() => true)) return
  try {
    await self.registration.update()
  } catch {
    await self.registration.unregister()
    for (const k of await caches.keys()) if (k.startsWith(PREFIX)) await caches.delete(k)
  }
}

/** The cached shell is up; the network answered later. Anything but the
 *  page (a redirect to sign-in, an error) is shown in its place, as it
 *  would have been without the worker. */
async function followUp(event, network) {
  let res
  try { res = await network } catch { return }
  if (isPage(res)) return checkBuild(res)
  const id = event.resultingClientId || event.clientId
  for (let i = 0; i < 50; i++) {
    const client = id ? await self.clients.get(id) : null
    if (client && 'navigate' in client) {
      passUntil = Date.now() + 15000
      await client.navigate(client.url).catch(() => { passUntil = 0 })
      return
    }
    await wait(100)
  }
}
