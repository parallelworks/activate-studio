/// <reference types="vite/client" />
import { useSyncExternalStore } from 'react'

/**
 * The page's side of the app shell kept on the device (web/shell/sw.js):
 * it registers the service worker, says when a newer build of the page
 * waits in it, and reloads onto that build when asked.
 *
 * The worker is registered in a production build in a browser, on HTTPS or
 * on this computer. Not in the desktop apps' windows: their page comes from
 * a server on the same computer, so there is nothing to wait for, and their
 * local proxy writes its own script into each page (desktop/wails/proxy.go),
 * which a kept copy would hold stale.
 */

type ShellWindow = Window & { __studioShell?: unknown; webkit?: { messageHandlers?: { studioShell?: unknown } } }

/** True in the macOS or Linux app's window. */
export function inDesktopWindow(w: ShellWindow = window as ShellWindow): boolean {
  return '__studioShell' in w || !!w.webkit?.messageHandlers?.studioShell
}

/** Whether this page keeps its shell on the device. */
export function shellWorkerWanted(w: ShellWindow = window as ShellWindow, prod = import.meta.env.PROD): boolean {
  return prod && 'serviceWorker' in w.navigator && w.isSecureContext && !inDesktopWindow(w)
}

/** The page's own entry files, as index.html names them (/assets/…). */
export function pageEntries(doc: Document = document): string[] {
  const out: string[] = []
  for (const el of doc.querySelectorAll('script[type="module"][src], link[rel="stylesheet"][href]')) {
    const ref = el.getAttribute('src') ?? el.getAttribute('href')
    if (!ref) continue
    try {
      const path = new URL(ref, doc.baseURI).pathname
      if (path.includes('/assets/')) out.push(path)
    } catch { /* not a URL */ }
  }
  return out
}

/** Whether the page runs the build whose entry files are `entry`. */
export function runsBuild(entry: string[], page: string[] = pageEntries()): boolean {
  return entry.length > 0 && entry.every(f => page.includes(f))
}

let newer = false
const listeners = new Set<() => void>()
function setNewer(v: boolean): void {
  if (newer === v) return
  newer = v
  for (const l of [...listeners]) l()
}

/** True while a newer build of the page waits on the device. */
export function useShellUpdate(): boolean {
  return useSyncExternalStore(l => { listeners.add(l); return () => { listeners.delete(l) } }, () => newer, () => false)
}

const within = <T,>(p: Promise<T>, ms: number): Promise<T | undefined> =>
  Promise.race([p, new Promise<undefined>(r => setTimeout(() => r(undefined), ms))])

/** Asks a worker which build it carries. */
function buildOf(w: ServiceWorker): Promise<{ build: string; entry: string[] } | undefined> {
  const ch = new MessageChannel()
  const got = new Promise<{ build: string; entry: string[] }>(r => { ch.port1.onmessage = e => r(e.data) })
  w.postMessage({ type: 'build' }, [ch.port2])
  return within(got, 3000)
}

/** A waiting worker: when the page already runs its build (the server
 *  answered the page itself), it takes over quietly; otherwise the page
 *  offers a reload. */
async function settle(reg: ServiceWorkerRegistration): Promise<void> {
  const w = reg.waiting
  if (!w || !navigator.serviceWorker.controller) { setNewer(false); return }
  const info = await buildOf(w)
  if (info && runsBuild(info.entry)) { w.postMessage({ type: 'skip-waiting' }); setNewer(false); return }
  setNewer(true)
}

let registration: ServiceWorkerRegistration | null = null
let lastCheck = 0
const CHECK_GAP_MS = 15 * 60_000

/** Registers the worker once the page has loaded. */
export function registerShellWorker(): void {
  if (!shellWorkerWanted()) return
  const start = () => {
    navigator.serviceWorker.register('./sw.js', { scope: './', updateViaCache: 'none' }).then(reg => {
      registration = reg
      lastCheck = Date.now()
      reg.addEventListener('updatefound', () => {
        const n = reg.installing
        n?.addEventListener('statechange', () => { if (n.state === 'installed') void settle(reg) })
      })
      void settle(reg)
      // A window left open looks for a new build now and then, and when it
      // is shown again after a while; the browser itself looks only when a
      // page opens.
      const look = () => {
        if (document.visibilityState !== 'visible' || Date.now() - lastCheck < CHECK_GAP_MS) return
        lastCheck = Date.now()
        void reg.update().catch(() => {})
      }
      document.addEventListener('visibilitychange', look)
      setInterval(look, CHECK_GAP_MS)
    }, () => { /* not kept: the page loads from the network as before */ })
  }
  if (document.readyState === 'complete') start()
  else window.addEventListener('load', start, { once: true })
}

/**
 * Reloads the page onto the newest build: a new worker is looked for and
 * put in charge first, so the reload is not answered by the build the page
 * runs now. Bounded, so a slow network delays the reload by seconds at
 * most; the reload itself still asks the server first.
 */
export async function reloadOntoNewest(): Promise<void> {
  try {
    const reg = registration ?? (shellWorkerWanted() ? await navigator.serviceWorker.getRegistration() : undefined) ?? null
    if (reg) {
      await within(reg.update().catch(() => {}), 5000)
      const n = reg.installing
      if (n) {
        await within(new Promise<void>(r => {
          const done = () => { if (n.state === 'installed' || n.state === 'redundant' || n.state === 'activated') r() }
          n.addEventListener('statechange', done)
          done()
        }), 10_000)
      }
      if (reg.waiting && navigator.serviceWorker.controller) {
        const changed = new Promise<void>(r => navigator.serviceWorker.addEventListener('controllerchange', () => r(), { once: true }))
        reg.waiting.postMessage({ type: 'skip-waiting' })
        await within(changed, 5000)
      }
    }
  } catch { /* reload as before */ }
  location.reload()
}
