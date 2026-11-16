import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Button, Callout, Checkbox, Loader, Modal, SegmentedControl, toast } from '@parallelworks/ui'
import { sectionAllowed, useAppConfig } from '../config'
import { TransferPane, type PaneState } from './TransferPane'
import { TransferPlanDialog } from './TransferPlanDialog'
import { ACTIVITY_HASH, transferHash } from '../views/TransfersView'
import {
  destinationWhy, libraryLocation, plural, sourceWhy, transferApi, transferTitle, transfersPresent,
  type Plan, type PlanOptions, type PlanRequest, type Transfer, type TransferLocation,
} from '../transferApi'

/** One selected Search hit or Query row: its library and its path there,
 *  as the page printed it. */
export interface ResultItem { library: string; path: string }

/** A selection's key: library and path, so the same path in two libraries
 *  is two items. */
export const itemKey = (library: string, path: string) => `${library}\u0000${path}`
export const keyItem = (key: string): ResultItem => {
  const at = key.indexOf('\u0000')
  return at < 0 ? { library: '', path: key } : { library: key.slice(0, at), path: key.slice(at + 1) }
}
/** The paths of a selection, once each, for labels, which act on paths. */
export const keyPaths = (keys: Iterable<string>) => [...new Set([...keys].map(k => keyItem(k).path))]

/** Whether this viewer has the Transfers page: the server has transfers
 *  and the pages setting shows it, as the sidebar decides. */
export function useTransfersShown(): boolean {
  const cfg = useAppConfig()
  const [present, setPresent] = useState<boolean | null>(null)
  useEffect(() => {
    let live = true
    void transfersPresent().then(p => { if (live) setPresent(p) })
    return () => { live = false }
  }, [])
  return present === true && sectionAllowed('transfers', cfg)
}

/** A selection split into one group per library, in the order first seen. */
export interface ItemGroup { library: string; label: string; paths: string[] }
export function groupByLibrary(items: ResultItem[], labelOf: (id: string) => string): ItemGroup[] {
  const by = new Map<string, ItemGroup>()
  for (const it of items) {
    let g = by.get(it.library)
    if (!g) { g = { library: it.library, label: labelOf(it.library), paths: [] }; by.set(it.library, g) }
    if (!g.paths.includes(it.path)) g.paths.push(it.path)
  }
  return [...by.values()]
}

/** "12 files in Projects, 3 in Archive". */
export function groupsText(groups: { label: string; paths: string[] }[], noun: string): string {
  return groups.map((g, i) => `${i === 0 ? plural(g.paths.length, noun) : g.paths.length.toLocaleString()} in ${g.label}`).join(', ')
}

/** Where a transfer of these items goes first: a place that takes files
 *  other than the items' own libraries, so the plan is no copy onto itself
 *  or into its own root. A place of another kind than a library first, as
 *  the Transfers page picks its second side; null when there is none, and
 *  the person chooses. */
export function defaultDestination(locations: TransferLocation[], items: ResultItem[]): TransferLocation | null {
  const own = new Set(items.map(it => libraryLocation(it.library)))
  const open = locations.filter(l => !own.has(l.id) && !destinationWhy(l))
  return open.find(l => l.kind !== 'library') ?? open[0] ?? null
}

/** The toast for what a dialog started: one transfer opens in Transfers;
 *  several are one toast, updated in place as each starts, that opens the
 *  list. */
export function startedToast(all: Transfer[], id: string): void {
  const t = all[all.length - 1]
  if (!t) return
  if (all.length === 1) {
    // Named by what it does and where, not by the server's label, which
    // names the destination already.
    toast.success(`${transferTitle({ ...t, label: '' })} from ${t.source.label} to ${t.destination.label}`, {
      id, title: 'Transfer started',
      action: { label: 'Open in Transfers', href: transferHash(t.id) },
    })
    return
  }
  const to = [...new Set(all.map(x => x.destination.label))]
  toast.success(`From ${[...new Set(all.map(x => x.source.label))].join(', ')} to ${to.join(', ')}`, {
    id, title: `${plural(all.length, 'transfer')} started`,
    action: { label: 'Open Transfers', href: ACTIVITY_HASH },
  })
}

let toastSeq = 0

type Action = 'copy' | 'move'
type Step = { plan: Plan; request: PlanRequest; label: string }

/**
 * Transfer what is selected in Search or Query: Copy or Move, and a
 * destination chosen with the Transfers page's own location picker and
 * folder browser. A transfer has one source location, so a selection over
 * several libraries makes one transfer per library: each is planned, then
 * the plans are confirmed one at a time in the Transfers page's plan
 * dialog. A library that cannot be a source is left out, with its reason,
 * before Continue. The destination starts at a place other than the
 * items' libraries (defaultDestination). What starts is one toast: one
 * transfer opens in Transfers, several open the list. The page stays where
 * it is.
 */
export function ResultsTransferDialog({
  items, noun = 'file', labels = {}, onClose, onDone,
}: {
  items: ResultItem[]
  /** What an item is, for the header: "file" or "folder". */
  noun?: string
  /** Library labels the page knows, for a library Transfers does not list. */
  labels?: Record<string, string>
  onClose: () => void
  /** After the last plan, or a Cancel part way, with what started. */
  onDone: (started: Transfer[]) => void
}) {
  const [locations, setLocations] = useState<TransferLocation[] | null>(null)
  const [locError, setLocError] = useState('')
  const [action, setAction] = useState<Action>('copy')
  const [replace, setReplace] = useState(false)
  const [defaults, setDefaults] = useState<PlanOptions>({})
  const [dest, setDest] = useState<PaneState>({ locationId: null, path: '', selected: [] })
  const [planning, setPlanning] = useState(false)
  const [refused, setRefused] = useState<{ label: string; text: string }[]>([])
  const [ready, setReady] = useState<Step[]>([])
  const [steps, setSteps] = useState<Step[] | null>(null)
  const [at, setAt] = useState(0)
  const [started, setStarted] = useState<Transfer[]>([])
  // One toast for this dialog's starts, updated as each starts.
  const toastId = useRef(`results-transfer-${++toastSeq}`)
  const refusedRef = useRef<HTMLDivElement>(null)

  useEffect(() => {
    transferApi.locations().then(locs => {
      setLocations(locs)
      setDest(d => {
        if (d.locationId) return d
        return { locationId: defaultDestination(locs, items)?.id ?? null, path: '', selected: [] }
      })
    }, e => { setLocError((e as Error).message); setLocations([]) })
    // A transfer starts from the defaults in Settings, Transfers, as on the Transfers page.
    transferApi.settings().then(st => setDefaults({ mover: st.defaults.mover, checksum: st.defaults.checksum, preserveTimes: st.defaults.preserveTimes }), () => {})
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  // Leaving the page, as by a link in the pane or a toast, closes the
  // dialog and any plans not yet confirmed.
  useEffect(() => {
    const away = () => { if (started.length) onDone(started); else onClose() }
    window.addEventListener('hashchange', away)
    return () => window.removeEventListener('hashchange', away)
  }, [onClose, onDone, started])

  const locOf = useCallback((id: string) => locations?.find(l => l.id === libraryLocation(id)) ?? null, [locations])
  const groups = useMemo(
    () => groupByLibrary(items, id => locOf(id)?.label ?? labels[id] ?? id),
    [items, locOf, labels],
  )
  // Each group as a source: why it cannot be one for this action, if so.
  const checked = groups.map(g => {
    const loc = locOf(g.library)
    const why = locations === null ? null
      : loc ? sourceWhy(action, loc) : `${g.label} is not among the places Studio transfers from.`
    return { ...g, why, readOnly: !!loc && loc.available && !loc.writable }
  })
  const usable = checked.filter(g => !g.why)
  const destLoc = locations?.find(l => l.id === dest.locationId) ?? null
  const destWhy = locations === null ? null : destinationWhy(destLoc)

  // Any change makes the plans in hand stale.
  const changed = () => { setRefused([]); setReady([]) }

  const requestFor = (g: ItemGroup): PlanRequest => ({
    action,
    source: { location: libraryLocation(g.library), paths: g.paths },
    destination: { location: destLoc!.id, path: dest.path },
    options: { ...defaults, replace },
  })

  const plan = async () => {
    setPlanning(true); setRefused([]); setReady([])
    const out = await Promise.all(usable.map(async g => {
      const request = requestFor(g)
      try { return { ok: true as const, step: { plan: await transferApi.plan(request), request, label: g.label } } }
      catch (e) { return { ok: false as const, label: g.label, text: (e as Error).message } }
    }))
    setPlanning(false)
    const good = out.flatMap(o => (o.ok ? [o.step] : []))
    const bad = out.flatMap(o => (o.ok ? [] : [{ label: o.label, text: o.text }]))
    if (!bad.length) { setSteps(good); setAt(0); return }
    // The server's reasons, shown; the rest can go on without them.
    setRefused(bad); setReady(good)
  }

  // A refusal shows where it is seen: scrolled into view and focused, as
  // the callouts sit under the destination pane, below the fold of a
  // dialog in a short window.
  useEffect(() => {
    const el = refusedRef.current
    if (!refused.length || !el) return
    el.scrollIntoView?.({ block: 'nearest' })
    el.focus({ preventScroll: true })
  }, [refused])

  const finish = (all: Transfer[]) => { onDone(all) }
  const onStarted = (t: Transfer) => {
    const all = [...started, t]
    startedToast(all, toastId.current)
    setStarted(all)
    if (steps && at + 1 < steps.length) setAt(at + 1)
    else finish(all)
  }

  if (steps && steps[at]) {
    const s = steps[at]
    return (
      <TransferPlanDialog key={at} plan={s.plan} request={s.request} step={{ index: at, count: steps.length }}
        onClose={() => finish(started)} onStarted={onStarted}
        onReplanned={p => setSteps(st => st && st.map((x, i) => (i === at ? { ...x, plan: p } : x)))} />
    )
  }

  const total = groups.reduce((n, g) => n + g.paths.length, 0)
  const blocked = !destLoc || !!destWhy || !usable.length || locations === null
  const why = locations === null ? '' : !usable.length ? 'Nothing selected can be sent.' : destWhy ?? ''
  return (
    <Modal open onClose={onClose} size="lg" className="xfer-results-dialog"
      title={`Transfer ${plural(total, 'item')}`}
      description={groupsText(groups, noun)}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          {ready.length > 0 && refused.length > 0
            ? <Button variant="primary" onClick={() => { setSteps(ready); setAt(0) }}>Continue without {refused.length === 1 ? refused[0].label : 'them'}</Button>
            : <Button variant="primary" disabled={blocked || planning} loading={planning} focusableWhenDisabled title={why || undefined}
                onClick={() => void plan()}>Continue</Button>}
        </>
      }
    >
      <div className="xfer-results">
        <div className="xfer-results-row">
          <SegmentedControl<Action> label="Action" size="sm" value={action} onValueChange={v => { setAction(v); changed() }}
            options={[{ value: 'copy', label: 'Copy' }, { value: 'move', label: 'Move' }]} />
          <span className="muted xfer-action-note">
            {action === 'copy' ? 'Copies the selected items into the folder chosen below.' : 'Copies, checks, then removes the originals.'}
          </span>
        </div>
        {checked.filter(g => g.why).map(g => (
          <Callout key={g.library} size="sm" tone={g.readOnly ? 'muted' : 'warning'}
            title={`${plural(g.paths.length, noun)} in ${g.label} left out`}
            action={g.readOnly && action === 'move'
              ? <Button size="sm" variant="secondary" onClick={() => { setAction('copy'); changed() }}>Copy instead</Button>
              : undefined}>
            {g.why}
          </Callout>
        ))}
        {locError && <Callout tone="error" size="sm" title="Cannot list locations">{locError}</Callout>}
        {locations === null
          ? <div className="muted xfer-pad"><Loader inline size={16} label="Reading locations…" /></div>
          : (
            <TransferPane name="Destination" locations={locations} state={dest} selectable={false}
              onChange={next => { setDest(next); changed() }} onLoaded={noop} refreshKey={0} />
          )}
        <label className="xfer-check"><Checkbox checked={replace} onCheckedChange={v => { setReplace(!!v); changed() }} />
          Replace files that already exist at the destination</label>
        {destLoc && !destWhy && <p className="muted xfer-results-to">To {destLoc.label}: {dest.path || '/'}</p>}
        {refused.length > 0 && (
          <div ref={refusedRef} tabIndex={-1} className="xfer-results-refused" data-testid="xfer-results-refused">
            {refused.map(r => (
              <Callout key={r.label} tone="error" size="sm" live title={`No plan for ${r.label}`}>{r.text}</Callout>
            ))}
          </div>
        )}
      </div>
    </Modal>
  )
}

const noop = () => {}
