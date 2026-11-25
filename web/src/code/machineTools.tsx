import { type ReactNode, useCallback, useEffect, useMemo, useState } from 'react'
import { isConnected, type PwCodeMachine } from '@parallelworks/code'
import { Button, Checkbox, CopyButton, InfoTip, type MenuEntry, Popover, toast, useConfirm } from '@parallelworks/ui'
import { ToolsIcon } from '@parallelworks/ui/icons'
import { AllowsNote, allowsSaid } from '../components/MachineToolsAllow'
import { forgetMachineTools, type MachineTools, removedSaid, removeTools, runSetup, type SetupResult, setupSaid } from '../components/studioTools'
import { VIEWER_CACHES_CLEARED } from '../viewerCache'

/**
 * Whether pw code on each of the person's machines has the Studio's tools,
 * as a quiet mark on the machine: in the machine menu's rows and beside the
 * chosen machine, where pressing it says how it stands. The machine's menu
 * checks, adds and removes them. A session started on a machine that can
 * have them and was never asked about carries a ticked box (ToolsChoice);
 * from then on the Studio adds them as a session starts there (server-go
 * chat, bridgeAtStart).
 */

type M = Pick<PwCodeMachine, 'id' | 'name' | 'status'>

export interface ToolsView {
  on: boolean
  /** The Studio's own machine, whose sessions always have them. */
  own: boolean
  tools?: MachineTools
  why?: string
}

const REREAD_MS = 5_000

interface Read { why: string; by: Map<string, MachineTools> }

/** GET /api/mcp/bridge/machines: read once, after a change, and as the person
 *  comes back to the tab (they may have run the command meanwhile). */
export function useMachineTools(isOwn: (machine: M) => boolean | undefined) {
  const [read, setRead] = useState<Read | null>(null)
  const { confirm, dialog } = useConfirm()
  const load = useCallback(async () => {
    try {
      const r = await fetch('/api/mcp/bridge/machines')
      const d = r.ok ? await r.json() as { why?: string; machines?: MachineTools[] } : null
      if (d) setRead({ why: d.why ?? '', by: new Map((d.machines ?? []).map(m => [m.machineId, m])) })
    } catch { /* no mark */ }
  }, [])
  useEffect(() => {
    let at = Date.now()
    void load()
    const back = () => { if (!document.hidden && Date.now() - at > REREAD_MS) { at = Date.now(); void load() } }
    const changed = () => { setRead(null); void load() }
    document.addEventListener('visibilitychange', back)
    window.addEventListener(VIEWER_CACHES_CLEARED, changed)
    return () => {
      document.removeEventListener('visibilitychange', back)
      window.removeEventListener(VIEWER_CACHES_CLEARED, changed)
    }
  }, [load])

  const view = useCallback((machine: M | undefined): ToolsView | undefined => {
    if (!machine) return undefined
    const own = isOwn(machine)
    if (own === undefined) return undefined
    if (own) return { on: true, own: true }
    const tools = read?.by.get(machine.id)
    if (tools) return { on: tools.state === 'set' || (tools.state === 'offer' && !!tools.attach), own: false, tools, why: tools.why }
    return read?.why ? { on: false, own: false, why: read.why } : undefined
  }, [read, isOwn])

  /** Adds or checks them there, and asks before it replaces another's entry. */
  const setUp = useCallback(async (tools: MachineTools, opts: { replace?: boolean; check?: boolean } = {}): Promise<void> => {
    try {
      const r: SetupResult = await runSetup({ machineId: tools.machineId, ...opts })
      if (r.ask) {
        void confirm({
          title: `Replace the Studio's tools on ${tools.machine}?`,
          description: r.why,
          confirmLabel: 'Replace',
          onConfirm: () => setUp(tools, { replace: true }),
        })
      } else if (r.worked) toast.success(setupSaid(r, tools.machine, false), { id: 'machine-tools' })
      else toast.error(setupSaid(r, tools.machine, false), { id: 'machine-tools' })
    } catch (e) {
      toast.error((e as Error).message, { id: 'machine-tools' })
    }
    await load()
  }, [confirm, load])

  const add = useCallback((tools: MachineTools) => {
    void confirm({
      title: `Add the Studio's tools to ${tools.machine}?`,
      description: 'The Studio sets up pw code there over pw ssh as you. Sessions you start there after that have them.',
      children: <AllowsNote allows={tools.allows} />,
      confirmLabel: 'Add',
      onConfirm: () => setUp(tools),
    })
  }, [confirm, setUp])

  const remove = useCallback((tools: MachineTools) => {
    void confirm({
      title: `Remove the Studio's tools from ${tools.machine}?`,
      description: 'This takes them out of pw code there, with the rules that let the ones that only read run without asking.',
      confirmLabel: 'Remove',
      onConfirm: async () => {
        try {
          toast.success(removedSaid(await removeTools({ machineId: tools.machineId }), tools.machine), { id: 'machine-tools' })
        } catch (e) {
          toast.error((e as Error).message, { id: 'machine-tools' })
        }
        await load()
      },
    })
  }, [confirm, load])

  /** What the machine's menu offers for its tools. */
  const entries = useCallback((machine: M | undefined): MenuEntry[] => {
    const v = view(machine)
    const tools = v?.tools
    if (!machine || !tools) return []
    const items: MenuEntry[] = []
    const off = isConnected(machine as PwCodeMachine) ? undefined : `${machine.name} is not connected.`
    if (tools.command) {
      if (tools.state === 'set') {
        items.push(
          { label: 'Copy the command to remove', description: `Run it on ${tools.machine}`, copy: { text: tools.uncommand ?? '', label: 'command' } },
          { label: 'I removed them', onClick: () => { void forgetMachineTools(tools.machineId).then(load) } },
        )
      } else items.push({ label: 'Copy the command to add', description: `Run it once on ${tools.machine}`, copy: { text: tools.command, label: 'command' } })
    } else if (tools.state === 'set') {
      const why = off ?? (tools.available ? undefined : 'The Studio does not reach it now.')
      items.push(
        { label: 'Check', onClick: () => { toast.info(`Checking pw code on ${tools.machine}…`, { id: 'machine-tools' }); void setUp(tools, { check: true }) }, ...(why ? { disabled: true, disabledReason: why } : {}) },
        { label: 'Remove', onClick: () => remove(tools), ...(why ? { disabled: true, disabledReason: why } : {}) },
      )
    } else if (tools.available) items.push({ label: 'Add', onClick: () => add(tools) })
    return items.length ? [{ group: `The Studio's tools on ${tools.machine}`, items }] : []
  }, [view, load, setUp, remove, add])

  return useMemo(() => ({ view, entries, add, reload: load, dialog }), [view, entries, add, load, dialog])
}

export type MachineToolsControl = ReturnType<typeof useMachineTools>

/** The mark: the tools, or the tools struck through. */
export function ToolsMark({ on }: { on: boolean }) {
  return <span className="code-tools-mark" data-on={on} data-testid="tools-mark" aria-hidden><ToolsIcon /></span>
}

const has = (name: string) => `pw code on ${name} has this Studio's tools`
const hasNot = (name: string) => `pw code on ${name} does not have this Studio's tools`

/** The mark beside the chosen machine, which says how it stands. */
export function ToolsMarkButton({ machine, control, mode }: { machine: M; control: MachineToolsControl; mode?: string }) {
  const v = control.view(machine)
  if (!v) return null
  const label = v.on ? has(machine.name) : hasNot(machine.name)
  const tools = v.tools
  let body: ReactNode
  if (v.on) {
    body = <>
      <p>{v.own ? `Sessions here on ${machine.name} start with this Studio's tools.` : `${label}.`}</p>
      {!v.own && (mode === 'read-only' || mode === 'plan') && <p className="muted" data-testid="machine-tools-mode">In {mode === 'plan' ? 'plan' : 'read-only'} mode it does not run them for a chat here.</p>}
    </>
  } else if (tools?.state === 'command' && tools.command) {
    body = <>
      <p>Run this once on {machine.name} to give its pw code this Studio's tools:</p>
      <div className="code-tools-command"><code>{tools.command}</code><CopyButton text={tools.command} label="command" /></div>
    </>
  } else {
    body = <>
      <p>{tools?.held ? `${label}: ${tools.held.replace(/\.$/, '')}.` : v.why || `${label}.`}</p>
      {tools && tools.available && <p><Button variant="secondary" size="sm" onClick={() => control.add(tools)}>Add</Button></p>}
    </>
  }
  return (
    <Popover align="start" aria-label={label} className="code-tools-popover"
      trigger={<button type="button" className="code-chip code-tools-button" aria-label={label} title={label} data-testid="machine-tools-button"><ToolsMark on={v.on} /></button>}>
      <div data-testid="machine-tools-popover">{body}</div>
    </Popover>
  )
}

/** Whether a start on the machine asks about its tools: they can be added
 *  there and the person has not said. */
export function asksTools(v: ToolsView | undefined, machine: M | undefined): boolean {
  const t = v?.tools
  return !!t && t.state === 'offer' && !v.on && !!t.available && !t.chosen && isConnected(machine as PwCodeMachine | undefined)
}

/** Whether a start there adds them without asking. */
export function addsTools(v: ToolsView | undefined): boolean {
  const t = v?.tools
  return !!t && t.state === 'offer' && !v.on && !!t.available && !!t.chosen && !t.held
}

/** The box a first start on a machine carries, ticked. */
export function ToolsChoice({ tools, checked, onChange, disabled }: { tools: MachineTools; checked: boolean; onChange: (on: boolean) => void; disabled?: boolean }) {
  return (
    <div className="code-tools-choice" data-testid="machine-tools-choice">
      <label>
        <Checkbox checked={checked} disabled={disabled} onCheckedChange={on => onChange(!!on)} />
        <span>Add the Studio's tools to {tools.machine}</span>
      </label>
      <span className="muted">
        Sessions there can then search your libraries.
        {tools.allows?.length ? <> <InfoTip text={allowsSaid(tools.allows)} /></> : null}
      </span>
    </div>
  )
}
