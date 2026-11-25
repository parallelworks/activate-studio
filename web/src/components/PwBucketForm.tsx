import { useCallback, useEffect, useMemo, useState } from 'react'
import { Button, Callout, Checkbox, Field, Input, Loader, Select, toast } from '@parallelworks/ui'
import { BUCKET_EVERY, api, pwBucketPlace, type PwBucket, type PwBucketFolder, type SettingsLibrary } from '../api'
import { reloadAppConfig, useAppConfig } from '../config'
import { reloadWhenLibrariesBuilt } from '../libraryWatch'
import { plural } from '../plural'
import { postJson } from './LibraryFormParts'

/** The bucket and the folder in it a library is made of. */
const libraryAt = (libs: SettingsLibrary[], uri: string, path = '') => libs.find(l => l.bucket?.location === uri && l.bucket.path === path)

const say = (e: unknown) => String((e as Error)?.message ?? e)

/**
 * A library of an ACTIVATE bucket, or of several at once (POST
 * /api/libraries/bucket, once for each). Studio lists and reads a bucket
 * with the person's own pw sign-in. One bucket can be narrowed to a folder
 * and named; more are added whole, under their own names, with the same
 * interval and endpoint. Both are remembered from the buckets added before.
 */
export function PwBucketForm({ uri, buckets, holdHeight, onAdded }: {
  uri: string
  buckets: PwBucket[]
  holdHeight: () => void
  onAdded: (lib: { id: string; label: string; bucket?: unknown }, count: number) => void
}) {
  const signIn = !!useAppConfig().signIn
  const bucket = buckets.find(b => b.uri === uri)
  const [libs, setLibs] = useState<SettingsLibrary[]>([])
  const [dir, setDir] = useState('')
  const [folder, setFolder] = useState<PwBucketFolder | null>(null)
  const [looking, setLooking] = useState(false)
  const [lookError, setLookError] = useState('')
  const [more, setMore] = useState<string[]>([])
  const [label, setLabel] = useState('')
  const [every, setEvery] = useState<number | null>(null)
  const [endpoint, setEndpoint] = useState<string | null>(null)
  const [options, setOptions] = useState(false)
  const [busy, setBusy] = useState(false)
  const [errors, setErrors] = useState<{ name: string; message: string }[]>([])

  useEffect(() => {
    let live = true
    api.libraries().then(r => { if (live) setLibs(r.libraries ?? []) }, () => {})
    return () => { live = false }
  }, [])
  // What was chosen for the buckets added before.
  const before = useMemo(() => libs.filter(l => l.bucket?.location.startsWith('pw://')).map(l => l.bucket!), [libs])
  const everyNow = every ?? before[before.length - 1]?.every ?? 1440
  // An endpoint is one region's: what was said for this one, else what the
  // last bucket there was given.
  const endpointOf = (b?: PwBucket) => before.filter(x => x.csp === b?.csp && x.region === b?.region && x.endpoint).pop()?.endpoint ?? ''
  const endpointNow = endpoint ?? endpointOf(bucket)
  const several = more.length > 0
  const others = buckets.filter(b => b.uri !== uri)
  const added = !several && libraryAt(libs, uri, dir)

  const look = useCallback(async (path: string) => {
    holdHeight(); setLooking(true); setLookError('')
    try {
      const f = await api.pwBucketFolder(uri, path, endpointNow)
      setFolder(f); setDir(f.path)
    } catch (e) { setLookError(say(e)) } finally { setLooking(false) }
  }, [uri, endpointNow, holdHeight])

  const add = async () => {
    holdHeight(); setBusy(true); setErrors([])
    const done: { uri: string; lib: SettingsLibrary }[] = []
    const failed: { uri: string; name: string; message: string }[] = []
    const asked = [uri, ...more]
    // One after another: the platform is asked for one credential at a time.
    for (const u of asked) {
      const b = buckets.find(x => x.uri === u)
      const at = b?.csp === bucket?.csp && b?.region === bucket?.region ? endpointNow.trim() : endpointOf(b)
      try {
        const r = await postJson<{ library: SettingsLibrary }>('/api/libraries/bucket', {
          bucket: u, every: everyNow, endpoint: at || undefined,
          ...(several ? {} : { path: dir, label: label.trim() || undefined }),
        })
        done.push({ uri: u, lib: r.library })
      } catch (e) {
        failed.push({ uri: u, name: b?.label ?? u, message: say(e) })
      }
    }
    setBusy(false)
    if (!failed.length) {
      onAdded(several ? { id: done[0].lib.id, label: plural(done.length, 'bucket'), bucket: true } : done[0].lib, done.length)
      return
    }
    // What was added stays added; the rest is left to try again.
    setErrors(failed)
    if (done.length) {
      toast.success(`Listing ${done.map(d => d.lib.label).join(', ')}.`)
      setLibs(l => [...l, ...done.map(d => d.lib)])
      setMore(m => m.filter(u => !done.some(d => d.uri === u)))
      reloadAppConfig()
      reloadWhenLibrariesBuilt()
    }
  }

  if (!bucket) return null
  if (bucket.why) return <Callout tone="info" title={`${bucket.label} cannot be indexed here`}>{bucket.why}</Callout>
  const count = 1 + more.length
  return (
    <div className="copy-library-form pw-bucket-form">
      <div className="lib-review" role="group" aria-label="Review">
        {!several && (
          <Field label="Folder" layout="inline" size="sm" group>
            <div className="copy-library-folder">
              <div className="copy-library-path">
                {dir ? <code>{dir}</code> : <span>The whole bucket</span>}
                {!folder && <Button size="sm" variant="ghost" loading={looking} onClick={() => void look('')}>Choose a folder</Button>}
                {folder && folder.parent !== null && <Button size="sm" variant="ghost" disabled={looking} onClick={() => void look(folder.parent ?? '')}>Up</Button>}
                {folder && <Button size="sm" variant="ghost" onClick={() => { holdHeight(); setFolder(null); setDir('') }}>Use the whole bucket</Button>}
              </div>
              {looking && folder && <Loader inline size={16} label="Listing…" />}
              {folder && !looking && (folder.dirs.length
                ? (
                  <ul className="copy-library-dirs" aria-label="Folders in it">
                    {folder.dirs.map(d => <li key={d}><Button size="sm" variant="ghost" onClick={() => void look(dir ? `${dir}/${d}` : d)}>{d}/</Button></li>)}
                  </ul>
                )
                : <p className="muted">No folders in it{folder.objects ? `; ${folder.more ? 'over ' : ''}${plural(folder.objects, 'object')}` : ''}.</p>)}
              {folder?.more && <p className="muted">There is more in it than is shown.</p>}
              {lookError && <Callout tone="error" size="sm">{lookError}</Callout>}
            </div>
          </Field>
        )}
        {!several && (
          <Field label="Name" layout="inline" size="sm">
            <Input size="sm" value={label} placeholder={dir.split('/').pop() || bucket.label} onChange={e => setLabel(e.target.value)} />
          </Field>
        )}
        {others.length > 0 && (
          <Field label="Add as well" layout="inline" size="sm" group
            description={several ? 'Each is added whole, as a library of its own with the bucket\'s name.' : 'Tick more buckets to add them in one go, each a library of its own.'}>
            <ul className="pw-bucket-list" aria-label="More buckets to add">
              {others.map(b => {
                const have = libraryAt(libs, b.uri)
                return (
                  <li key={b.uri}>
                    <label className="lib-add-my-line" title={b.why || b.uri}>
                      <Checkbox aria-label={`Add ${b.label}`} checked={more.includes(b.uri)} disabled={busy || !!b.why || !!have}
                        onCheckedChange={v => { holdHeight(); setMore(m => (v ? [...m, b.uri] : m.filter(u => u !== b.uri))) }} />
                      <span><span className="pw-bucket-name">{b.label}</span> <span className="muted">{have ? 'Added already' : b.why ? b.why : `${b.namespace} · ${pwBucketPlace(b)}`}</span></span>
                    </label>
                  </li>
                )
              })}
            </ul>
          </Field>
        )}
        <Field label="List again" layout="inline" size="sm">
          <Select<number> aria-label="List again" size="sm" width="content" items={BUCKET_EVERY} value={everyNow} onValueChange={v => setEvery(v ?? 1440)} />
        </Field>
        <div className="lib-more">
          <Button variant="ghost" size="sm" aria-expanded={options} onClick={() => { holdHeight(); setOptions(o => !o) }}>{options ? 'Fewer options' : 'More options'}</Button>
          {options && (
            <div className="lib-more-body"><div className="lib-more-fields">
              <Input label="Endpoint" hint="optional" size="sm" mono spellCheck={false} autoComplete="off" placeholder="the store's own" value={endpointNow}
                description={`Another address to ask the store at, where this Studio reaches it only through one: a VPC endpoint, such as https://bucket.vpce-0example.s3.us-gov-west-1.vpce.amazonaws.com.${several ? ` For the buckets in ${pwBucketPlace(bucket)}.` : ''}`}
                onChange={e => setEndpoint(e.target.value)} />
            </div></div>
          )}
        </div>
      </div>
      <p className="muted copy-library-note">
        {several ? 'Each is' : 'It is'} listed and read with your own ACTIVATE sign-in{signIn ? ', and only you see it here' : ''}. {several ? 'A listing' : 'The listing'} takes
        about one request per 1,000 objects{bucket.csp === 'azure' ? ', or one for each folder in Azure' : ''} and indexes each object&apos;s name, size and upload time.
        {' '}No object is read to index it. {bucket.csp === 'aws' ? 'On AWS a listing costs about $0.005 per 1,000 requests.' : 'The store\'s own request prices apply.'}
        {' '}Opening a file reads that object alone: its first 512 KB for a text preview, the whole object up to 50 MB for a download or a document preview.
      </p>
      {added && <Callout tone="info" size="sm">That is already a library of yours, {added.label}. Choose a folder in it, or another bucket.</Callout>}
      {errors.map(e => <Callout key={e.name} tone="error" live>{several || errors.length > 1 ? `${e.name}: ` : ''}{e.message}</Callout>)}
      <div className="query-actions">
        <Button variant="primary" loading={busy} disabled={busy || !!added} onClick={() => void add()}>{several ? `List and index ${count} buckets` : 'List and index'}</Button>
      </div>
    </div>
  )
}
