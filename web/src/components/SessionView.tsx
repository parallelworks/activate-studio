import { useEffect, useMemo, useRef, useState } from 'react'
import { currentPlace, frameVerdict, type FrameVerdict } from '../sessionFrame'

/**
 * One platform session, shown in a frame where the browser allows it and
 * otherwise as a card that opens it in its own tab. A session that is not
 * running yet is polled until it is, so a chat note posted while a run is
 * still starting fills in on its own.
 */

export interface SessionInfo {
  name: string; user: string; type: string; status: string; healthy: boolean | null
  url: string | null; ownHost: boolean
  run: { slug: string; workflow: string; number: number | null; status: string | null } | null
  resource: string | null; createdAt: string | null
}

const WHY: Record<Exclude<FrameVerdict, 'frame'>, string> = {
  'platform-address': 'It is served under the platform\'s own address, which does not allow being shown inside another page.',
  'inside-platform': 'The Studio is open inside the platform\'s page. Open the Studio in its own tab to see sessions inside it.',
  'other-site': 'It is on a different domain from this Studio.',
  'no-address': 'The platform has not given it an address yet.',
}

export function SessionView({ user, name, initial }: { user: string; name: string; initial?: SessionInfo }) {
  const [s, setS] = useState<SessionInfo | null>(initial ?? null)
  const [error, setError] = useState('')
  const [nonce, setNonce] = useState(0)

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

  const verdict = useMemo(() => (s ? frameVerdict(s, currentPlace()) : 'no-address'), [s])
  const running = s?.status === 'running'
  const framed = !!s && running && verdict === 'frame'

  // Inside a chat embed, a view with no frame is a few lines tall; tell the
  // chat its height so the reply does not hold an empty box. A framed
  // session takes the chat's default height.
  const rootRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (window.parent === window) return
    const post = () => window.parent.postMessage({ type: 'ade-embed-height', height: framed ? null : rootRef.current?.scrollHeight ?? null }, window.location.origin)
    post()
    if (framed || !rootRef.current) return
    const ro = new ResizeObserver(post)
    ro.observe(rootRef.current)
    return () => ro.disconnect()
  }, [framed, s, error])

  return (
    <div className={`session-view${framed ? '' : ' compact'}`} ref={rootRef}>
      <div className="session-bar">
        <span className={`status-dot ${running ? 'ok' : 'warn'}`} />
        <span className="session-name">{name}</span>
        {s?.run && <span className="muted session-meta">{s.run.workflow} · {s.run.slug}</span>}
        {s && !running && <span className="muted session-meta">{s.status || 'not running'}</span>}
        <span className="session-bar-actions">
          {running && verdict === 'frame' && (
            <button type="button" className="btn-ghost" onClick={() => setNonce(n => n + 1)} title="Reload the session">Reload</button>
          )}
          {s?.url && <a className="btn-secondary" href={s.url} target="_blank" rel="noopener noreferrer">Open in new tab</a>}
        </span>
      </div>
      {error && !s && <p className="session-note banner-error">{error}</p>}
      {s && !running && <p className="session-note muted">Session {name} is {s.status || 'not running'}. This view updates when it is running.</p>}
      {framed && (
        <>
          <iframe
            key={nonce}
            src={s.url!}
            title={name}
            className="session-frame"
            sandbox="allow-same-origin allow-scripts allow-forms allow-popups allow-popups-to-escape-sandbox allow-downloads allow-modals"
            allow="clipboard-read; clipboard-write; fullscreen"
          />
          <p className="session-hint muted">
            Empty, or refused to connect? Open the session in a new tab once, which signs this browser in to it, then reload here.
          </p>
        </>
      )}
      {s && running && verdict !== 'frame' && (
        <p className="session-note muted">This session opens in its own tab. {WHY[verdict]}</p>
      )}
    </div>
  )
}
