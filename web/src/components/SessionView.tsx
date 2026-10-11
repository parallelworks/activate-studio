import { useEffect, useRef, useState } from 'react'

/**
 * One platform session as a card: its state, the run that opened it, and
 * buttons that open it in a window of its own or a new tab.
 *
 * Sessions are not framed inside the Studio. The browser signs in to each
 * session's host through the platform's login page, which refuses to load
 * in a frame, and on the customer platform a session that was signed in
 * still refused its frame. A window of its own loads every session the way
 * the platform does. A session that is not running yet is polled until it
 * is, so a chat note posted while a run is still starting fills in.
 */

export interface SessionInfo {
  name: string; user: string; type: string; status: string; healthy: boolean | null
  url: string | null; ownHost: boolean; platformUrl?: string | null
  run: { slug: string; workflow: string; number: number | null; status: string | null } | null
  resource: string | null; createdAt: string | null
}

/** The windows this page opened, by session, so a second Open focuses the
 *  window the session already has. The browser cannot find them by name:
 *  each is cut off from this page (no opener), so the session gets no
 *  handle back to the Studio. */
const sessionWindows = new Map<string, Window>()

/** Whether this page runs inside another site's page, as the Studio does
 *  when opened from the platform: the top window is then on the platform. */
export function insidePlatform(): boolean {
  try { return window.top !== window && window.top!.location.origin !== window.location.origin } catch { return true }
}

/** Opens a URL in a new tab through a link, which a sandboxed frame allows
 *  where it may refuse a scripted window. */
function openTab(url: string): void {
  const a = document.createElement('a')
  a.href = url; a.target = '_blank'; a.rel = 'noopener noreferrer'
  document.body.appendChild(a); a.click(); a.remove()
}

/**
 * Opens a session, or focuses the window it already has. Inside the
 * platform's page the session's ACTIVATE page opens in a new tab: the
 * platform shows it embedded there, and a window opened from inside the
 * platform's frame inherits that frame's restrictions. Elsewhere the
 * session gets a window sized for work, and a new tab when the browser
 * blocks the window.
 */
export function openSessionWindow(s: { user: string; name: string; url: string | null; platformUrl?: string | null }): boolean {
  if (insidePlatform()) {
    const target = s.platformUrl || s.url
    if (!target) return false
    openTab(target)
    return true
  }
  if (!s.url) return false
  const key = `${s.user}/${s.name}`
  const open = sessionWindows.get(key)
  if (open && !open.closed) { open.focus(); return true }
  const w = window.open('', '_blank', 'popup,width=1280,height=860')
  if (!w) { openTab(s.url); return true }
  try { w.opener = null } catch { /* not ours to change */ }
  w.location.href = s.url
  sessionWindows.set(key, w)
  return true
}

export function SessionView({ user, name, initial }: { user: string; name: string; initial?: SessionInfo }) {
  const [s, setS] = useState<SessionInfo | null>(initial ?? null)
  const [error, setError] = useState('')
  const [blocked, setBlocked] = useState(false)

  useEffect(() => {
    let stop = false
    let timer = 0
    const load = () => {
      fetch(`/api/sessions/item?user=${encodeURIComponent(user)}&name=${encodeURIComponent(name)}`)
        .then(async r => {
          const d = await r.json().catch(() => ({}))
          if (!r.ok) throw new Error((d as { error?: string }).error ?? `${r.status}`)
          return d as SessionInfo
        })
        .then(d => {
          if (stop) return
          setS(d); setError('')
          if (d.status !== 'running') timer = window.setTimeout(load, 10_000)
        })
        .catch(e => { if (!stop) { setError(String((e as Error).message)); timer = window.setTimeout(load, 15_000) } })
    }
    load()
    return () => { stop = true; window.clearTimeout(timer) }
  }, [user, name])

  const running = s?.status === 'running'

  // Inside a chat embed, tell the chat the card's height so the reply does
  // not hold an empty box.
  const rootRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (window.parent === window || !rootRef.current) return
    const post = () => window.parent.postMessage({ type: 'ade-embed-height', height: rootRef.current?.scrollHeight ?? null }, window.location.origin)
    post()
    const ro = new ResizeObserver(post)
    ro.observe(rootRef.current)
    return () => ro.disconnect()
  }, [s, error, blocked])

  return (
    <div className="session-view" ref={rootRef}>
      <div className="session-bar">
        <span className={`status-dot ${running ? 'ok' : 'warn'}`} />
        <span className="session-name">{name}</span>
        <span className="muted session-meta">
          {[s?.run ? `${s.run.workflow} · ${s.run.slug}` : s?.type, s?.resource ? `on ${s.resource}` : '', s && !running ? (s.status || 'not running') : ''].filter(Boolean).join(' · ')}
        </span>
        {running && s?.url && (
          <span className="session-bar-actions">
            <button type="button" className="btn-primary" onClick={() => setBlocked(!openSessionWindow(s))}
              title={insidePlatform() ? 'Open its page on ACTIVATE, which shows it embedded' : 'Open in a window of its own'}>{insidePlatform() && s.platformUrl ? 'Open in ACTIVATE' : 'Open'}</button>
            <a className="btn-secondary" href={s.url} target="_blank" rel="noopener noreferrer">Open in new tab</a>
          </span>
        )}
      </div>
      {error && !s && <p className="session-note banner-error">{error}</p>}
      {s && !running && <p className="session-note muted">Session {name} is {s.status || 'not running'}. This card updates when it is running.</p>}
      {blocked && <p className="session-note muted">This session has no address yet.</p>}
    </div>
  )
}
