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
 */
export function MachineToolsOffer({ machines: listed, selected }: {
  /** The Code page's machines, as its listing gives them. */
  machines: readonly Pick<PwCodeMachine, 'id' | 'status'>[]
  /** The machine chosen on the page, if one is. */
  selected?: string
}) {
  const [machines, setMachines] = useState<MachineTools[] | null>(null)
  const [busy, setBusy] = useState(false)
  const [said, setSaid] = useState<{ tone: 'success' | 'warning'; text: string } | null>(null)
  const [skipped, setSkipped] = useState<string[]>([])
  // Offered once per machine: one shown in an earlier visit is not shown again.
  const [before] = useState(offered)
  const load = useCallback(async () => {
    try {
      const r = await fetch('/api/mcp/bridge/machines')
      const d = r.ok ? await r.json() as { machines?: MachineTools[] } : null
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

function Offer({ next, busy, onAdd, onDecline }: { next: MachineTools; busy: boolean; onAdd: () => void; onDecline: () => void }) {
  useEffect(() => { markOffered(next.machineId) }, [next.machineId])
  return (
    <div data-testid="machine-tools-offer"><Callout tone="info" size="sm" className="studio-tools-setup">
      <p>
        {busy
          ? <>Adding this Studio's tools to pw code on {next.machine}, as you…</>
          : <>Add this Studio's tools (library search, queries, reading files and the rest) to pw code on {next.machine}? The Studio sets it up there over pw ssh as you, with that machine's own ACTIVATE sign-in, so sessions you start there have them.</>}
      </p>
      <p>
        <Button variant="primary" size="sm" loading={busy} disabled={busy} onClick={onAdd}>Add</Button>{' '}
        <Button variant="ghost" size="sm" disabled={busy} onClick={onDecline}>Not on {next.machine}</Button>
      </p>
    </Callout></div>
  )
}
