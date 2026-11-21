import { useCallback, useEffect, useRef, useState } from 'react'
import {
  Button, Callout, Checkbox, Disclosure, Input, Loader, SegmentedControl, Select, toast, type SelectItem,
} from '@parallelworks/ui'
import { TransferPane, type PaneHandle, type PaneState } from '../components/TransferPane'
import { LocationDialog, type LocationPrefill } from '../components/SavedLocationDialog'
import { TransferPlanDialog } from '../components/TransferPlanDialog'
import { TransferActivity } from '../components/TransferActivity'
import { KeepCopyDialog } from '../components/KeepCopyDialog'
import { api } from '../api'
import {
  ACTION_TEXT, MOVER_TEXT, TRANSFER_SETTINGS_CHANGED, destinationWhy, sourcePaths, testText, transferApi,
  type MoverName, type Plan, type PlanOptions, type PlanRequest, type SavedLocation, type SettingTest, type Transfer, type TransferAction, type TransferLocation,
} from '../transferApi'

/** What the save dialog opens with for a library's files: what the server
 *  suggests, and a line saying where it came from and what is missing. */
export function prefillFor(loc: TransferLocation): { prefill: LocationPrefill; note: string } {
  const s = loc.suggest ?? { label: `${loc.label} files` }
  const prefill: LocationPrefill = { label: s.label, host: s.host, root: s.root, port: s.port, sshOptions: s.sshOptions }
  const missing = [s.host ? '' : 'host', s.root ? '' : 'folder'].filter(Boolean)
  let note = s.host || s.root
    ? `Filled in with what Studio knows of where the files of ${loc.label} are.`
    : `Studio does not know where the files of ${loc.label} are.`
  if (missing.length) note += ` Enter the ${missing.join(' and the ')}.`
  return { prefill, note }
}

/** The page's two views, kept in the hash so Back returns from one to the other. */
export const ACTIVITY_HASH = '#view=transfers'
export const NEW_HASH = '#view=transfers:new'
/** The list with one transfer's details open. */
export const transferHash = (id: string) => `${ACTIVITY_HASH}?transfer=${encodeURIComponent(id)}`
const isNewHash = () => location.hash.startsWith(NEW_HASH)

function useNarrow(query = '(max-width: 900px)'): boolean {
  const [narrow, setNarrow] = useState(() => typeof matchMedia === 'function' && matchMedia(query).matches)
  useEffect(() => {
    if (typeof matchMedia !== 'function') return
    const mq = matchMedia(query)
    const on = () => setNarrow(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [query])
  return narrow
}

type Side = 'a' | 'b'
const other = (s: Side): Side => (s === 'a' ? 'b' : 'a')

const DEFAULT_MOVERS: MoverName[] = ['rsync', 'rclone', 'studio-copy', 'conduit']

/**
 * Transfers: the list of every transfer, yours and the assistant's, and
 * from "New transfer" two locations side by side, each browsed to a folder,
 * with Start sending what one has selected into the other's open folder.
 * Every start goes through a plan the person confirms.
 */
export function TransfersView() {
  const [mode, setMode] = useState<'activity' | 'new'>(() => (isNewHash() ? 'new' : 'activity'))
  // Keeping a local copy of a folder here and indexing it: offered where
  // libraries are built on this computer (sign-in off).
  const [copyOffered, setCopyOffered] = useState(false)
  const [keeping, setKeeping] = useState(false)
  useEffect(() => {
    let live = true
    api.libraries().then(r => { if (live) setCopyOffered(!!r?.canBuild) }, () => {})
    return () => { live = false }
  }, [])
  useEffect(() => {
    const on = () => setMode(isNewHash() ? 'new' : 'activity')
    window.addEventListener('hashchange', on)
    return () => window.removeEventListener('hashchange', on)
  }, [])
  const openNew = () => { location.hash = NEW_HASH }
  const backToActivity = () => { location.hash = ACTIVITY_HASH }
  // The sidebar's Transfers item always leads back to the list.
  useEffect(() => {
    window.addEventListener('ade:transfers-home', backToActivity)
    return () => window.removeEventListener('ade:transfers-home', backToActivity)
  }, [])

  const [locations, setLocations] = useState<TransferLocation[] | null>(null)
  const [locError, setLocError] = useState('')
  const [panes, setPanes] = useState<Record<Side, PaneState>>({ a: { locationId: null, path: '', selected: [] }, b: { locationId: null, path: '', selected: [] } })
  const [handles, setHandles] = useState<Record<Side, PaneHandle>>({ a: { location: null, entries: [] }, b: { location: null, entries: [] } })
  const [refresh, setRefresh] = useState<Record<Side, number>>({ a: 0, b: 0 })
  const [action, setAction] = useState<TransferAction>('copy')
  const [options, setOptions] = useState<Required<Omit<PlanOptions, 'label'>> & { label: string }>({
    mover: 'auto', replace: false, deleteExtra: false, checksum: false, preserveTimes: true, label: '',
  })
  const [movers, setMovers] = useState<{ name: string; ok: boolean; why?: string }[] | null>(null)
  const [planning, setPlanning] = useState<Side | null>(null)
  const [planError, setPlanError] = useState('')
  const [pending, setPending] = useState<{ plan: Plan; request: PlanRequest } | null>(null)
  const [highlight, setHighlight] = useState<string | null>(null)
  const [added, setAdded] = useState<Transfer | null>(null)
  // Saved locations are offered only with sign-in off (settings not
  // managed); until the settings are read, they are not offered.
  const [canSave, setCanSave] = useState(false)
  const [saving, setSaving] = useState<{ side: Side; prefill?: LocationPrefill; note?: string } | null>(null)
  const narrow = useNarrow()
  // Which movers reach from the first location to the second, asked as
  // the locations change; a plan then gives the answer for its direction.
  const aLoc = panes.a.locationId
  const bLoc = panes.b.locationId
  useEffect(() => {
    if (!aLoc || !bLoc || aLoc === bLoc) return
    let live = true
    transferApi.movers(aLoc, bLoc).then(m => { if (live && m.length) setMovers(m) }, () => {})
    return () => { live = false }
  }, [aLoc, bLoc])
  const [narrowSource, setNarrowSource] = useState<Side>('a')

  const loadLocations = useCallback(async () => {
    try {
      const locs = await transferApi.locations()
      setLocations(locs); setLocError('')
      setPanes(p => {
        const usable = locs.filter(l => l.available)
        const a = p.a.locationId && locs.some(l => l.id === p.a.locationId) ? p.a.locationId : usable[0]?.id ?? null
        const b = p.b.locationId && locs.some(l => l.id === p.b.locationId) ? p.b.locationId
          : (() => {
            // A writable place of another kind first, as a library and this computer.
            const kindA = locs.find(l => l.id === a)?.kind
            return usable.find(l => l.id !== a && l.writable && l.kind !== kindA)?.id
              ?? usable.find(l => l.id !== a && l.writable)?.id ?? usable.find(l => l.id !== a)?.id ?? null
          })()
        return {
          a: a === p.a.locationId ? p.a : { locationId: a, path: '', selected: [] },
          b: b === p.b.locationId ? p.b : { locationId: b, path: '', selected: [] },
        }
      })
    } catch (e) {
      setLocError((e as Error).message); setLocations(l => l ?? [])
    }
  }, [])
  useEffect(() => { if (mode === 'new') void loadLocations() }, [mode, loadLocations])
  // A new transfer starts from the defaults in Settings, Transfers.
  useEffect(() => {
    transferApi.settings().then(st => {
      setOptions(o => ({ ...o, mover: st.defaults.mover, checksum: st.defaults.checksum, preserveTimes: st.defaults.preserveTimes }))
      setCanSave(!st.managed)
    }, () => {})
  }, [])

  const setPane = (s: Side) => (next: PaneState) => {
    setPanes(p => ({ ...p, [s]: next }))
    setPlanError('')
  }
  const onLoaded = useCallback((s: Side) => (h: PaneHandle) => setHandles(p => ({ ...p, [s]: h })), [])
  const loadedA = useCallback((h: PaneHandle) => onLoaded('a')(h), [onLoaded])
  const loadedB = useCallback((h: PaneHandle) => onLoaded('b')(h), [onLoaded])

  // A transfer that lands lists its destination again, and a move's source too.
  const panesRef = useRef(panes)
  panesRef.current = panes
  const onLanded = useCallback((t: Transfer) => {
    const p = panesRef.current
    setRefresh(r => {
      const next = { ...r }
      for (const s of ['a', 'b'] as Side[]) {
        const loc = p[s].locationId
        if (loc === t.destination.location || (t.action === 'move' && loc === t.source.location)) next[s]++
      }
      return next
    })
  }, [])

  const locOf = (s: Side) => locations?.find(l => l.id === panes[s].locationId) ?? null
  const check = (from: Side): { request: PlanRequest } | { why: string } => {
    const src = sourcePaths(action, { location: locOf(from), path: panes[from].path, selected: panes[from].selected, entries: handles[from].entries })
    if ('why' in src) return src
    const dst = locOf(other(from))
    const why = destinationWhy(dst)
    if (why) return { why }
    const opts: PlanOptions = { mover: options.mover, checksum: options.checksum, preserveTimes: options.preserveTimes }
    if (action !== 'sync') opts.replace = options.replace
    if (action === 'sync') opts.deleteExtra = options.deleteExtra
    if (options.label.trim()) opts.label = options.label.trim()
    return {
      request: {
        action,
        source: { location: panes[from].locationId!, paths: src.paths },
        destination: { location: dst!.id, path: panes[other(from)].path },
        options: opts,
      },
    }
  }

  const start = async (from: Side) => {
    const c = check(from)
    if ('why' in c) return
    setPlanning(from); setPlanError('')
    try {
      const plan = await transferApi.plan(c.request)
      setMovers(plan.movers)
      setPending({ plan, request: c.request })
    } catch (e) {
      setPlanError((e as Error).message)
    } finally { setPlanning(null) }
  }

  const openSave = (side: Side) => (from?: TransferLocation) => {
    setSaving(from ? { side, ...prefillFor(from) } : { side })
  }
  // The dialog tested the location; it is added, the list is read again,
  // and the pane it was opened from switches to it. The toast carries the
  // test's result in Settings' words, since the dialog closes.
  const saveLocation = async (l: SavedLocation, result: SettingTest | null) => {
    const at = saving?.side ?? 'b'
    const { id, location: kept } = await transferApi.addLocation(l)
    window.dispatchEvent(new Event(TRANSFER_SETTINGS_CHANGED))
    await loadLocations()
    setPanes(p => ({ ...p, [at]: { locationId: id, path: '', selected: [] } }))
    setPlanError('')
    setSaving(null)
    const test = result ? ` ${testText(result)}${result.ok ? '' : '.'}` : ''
    toast.success(`Saved ${kept.label}.${test} It is also in Settings, Transfers.`)
  }

  const started = (t: Transfer) => {
    setPending(null)
    setHighlight(t.id); setAdded(t)
    setPanes(p => ({ a: { ...p.a, selected: [] }, b: { ...p.b, selected: [] } }))
    // Back to the list, with the new transfer's details open beside it, as
    // a click on its row opens them; it runs on the server either way.
    location.hash = transferHash(t.id)
  }

  const moverItems: SelectItem<MoverName>[] = [
    { value: 'auto', label: 'Automatic', description: 'The first mover that reaches both ends' },
    ...(movers
      ? movers.map(m => ({ value: m.name as MoverName, label: MOVER_TEXT[m.name] ?? m.name, description: m.ok ? undefined : (m.why ?? 'Cannot reach both ends'), disabled: !m.ok }))
      : DEFAULT_MOVERS.map(n => ({ value: n, label: MOVER_TEXT[n] ?? n }))),
  ]

  // The list stays mounted under the new-transfer view, so it keeps reading
  // progress and a transfer that lands still lists its destination again.
  const activity = (
    <div className="xfer-view" hidden={mode !== 'activity'}>
      {copyOffered && (
        <div className="xfer-keep-bar">
          <Button variant="ghost" size="sm" onClick={() => setKeeping(true)}>Keep a local copy and index it…</Button>
          <span className="muted">For a host that takes SFTP only, or a cloud drive: a folder there, copied here and searchable.</span>
        </div>
      )}
      <TransferActivity highlightId={highlight} added={added} onLanded={onLanded} onNew={openNew} />
      <KeepCopyDialog open={keeping} onClose={() => setKeeping(false)} />
    </div>
  )
  if (mode === 'activity') return <>{activity}</>

  const startButton = (from: Side, arrow: 'right' | 'left' | 'down') => {
    const c = check(from)
    const why = 'why' in c ? c.why : ''
    const fromLabel = locOf(from)?.label ?? 'this side'
    const toLabel = locOf(other(from))?.label ?? 'the other side'
    return (
      <div className="xfer-start" key={`start-${from}`}>
        <Button variant="primary" size="sm" disabled={!!why || planning !== null} loading={planning === from}
          focusableWhenDisabled title={why || undefined}
          aria-label={`Start: ${ACTION_TEXT[action].toLowerCase()} from ${fromLabel} to ${toLabel}`}
          onClick={() => void start(from)}>
          {arrow === 'left' && <span aria-hidden="true">←</span>}
          Start
          {arrow === 'right' && <span aria-hidden="true">→</span>}
          {arrow === 'down' && <span aria-hidden="true">↓</span>}
        </Button>
        {why && <span className="xfer-start-why">{why}</span>}
      </div>
    )
  }

  const order: Side[] = narrow && narrowSource === 'b' ? ['b', 'a'] : ['a', 'b']
  const pane = (s: Side, i: number) => (
    <TransferPane key={s} name={i === 0 ? 'First location' : 'Second location'} locations={locations ?? []} state={panes[s]}
      onChange={setPane(s)} onLoaded={s === 'a' ? loadedA : loadedB} refreshKey={refresh[s]}
      role={narrow ? (s === narrowSource ? 'source' : 'destination') : undefined}
      onSaveLocation={canSave ? openSave(s) : undefined} />
  )

  return (
    <>
    {activity}
    <div className="xfer-view xfer-new">
      <div className="xfer-toolbar">
        <Button variant="ghost" size="sm" onClick={backToActivity}>
          <span aria-hidden="true">←</span> Back to activity
        </Button>
        <SegmentedControl<TransferAction> label="Action" size="sm" value={action} onValueChange={v => { setAction(v); setPlanError('') }}
          options={[{ value: 'copy', label: 'Copy' }, { value: 'move', label: 'Move' }, { value: 'sync', label: 'Sync' }]} />
        <span className="xfer-action-note muted">
          {action === 'copy' && 'Copies what is selected into the other side\'s open folder.'}
          {action === 'move' && 'Copies, checks, then removes the originals.'}
          {action === 'sync' && 'Sends new and changed files so the other side\'s open folder matches one folder.'}
        </span>
      </div>

      <Disclosure summary="Transfer options" className="xfer-options" panelClassName="xfer-options-panel">
        <div className="xfer-options-grid">
          <label className="xfer-option-field">
            <span>Mover</span>
            <Select<MoverName> aria-label="Mover" size="sm" items={moverItems} value={options.mover}
              onValueChange={v => { if (v) setOptions(o => ({ ...o, mover: v })) }} />
          </label>
          <label className="xfer-option-field">
            <span>Label</span>
            <Input size="sm" aria-label="Label" placeholder="Shown in the activity list" value={options.label}
              onChange={e => setOptions(o => ({ ...o, label: e.target.value }))} maxLength={120} />
          </label>
          {action !== 'sync' && (
            <label className="xfer-check"><Checkbox checked={options.replace} onCheckedChange={v => setOptions(o => ({ ...o, replace: !!v }))} />
              Replace files that already exist at the destination</label>
          )}
          {action === 'sync' && (
            <label className="xfer-check"><Checkbox checked={options.deleteExtra} onCheckedChange={v => setOptions(o => ({ ...o, deleteExtra: !!v }))} />
              Delete files at the destination that are not in the source</label>
          )}
          <label className="xfer-check"><Checkbox checked={options.checksum} onCheckedChange={v => setOptions(o => ({ ...o, checksum: !!v }))} />
            Compare by checksum</label>
          <label className="xfer-check"><Checkbox checked={options.preserveTimes} onCheckedChange={v => setOptions(o => ({ ...o, preserveTimes: !!v }))} />
            Keep modification times</label>
        </div>
      </Disclosure>

      {locError && <Callout tone="error" size="sm" className="xfer-callout" title="Cannot list locations">{locError}</Callout>}
      {planError && <Callout tone="error" size="sm" className="xfer-callout" live title="No plan" onDismiss={() => setPlanError('')}>{planError}</Callout>}
      {locations === null && <div className="muted xfer-pad"><Loader inline size={16} label="Reading locations…" /></div>}

      {locations !== null && (
        narrow
          ? (
            <div className="xfer-stack">
              <SegmentedControl<Side> label="Which side is the source" size="sm" fill value={narrowSource} onValueChange={setNarrowSource}
                options={[
                  { value: 'a', label: `From ${locOf('a')?.label ?? 'first'}` },
                  { value: 'b', label: `From ${locOf('b')?.label ?? 'second'}` },
                ]} />
              {pane(order[0], order[0] === 'a' ? 0 : 1)}
              {startButton(narrowSource, 'down')}
              {pane(order[1], order[1] === 'a' ? 0 : 1)}
            </div>
          )
          : (
            <div className="xfer-panes">
              {pane('a', 0)}
              <div className="xfer-middle" role="group" aria-label="Start a transfer">
                {startButton('a', 'right')}
                {startButton('b', 'left')}
              </div>
              {pane('b', 1)}
            </div>
          )
      )}

      {saving && (
        <LocationDialog first={null} prefill={saving.prefill} note={saving.note}
          onClose={() => setSaving(null)} onSave={saveLocation} />
      )}
      {pending && (
        <TransferPlanDialog plan={pending.plan} request={pending.request}
          onClose={() => setPending(null)} onStarted={started}
          onReplanned={plan => { setMovers(plan.movers); setPending(p => (p ? { ...p, plan } : p)) }} />
      )}
    </div>
    </>
  )
}
