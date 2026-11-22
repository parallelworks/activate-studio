import { useCallback, useEffect, useState } from 'react'
import { Button, Callout, Combobox, Field, Input, Loader, Select, type ComboboxGroup } from '@parallelworks/ui'
import { COPY_EVERY, type IndexDepth, type SettingsLibrary } from '../api'
import {
  KIND_ORDER, TRANSFER_SETTINGS_CHANGED, testText, transferApi,
  type BrowseResult, type SavedLocation, type SettingTest, type TransferLocation,
} from '../transferApi'
import { DEFAULT_LIBRARY_DEPTH, IndexDepthControl, meaningLastNote, useOcrHere } from './IndexDepth'
import { postJson } from './LibraryFormParts'
import { LocationDialog } from './SavedLocationDialog'

/** The Transfers locations a copy can come from: hosts, clusters and rclone
 *  remotes. Libraries and this computer's folders are built in place. */
export const COPY_KINDS = ['saved', 'host', 'cluster', 'rclone'] as const
const NEW_LOCATION = '\u0000new-location'

/** The location picker's groups: the kinds a copy comes from, in the
 *  Transfers page's order, then saving a new location. */
export function copyLocationItems(locations: TransferLocation[]): ComboboxGroup<string>[] {
  const groups: ComboboxGroup<string>[] = KIND_ORDER
    .filter(k => (COPY_KINDS as readonly string[]).includes(k.kind))
    .map(({ kind, label }) => ({
      label,
      items: locations.filter(l => l.kind === kind).map(l => ({
        value: l.id,
        label: l.label,
        description: l.available ? l.detail : `Unavailable: ${l.why ?? 'not reachable now'}`,
        disabled: !l.available,
      })),
    }))
    .filter(g => g.items.length)
  groups.push({ label: 'New', items: [{ value: NEW_LOCATION, label: 'Save a location…', description: 'An SFTP or ssh host, with the folder Studio starts in' }] })
  return groups
}

/**
 * A copy library (POST /api/libraries/copy): a folder in a Transfers
 * location, such as an SFTP drop, an ssh host, a cluster or an rclone
 * remote, that Studio copies into its own data on this computer, indexes as
 * it indexes a folder here, and keeps in step by looking for changes on an
 * interval. Search, the Library tree, previews and the assistant then work
 * on it as on any library. The copy is read only; files go to the location.
 */
export function CopyLibraryForm({ onAdded }: { onAdded?: (lib: SettingsLibrary) => void }) {
  const [locations, setLocations] = useState<TransferLocation[] | null>(null)
  const [loadError, setLoadError] = useState('')
  const [locationId, setLocationId] = useState<string | null>(null)
  const [path, setPath] = useState('')
  const [listing, setListing] = useState<BrowseResult | null>(null)
  const [listError, setListError] = useState('')
  const [listing0, setListing0] = useState(false)
  // Whether the person chose the folder shown. Browsing starts in the
  // location's own start folder, often a whole home folder, so that is
  // never copied without being chosen.
  const [picked, setPicked] = useState(false)
  const [label, setLabel] = useState('')
  const [every, setEvery] = useState(15)
  const [depth, setDepth] = useState<IndexDepth>(DEFAULT_LIBRARY_DEPTH)
  const ocr = useOcrHere(true)
  const [saving, setSaving] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [note, setNote] = useState('')

  const load = useCallback(async () => {
    try {
      const all = await transferApi.locations()
      setLocations(all.filter(l => (COPY_KINDS as readonly string[]).includes(l.kind)))
      setLoadError('')
    } catch (e) { setLoadError(String((e as Error).message ?? e)); setLocations([]) }
  }, [])
  useEffect(() => { void load() }, [load])

  // The folder list of the chosen location: where browsing starts, then
  // each folder opened.
  const open = useCallback(async (loc: string, p: string | null, chosen = true) => {
    setListing0(true); setListError('')
    try {
      const r = await transferApi.browse(loc, p)
      setListing(r); setPath(r.path); setPicked(chosen)
    } catch (e) { setListing(null); setListError(String((e as Error).message ?? e)) } finally { setListing0(false) }
  }, [])

  const choose = (v: string | null) => {
    if (v === NEW_LOCATION) { setSaving(true); return }
    setLocationId(v); setPath(''); setListing(null); setError(''); setPicked(false)
    if (v) void open(v, null, false)
  }

  const saveLocation = async (l: SavedLocation, result: SettingTest | null) => {
    const { id, location: kept } = await transferApi.addLocation(l)
    window.dispatchEvent(new Event(TRANSFER_SETTINGS_CHANGED))
    await load()
    setSaving(false)
    setNote(`Saved ${kept.label}.${result ? ` ${testText(result)}${result.ok ? '' : '.'}` : ''} It is also in Settings, Transfers.`)
    choose(id)
  }

  const chosen = locations?.find(l => l.id === locationId) ?? null
  const folderName = path.replace(/\/+$/, '').split('/').pop() || chosen?.label || ''
  const dirs = (listing?.entries ?? []).filter(e => e.type === 'dir')

  const add = async () => {
    if (!locationId) return
    setBusy(true); setError('')
    try {
      const r = await postJson<{ library: SettingsLibrary }>('/api/libraries/copy', {
        location: locationId, path, label: label.trim() || undefined, every, depth,
      })
      onAdded?.(r.library)
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(false) }
  }

  if (locations === null) return <div className="muted"><Loader inline size={16} label="Reading the Transfers locations…" /></div>

  return (
    <div className="copy-library-form">
      {loadError && <Callout tone="warning" size="sm">{loadError}</Callout>}
      {note && <Callout tone="success" size="sm" live>{note}</Callout>}
      <Field label="Location" description={locations.length ? undefined : 'An SFTP or ssh host is saved with Save a location, or in Settings, Transfers.'}>
        <Combobox<string>
          aria-label="Location"
          placeholder={locations.length ? 'Choose a location' : 'Save a location to begin'}
          items={copyLocationItems(locations)}
          value={locationId}
          onValueChange={choose}
          itemToStringLabel={v => locations.find(l => l.id === v)?.label ?? v}
          size="sm"
        />
      </Field>
      {chosen && (
        <div className="copy-library-folder" role="group" aria-label="Folder to copy">
          <div className="copy-library-path">
            <span className="muted">Folder:</span>{' '}
            <code>{path ? `${chosen.label}/${path}` : `${chosen.label} (its top folder)`}</code>
            {listing?.parent !== null && listing && (
              <Button size="sm" variant="ghost" disabled={listing0} onClick={() => void open(chosen.id, listing.parent ?? '')}>Up</Button>
            )}
            {listing && !picked && (
              <Button size="sm" variant="ghost" disabled={listing0} onClick={() => setPicked(true)}>Copy this whole folder</Button>
            )}
          </div>
          {listing && !picked && !listing0 && <p className="muted">Open the folder to copy below, or choose this whole folder.</p>}
          {listing0 && <Loader inline size={16} label="Listing…" />}
          {listError && <Callout tone="error" size="sm">{listError}</Callout>}
          {listing && !listing0 && (
            dirs.length
              ? (
                <ul className="copy-library-dirs" aria-label="Folders in it">
                  {dirs.map(d => (
                    <li key={d.name}>
                      <Button size="sm" variant="ghost" onClick={() => void open(chosen.id, path ? `${path}/${d.name}` : d.name)}>{d.name}/</Button>
                    </li>
                  ))}
                </ul>
              )
              : <p className="muted">No folders in it{listing.entries.length ? `; ${listing.entries.length} ${listing.entries.length === 1 ? 'file' : 'files'}` : ''}.</p>
          )}
        </div>
      )}
      <div className="settings-grid">
        <Input label="Label" hint="optional" placeholder={folderName || 'Shown in the Library switcher'} value={label} onChange={e => setLabel(e.target.value)} />
        <Field label="Look for changes">
          <Select<number> aria-label="Look for changes" items={COPY_EVERY} value={every} onValueChange={v => setEvery(v ?? 15)} />
        </Field>
      </div>
      <IndexDepthControl value={depth} onChange={setDepth} disabled={busy} ocr={ocr} note={meaningLastNote} />
      <p className="muted copy-library-note">
        Studio keeps a copy of the folder in its own data on this computer, which takes as much space as the folder holds, and indexes the copy.
        The copy is read only: put files in the location itself, and they arrive at the next look for changes.
      </p>
      {error && <Callout tone="error" live>{error}</Callout>}
      <div className="query-actions">
        <Button variant="primary" loading={busy} disabled={!chosen || !listing || !picked || busy} onClick={() => void add()}>Copy and index</Button>
      </div>
      {saving && <LocationDialog first={null} onClose={() => setSaving(false)} onSave={saveLocation} />}
    </div>
  )
}
