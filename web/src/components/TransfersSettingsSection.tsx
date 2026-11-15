import { useEffect, useState } from 'react'
import {
  Button, Callout, Checkbox, ConfirmModal, Disclosure, Input, Loader, Modal, NumberInput, Select, StatusLabel, Switch, toast, type SelectItem,
} from '@parallelworks/ui'
import {
  HOST_LOCATIONS, TransferError, bytesText, fieldError, placesReady, testText, transferApi,
  type ComputerFolder, type ConduitMount, type MoverName, type SavedLocation, type SettingTest, type TransferLocation, type TransferSettings,
} from '../transferApi'
import { ReadySummary } from './TransfersReady'

/** Why a saved location's fields cannot be used, field by field, checked
 *  before anything is sent. Empty when they can. */
export function locationProblems(d: { host: string; root: string }): { host?: string; root?: string } {
  const out: { host?: string; root?: string } = {}
  const host = d.host.trim()
  if (!host) out.host = 'Enter the host.'
  else if (/\s/.test(host)) out.host = 'A host has no spaces.'
  else if (host.startsWith('-')) out.host = 'A host cannot start with a dash.'
  const root = d.root.trim()
  if (!root) out.root = 'Enter the folder.'
  else if (!root.startsWith('/')) out.root = 'Start the folder with /, as /srv/archive.'
  else if (root.split('/').includes('..')) out.root = 'The folder cannot contain "..".'
  return out
}

type Field = 'host' | 'root' | 'port' | 'sshOptions' | 'label'

/** The field a server 400 is about, from its leading name ("root: ...",
 *  "savedLocations[2].host: ..."), with the rest of the message. */
function serverField(message: string): { field: Field; text: string } | null {
  const f = fieldError(message)
  if (!f) return null
  const last = f.field.split('.').pop() as string
  return (['host', 'root', 'port', 'sshOptions', 'label'] as string[]).includes(last) ? { field: last as Field, text: f.text } : null
}

/** The dialog that adds or edits a saved location. Save tests it first and
 *  keeps it only when the test passes, or when asked to keep it anyway. */
function LocationDialog({ first, onClose, onSave }: {
  first: SavedLocation | null
  onClose: () => void
  onSave: (l: SavedLocation, result: SettingTest | null) => Promise<void>
}) {
  const [d, setD] = useState({
    label: first?.label ?? '', host: first?.host ?? '', root: first?.root ?? '',
    port: first?.port ? String(first.port) : '', sshOptions: first?.sshOptions ?? '',
  })
  const [touched, setTouched] = useState({ host: !!first, root: !!first })
  const [busy, setBusy] = useState<'test' | 'save' | null>(null)
  const [result, setResult] = useState<SettingTest | null>(null)
  const [server, setServer] = useState<{ field: Field; text: string } | null>(null)
  const [error, setError] = useState('')
  const problems: Partial<Record<Field, string>> = locationProblems(d)
  const port = d.port.trim()
  if (port && !(/^\d+$/.test(port) && +port >= 1 && +port <= 65535)) problems.port = 'A port from 1 to 65535.'
  if (server && !problems[server.field]) problems[server.field] = server.text
  const ok = !problems.host && !problems.root && !problems.port
  const row = (): SavedLocation => {
    const out: SavedLocation = { ...(first ?? { id: '' }), label: d.label.trim() || d.host.trim(), host: d.host.trim(), root: d.root.trim() }
    delete out.port; delete out.sshOptions
    if (port) out.port = +port
    if (d.sshOptions.trim()) out.sshOptions = d.sshOptions.trim()
    return out
  }
  const fail = (e: unknown) => {
    const msg = (e as Error).message
    const f = serverField(msg)
    if (f) { setServer(f); setTouched({ host: true, root: true }) } else setError(msg)
  }

  const keep = async (r: SettingTest | null = result) => {
    setBusy('save'); setError('')
    try { await onSave(row(), r) } catch (e) { fail(e) } finally { setBusy(null) }
  }
  const testThenSave = async () => {
    setTouched({ host: true, root: true })
    if (!ok) return
    setBusy('test'); setError(''); setResult(null)
    const l = row()
    let r: SettingTest
    try {
      r = await transferApi.testSetting({ host: l.host, root: l.root, rsyncPath: l.rsyncPath, port: l.port, sshOptions: l.sshOptions })
    } catch (e) {
      // A 400 names the field it is about; that shows under the field.
      setBusy(null)
      if (e instanceof TransferError && e.status === 400 && serverField(e.message)) { fail(e); return }
      r = { ok: false, why: (e as Error).message }
    }
    setResult(r); setBusy(null)
    if (r.ok) await keep(r)
  }
  const edit = (patch: Partial<typeof d>) => { setD({ ...d, ...patch }); setResult(null); setServer(null) }
  const shown = (f: 'host' | 'root') => (touched[f] ? problems[f] : undefined)

  return (
    <Modal open onClose={onClose} size="md" title={first ? 'Edit saved location' : 'Add a saved location'}
      description="A folder on another computer that Studio reaches over ssh."
      onSubmit={() => void testThenSave()}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          {result && !result.ok && <Button variant="secondary" onClick={() => void keep()} loading={busy === 'save'}>Save anyway</Button>}
          <Button variant="primary" type="submit" loading={busy !== null} disabled={busy !== null}>{busy === 'save' ? 'Saving…' : result && !result.ok ? 'Test again' : 'Test and save'}</Button>
        </>
      }>
      <div className="xfer-dialog-fields">
        <Input label="Label" value={d.label} placeholder="Archive" onChange={e => edit({ label: e.target.value })}
          description="How it is named in the location picker. The host is used when this is empty." />
        <Input label="Host" mono value={d.host} placeholder="user@archive-host" autoComplete="off" spellCheck={false}
          description="An ssh destination: user@host, or a Host name from your ssh config."
          error={shown('host')}
          onBlur={() => setTouched(t => ({ ...t, host: true }))} onChange={e => edit({ host: e.target.value })} />
        <Input label="Folder" mono value={d.root} placeholder="/srv/archive" autoComplete="off" spellCheck={false}
          description="Transfers stay inside this folder."
          error={shown('root')}
          onBlur={() => setTouched(t => ({ ...t, root: true }))} onChange={e => edit({ root: e.target.value })} />
        <Disclosure summary="Advanced" defaultOpen={!!(first?.port || first?.sshOptions)}>
          <div className="xfer-dialog-fields">
            <Input label="Port" mono value={d.port} placeholder="22" inputMode="numeric" autoComplete="off"
              description="Only when the ssh config does not give it." error={problems.port}
              onChange={e => edit({ port: e.target.value })} />
            <Input label="ssh options" mono value={d.sshOptions} placeholder="-o IdentityFile=~/.ssh/archive_key" autoComplete="off" spellCheck={false}
              description="Passed to ssh for this location." error={problems.sshOptions}
              onChange={e => edit({ sshOptions: e.target.value })} />
          </div>
        </Disclosure>
        {busy === 'test' && <p className="muted"><Loader inline size={14} label="Testing the connection…" /></p>}
        {result && <TestResult r={result} />}
        {error && <Callout tone="error" size="sm" live>{error}</Callout>}
      </div>
    </Modal>
  )
}

/** A test's checks, each on a line, or the reason it failed. */
function TestResult({ r }: { r: SettingTest }) {
  if (!r.ok) {
    return (
      <Callout tone="error" size="sm" live title="Test failed">
        {r.why ?? 'No reason was given.'} You can fix the fields and test again, or save it anyway.
      </Callout>
    )
  }
  const checks: [string, boolean | undefined][] = [
    [r.sftpOnly ? 'Reached over SFTP' : 'Reached over ssh', true],
    ['Folder exists', true],
    ['Writable', r.writable],
    [r.rsync && r.rsyncVersion ? `rsync ${r.rsyncVersion} there` : 'rsync there', r.sftpOnly ? undefined : r.rsync],
  ]
  return (
    <Callout tone={r.why ? 'warning' : 'success'} size="sm" live title="Connection works">
      <ul className="xfer-checks">
        {checks.filter(([, v]) => v !== undefined).map(([k, v]) => <li key={k} className={v ? 'yes' : 'no'}>{v ? '✓' : '✕'} {k}{!v && k.startsWith('rsync') ? ', Studio copy is used' : ''}</li>)}
        {r.freeBytes !== undefined && <li>{bytesText(r.freeBytes)} free</li>}
        {r.roundTripMs !== undefined && <li>{Math.round(r.roundTripMs)} ms round trip</li>}
      </ul>
      {r.sftpOnly && <p className="xfer-checks-note">This account takes SFTP only. Copies use Studio copy over SFTP; rsync is not available there.</p>}
      {r.rsync && r.rsyncText && <p className="xfer-checks-note">{r.rsyncText}.</p>}
      {r.why && <p className="xfer-checks-note">{r.why.charAt(0).toUpperCase() + r.why.slice(1)}.</p>}
    </Callout>
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

  const moverItems: SelectItem<MoverName>[] = [
    { value: 'auto', label: 'Automatic', description: 'The first that reaches both ends' },
    { value: 'rsync', label: 'rsync', disabled: !s.movers.rsync.enabled },
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
