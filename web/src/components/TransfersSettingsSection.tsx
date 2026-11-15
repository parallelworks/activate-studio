import { useEffect, useState } from 'react'
import {
  Button, Callout, Checkbox, ConfirmModal, Input, Loader, NumberInput, Select, StatusLabel, Switch, toast, type SelectItem,
} from '@parallelworks/ui'
import {
  HOST_LOCATIONS, placesReady, testText, transferApi,
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

/**
 * Settings, Transfers. It opens on what works with no setup: the places
 * found and whether rsync is here. Then saved locations (each added through
 * a dialog that tests it), folders on this computer, library hosts, how
 * files move, and defaults. Saved locations save as they are added or
 * removed; the rest saves with Save. With `managed` (sign-in on), the
 * operator's environment decides and everything shows disabled.
 */
export function TransfersSettingsSection() {
  const [s, setS] = useState<TransferSettings | null>(null)
  const [locations, setLocations] = useState<TransferLocation[]>([])
  const [loadError, setLoadError] = useState('')
  const [dirty, setDirty] = useState(false)
  const [busy, setBusy] = useState(false)
  const [saveError, setSaveError] = useState('')
  const [dialog, setDialog] = useState<{ index: number | null } | null>(null)
  const [removing, setRemoving] = useState<number | null>(null)
  const [tests, setTests] = useState<Record<string, { busy?: boolean; result?: SettingTest; error?: string }>>({})
  const [folder, setFolder] = useState({ path: '', label: '' })

  const reloadLocations = () => transferApi.locations().then(setLocations, () => {})
  useEffect(() => {
    transferApi.settings().then(setS, e => setLoadError((e as Error).message))
    void reloadLocations()
  }, [])

  if (!s) {
    return (
      <>
        <h1>Transfers</h1>
        {loadError ? <Callout tone="error">{loadError}</Callout> : <p className="muted"><Loader inline size={16} label="Loading…" /></p>}
      </>
    )
  }

  const off = s.managed
  const change = (patch: Partial<TransferSettings>) => { setS({ ...s, ...patch }); setDirty(true); setSaveError('') }
  const movers = (patch: Partial<TransferSettings['movers']>) => change({ movers: { ...s.movers, ...patch } })
  const defaults = (patch: Partial<TransferSettings['defaults']>) => change({ defaults: { ...s.defaults, ...patch } })
  const setFolders = (computerFolders: ComputerFolder[]) => change({ computerFolders })
  const hostLocations = s[HOST_LOCATIONS]
  const hosts = s.detected?.libraryHosts ?? []

  const persist = async (next: TransferSettings) => {
    const { managed: _m, mcpServers: _x, detected: _d, ...input } = next
    void _m; void _x; void _d
    const saved = await transferApi.saveSettings(input)
    setS(saved); setDirty(false)
    void reloadLocations()
    return saved
  }
  const save = async () => {
    setBusy(true); setSaveError('')
    try { await persist(s); toast.success('Saved. New transfers use these settings.') } catch (e) { setSaveError((e as Error).message) } finally { setBusy(false) }
  }
  const saveLocation = async (l: SavedLocation, result: SettingTest | null) => {
    const list = [...s.savedLocations]
    const at = dialog?.index ?? list.length
    list[at] = l
    const saved = await persist({ ...s, savedLocations: list })
    // The test's result stays beside the row it was for.
    const kept = saved.savedLocations[at]
    if (result && kept) setTests(t => ({ ...t, [`loc-${kept.id || at}`]: { result } }))
    setDialog(null)
    toast.success(`Saved ${l.label}.`)
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
      <p className="muted">Folders on other computers, reached over ssh.</p>
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
                    <Button size="sm" variant="ghost" disabled={off} onClick={() => setDialog({ index: i })}>Edit</Button>
                    <Button size="sm" variant="ghost" disabled={off} onClick={() => setRemoving(i)}>Remove</Button>
                  </td>
                </tr>
                {tests[k] && <tr className="xfer-settings-testrow"><td colSpan={4}><TestLine k={k} /></td></tr>}
              </tbody>
            )
          })}
        </table>
      )}
      <Button size="sm" variant="secondary" className="xfer-settings-add" disabled={off} onClick={() => setDialog({ index: null })}>Add a location</Button>

      <div className="tool-group">Folders on this computer</div>
      <p className="muted">Folders on the computer Studio runs on that transfers may read and write.</p>
      <ul className="xfer-settings-folders">
        {s.computerFolders.map((f, i) => (
          <li key={f.path}>
            {f.builtIn
              ? <Switch size="sm" checked={f.enabled} disabled={off} aria-label={`Use ${f.label}`}
                  onCheckedChange={v => setFolders(s.computerFolders.map((x, j) => (j === i ? { ...x, enabled: v } : x)))} />
              : <span className="xfer-settings-spacer" />}
            <span className="xfer-settings-folder-label">{f.label}</span>
            <code className="muted">{f.path}</code>
            {!f.builtIn && <Button size="sm" variant="ghost" disabled={off} onClick={() => setFolders(s.computerFolders.filter((_, j) => j !== i))}>Remove</Button>}
          </li>
        ))}
      </ul>
      {!off && (
        <div className="xfer-settings-inline xfer-settings-addfolder">
          <Input size="sm" aria-label="Folder to add" mono placeholder="/home/user/Projects" value={folder.path}
            onChange={e => setFolder({ ...folder, path: e.target.value })}
            error={folder.path && !folder.path.trim().startsWith('/') ? 'Start the folder with /' : undefined} />
          <Input size="sm" aria-label="Its name" placeholder="Name" value={folder.label}
            onChange={e => setFolder({ ...folder, label: e.target.value })} />
          <Button size="sm" variant="secondary" disabled={!folder.path.trim().startsWith('/') || s.computerFolders.some(f => f.path === folder.path.trim())}
            onClick={() => {
              const path = folder.path.trim()
              setFolders([...s.computerFolders, { path, label: folder.label.trim() || path.split('/').filter(Boolean).pop() || path, builtIn: false, enabled: true }])
              setFolder({ path: '', label: '' })
            }}>Add folder</Button>
        </div>
      )}

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
        <div className="query-actions">
          <Button variant="primary" loading={busy} disabled={!dirty} onClick={() => void save()}>{busy ? 'Saving…' : 'Save'}</Button>
        </div>
      )}
      {saveError && <Callout tone="error" live>{saveError}</Callout>}

      {dialog && (
        <LocationDialog first={dialog.index === null ? null : s.savedLocations[dialog.index]}
          onClose={() => setDialog(null)} onSave={saveLocation} />
      )}
      <ConfirmModal open={removing !== null} onClose={() => setRemoving(null)} destructive
        title={`Remove ${removing !== null ? s.savedLocations[removing]?.label : ''}?`}
        description="It leaves the location picker. Files there are not touched."
        confirmLabel="Remove"
        onConfirm={async () => {
          if (removing === null) return
          await persist({ ...s, savedLocations: s.savedLocations.filter((_, j) => j !== removing) })
        }} />
    </div>
  )
}
