import { useEffect, useRef, useState } from 'react'
import { Button, Callout, type CalloutTone, CopyButton, Loader, NumberInput } from '@parallelworks/ui'
import { followStudioToPort } from '../nativeShell'
import { errText, json, post, type SetupStatus } from './PrimaryFolder'

/**
 * Settings, General, Server: the port the Studio listens on, which its MCP
 * endpoint (/api/mcp) shares. The macOS app saves a new port for its
 * launcher and restarts on it, and its window follows; elsewhere the port
 * is the PORT environment variable, read at startup.
 */

const sleep = (ms: number) => new Promise(r => setTimeout(r, ms))

/** The Studio's address on another port of this host. */
export function addressOn(port: number, loc: Pick<Location, 'protocol' | 'hostname'> = location): string {
  return `${loc.protocol}//${loc.hostname}:${port}/`
}

/** Waits for a server to answer at `url`. Another port is another origin,
 *  so this asks without reading the answer: any answer means it is up. */
export async function waitForAddress(url: string, pollMs: number, alive: () => boolean, limitMs = 90_000): Promise<boolean> {
  const until = Date.now() + limitMs
  while (alive() && Date.now() < until) {
    await sleep(pollMs)
    try {
      await fetch(`${url}api/version`, { mode: 'no-cors', cache: 'no-store' })
      return true
    } catch { /* not up yet */ }
  }
  return false
}

export function ServerSection({ pollMs = 1000, navigate = (url: string) => { location.href = url } }: {
  pollMs?: number
  /** For tests: how the page moves to the new address. */
  navigate?: (url: string) => void
}) {
  const [status, setStatus] = useState<SetupStatus | null>(null)
  const [port, setPort] = useState<number | null>(null)
  const [busy, setBusy] = useState(false)
  const [moving, setMoving] = useState<number | null>(null)
  const [note, setNote] = useState<{ tone: CalloutTone; text: string } | null>(null)
  const alive = useRef(true)
  useEffect(() => {
    alive.current = true
    json<SetupStatus>('/api/setup/status').then(s => {
      if (!alive.current) return
      setStatus(s)
      setPort(s.portNext ?? s.port ?? null)
    }).catch(() => {})
    return () => { alive.current = false }
  }, [])

  const running = status?.port ?? (Number(location.port) || null)
  const mcp = `${location.origin}/api/mcp`

  const save = async () => {
    if (!port) return
    setBusy(true)
    setNote(null)
    try {
      const r = await post<{ saved: boolean; restarting: boolean; port: number; note?: string }>('/api/setup/port', { port })
      if (r.restarting) {
        setMoving(r.port)
        const url = addressOn(r.port)
        // The app's window follows the Studio itself; a browser tab moves
        // once the new address answers.
        if (await followStudioToPort(r.port, location.hash)) return
        const up = await waitForAddress(url, pollMs, () => alive.current)
        if (!alive.current) return
        if (up) navigate(`${url}${location.hash}`)
        else { setMoving(null); setNote({ tone: 'error', text: `The Studio did not answer at ${url}. Open the app again.` }) }
        return
      }
      if (r.note) setNote({ tone: r.saved ? 'info' : 'warning', text: r.note })
      setStatus(s => s && { ...s, portNext: r.saved && r.port !== s.port ? r.port : null })
    } catch (e) {
      setNote({ tone: 'error', text: errText(e) })
    } finally {
      if (alive.current) setBusy(false)
    }
  }

  return (
    <div className="settings-field">
      <p className="field-label" style={{ marginTop: 0 }}>Port</p>
      {status?.standalone && status.launcher ? (
        <>
          <p className="muted view-sub">
            The Studio and its MCP endpoint answer on one port of this computer, now {running}. A new port takes a
            restart, after which this window opens the new address. MCP clients set up with the old address need the
            new one, and choices this window remembers, such as the theme and open panels, start over there.
          </p>
          {moving !== null ? (
            <Loader inline size={16} label={`Restarting the Studio on port ${moving}…`} />
          ) : (
            <div className="key-row">
              <NumberInput aria-label="Port" className="port-input" min={1024} max={65535} value={port} onValueChange={v => setPort(v)} />
              <Button variant="primary" loading={busy} disabled={!port || port === (status.portNext ?? running)}
                onClick={() => void save()}>
                Save and restart
              </Button>
            </div>
          )}
          {status.portNext && moving === null && (
            <p className="muted key-note">Port {status.portNext} is saved for the next start.</p>
          )}
        </>
      ) : (
        <p className="muted view-sub">
          The Studio and its MCP endpoint answer on one port{running ? `, now ${running}` : ''}. It is the{' '}
          <code>PORT</code> environment variable, read when the Studio starts (4080 when unset): set it, for
          example <code>PORT=4090</code>, and restart the Studio.
        </p>
      )}
      {note && <Callout tone={note.tone} live>{note.text}</Callout>}
      <p className="field-label">MCP endpoint</p>
      <p className="muted view-sub">
        <code>{mcp}</code><CopyButton text={mcp} label="MCP address" /> Settings, External access has the commands that
        connect a client to it.
      </p>
    </div>
  )
}
