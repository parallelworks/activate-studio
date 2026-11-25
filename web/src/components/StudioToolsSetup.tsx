import { useCallback, useEffect, useRef, useState } from 'react'
import { Button, Callout } from '@parallelworks/ui'
import { AllowsNote, RemoveTools } from './MachineToolsAllow'
import { KEEPS_TOOLS, type MachineTools, type SetupResult, machineHasSaid, runSetup, setupSaid, takeAutoSetup } from './studioTools'

/** GET /api/mcp/bridge: how this Studio's tools reach a pw code session of
 *  the viewer's on another machine. */
export interface BridgeReach {
  available: boolean
  /** Why the tools cannot reach another machine from this Studio. */
  why?: string
  /** The Studio's MCP endpoint at its address on ACTIVATE. */
  url?: string
  /** The command the person runs once on the machine, with a key of theirs. */
  command?: string
  /** The command to paste in a terminal on the machine that adds what Set up
   *  for me adds: it uses pw's own sign-in there, so it needs no key. */
  paste?: string
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
  /** Whether the Studio can set it up on the chat's machine for them
   *  (with ?conversation=). */
  setup?: MachineTools
}

async function readReach(conversationId?: string): Promise<BridgeReach | null> {
  try {
    const r = await fetch(`/api/mcp/bridge${conversationId ? `?conversation=${encodeURIComponent(conversationId)}` : ''}`)
    return r.ok ? await r.json() as BridgeReach : null
  } catch { return null }
}

/**
 * The setup card for the Studio's tools in pw code on another machine. A
 * session the Studio starts has them already; one elsewhere gets them once
 * pw code there has the Studio. "Set up for me" has the Studio do that
 * over pw ssh as the person, with the machine's own ACTIVATE sign-in, and
 * runs as the card opens when they asked for it as they continued the
 * session. "Do it myself" shows the command, for a machine pw ssh does not
 * reach. "Check that it worked" asks the Studio whether a session of
 * theirs has reached the tools.
 */
export function StudioToolsSetup({ machineName, conversationId, onDismiss, defaultOpen = false }: {
  machineName?: string
  conversationId?: string
  onDismiss?: () => void
  defaultOpen?: boolean
}) {
  const [reach, setReach] = useState<BridgeReach | null>(null)
  const [open, setOpen] = useState(defaultOpen)
  const [checking, setChecking] = useState(false)
  const [checked, setChecked] = useState(false)
  const [copied, setCopied] = useState('')
  const [withKey, setWithKey] = useState(false)
  const [running, setRunning] = useState(false)
  const [result, setResult] = useState<SetupResult | null>(null)
  const [failed, setFailed] = useState('')
  const [removed, setRemoved] = useState('')
  const auto = useRef(false)
  const read = useCallback(async () => { setReach(await readReach(conversationId)) }, [conversationId])
  useEffect(() => { void read() }, [read])
  const where = reach?.setup?.machine || machineName || 'the other machine'

  const setUp = useCallback(async (opts: { replace?: boolean; check?: boolean } = {}) => {
    if (!conversationId) return
    setRunning(true)
    setFailed('')
    try {
      const r = await runSetup({ conversationId, ...opts })
      setResult(r)
      if (r.done && !r.worked) setOpen(true)
      await read()
    } catch (e) {
      setFailed((e as Error).message)
      setOpen(true)
    } finally {
      setRunning(false)
    }
  }, [conversationId, read])

  // Asked for as the session was continued: run once the card knows it can.
  useEffect(() => {
    if (auto.current || !reach || !conversationId) return
    auto.current = true
    if (takeAutoSetup(conversationId) && reach.setup?.available) void setUp()
  }, [reach, conversationId, setUp])

  if (!reach) return null
  const check = async () => {
    setChecking(true)
    await read()
    setChecking(false)
    setChecked(true)
  }
  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(text)
    } catch { setCopied('') }
  }
  // On a machine the Studio set up: its next Check also allows the tools
  // that only read there, and Remove takes both back.
  const remove = conversationId ? <RemoveTools where={where} body={{ conversationId }} onRemoved={setRemoved} /> : null
  const manage = conversationId && reach.setup?.state === 'set' && reach.setup.available
    ? <>
        <AllowsNote allows={reach.setup.allows} lead="Check it again" />
        <p><button type="button" className="code-approval-more" disabled={running} onClick={() => void setUp({ check: true })}>{running ? 'Checking…' : 'Check it again'}</button>{' '}{remove}</p>
      </>
    : null
  if (removed) {
    return (
      <div data-testid="studio-tools-removed"><Callout tone="success" size="sm" className="studio-tools-setup" onDismiss={onDismiss}>
        {removed}
      </Callout></div>
    )
  }
  if (result?.done && result.worked) {
    return (
      <div data-testid="studio-tools-done"><Callout tone="success" size="sm" className="studio-tools-setup" onDismiss={onDismiss}>
        <p>{setupSaid(result, where)}</p>
        {remove && <p>{remove}</p>}
      </Callout></div>
    )
  }
  // A session of theirs somewhere: not this machine's, where that is known.
  if (reach.seen && !result && !failed && (checked || !reach.setup || reach.setup.state === 'set' || reach.setup.state === 'unavailable')) {
    return (
      <Callout tone="success" size="sm" className="studio-tools-setup" onDismiss={onDismiss}>
        <p>pw code reaches this Studio's tools as you: a session of yours last did at {new Date(reach.seen.at).toLocaleTimeString()}.</p>
        {manage}
      </Callout>
    )
  }
  if (reach.setup?.state === 'set' && !result && !failed) {
    return (
      <Callout tone="success" size="sm" className="studio-tools-setup" onDismiss={onDismiss}>
        <p>{machineHasSaid(where)}</p>
        {manage}
      </Callout>
    )
  }
  const startThere = <>
    Start a pw code session there: every session started there from then on has the Studio's tools
    ({reach.tools} of them), as you, with your own libraries. One already open {KEEPS_TOOLS}.
    {!reach.platformTools && <> The platform's tools come once you sign in with ACTIVATE in this Studio (Settings, Model access).</>}
  </>
  const keySteps = (first: string, second: string) => <>
    <p>
      {first}. Make an ACTIVATE API key with the platform scope
      {reach.keyPage ? <> under <a href={reach.keyPage} target="_blank" rel="noreferrer">API keys in ACTIVATE</a></> : <> (Account, Authentication, API Keys)</>}.
    </p>
    <p>{second}. In a terminal on {where}, run this once, with your key in place of {reach.keyHole}:</p>
    <pre className="studio-tools-command">{reach.command}</pre>
    <p>
      <Button variant="secondary" size="sm" onClick={() => void copy(reach.command ?? '')}>{copied === reach.command ? 'Copied' : reach.paste ? 'Copy the key command' : 'Copy the command'}</Button>
    </p>
  </>
  const canRun = !!conversationId && !!reach.setup?.available
  return (
    <Callout tone={failed || result ? 'warning' : 'info'} size="sm" className="studio-tools-setup" onDismiss={onDismiss}>
      <p>
        {running
          ? <>Adding this Studio's tools to pw code on {where}, as you…</>
          : <>pw code on {where} does not have this Studio's tools (library search, queries, reading files and the rest) yet.</>}
      </p>
      {canRun && !running && !result?.ask && <AllowsNote allows={reach.setup?.allows} />}
      {failed && <p data-testid="studio-tools-failed">{failed}</p>}
      {result?.ask && (
        <p>
          {result.why}{' '}
          <Button variant="secondary" size="sm" disabled={running} onClick={() => void setUp({ replace: true })}>Replace it</Button>
        </p>
      )}
      {result?.done && !result.worked && <p>{setupSaid(result, where)}</p>}
      {!running && !result?.ask && (
        <p>
          {canRun && <Button variant="primary" size="sm" onClick={() => void setUp({ check: !!result || !!failed })}>{failed || result ? 'Try again' : 'Set up for me'}</Button>}{' '}
          <button type="button" className="code-approval-more" aria-expanded={open} onClick={() => setOpen(o => !o)}>
            {open ? 'Hide the steps' : canRun ? 'Do it myself' : 'Set them up once'}
          </button>
        </p>
      )}
      {!canRun && reach.setup?.why && reach.available && <p className="muted">{reach.setup.why}</p>}
      {open && (!reach.available
        ? <p>{reach.why}</p>
        : <>
            {reach.paste
              ? <>
                  <p>1. In a terminal on {where}, paste this once. It adds the same entry the Studio would, and allows no tool without asking; the entry uses pw's own ACTIVATE sign-in there, so it needs no key (it needs curl):</p>
                  <pre className="studio-tools-command" data-testid="studio-tools-paste">{reach.paste}</pre>
                  <p>
                    <Button variant="secondary" size="sm" onClick={() => void copy(reach.paste ?? '')}>{copied === reach.paste ? 'Copied' : 'Copy the command'}</Button>{' '}
                    <button type="button" className="code-approval-more" aria-expanded={withKey} onClick={() => setWithKey(k => !k)}>
                      {withKey ? 'Hide the key steps' : 'No curl there? Use an ACTIVATE API key'}
                    </button>
                  </p>
                  {withKey && keySteps('a', 'b')}
                  <p>2. {startThere}</p>
                </>
              : <>
                  {keySteps('1', '2')}
                  <p>3. {startThere}</p>
                </>}
            <p>
              <Button variant="secondary" size="sm" loading={checking} disabled={checking} onClick={() => void check()}>Check that it worked</Button>
              {checked && !checking && <span className="muted studio-tools-check"> Not yet: no pw code session of yours has reached the tools since the Studio started.</span>}
            </p>
          </>)}
    </Callout>
  )
}
