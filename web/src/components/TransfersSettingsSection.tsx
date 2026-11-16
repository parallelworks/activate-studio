import { useEffect, useRef, useState } from 'react'
import {
  Button, Callout, Checkbox, ConfirmModal, Input, Loader, NumberInput, Select, StatusLabel, Switch, toast, type SelectItem,
} from '@parallelworks/ui'
import {
  HOST_LOCATIONS, TRANSFER_SETTINGS_CHANGED, TransferError, placesReady, testText, transferApi,
  type ComputerFolder, type ConduitMount, type MoverName, type RcloneDetected, type SavedLocation, type SettingTest, type TransferLocation, type TransferSettings,
} from '../transferApi'
import { ReadySummary } from './TransfersReady'
import { LocationDialog } from './SavedLocationDialog'

export { locationProblems } from './SavedLocationDialog'

/** rclone: on or off, the program, and what was found: its version and
 *  the remotes of its configuration, each a location. Studio never
 *  changes that configuration, so remotes are added with rclone config. */
function RcloneMover({ value, detected, disabled, onChange }: {
  value: { enabled: boolean; path: string }
  detected?: RcloneDetected
  disabled: boolean
  onChange: (v: { enabled: boolean; path: string }) => void
}) {
  let found = ''
  if (detected && value.enabled && !disabled) {
    if (!detected.found) found = detected.why ? `${detected.why.charAt(0).toUpperCase()}${detected.why.slice(1)}.` : 'Not found on this computer.'
    else found = `Found${detected.path ? ` at ${detected.path}` : ''}${detected.version ? `, version ${detected.version}` : ''}.`
  }
  return (
    <div className="xfer-settings-mover">
      <label className="xfer-check"><Switch size="sm" checked={value.enabled && !disabled} disabled={disabled}
        onCheckedChange={v => onChange({ ...value, enabled: v })} aria-label="rclone" /> <b>rclone</b></label>
      <span className="muted">
        Each remote of your rclone configuration is a location, and rclone moves files between it and this computer or another remote.
        Studio uses rclone and its configuration as they are and never changes them.{' '}
        {disabled ? 'With sign-in on it is not used, since everyone would act with this server\'s rclone credentials.' : found}
      </span>
      {value.enabled && !disabled && (
        <Input size="sm" label="Program" mono value={value.path}
          description="A name looked for in Homebrew's folders on macOS, then on PATH; or an absolute path."
          onChange={e => onChange({ ...value, path: e.target.value })} />
      )}
      {value.enabled && !disabled && detected?.found && (
        detected.remotes.length
          ? (
            <table className="xfer-settings-table" aria-label="rclone remotes">
              <thead><tr><th>Remote</th><th>Type</th></tr></thead>
              <tbody>{detected.remotes.map(r => <tr key={r.name}><td className="xfer-mono">{r.name}</td><td>{r.type}</td></tr>)}</tbody>
            </table>
          )
          : <span className="muted">rclone has no remotes. Add them with <code>rclone config</code>, then open this page again.</span>
      )}
    </div>
  )
}

/** Where Studio locations sit in CONDUIT's namespace: a location and a
 *  folder in it, and the same folder's path as CONDUIT names it. */
function ConduitMounts({ mounts, locations, disabled, onChange }: {
  mounts: ConduitMount[]
  locations: TransferLocation[]
  disabled: boolean
  onChange: (m: ConduitMount[]) => void
}) {
  const items: SelectItem<string>[] = locations.filter(l => l.kind !== 'conduit').map(l => ({ value: l.id, label: l.label }))
  const set = (i: number, patch: Partial<ConduitMount>) => onChange(mounts.map((m, j) => (j === i ? { ...m, ...patch } : m)))
  return (
    <div className="xfer-settings-mounts">
      <p className="muted">Which folders CONDUIT can reach, and the path it knows each by. Transfers between two of these can use it.</p>
      {mounts.length > 0 && (
        <table className="xfer-settings-table" aria-label="CONDUIT folders">
          <thead><tr><th>Location</th><th>Folder in it</th><th>CONDUIT path</th><th><span className="sr-only">Actions</span></th></tr></thead>
          <tbody>
            {mounts.map((m, i) => (
              <tr key={i}>
                <td><Select<string> aria-label={`Location ${i + 1}`} size="sm" items={items} value={m.location || undefined} placeholder="Choose" disabled={disabled}
                  onValueChange={v => set(i, { location: v ?? '' })} /></td>
                <td><Input size="sm" mono aria-label={`Folder in location ${i + 1}`} placeholder="(the whole location)" value={m.path} disabled={disabled}
                  onChange={e => set(i, { path: e.target.value })} /></td>
                <td><Input size="sm" mono aria-label={`CONDUIT path ${i + 1}`} placeholder="/site/project" value={m.conduit} disabled={disabled}
                  error={m.conduit && !m.conduit.startsWith('/') ? 'Start with /' : undefined}
                  onChange={e => set(i, { conduit: e.target.value })} /></td>
                <td className="xfer-settings-actions"><Button size="sm" variant="ghost" disabled={disabled} onClick={() => onChange(mounts.filter((_, j) => j !== i))}>Remove</Button></td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <Button size="sm" variant="secondary" className="xfer-settings-add" disabled={disabled}
        onClick={() => onChange([...mounts, { location: '', path: '', conduit: '' }])}>Add a CONDUIT folder</Button>
    </div>
  )
}

/** The fields that save with the Save button, each with how it is named
 *  when it changed elsewhere. Saved locations and computer folders save as
 *  they change, so they are never held as unsaved edits. */
type Group = { name: string; get: (s: TransferSettings) => unknown; set: (s: TransferSettings, v: unknown) => TransferSettings }
const GROUPS: Group[] = [
  { name: 'rsync', get: s => s.movers.rsync, set: (s, v) => ({ ...s, movers: { ...s.movers, rsync: v as TransferSettings['movers']['rsync'] } }) },
  { name: 'rclone', get: s => s.movers.rclone, set: (s, v) => ({ ...s, movers: { ...s.movers, rclone: v as TransferSettings['movers']['rclone'] } }) },
  { name: 'CONDUIT', get: s => s.movers.conduit, set: (s, v) => ({ ...s, movers: { ...s.movers, conduit: v as TransferSettings['movers']['conduit'] } }) },
  { name: 'the defaults for a new transfer', get: s => s.defaults, set: (s, v) => ({ ...s, defaults: v as TransferSettings['defaults'] }) },
  { name: 'library hosts', get: s => s[HOST_LOCATIONS], set: (s, v) => ({ ...s, [HOST_LOCATIONS]: v as TransferSettings['hostLocations'] }) },
]
/** The lists that save at once, for saying what changed elsewhere. */
const LISTS: Pick<Group, 'name' | 'get'>[] = [
  { name: 'saved locations', get: s => s.savedLocations },
  { name: 'folders on this computer', get: s => s.computerFolders },
]

/** JSON with sorted keys, so two reads of the same value compare equal. */
function stable(v: unknown): string {
  return JSON.stringify(v, (_k, x: unknown) => (x && typeof x === 'object' && !Array.isArray(x)
    ? Object.fromEntries(Object.entries(x as Record<string, unknown>).sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0)))
    : x))
}
const same = (a: unknown, b: unknown) => stable(a) === stable(b)

/** The Save fields the draft changed from the copy it was edited from. */
export function editedGroups(base: TransferSettings, draft: TransferSettings): string[] {
  return GROUPS.filter(g => !same(g.get(base), g.get(draft))).map(g => g.name)
}

/** The draft's unsaved edits laid over a newer read (`fresh`) of what it
 *  was edited from (`base`). `elsewhere` names what changed in between,
 *  and `conflicts` the edited fields that changed there to something
 *  else. */
export function mergeEdits(base: TransferSettings, draft: TransferSettings, fresh: TransferSettings) {
  let next = fresh
  const conflicts: string[] = []
  for (const g of GROUPS) {
    if (same(g.get(base), g.get(draft))) continue
    next = g.set(next, g.get(draft))
    if (!same(g.get(fresh), g.get(base)) && !same(g.get(fresh), g.get(draft))) conflicts.push(g.name)
  }
  const elsewhere = [...LISTS, ...GROUPS].filter(g => !same(g.get(fresh), g.get(base))).map(g => g.name)
  return { draft: next, elsewhere, conflicts }
}

const listText = (xs: string[]) => (xs.length < 2 ? xs.join('') : `${xs.slice(0, -1).join(', ')} and ${xs.at(-1)}`)

/** What a PUT takes: the section without its read-only fields. */
function input(s: TransferSettings) {
  const { managed: _m, mcpServers: _x, detected: _d, ...rest } = s
  void _m; void _x; void _d
  return rest
}

/**
 * Settings, Transfers. It opens on what works with no setup: the places
 * found and whether rsync is here. Then saved locations (each added through
 * a dialog that tests it), folders on this computer, library hosts, how
 * files move, and defaults. Saved locations and folders save as they are
 * added, changed or removed; the rest saves with Save. With `managed`
 * (sign-in on), the operator's environment decides and everything shows
 * disabled.
 *
 * The section is read again each time it is shown (`active` turning true),
 * when the window regains focus, and when the Transfers page saves a
 * location. Unsaved Save edits are kept: a read then updates only the lists
 * that save at once. Every save names the revision it was edited from, so a
 * save from an older read is refused (409) and never drops a location saved
 * elsewhere; the page then reads the section again and keeps the edits.
 */
export function TransfersSettingsSection({ active = true }: { active?: boolean } = {}) {
  const [st, setSt] = useState<{ base: TransferSettings; draft: TransferSettings } | null>(null)
  const stRef = useRef(st)
  const put = (next: { base: TransferSettings; draft: TransferSettings }) => { stRef.current = next; setSt(next) }
  const [locations, setLocations] = useState<TransferLocation[]>([])
  const [loadError, setLoadError] = useState('')
  const [busy, setBusy] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [changed, setChanged] = useState<{ elsewhere: string[]; conflicts: string[] } | null>(null)
  const [dialog, setDialog] = useState<{ id: string | null } | null>(null)
  const [removing, setRemoving] = useState<string | null>(null)
  const [tests, setTests] = useState<Record<string, { busy?: boolean; result?: SettingTest; error?: string }>>({})
  const [folder, setFolder] = useState({ path: '', label: '' })
  const [folderError, setFolderError] = useState('')
  const reads = useRef(0)
  const activeRef = useRef(active)
  activeRef.current = active

  const reloadLocations = () => transferApi.locations().then(setLocations, () => {})
  /** Takes a newer read of the section. With no unsaved edits it replaces
   *  the page's copy; with some, only the lists that save at once. */
  const take = (fresh: TransferSettings) => {
    const cur = stRef.current
    if (!cur || !editedGroups(cur.base, cur.draft).length) { put({ base: fresh, draft: fresh }); return }
    const lists = { savedLocations: fresh.savedLocations, computerFolders: fresh.computerFolders, detected: fresh.detected, mcpServers: fresh.mcpServers }
    put({ base: { ...cur.base, ...lists }, draft: { ...cur.draft, ...lists } })
  }
  /** Takes a newer read with its revision, the unsaved edits laid over it. */
  const adopt = (fresh: TransferSettings) => {
    const cur = stRef.current
    const m = cur ? mergeEdits(cur.base, cur.draft, fresh) : { draft: fresh, elsewhere: [], conflicts: [] }
    put({ base: fresh, draft: m.draft })
    return m
  }
  const reload = async () => {
    const n = ++reads.current
    try {
      const fresh = await transferApi.settings()
      if (n === reads.current) { take(fresh); setLoadError('') }
    } catch (e) {
      if (!stRef.current) setLoadError((e as Error).message)
    }
    void reloadLocations()
  }
  useEffect(() => { if (active) void reload() }, [active])
  useEffect(() => {
    const again = () => { if (activeRef.current && document.visibilityState !== 'hidden') void reload() }
    window.addEventListener('focus', again)
    document.addEventListener('visibilitychange', again)
    window.addEventListener(TRANSFER_SETTINGS_CHANGED, again)
    return () => {
      window.removeEventListener('focus', again)
      document.removeEventListener('visibilitychange', again)
      window.removeEventListener(TRANSFER_SETTINGS_CHANGED, again)
    }
  }, [])

  if (!st) {
    return (
      <>
        <h1>Transfers</h1>
        {loadError ? <Callout tone="error">{loadError}</Callout> : <p className="muted"><Loader inline size={16} label="Loading…" /></p>}
      </>
    )
  }

  const s = st.draft
  const off = s.managed
  const dirty = editedGroups(st.base, st.draft).length > 0
  const change = (patch: Partial<TransferSettings>) => {
    const cur = stRef.current!
    put({ ...cur, draft: { ...cur.draft, ...patch } }); setSaveError('')
  }
  const movers = (patch: Partial<TransferSettings['movers']>) => change({ movers: { ...s.movers, ...patch } })
  const defaults = (patch: Partial<TransferSettings['defaults']>) => change({ defaults: { ...s.defaults, ...patch } })
  const hostLocations = s[HOST_LOCATIONS]
  const hosts = s.detected?.libraryHosts ?? []

  /** Saves one change to the lists that save at once, made to the section
   *  as last read; when that read is stale (409), the section is read
   *  again and the change made to it. The Save fields go as last read, so
   *  unsaved edits stay unsaved. */
  const now = async (op: (b: TransferSettings) => TransferSettings) => {
    const send = () => { const b = stRef.current!.base; return transferApi.saveSettings(input(op(b))) }
    let saved: TransferSettings
    try {
      saved = await send()
    } catch (e) {
      if (!(e instanceof TransferError && e.status === 409)) throw e
      adopt(await transferApi.settings())
      saved = await send()
    }
    adopt(saved)
    void reloadLocations()
    return saved
  }
  /** Save: the Save fields as edited, with the lists as last read (they
   *  save on their own). On a 409 the section is read again and the edits
   *  laid over it; with no conflict they are saved on top at once, and
   *  what changed elsewhere is said. A conflict waits for the person. */
  const save = async () => {
    setBusy(true); setSaveError(''); setChanged(null)
    const send = () => {
      const cur = stRef.current!
      return transferApi.saveSettings(input({ ...cur.draft, savedLocations: cur.base.savedLocations, computerFolders: cur.base.computerFolders, revision: cur.base.revision }))
    }
    const done = (saved: TransferSettings, note = '') => {
      put({ base: saved, draft: saved })
      void reloadLocations()
      toast.success(`Saved. New transfers use these settings.${note}`)
    }
    try {
      try {
        done(await send())
      } catch (e) {
        if (!(e instanceof TransferError && e.status === 409)) throw e
        const m = adopt(await transferApi.settings())
        void reloadLocations()
        if (m.conflicts.length) { setChanged({ elsewhere: m.elsewhere, conflicts: m.conflicts }); return }
        const note = m.elsewhere.length ? ` Meanwhile ${listText(m.elsewhere)} changed elsewhere, and those changes are kept.` : ''
        done(await send(), note)
      }
    } catch (e) {
      setSaveError((e as Error).message)
    } finally { setBusy(false) }
  }
  const discard = async () => {
    setChanged(null); setSaveError('')
    const fresh = await transferApi.settings().catch((e: Error) => { setSaveError(e.message); return null })
    if (fresh) put({ base: fresh, draft: fresh })
  }
  const saveLocation = async (l: SavedLocation, result: SettingTest | null) => {
    const id = dialog?.id ?? null
    let kept: SavedLocation | undefined
    if (id === null) {
      kept = (await transferApi.addLocation(l)).location
      adopt(await transferApi.settings())
      void reloadLocations()
    } else {
      const saved = await now(b => {
        if (!b.savedLocations.some(x => x.id === id)) throw new Error(`${l.label} was removed elsewhere. Close this and add it again to keep it.`)
        return { ...b, savedLocations: b.savedLocations.map(x => (x.id === id ? l : x)) }
      })
      kept = saved.savedLocations.find(x => x.id === id)
    }
    // The test's result stays beside the row it was for.
    if (result && kept) setTests(t => ({ ...t, [`loc-${kept.id}`]: { result } }))
    setDialog(null)
    toast.success(`Saved ${l.label}.`)
  }
  /** A change to the folders, saved at once. */
  const folders = async (op: (fs: ComputerFolder[]) => ComputerFolder[], done: string) => {
    setFolderError('')
    try {
      await now(b => ({ ...b, computerFolders: op(b.computerFolders) }))
      toast.success(done)
      return true
    } catch (e) {
      setFolderError((e as Error).message)
      return false
    }
  }
  const addFolder = async () => {
    const path = folder.path.trim()
    const label = folder.label.trim() || path.split('/').filter(Boolean).pop() || path
    const ok = await folders(fs => (fs.some(f => f.path === path) ? fs : [...fs, { path, label, builtIn: false, enabled: true }]), `Added ${label}. Transfers can use it now.`)
    if (ok) setFolder({ path: '', label: '' })
  }

  const runTest = async (key: string, body: Parameters<typeof transferApi.testSetting>[0]) => {
    setTests(t => ({ ...t, [key]: { busy: true } }))
    try {
      const result = await transferApi.testSetting(body)
      setTests(t => ({ ...t, [key]: { result } }))
    } catch (e) {
      setTests(t => ({ ...t, [key]: { error: (e as Error).message } }))
    }
  }
  const TestLine = ({ k }: { k: string }) => {
    const t = tests[k]
    if (!t) return null
    if (t.busy) return <span className="muted"><Loader inline size={14} label="Testing…" /></span>
    if (t.error) return <span className="xfer-test bad" role="status">{t.error}</span>
    return <span className={`xfer-test ${t.result!.ok ? 'good' : 'bad'}`} role="status">{testText(t.result!)}</span>
  }

  const rclone = s.movers.rclone ?? { enabled: true, path: 'rclone' }
  const moverItems: SelectItem<MoverName>[] = [
    { value: 'auto', label: 'Automatic', description: 'The first that reaches both ends' },
    { value: 'rsync', label: 'rsync', disabled: !s.movers.rsync.enabled },
    { value: 'rclone', label: 'rclone', disabled: !rclone.enabled },
    { value: 'studio-copy', label: 'Studio copy' },
    { value: 'conduit', label: 'CONDUIT', disabled: !s.movers.conduit.enabled },
  ]
  const serverItems: SelectItem<string>[] = s.mcpServers.map(m => ({ value: m.id, label: m.label, description: m.connected ? 'Connected' : 'Not connected' }))
  const ready = placesReady(locations, s)

  return (
    <div className="xfer-settings">
      <h1>Transfers</h1>
      <p className="muted view-sub">Where the Transfers page can send files, and how they move. Libraries and your clusters need no setup here.</p>
      <ReadySummary ready={ready} />
      {off && <Callout tone="muted" className="xfer-settings-managed">The operator of this Studio sets these, in its environment. They are shown here read only.</Callout>}

      <div className="tool-group">Saved locations</div>
      <p className="muted">Folders on other computers, reached over ssh. Adding, editing or removing one saves at once.</p>
      {s.savedLocations.length === 0 && <p className="muted">None yet.</p>}
      {s.savedLocations.length > 0 && (
        <table className="xfer-settings-table">
          <thead><tr><th>Name</th><th>Host</th><th>Folder</th><th><span className="sr-only">Actions</span></th></tr></thead>
          {s.savedLocations.map((l, i) => {
            const k = `loc-${l.id || i}`
            // Each location is its own body, so its test result can sit on
            // a full-width line under it.
            return (
              <tbody key={k}>
                <tr>
                  <td>{l.label}</td>
                  <td className="xfer-mono">{l.host}</td>
                  <td className="xfer-mono">{l.root}</td>
                  <td className="xfer-settings-actions">
                    <Button size="sm" variant="ghost" disabled={off} onClick={() => void runTest(k, { host: l.host, root: l.root, rsyncPath: l.rsyncPath, port: l.port, sshOptions: l.sshOptions })}>Test</Button>
                    <Button size="sm" variant="ghost" disabled={off} onClick={() => setDialog({ id: l.id })}>Edit</Button>
                    <Button size="sm" variant="ghost" disabled={off} onClick={() => setRemoving(l.id)}>Remove</Button>
                  </td>
                </tr>
                {tests[k] && <tr className="xfer-settings-testrow"><td colSpan={4}><TestLine k={k} /></td></tr>}
              </tbody>
            )
          })}
        </table>
      )}
      <Button size="sm" variant="secondary" className="xfer-settings-add" disabled={off} onClick={() => setDialog({ id: null })}>Add a location</Button>

      <div className="tool-group">Folders on this computer</div>
      <p className="muted">Folders on the computer Studio runs on that transfers may read and write. Changes here save at once.</p>
      <ul className="xfer-settings-folders">
        {s.computerFolders.map(f => (
          <li key={f.builtIn ? f.label : f.path}>
            {f.builtIn
              ? <Switch size="sm" checked={f.enabled} disabled={off} aria-label={`Use ${f.label}`}
                  onCheckedChange={v => void folders(fs => fs.map(x => (x.builtIn && x.label === f.label ? { ...x, enabled: v } : x)), `Saved. ${f.label} is ${v ? 'offered' : 'no longer offered'} to transfers.`)} />
              : <span className="xfer-settings-spacer" />}
            <span className="xfer-settings-folder-label" title={f.label}>{f.label}</span>
            <code className="muted xfer-settings-folder-path" title={f.path}>{f.path}</code>
            {!f.builtIn && <Button size="sm" variant="ghost" className="xfer-settings-folder-remove" disabled={off} aria-label={`Remove ${f.label}`}
              onClick={() => void folders(fs => fs.filter(x => x.builtIn || x.path !== f.path), `Removed ${f.label}. Its files are not touched.`)}>Remove</Button>}
          </li>
        ))}
      </ul>
      {!off && (
        <div className="xfer-settings-inline xfer-settings-addfolder">
          <Input size="sm" aria-label="Folder to add" mono placeholder="/home/user/Projects" value={folder.path}
            onChange={e => { setFolder({ ...folder, path: e.target.value }); setFolderError('') }}
            error={folder.path && !folder.path.trim().startsWith('/') ? 'Start the folder with /' : undefined} />
          <Input size="sm" aria-label="Its name" placeholder="Name" value={folder.label}
            onChange={e => setFolder({ ...folder, label: e.target.value })} />
          <Button size="sm" variant="secondary" disabled={!folder.path.trim().startsWith('/') || s.computerFolders.some(f => f.path === folder.path.trim())}
            onClick={() => void addFolder()}>Add folder</Button>
        </div>
      )}
      {folderError && <Callout tone="error" size="sm" live>{folderError}</Callout>}

      {hostLocations && (
        <>
          <div className="tool-group">Library hosts</div>
          <label className="xfer-check">
            <Switch size="sm" checked={hostLocations.enabled} disabled={off} aria-label="Offer library hosts"
              onCheckedChange={v => change({ [HOST_LOCATIONS]: { ...hostLocations, enabled: v } })} />
            Offer the home folder of each computer a library is on
          </label>
          {hostLocations.enabled && hosts.length > 0 && (
            <ul className="xfer-settings-folders">
              {hosts.map(h => (
                <li key={h}>
                  <Checkbox checked={!hostLocations.hidden.includes(h)} disabled={off} aria-label={`Offer ${h}`}
                    onCheckedChange={v => change({ [HOST_LOCATIONS]: { ...hostLocations, hidden: v ? hostLocations.hidden.filter(x => x !== h) : [...hostLocations.hidden, h] } })} />
                  <code>{h}</code>
                </li>
              ))}
            </ul>
          )}
          {hostLocations.enabled && !hosts.length && <p className="muted">No library is on another computer.</p>}
        </>
      )}

      <div className="tool-group">How files move</div>
      <div className="xfer-settings-movers">
        <div className="xfer-settings-mover">
          <label className="xfer-check"><Switch size="sm" checked={s.movers.rsync.enabled} disabled={off}
            onCheckedChange={v => movers({ rsync: { ...s.movers.rsync, enabled: v } })} aria-label="rsync" /> <b>rsync</b></label>
          <span className="muted">
            Used where both ends have it.{' '}
            {s.detected && (!s.detected.rsyncLocal ? 'Not found on this computer.'
              : s.detected.rsyncText ? `Found ${s.detected.rsyncText}.`
                : `Found here${s.detected.rsyncVersion ? `, version ${s.detected.rsyncVersion}` : ''}.`)}
          </span>
          {s.movers.rsync.enabled && (
            <Input size="sm" label="Program" mono value={s.movers.rsync.path} disabled={off}
              onChange={e => movers({ rsync: { ...s.movers.rsync, path: e.target.value } })} />
          )}
        </div>
        <RcloneMover value={rclone} detected={s.detected?.rclone} disabled={off}
          onChange={v => movers({ rclone: v })} />
        <div className="xfer-settings-mover">
          <span className="xfer-check"><b>Studio copy</b> <StatusLabel variant="success">Always on</StatusLabel></span>
          <span className="muted">Studio reads each file over ssh and writes it itself, wherever rsync is missing.</span>
        </div>
        <div className="xfer-settings-mover">
          <label className="xfer-check"><Switch size="sm" checked={s.movers.conduit.enabled} disabled={off || !s.mcpServers.length}
            onCheckedChange={v => movers({ conduit: { ...s.movers.conduit, enabled: v } })} aria-label="CONDUIT" /> <b>CONDUIT</b></label>
          <span className="muted">A site's data transfer service, used when both ends are in its storage.</span>
          {!s.mcpServers.length && (
            <span className="muted">To use it, attach it in <a href="#view=settings:tools">Assistant tools, Attached MCP servers</a>.</span>
          )}
          {s.mcpServers.length > 0 && s.movers.conduit.enabled && (
            <div className="xfer-settings-conduit">
              <div className="xfer-settings-inline">
                <Select<string> aria-label="CONDUIT server" size="sm" items={serverItems} value={s.movers.conduit.server ?? undefined}
                  placeholder="Choose the attached server" disabled={off}
                  onValueChange={v => movers({ conduit: { ...s.movers.conduit, server: v } })} />
                <Button size="sm" variant="secondary" disabled={off || !s.movers.conduit.server}
                  onClick={() => void runTest('conduit', { conduit: s.movers.conduit.server! })}>Test</Button>
                <TestLine k="conduit" />
              </div>
              <ConduitMounts mounts={s.movers.conduit.mounts ?? []} locations={locations} disabled={off}
                onChange={mounts => movers({ conduit: { ...s.movers.conduit, mounts } })} />
            </div>
          )}
        </div>
      </div>

      <div className="tool-group">Defaults for a new transfer</div>
      <div className="xfer-options-grid xfer-settings-defaults">
        <label className="xfer-option-field"><span>How files move</span>
          <Select<MoverName> aria-label="Default mover" size="sm" items={moverItems} value={s.defaults.mover} disabled={off}
            onValueChange={v => { if (v) defaults({ mover: v }) }} />
        </label>
        <label className="xfer-option-field"><span>Files at once</span>
          <NumberInput aria-label="Files at once" size="sm" min={1} max={16} step={1} value={s.defaults.parallel} disabled={off}
            onValueChange={v => defaults({ parallel: Math.min(16, Math.max(1, Math.round(v ?? 4))) })} />
        </label>
        <label className="xfer-check"><Checkbox checked={s.defaults.checksum} disabled={off} onCheckedChange={v => defaults({ checksum: !!v })} /> Compare by checksum</label>
        <label className="xfer-check"><Checkbox checked={s.defaults.preserveTimes} disabled={off} onCheckedChange={v => defaults({ preserveTimes: !!v })} /> Keep modification times</label>
      </div>

      {!off && (
        <div className="query-actions xfer-settings-save">
          <Button variant="primary" loading={busy} disabled={!dirty} onClick={() => void save()}>{busy ? 'Saving…' : 'Save'}</Button>
          {dirty && <StatusLabel variant="warning">Unsaved changes</StatusLabel>}
          <span className="muted">Save keeps how files move, library hosts and the defaults.</span>
        </div>
      )}
      {changed && (
        <Callout tone="warning" live className="xfer-settings-changed">
          Transfers settings changed elsewhere since this page loaded{changed.elsewhere.length ? ` (${listText(changed.elsewhere)})` : ''}.
          {' '}Your unsaved changes to {listText(changed.conflicts)} differ from what was saved there, so nothing was saved.
          {' '}Save again to keep your changes, or Reload to show the saved settings and drop them.{' '}
          <Button size="sm" variant="secondary" onClick={() => void discard()}>Reload</Button>
        </Callout>
      )}
      {saveError && <Callout tone="error" live>{saveError}</Callout>}

      {dialog && (
        <LocationDialog first={dialog.id === null ? null : s.savedLocations.find(l => l.id === dialog.id) ?? null}
          onClose={() => setDialog(null)} onSave={saveLocation} />
      )}
      <ConfirmModal open={removing !== null} onClose={() => setRemoving(null)} destructive
        title={`Remove ${removing !== null ? s.savedLocations.find(l => l.id === removing)?.label ?? '' : ''}?`}
        description="It leaves the location picker. Files there are not touched."
        confirmLabel="Remove"
        onConfirm={async () => {
          if (removing === null) return
          const label = s.savedLocations.find(l => l.id === removing)?.label ?? removing
          try {
            await now(b => ({ ...b, savedLocations: b.savedLocations.filter(l => l.id !== removing) }))
            toast.success(`Removed ${label}.`)
          } catch (e) { toast.error((e as Error).message) }
        }} />
    </div>
  )
}
