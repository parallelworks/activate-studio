import { useCallback, useEffect, useRef, useState, type PointerEvent as ReactPointerEvent } from 'react'
import { Button, Callout, Loader, Popover, Select, StatusDot, type BadgeVariant } from '@parallelworks/ui'
import type RFB from '@novnc/novnc'
import { NeedsActivate } from './NeedsActivate'
import { statusLook } from '../statusLook'
import { needsActivate } from '../accounts'

/**
 * A platform desktop session drawn in the Studio. A desktop has no host of
 * its own and its platform page refuses frames, so the Studio draws it with
 * noVNC's RFB client over the server's relay of the desktop's socket
 * (/api/session-desktop/<user>/<name>/websockify), which connects as the
 * viewer. The bar has the view (fit, scale or actual size), quality,
 * clipboard, Ctrl+Alt+Del, reconnect and the desktop's start-up logs. A
 * desktop that is not running yet is polled until it is; a dropped
 * connection is tried again with a growing pause, and one the Studio
 * refused (no sign-in, no credential of the person's own) says why and
 * waits for Reconnect.
 */

type Phase = 'waiting' | 'connecting' | 'connected' | 'closed' | 'failed' | 'refused'
export type DesktopFit = 'fit' | 'scale' | 'actual'
export type DesktopQuality = 'low' | 'medium' | 'high'

const FITS: { value: DesktopFit; label: string; description: string }[] = [
  { value: 'fit', label: 'Fit', description: 'The desktop takes the size of the view you used last' },
  { value: 'scale', label: 'Scale', description: 'The desktop keeps its size, scaled to this view' },
  { value: 'actual', label: 'Actual size', description: 'Pixel for pixel; Pan or the scroll bars reach the rest of a larger desktop' },
]
const QUALITIES: { value: DesktopQuality; label: string }[] = [
  { value: 'high', label: 'High quality' },
  { value: 'medium', label: 'Medium quality' },
  { value: 'low', label: 'Low bandwidth' },
]
/** noVNC's JPEG quality and zlib compression, 0 to 9 each. */
const LEVELS: Record<DesktopQuality, { quality: number; compression: number }> = {
  high: { quality: 9, compression: 2 },
  medium: { quality: 6, compression: 6 },
  low: { quality: 2, compression: 9 },
}

const MAX_TRIES = 30
/** Tries for a desktop the relay cannot find (404) while the platform
 *  still lists it as running: about 23 seconds of pauses, then it stops. */
export const MISSING_TRIES = 5
export const retryDelay = (n: number) => Math.min(1000 * 2 ** n, 8000)

/** The relay's address for a desktop, on the page's own origin. */
export function desktopSocketUrl(user: string, name: string, loc: Pick<Location, 'protocol' | 'host'> = window.location): string {
  return `${loc.protocol === 'https:' ? 'wss:' : 'ws:'}//${loc.host}/api/session-desktop/${encodeURIComponent(user)}/${encodeURIComponent(name)}/websockify`
}
const logsUrl = (user: string, name: string) => `/api/session-desktop/${encodeURIComponent(user)}/${encodeURIComponent(name)}/logs`

/** Whether a session is a platform desktop the Studio draws itself: the
 *  server says so (type desktop with VNC software), by the rule its relay
 *  opens desktops by, so a desktop the relay would refuse is not drawn. */
export function isDesktop(s: { desktop?: boolean | null } | null | undefined): boolean {
  return s?.desktop === true
}

/** noVNC loads once, in a chunk of its own, when a desktop first shows. */
let rfbModule: Promise<{ default: typeof RFB }> | null = null
const loadRFB = () => (rfbModule ??= import('@novnc/novnc'))

function stored<T extends string>(key: string, fallback: T, allowed: readonly { value: T }[]): T {
  try {
    const v = localStorage.getItem(key)
    return allowed.some(a => a.value === v) ? v as T : fallback
  } catch { return fallback }
}
function store(key: string, v: string): void { try { localStorage.setItem(key, v) } catch { /* not kept */ } }

interface Logs { xstartup: string; xvnc: string }
/** The relay's logs, or the reason it would refuse the desktop. */
async function readLogs(user: string, name: string): Promise<{ status: number; logs?: Logs; error?: string }> {
  const r = await fetch(logsUrl(user, name))
  const d = await r.json().catch(() => ({})) as Partial<Logs> & { error?: string }
  if (!r.ok) return { status: r.status, error: d.error ?? `HTTP ${r.status}` }
  return { status: r.status, logs: { xstartup: d.xstartup ?? '', xvnc: d.xvnc ?? '' } }
}

/** Fit resizes a desktop to one view at a time: the one used last (shown
 *  last, or clicked). The window's focus is no guide: the desktop app's
 *  WebKitGTK window can report none while it takes keys. So a desktop in the side view and over the whole
 *  Studio, or in a chat card too, does not change size between them. A
 *  chat card is a page of its own in a frame, so the claim is also sent to
 *  the Studio's other pages and frames, and view ids are unique across
 *  them. */
let resizer = ''
/** This page's views, the one used last at the end: when the view that
 *  resizes goes away (Full screen closed), the one used before it takes
 *  over. */
const views: string[] = []
const realm = Math.random().toString(36).slice(2, 10)
let nextView = 1
const resizerChanged = new Set<() => void>()
const claims: BroadcastChannel | null = (() => {
  try { return typeof BroadcastChannel === 'function' ? new BroadcastChannel('studio.desktop.resize') : null } catch { return null }
})()
function takeClaim(id: string): void {
  if (resizer === id) return
  resizer = id
  resizerChanged.forEach(f => f())
}
claims?.addEventListener('message', (e: MessageEvent) => { if (typeof e.data === 'string') takeClaim(e.data) })
function claimResize(id: string): void {
  const at = views.indexOf(id)
  if (at >= 0) { views.splice(at, 1); views.push(id) }
  if (resizer === id) return
  takeClaim(id)
  try { claims?.postMessage(id) } catch { /* this page's views still agree */ }
}

const tail = (s: string, n = 40) => s.split('\n').slice(-n).join('\n').trim()

export function DesktopView({ user, name, className = '', onClosed }: { user: string; name: string; className?: string; onClosed?: () => void }) {
  const screen = useRef<HTMLDivElement>(null)
  const rfb = useRef<RFB | null>(null)
  const tries = useRef(0)
  const [status, setStatus] = useState<string | null>(null)
  const [recheck, setRecheck] = useState(0)
  const [phase, setPhase] = useState<Phase>('waiting')
  const [reason, setReason] = useState('')
  const [attempt, setAttempt] = useState(0)
  const [fit, setFit] = useState<DesktopFit>(() => stored('studio.desktop.fit', 'fit', FITS))
  const [quality, setQuality] = useState<DesktopQuality>(() => stored('studio.desktop.quality', 'high', QUALITIES))
  const [fromDesktop, setFromDesktop] = useState('')
  const [toDesktop, setToDesktop] = useState('')
  const [showLogs, setShowLogs] = useState(false)
  // In Actual size, Pan makes a drag or the wheel move around the desktop
  // in place of reaching it.
  const [pan, setPan] = useState(false)
  const [logs, setLogs] = useState<Logs | null>(null)
  const [id] = useState(() => `${realm}-${nextView++}`)
  const settings = useRef({ fit, quality, pan })
  settings.current = { fit, quality, pan }

  // The view and quality on a client: at once when it is made, so Fit's
  // first resize is asked for as soon as the desktop can take one, and
  // whenever they change. Fit resizes the desktop only from the view used
  // last, while its page is shown. Actual size leaves the desktop unscaled
  // and unclipped, so the page scrolls it with scroll bars; the wheel over
  // the desktop goes to the desktop's own windows.
  const apply = useCallback((c: RFB | null) => {
    if (!c) return
    const { fit: f, quality: q, pan: p } = settings.current
    c.scaleViewport = f !== 'actual'
    c.clipViewport = false
    c.resizeSession = f === 'fit' && resizer === id && document.visibilityState === 'visible'
    c.viewOnly = f === 'actual' && p
    c.qualityLevel = LEVELS[q].quality
    c.compressionLevel = LEVELS[q].compression
  }, [id])
  useEffect(() => {
    views.push(id)
    claimResize(id)
    const on = () => apply(rfb.current)
    resizerChanged.add(on)
    document.addEventListener('visibilitychange', on)
    return () => {
      resizerChanged.delete(on)
      document.removeEventListener('visibilitychange', on)
      const at = views.indexOf(id)
      if (at >= 0) views.splice(at, 1)
      if (resizer === id) {
        resizer = ''
        const before = views[views.length - 1]
        if (before) claimResize(before)
      }
    }
  }, [id, apply])

  // The session's state on the platform, until it is running.
  useEffect(() => {
    let live = true
    let timer = 0
    const tick = () => {
      fetch(`/api/sessions/item?user=${encodeURIComponent(user)}&name=${encodeURIComponent(name)}`)
        .then(async r => {
          const d = await r.json().catch(() => ({})) as { status?: string; error?: string }
          if (!r.ok) throw new Error(d.error ?? `${r.status}`)
          return d
        })
        .then(d => {
          if (!live) return
          setStatus(d.status ?? '')
          if (d.status !== 'running') timer = window.setTimeout(tick, 10_000)
        })
        .catch(e => { if (live) { setStatus(''); setReason(String((e as Error).message)); timer = window.setTimeout(tick, 15_000) } })
    }
    tick()
    return () => { live = false; window.clearTimeout(timer) }
  }, [user, name, recheck])

  const running = status === 'running'

  // Why a connection dropped: the relay's own answer decides between
  // trying again, waiting for the session, and saying it was refused.
  const diagnose = useCallback(async (): Promise<'retry' | 'missing' | 'stop'> => {
    try {
      const r = await readLogs(user, name)
      if (r.status === 401 || r.status === 403 || r.status === 409) { setPhase('refused'); setReason(r.error ?? ''); return 'stop' }
      if (r.status === 404) { setReason(r.error ?? ''); return 'missing' }
      if (r.error) setReason(r.error)
    } catch { /* the Studio did not answer; try again */ }
    return 'retry'
  }, [user, name])

  // One RFB connection per attempt while the session runs.
  useEffect(() => {
    if (!running || !screen.current) { setPhase('waiting'); return }
    let gone = false
    let timer = 0
    let client: RFB | null = null
    let open = false
    setPhase('connecting')
    loadRFB().then(({ default: RFBClient }) => {
      if (gone || !screen.current) return
      screen.current.replaceChildren()
      client = new RFBClient(screen.current, desktopSocketUrl(user, name), { wsProtocols: ['binary'], shared: true })
      rfb.current = client
      client.showDotCursor = true
      client.background = 'transparent'
      apply(client)
      open = true
      client.addEventListener('connect', () => {
        if (gone) return
        tries.current = 0
        setReason(''); setPhase('connected')
      })
      client.addEventListener('disconnect', (e: Event) => {
        open = false
        if (gone) return
        rfb.current = null
        if ((e as CustomEvent<{ clean: boolean }>).detail?.clean) { setPhase('closed'); setReason('The desktop closed the connection.'); return }
        void diagnose().then(next => {
          if (gone || next === 'stop') return
          // No desktop at the relay: the session may have stopped. Its
          // state is read again after a pause, and a desktop the platform
          // still lists as running is tried again, a few times only.
          if (next === 'missing') {
            if (tries.current >= MISSING_TRIES) { setPhase('failed'); return }
            tries.current++
            setPhase('connecting')
            timer = window.setTimeout(() => { setStatus(null); setRecheck(n => n + 1) }, retryDelay(tries.current - 1))
            return
          }
          if (tries.current >= MAX_TRIES) { setPhase('failed'); return }
          tries.current++
          setPhase('connecting')
          timer = window.setTimeout(() => setAttempt(a => a + 1), retryDelay(tries.current - 1))
        })
      })
      client.addEventListener('securityfailure', (e: Event) => {
        if (gone) return
        setPhase('failed')
        setReason(`The desktop refused the VNC handshake${(e as CustomEvent<{ reason?: string }>).detail?.reason ? `: ${(e as CustomEvent<{ reason?: string }>).detail.reason}` : '.'}`)
      })
      client.addEventListener('clipboard', (e: Event) => {
        const text = (e as CustomEvent<{ text: string }>).detail?.text ?? ''
        setFromDesktop(text)
        // Copied on this computer too where the browser allows it unasked.
        navigator.clipboard?.writeText(text).catch(() => { /* shown in Clipboard instead */ })
      })
    }).catch(() => { if (!gone) { setPhase('failed'); setReason('The desktop viewer did not load.') } })
    return () => {
      gone = true
      window.clearTimeout(timer)
      if (open) client?.disconnect()
      if (rfb.current === client) rfb.current = null
    }
  }, [running, attempt, user, name, diagnose, apply])

  useEffect(() => { apply(rfb.current) }, [fit, quality, pan, phase, apply])

  // Pan: a drag moves the view over the desktop. The view is noVNC's own
  // scrolling box inside the screen.
  const panning = fit === 'actual' && pan
  const startPan = (e: ReactPointerEvent) => {
    const box = screen.current?.firstElementChild as HTMLElement | null
    if (!box || e.button !== 0) return
    e.preventDefault()
    const from = { x: e.clientX, y: e.clientY, left: box.scrollLeft, top: box.scrollTop }
    const move = (m: PointerEvent) => { box.scrollLeft = from.left - (m.clientX - from.x); box.scrollTop = from.top - (m.clientY - from.y) }
    const end = () => { window.removeEventListener('pointermove', move); window.removeEventListener('pointerup', end); window.removeEventListener('pointercancel', end) }
    window.addEventListener('pointermove', move); window.addEventListener('pointerup', end); window.addEventListener('pointercancel', end)
  }

  // A closed or failed connection may be a stopped desktop: whoever shows
  // this view (a chat card) checks the session again at once.
  const closedRef = useRef(onClosed)
  closedRef.current = onClosed
  useEffect(() => { if (phase === 'closed' || phase === 'failed') closedRef.current?.() }, [phase])

  // The start-up logs, while it connects or when asked for.
  const wantLogs = showLogs || phase === 'connecting' || phase === 'failed'
  useEffect(() => {
    if (!wantLogs || !running) return
    let live = true
    let timer = 0
    const tick = () => readLogs(user, name)
      .then(r => { if (live && r.logs) setLogs(r.logs) })
      .catch(() => { /* kept as they were */ })
      .finally(() => { if (live) timer = window.setTimeout(tick, 2000) })
    tick()
    return () => { live = false; window.clearTimeout(timer) }
  }, [wantLogs, running, user, name])

  const reconnect = () => { tries.current = 0; setReason(''); if (!running) setRecheck(n => n + 1); setAttempt(a => a + 1) }

  const dot: BadgeVariant = phase === 'connected' ? 'success' : phase === 'refused' || phase === 'failed' ? 'error' : phase === 'closed' ? 'muted' : phase === 'connecting' ? 'info' : statusLook(status ?? '').variant ?? 'warning'
  const state = {
    waiting: status === null ? 'Checking the desktop…' : `The desktop is ${status || 'not running'}`,
    connecting: tries.current ? 'Reconnecting…' : 'Connecting…',
    connected: 'Connected',
    closed: 'Disconnected',
    failed: 'Could not connect',
    refused: 'Not opened',
  }[phase]
  const logText = logs ? [tail(logs.xstartup), tail(logs.xvnc)].filter(Boolean).join('\n\n') : ''
  const connected = phase === 'connected'

  return (
    <div className={`desktop-view ${className}`} data-phase={phase} data-fit={fit}>
      <div className="desktop-bar">
        <StatusDot variant={dot} pulse={phase === 'connecting'} size={7} />
        <span className="desktop-state" role="status">{state}</span>
        <span className="desktop-actions">
          <Select aria-label="View" size="sm" width="content" items={FITS} value={fit}
            onValueChange={v => { if (v) { setFit(v); store('studio.desktop.fit', v) } }} />
          {fit === 'actual' && <Button variant="ghost" size="sm" aria-pressed={pan}
            title={pan ? 'Pan is on: drag or scroll to move around the desktop. Press again to use the desktop.' : 'Drag or scroll to move around a desktop larger than this view'}
            onClick={() => setPan(v => !v)}>Pan</Button>}
          <Select aria-label="Quality" size="sm" width="content" items={QUALITIES} value={quality}
            onValueChange={v => { if (v) { setQuality(v); store('studio.desktop.quality', v) } }} />
          <Popover title="Clipboard" align="end" className="desktop-clip"
            trigger={<Button variant="ghost" size="sm">Clipboard</Button>}>
            <label className="desktop-clip-label" htmlFor={`clip-from-${name}`}>Copied on the desktop</label>
            <textarea id={`clip-from-${name}`} className="desktop-clip-text" readOnly rows={3} value={fromDesktop} placeholder="Text copied on the desktop shows here." />
            <Button variant="secondary" size="sm" disabled={!fromDesktop}
              onClick={() => { navigator.clipboard?.writeText(fromDesktop).catch(() => { /* select it by hand */ }) }}>Copy to this computer</Button>
            <label className="desktop-clip-label" htmlFor={`clip-to-${name}`}>Send to the desktop</label>
            <textarea id={`clip-to-${name}`} className="desktop-clip-text" rows={3} value={toDesktop} onChange={e => setToDesktop(e.target.value)} placeholder="Paste here, then Send; paste on the desktop as usual." />
            <span className="desktop-clip-row">
              <Button variant="primary" size="sm" disabled={!connected || !toDesktop} onClick={() => rfb.current?.clipboardPasteFrom(toDesktop)}>Send</Button>
              <Button variant="ghost" size="sm" disabled={!connected}
                onClick={() => { navigator.clipboard?.readText().then(t => { setToDesktop(t); rfb.current?.clipboardPasteFrom(t) }).catch(() => { /* paste into the box instead */ }) }}>Send what this computer copied</Button>
            </span>
          </Popover>
          <Button variant="ghost" size="sm" disabled={!connected} title="Send Ctrl+Alt+Del to the desktop" onClick={() => rfb.current?.sendCtrlAltDel()}>Ctrl+Alt+Del</Button>
          <Button variant="ghost" size="sm" aria-pressed={showLogs} title="The desktop's start-up logs" onClick={() => setShowLogs(v => !v)}>Logs</Button>
          {!connected && phase !== 'connecting' && (phase !== 'waiting' || status === '') && <Button variant="secondary" size="sm" onClick={reconnect}>Reconnect</Button>}
        </span>
      </div>
      <div className="desktop-stage">
        <div className={`desktop-screen${panning ? ' desktop-panning' : ''}`} ref={screen}
          onPointerDownCapture={e => { claimResize(id); if (panning) startPan(e); else rfb.current?.focus() }} />
        {!connected && (
          <div className="desktop-overlay">
            {phase === 'waiting' && <p className="muted">{status === null ? <Loader inline size={14} label="Checking the desktop…" /> : `Desktop ${name} is ${status || 'not running'}. It shows here once it is running.`}</p>}
            {phase === 'connecting' && <p className="muted"><Loader inline size={14} label={tries.current ? 'Reconnecting to the desktop…' : 'Connecting to the desktop…'} /></p>}
            {phase === 'closed' && <p className="muted">{reason}</p>}
            {phase === 'refused' && needsActivate(reason)
              ? <NeedsActivate>{reason}</NeedsActivate>
              : (phase === 'failed' || phase === 'refused') && <Callout variant="bar" tone="error">{reason || 'The desktop did not answer.'}</Callout>}
            {reason && (phase === 'connecting' || phase === 'waiting') && <p className="muted desktop-reason">{reason}</p>}
            {logText && (phase === 'connecting' || phase === 'failed') && <pre className="desktop-logs" aria-label="Start-up logs">{logText}</pre>}
          </div>
        )}
      </div>
      {connected && showLogs && <pre className="desktop-logs desktop-logs-docked" aria-label="Start-up logs">{logText || 'No logs yet.'}</pre>}
    </div>
  )
}
