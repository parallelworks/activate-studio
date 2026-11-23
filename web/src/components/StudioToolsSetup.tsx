import { useCallback, useEffect, useState } from 'react'
import { ACCOUNTS_HREF } from '../accounts'
import { Button, Callout } from '@parallelworks/ui'

/** GET /api/mcp/bridge: how this Studio's tools reach a pw code session of
 *  the viewer's on another machine. */
export interface BridgeReach {
  available: boolean
  /** Why the tools cannot reach another machine from this Studio. */
  why?: string
  /** The Studio's MCP endpoint at its address on ACTIVATE. */
  url?: string
  /** The command the person runs once on the machine. */
  command?: string
  /** The text in the command the person replaces with their key. */
  keyHole?: string
  /** Where the person makes an ACTIVATE API key. */
  keyPage?: string
  /** How many of the Studio's tools a session of theirs gets. */
  tools?: number
  /** Whether they have an ACTIVATE credential of their own in this Studio. */
  ownKey?: boolean
  platformTools?: boolean
  /** When a pw code session of theirs last reached the tools. */
  seen?: { at: string; method: string; client?: string } | null
}

async function readReach(): Promise<BridgeReach | null> {
  try {
    const r = await fetch('/api/mcp/bridge')
    return r.ok ? await r.json() as BridgeReach : null
  } catch { return null }
}

/**
 * The setup card for the Studio's tools in pw code on another machine. A
 * session the Studio starts has them already; one elsewhere gets them once
 * the person adds the Studio to pw code there, with the command shown here
 * and an ACTIVATE API key of theirs. "Check that it worked" asks the Studio
 * whether a session of theirs has reached the tools.
 */
export function StudioToolsSetup({ machineName, onDismiss, defaultOpen = false }: {
  machineName?: string
  onDismiss?: () => void
  defaultOpen?: boolean
}) {
  const [reach, setReach] = useState<BridgeReach | null>(null)
  const [open, setOpen] = useState(defaultOpen)
  const [checking, setChecking] = useState(false)
  const [checked, setChecked] = useState(false)
  const [copied, setCopied] = useState(false)
  const read = useCallback(async () => { setReach(await readReach()) }, [])
  useEffect(() => { void read() }, [read])
  if (!reach) return null
  const where = machineName || 'the other machine'
  const check = async () => {
    setChecking(true)
    await read()
    setChecking(false)
    setChecked(true)
  }
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(reach.command ?? '')
      setCopied(true)
    } catch { setCopied(false) }
  }
  if (reach.seen) {
    return (
      <Callout tone="success" size="sm" className="studio-tools-setup" onDismiss={onDismiss}>
        pw code reaches this Studio's tools as you: a session of yours last did at {new Date(reach.seen.at).toLocaleTimeString()}.
      </Callout>
    )
  }
  return (
    <Callout tone="info" size="sm" className="studio-tools-setup" onDismiss={onDismiss}>
      <p>
        pw code on {where} does not have this Studio's tools (library search, queries, reading files and the rest) yet.{' '}
        <button type="button" className="code-approval-more" aria-expanded={open} onClick={() => setOpen(o => !o)}>
          {open ? 'Hide the setup' : 'Set them up once'}
        </button>
      </p>
      {open && (!reach.available
        ? <p>{reach.why}</p>
        : <>
            <p>
              1. Make an ACTIVATE API key with the platform scope
              {reach.keyPage ? <> under <a href={reach.keyPage} target="_blank" rel="noreferrer">API keys in ACTIVATE</a></> : <> (Account, Authentication, API Keys)</>}.
            </p>
            <p>2. In a terminal on {where}, run this once, with your key in place of {reach.keyHole}:</p>
            <pre className="studio-tools-command">{reach.command}</pre>
            <p>
              <Button variant="secondary" size="sm" onClick={() => void copy()}>{copied ? 'Copied' : 'Copy the command'}</Button>
            </p>
            <p>
              3. Start a pw code session there, or in one already open type /mcp and reconnect studio. Every session there then
              has the Studio's tools ({reach.tools} of them), as you, with your own libraries.
              {!reach.platformTools && <> The platform's tools come once you sign in with ACTIVATE in this Studio, under <a href={ACCOUNTS_HREF}>Settings, Accounts</a>.</>}
            </p>
            <p>
              <Button variant="secondary" size="sm" loading={checking} disabled={checking} onClick={() => void check()}>Check that it worked</Button>
              {checked && !checking && <span className="muted studio-tools-check"> Not yet: no pw code session of yours has reached the tools since the Studio started.</span>}
            </p>
          </>)}
    </Callout>
  )
}
