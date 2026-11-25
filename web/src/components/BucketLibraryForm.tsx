import { useCallback, useEffect, useState } from 'react'
import { Button, Callout, Combobox, Field, Input, Loader, Select, type ComboboxGroup } from '@parallelworks/ui'
import { BUCKET_EVERY, type SettingsLibrary } from '../api'
import { transferApi, type BrowseResult, type TransferLocation } from '../transferApi'
import { intoView, postJson } from './LibraryFormParts'

/** The remotes a bucket library can list: rclone's, each with its type. */
export function bucketLocationItems(locations: TransferLocation[]): ComboboxGroup<string>[] {
  const items = locations.filter(l => l.kind === 'rclone').map(l => ({
    value: l.id,
    label: l.label,
    description: l.available ? l.detail : `Unavailable: ${l.why ?? 'not reachable now'}`,
    disabled: !l.available,
  }))
  return items.length ? [{ label: 'rclone remotes', items }] : []
}

/**
 * A bucket library (POST /api/libraries/bucket): a bucket, or a folder in
 * one, of an rclone remote. Any S3 compatible store works, AWS or on
 * premises (MinIO, Ceph and the like), since rclone needs only its
 * endpoint and keys, and so do the other stores rclone lists. Studio
 * indexes the listing alone, with one fast list: names, sizes and times,
 * no object read and no data moved. Opening a file reads that object.
 */
export function BucketLibraryForm({ onAdded }: { onAdded?: (lib: SettingsLibrary) => void }) {
  const [locations, setLocations] = useState<TransferLocation[] | null>(null)
  const [loadError, setLoadError] = useState('')
  const [locationId, setLocationId] = useState<string | null>(null)
  const [path, setPath] = useState('')
  const [listing, setListing] = useState<BrowseResult | null>(null)
  const [listError, setListError] = useState('')
  const [listing0, setListing0] = useState(false)
  const [label, setLabel] = useState('')
  const [every, setEvery] = useState(1440)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      const all = await transferApi.locations()
      setLocations(all.filter(l => l.kind === 'rclone'))
      setLoadError('')
    } catch (e) { setLoadError(String((e as Error).message ?? e)); setLocations([]) }
  }, [])
  useEffect(() => { void load() }, [load])

  const open = useCallback(async (loc: string, p: string | null) => {
    setListing0(true); setListError('')
    try {
      const r = await transferApi.browse(loc, p)
      setListing(r); setPath(r.path)
    } catch (e) { setListing(null); setListError(String((e as Error).message ?? e)) } finally { setListing0(false) }
  }, [])

  const choose = (v: string | null) => {
    setLocationId(v); setPath(''); setListing(null); setError('')
    if (v) void open(v, null)
  }

  const chosen = locations?.find(l => l.id === locationId) ?? null
  const folderName = path.replace(/\/+$/, '').split('/').pop() || chosen?.label || ''
  const dirs = (listing?.entries ?? []).filter(e => e.type === 'dir')

  const add = async () => {
    if (!locationId) return
    setBusy(true); setError('')
    try {
      const r = await postJson<{ library: SettingsLibrary }>('/api/libraries/bucket', {
        location: locationId, path, label: label.trim() || undefined, every,
      })
      onAdded?.(r.library)
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(false) }
  }

  if (locations === null) return <div className="muted"><Loader inline size={16} label="Reading the rclone remotes…" /></div>

  return (
    <div className="copy-library-form">
      {loadError && <Callout tone="warning" size="sm">{loadError}</Callout>}
      {!locations.length && !loadError && (
        <Callout tone="info" size="sm">
          No rclone remotes were found. Set one up with <code>rclone config</code> on this computer (an S3 compatible store needs its endpoint
          and keys), and it shows here and in Transfers. Settings, Transfers says where Studio looks for rclone.
        </Callout>
      )}
      <Field label="Remote">
        <Combobox<string>
          aria-label="Remote"
          placeholder={locations.length ? 'Choose an rclone remote' : 'No rclone remotes yet'}
          items={bucketLocationItems(locations)}
          value={locationId}
          onValueChange={choose}
          itemToStringLabel={v => locations.find(l => l.id === v)?.label ?? v}
          size="sm"
          disabled={!locations.length}
        />
      </Field>
      {chosen && (
        <div className="copy-library-folder" role="group" aria-label="Bucket or folder to index">
          <div className="copy-library-path">
            <span className="muted">Bucket or folder:</span>{' '}
            <code>{path ? `${chosen.label}/${path}` : `${chosen.label} (every bucket)`}</code>
            {listing?.parent !== null && listing && (
              <Button size="sm" variant="ghost" disabled={listing0} onClick={() => void open(chosen.id, listing.parent ?? '')}>Up</Button>
            )}
          </div>
          {listing0 && <Loader inline size={16} label="Listing…" />}
          {listError && <Callout tone="error" size="sm">{listError}</Callout>}
          {listing && !listing0 && (
            dirs.length
              ? (
                <ul className="copy-library-dirs" aria-label="Buckets and folders in it">
                  {dirs.map(d => (
                    <li key={d.name}>
                      <Button size="sm" variant="ghost" onClick={() => void open(chosen.id, path ? `${path}/${d.name}` : d.name)}>{d.name}/</Button>
                    </li>
                  ))}
                </ul>
              )
              : <p className="muted">No folders in it{listing.entries.length ? `; ${listing.entries.length} ${listing.entries.length === 1 ? 'object' : 'objects'}` : ''}.</p>
          )}
        </div>
      )}
      <div className="settings-grid">
        <Input label="Label" hint="optional" placeholder={folderName || 'Shown in the Library switcher'} value={label} onChange={e => setLabel(e.target.value)} />
        <Field label="List again">
          <Select<number> aria-label="List again" items={BUCKET_EVERY} value={every} onValueChange={v => setEvery(v ?? 1440)} />
        </Field>
      </div>
      <p className="muted copy-library-note">
        Studio lists it with one fast list, about one request per 1,000 objects, and indexes each object's name, size and upload time.
        No object is read to index it, so nothing is copied out of the store. On AWS a listing costs about $0.005 per 1,000 requests;
        a store on premises charges nothing. Opening a file reads that object alone: its first 512 KB for a text preview, the whole
        object up to 50 MB for a download or a document preview. Text inside the objects is not indexed yet.
      </p>
      {error && <div className="lib-form-errors" ref={intoView}><Callout tone="error" live>{error}</Callout></div>}
      <div className="query-actions">
        <Button variant="primary" loading={busy} disabled={!chosen || !listing || busy} onClick={() => void add()}>List and index</Button>
      </div>
    </div>
  )
}
