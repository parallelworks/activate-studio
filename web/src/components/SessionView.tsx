import { useEffect, useRef, useState } from 'react'
import { openBeside, openFullView } from '../sideBySide'

/**
 * One platform session as a card: its state, the run that opened it, and
 * buttons that open it in a window of its own or a new tab.
 *
 * The card does not frame the session: the browser signs in to each
 * session's host through the platform's login page, which refuses to load
 * in a frame. A window of its own loads every session the way the platform
 * does, and Beside chat sends it to the chat's side view, which shows it
 * through the Studio's relay. A session that is not running yet is polled until it
 * is, so a chat note posted while a run is still starting fills in.
 */

export interface SessionInfo {
  name: string; user: string; type: string; status: string; healthy: boolean | null
  url: string | null; ownHost: boolean; platformUrl?: string | null
  run: { slug: string; workflow: string; number: number | null; status: string | null } | null
  resource: string | null; createdAt: string | null
}

/** Whether a session belongs to a run: the run the platform recorded for
 *  it, or, with none recorded, a name that is the run's id or ends with it.
 *  A job that registers its own endpoint (a study viewer) leaves the record
 *  empty, and those workflows name the endpoint <prefix>-<run id>; the
 *  server applies the same rule (sessions.ts). */
export function sessionOfRun(s: Pick<SessionInfo, 'name' | 'run'>, slug: string): boolean {
  if (!slug) return false
  if (s.run) return s.run.slug === slug
  return s.name === slug || s.name.endsWith(`-${slug}`)
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
  // Beside the Studio: the right half of the screen, with the Studio's
  // window moved to the left half where it is an installed app.
  if (!openBeside(s.url, `session:${key}`)) openTab(s.url)
  return true
}

export function SessionView({ user, name, host = '', run = '', initial }: { user: string; name: string; host?: string; run?: string; initial?: SessionInfo }) {
  // A card inside a chat reply is a same-origin frame of the chat page,
  // which keeps the side view.
  const inChat = (() => { try { return window.parent !== window && window.parent.location.origin === window.location.origin } catch { return false } })()
  const [s, setS] = useState<SessionInfo | null>(initial ?? null)
  const [error, setError] = useState('')
  const [blocked, setBlocked] = useState(false)
  // In a chat reply the session shows under its bar, through the Studio's
  // relay, unless the reader hides it.
  const [inline, setInline] = useState(true)

  useEffect(() => {
    let stop = false
    let timer = 0
    const load = () => {
      fetch(`/api/sessions/item?user=${encodeURIComponent(user)}&name=${encodeURIComponent(name)}${host ? `&host=${encodeURIComponent(host)}` : ''}${run ? `&run=${encodeURIComponent(run)}` : ''}`)
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
  }, [user, name, host, run])

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
  }, [s, error, blocked, inline])

  return (
    <div className="session-view" ref={rootRef}>
      <div className="session-bar">
        <span className={`status-dot ${running ? 'ok' : 'warn'}`} />
        <span className="session-name">{s?.name ?? (name || (run ? `Session of ${run}` : host))}</span>
        <span className="muted session-meta">
          {[s?.run ? `${s.run.workflow} · ${s.run.slug}` : s?.type, s?.resource ? `on ${s.resource}` : '', s && !running ? (s.status || 'not running') : ''].filter(Boolean).join(' · ')}
        </span>
        {running && s?.url && (
          <span className="session-bar-actions">
            <button type="button" className="btn-primary" onClick={() => setBlocked(!openSessionWindow(s))}
              title={insidePlatform() ? 'Open its page on ACTIVATE, which shows it embedded' : 'Open in a window of its own'}>{insidePlatform() && s.platformUrl ? 'Open in ACTIVATE' : 'Open'}</button>
            {inChat && s.ownHost && (
              <>
                <button type="button" className="btn-secondary" onClick={() => setInline(v => !v)}>{inline ? 'Hide' : 'Show here'}</button>
                <button type="button" className="btn-secondary" title="Show it in the side view beside the chat"
                  onClick={() => window.parent.postMessage({ type: 'ade-side-view', session: s }, window.location.origin)}>Beside chat</button>
                <button type="button" className="btn-secondary" title="Show it over the whole Studio; Close or Escape returns"
                  onClick={() => openFullView(s, true)}>Full screen</button>
              </>
            )}
            <a className="btn-secondary" href={s.url} target="_blank" rel="noopener noreferrer">Open in new tab</a>
          </span>
        )}
      </div>
      {error && !s && <p className="session-note banner-error">{error}</p>}
      {s && !running && <p className="session-note muted">Session {name} is {s.status || 'not running'}. This card updates when it is running.</p>}
      {blocked && <p className="session-note muted">This session has no address yet.</p>}
      {inChat && inline && running && s?.ownHost && s.url && (
        <iframe className="session-inline" title={s.name} src={`/api/session-proxy/${new URL(s.url).host}/`} />
      )}
    </div>
  )
}
