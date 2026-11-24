import { useCallback, useEffect, useState } from 'react'
import { Button, Callout } from '@parallelworks/ui'
import { isConnected, type PwCodeMachine } from '@parallelworks/code'
import { type MachineTools, type SetupResult, runSetup, setMachineTools, setupSaid } from '../components/studioTools'

const OFFERED = 'ade-studio-tools-offered'

/** The machines this browser has offered the tools for once already. */
function offered(): string[] {
  try { return JSON.parse(localStorage.getItem(OFFERED) ?? '[]') as string[] } catch { return [] }
}

function markOffered(id: string): void {
  try {
    const all = offered().filter(x => x !== id)
    all.push(id)
    localStorage.setItem(OFFERED, JSON.stringify(all.slice(-200)))
  } catch { /* offered again after a reload */ }
}

/** What Add says of a machine that is not online. */
export const notConnected = (machine: string) => `${machine} is not connected. Start it, then add the tools.`

/**
 * The Code page's offer of the Studio's tools, once for each of the
 * person's machines that lacks them and that the Studio reaches over pw ssh
 * as them: Add sets it up there now; Not on <machine> keeps the Studio from
 * offering it there again until they choose it (in a chat's setup card, or
 * by ticking it as they continue a session there). Only a machine online
 * as the page's own machine list shows it (on, with its pw code connected)
 * is offered, and, where the page has a machine chosen, only that one.
 *
 * Where a session is about to start on one of them (always: a new chat, or
 * the Code page's start), it says each time that the session there lacks
 * the Studio's tools, with Add, or why it cannot.
 */
export function MachineToolsOffer({ machines: listed, selected, always = false, mode }: {
  /** The Code page's machines, as its listing gives them. */
  machines: readonly (Pick<PwCodeMachine, 'id' | 'status'> & { name?: string })[]
  /** The machine chosen on the page, if one is. */
  selected?: string
  always?: boolean
  /** The permission mode the session would start in. */
  mode?: string
}) {
  const [machines, setMachines] = useState<MachineTools[] | null>(null)
  const [none, setWhy] = useState('')
  const [busy, setBusy] = useState(false)
  const [said, setSaid] = useState<{ tone: 'success' | 'warning'; text: string } | null>(null)
  const [skipped, setSkipped] = useState<string[]>([])
  // Offered once per machine: one shown in an earlier visit is not shown again.
  const [before] = useState(offered)
  const load = useCallback(async () => {
    try {
      const r = await fetch('/api/mcp/bridge/machines')
      const d = r.ok ? await r.json() as { why?: string; machines?: MachineTools[] } : null
      setWhy(d?.why ?? '')
      setMachines(d?.machines ?? [])
    } catch { setMachines([]) }
  }, [])
  useEffect(() => { void load() }, [load])

  if (said) {
    return (
      <div data-testid="machine-tools-said"><Callout tone={said.tone} size="sm" className="studio-tools-setup" onDismiss={() => setSaid(null)}>
        {said.text}
      </Callout></div>
    )
  }
  const online = (id: string) => isConnected(listed.find(m => m.id === id))
  if (always) {
    const name = listed.find(m => m.id === selected)?.name ?? 'that machine'
    const at = machines?.find(m => m.machineId === selected)
    const why = at?.why || none
    if (!machines || !selected) return null
    if (at?.state === 'set' || (at?.attach && at.state === 'offer')) {
      // pw code asks before each tool in a turn sent from elsewhere, and
      // in read-only and plan mode runs none that would ask.
      return mode === 'read-only' || mode === 'plan'
        ? <p className="muted pwcode-start-note" data-testid="machine-tools-mode">In {mode === 'plan' ? 'plan' : 'read-only'} mode pw code on {name} does not run this Studio's tools for a chat here. In accept edits it asks you before each one.</p>
        : null
    }
    if (at && at.state !== 'unavailable' && online(selected)) {
      return <Offer next={at} busy={busy} lacks onAdd={() => void add(at)} onDecline={at.state === 'offer' ? () => { decline(at); void load() } : undefined} />
    }
    return <p className="muted pwcode-start-note" data-testid="machine-tools-none">{lacksTools(name)}{why ? ` They cannot be added there now: ${why}` : ''}</p>
  }
  const next = machines?.find(m => m.state === 'offer' && online(m.machineId) && (!selected || m.machineId === selected)
    && !skipped.includes(m.machineId) && !before.includes(m.machineId))
  return next ? <Offer next={next} busy={busy} onAdd={() => void add(next)} onDecline={() => decline(next)} /> : null

  async function add(next: MachineTools) {
    // Gone offline since the offer showed: said, and pw ssh is not tried.
    if (!online(next.machineId)) {
      setSaid({ tone: 'warning', text: notConnected(next.machine) })
      setSkipped(s => [...s, next.machineId])
      return
    }
    setBusy(true)
    try {
      const r: SetupResult = await runSetup({ machineId: next.machineId })
      if (r.ask) {
        setSaid({ tone: 'warning', text: `${r.why} Use Set up for me in a chat on ${next.machine} to replace it.` })
      } else {
        setSaid({ tone: r.worked ? 'success' : 'warning', text: setupSaid(r, next.machine, false) })
      }
      await load()
    } catch (e) {
      setSaid({ tone: 'warning', text: `${(e as Error).message}` })
      setSkipped(s => [...s, next.machineId])
    } finally {
      setBusy(false)
    }
  }
  function decline(next: MachineTools) {
    setSkipped(s => [...s, next.machineId])
    void setMachineTools(next.machineId, false)
  }
}

/** What a session on machine goes without. */
const lacksTools = (machine: string) => `pw code on ${machine} does not have this Studio's tools, so a session there works with its own.`

function Offer({ next, busy, lacks = false, onAdd, onDecline }: { next: MachineTools; busy: boolean; lacks?: boolean; onAdd: () => void; onDecline?: () => void }) {
  useEffect(() => { markOffered(next.machineId) }, [next.machineId])
  return (
    <div data-testid="machine-tools-offer"><Callout tone="info" size="sm" className="studio-tools-setup">
      <p>
        {busy
          ? <>Adding this Studio's tools to pw code on {next.machine}, as you…</>
          : lacks ? <>{lacksTools(next.machine)} Add them (library search, queries, reading files and the rest)? The Studio sets it up there over pw ssh as you, with that machine's own ACTIVATE sign-in, and a session started after that has them.</>
          : <>Add this Studio's tools (library search, queries, reading files and the rest) to pw code on {next.machine}? The Studio sets it up there over pw ssh as you, with that machine's own ACTIVATE sign-in, so sessions you start there have them.</>}
      </p>
      <p>
        <Button variant="primary" size="sm" loading={busy} disabled={busy} onClick={onAdd}>Add</Button>{' '}
        {onDecline && <Button variant="ghost" size="sm" disabled={busy} onClick={onDecline}>Not on {next.machine}</Button>}
      </p>
    </Callout></div>
  )
}
