import { useEffect, useState } from 'react'
import { Button, Callout, Input } from '@parallelworks/ui'
import { api, type IndexDepth, type SettingsLibrary } from '../api'
import { DEFAULT_LIBRARY_DEPTH, IndexDepthControl, meaningLastNote, useOcrHere } from './IndexDepth'
import { CreateConfirm, postJson, StepList, type CheckStep } from './LibraryFormParts'
import { FolderBrowser } from './PrimaryFolder'

const EMPTY = { indexRoot: '', sourceRoot: '', label: '', id: '' }

interface BuildCheck { ok: boolean; steps: CheckStep[]; create: string[] }

/** A folder offered as a library (GET /api/libraries/suggestions). */
export interface FolderSuggestion {
  id: string
  label: string
  path: string
  note: string
  /** 'full-disk-access': macOS lets the app read it only with Full Disk Access. */
  access: 'ok' | 'full-disk-access'
}

/** The suggested folders, or none where the server offers none. */
export function useFolderSuggestions(on: boolean): [FolderSuggestion[], () => void] {
  const [list, setList] = useState<FolderSuggestion[]>([])
  const [n, setN] = useState(0)
  useEffect(() => {
    if (!on) return
    let alive = true
    fetch('/api/libraries/suggestions')
      .then(r => (r.ok ? r.json() : { suggestions: [] }))
      .then((d: { suggestions?: FolderSuggestion[] }) => { if (alive) setList(d.suggestions ?? []) })
      .catch(() => { if (alive) setList([]) })
    return () => { alive = false }
  }, [on, n])
  return [list, () => setN(x => x + 1)]
}

/**
 * A library on the machine the Studio runs on. By default (`mode` connect)
 * an existing GUFI index, read only (POST /api/libraries): the server
 * checks the directory first and refuses one that is not a GUFI index,
 * with the reason; without an identifier it makes one from the label.
 *
 * With `mode` build, a folder the Studio indexes itself and keeps in step
 * with (POST /api/libraries/build): Check reports GUFI and the folders,
 * including any that do not exist yet, and Build the index asks before
 * making those. The index folder is optional; without it the Studio keeps
 * the index in its own data folder. The index depth (IndexDepth) shows
 * under the folder, Text + OCR to begin with.
 *
 * With `browse` (a single-user install) either folder can be picked from
 * the folders on this computer; `compact` leaves out the introduction, for
 * a page that gives its own. Only the folder that has to be given shows at
 * first; the optional ones are under More options.
 */
export function LocalLibraryForm({ onAdded, browse = false, compact = false, mode = 'connect' }: {
  onAdded?: (lib: SettingsLibrary) => void
  browse?: boolean
  compact?: boolean
  mode?: 'connect' | 'build'
}) {
  const build = mode === 'build'
  const [form, setForm] = useState(EMPTY)
  const [busy, setBusy] = useState<'check' | 'add' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [browsing, setBrowsing] = useState<'indexRoot' | 'sourceRoot' | null>(null)
  const [check, setCheck] = useState<BuildCheck | null>(null)
  const [checked, setChecked] = useState('')
  const [confirming, setConfirming] = useState(false)
  const [more, setMore] = useState(false)
  const [depth, setDepth] = useState<IndexDepth>(DEFAULT_LIBRARY_DEPTH)
  const ocr = useOcrHere(build)
  const [suggestions, reloadSuggestions] = useFolderSuggestions(build && browse)
  const [picked, setPicked] = useState<FolderSuggestion | null>(null)
  const set = (patch: Partial<typeof EMPTY>) => { setForm(f => ({ ...f, ...patch })); setError(null) }
  const indexName = form.indexRoot.trim().replace(/\/+$/, '').split('/').pop() || ''
  const sourceName = form.sourceRoot.trim().replace(/\/+$/, '').split('/').pop() || ''
  const fields = { sourceRoot: form.sourceRoot.trim(), indexRoot: form.indexRoot.trim() || undefined, label: form.label.trim() || undefined, id: form.id.trim() || undefined }
  const passed = !!check?.ok && checked === JSON.stringify(fields)

  const add = async () => {
    setBusy('add')
    setError(null)
    try {
      const r = await api.addLibrary({
        id: form.id.trim(), label: form.label.trim() || undefined,
        indexRoot: form.indexRoot.trim(), sourceRoot: form.sourceRoot.trim() || undefined,
      })
      setForm(EMPTY)
      onAdded?.(r.library)
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(null) }
  }

  const runCheck = async (f = fields) => {
    setBusy('check'); setError(null); setCheck(null)
    try {
      const r = await postJson<BuildCheck>('/api/libraries/build/check', { ...f, depth })
      setCheck(r)
      setChecked(JSON.stringify(f))
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(null) }
  }

  const startBuild = async () => {
    setBusy('add'); setError(null)
    try {
      const r = await postJson<{ library: SettingsLibrary }>('/api/libraries/build', { ...fields, depth, create: !!check?.create.length })
      setForm(EMPTY); setCheck(null); setChecked(''); setPicked(null)
      reloadSuggestions()
      onAdded?.(r.library)
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(null) }
  }

  // A suggested folder fills the folder and label and checks it at once,
  // which leaves Build the index.
  const pick = (sg: FolderSuggestion) => {
    const next = { ...form, sourceRoot: sg.path, label: sg.label }
    setForm(next); setPicked(sg); setError(null)
    void runCheck({ sourceRoot: next.sourceRoot, indexRoot: next.indexRoot.trim() || undefined, label: next.label, id: next.id.trim() || undefined })
  }
  const openFullDiskAccess = () => {
    fetch('/api/libraries/suggestions/full-disk-access', { method: 'POST' })
      .then(r => (r.ok ? null : r.json().then((d: { error?: string }) => setError(d.error ?? `${r.status}`))))
      .catch(e => setError(String((e as Error).message ?? e)))
  }

  const browseButton = (field: 'indexRoot' | 'sourceRoot', what: string) => browse && (
    <Button size="sm" variant="ghost" aria-expanded={browsing === field} onClick={() => setBrowsing(b => (b === field ? null : field))}>
      {browsing === field ? 'Close the folder list' : `Browse for the ${what}…`}
    </Button>
  )

  return (
    <div className="local-library-form">
      {!compact && (
        <p className="muted view-sub">
          {build
            ? 'A folder on this machine, indexed with GUFI here and kept in step with the folder. Read only in the Studio.'
            : 'An index already built on this machine with GUFI. Read only: the Studio searches it and never writes to it.'}
        </p>
      )}
      {build && suggestions.length > 0 && (
        <div className="lib-suggest" role="group" aria-label="Suggested folders">
          <span className="muted lib-suggest-label">Suggested:</span>
          {suggestions.map(sg => (
            <Button key={sg.id} size="sm" variant={picked?.id === sg.id && form.sourceRoot === sg.path ? 'secondary' : 'outline'}
              title={sg.path} disabled={busy !== null} onClick={() => pick(sg)}>
              {sg.label}
            </Button>
          ))}
        </div>
      )}
      {build && picked && form.sourceRoot === picked.path && picked.note && <p className="muted lib-suggest-note">{picked.note}</p>}
      {build && picked && form.sourceRoot === picked.path && picked.access === 'full-disk-access' && (
        <Callout tone="warning" title="Needs Full Disk Access">
          macOS lets ACTIVATE Studio read {picked.label} only with Full Disk Access. Turn it on for ACTIVATE Studio in System Settings,
          Privacy &amp; Security, Full Disk Access, then press Check again.{' '}
          <Button size="sm" variant="outline" onClick={openFullDiskAccess}>Open System Settings</Button>
        </Callout>
      )}
      <div className="local-library-main">
        {build ? (
          <Input label="Folder to index" mono placeholder="~/Documents/papers" spellCheck={false} value={form.sourceRoot}
            description="The folder to index. Created, empty, if it does not exist." onChange={e => set({ sourceRoot: e.target.value })} />
        ) : (
          <Input label="Index directory" mono placeholder="/path/to/index" spellCheck={false} value={form.indexRoot}
            description="The top of the GUFI index: the directory with db.db." onChange={e => set({ indexRoot: e.target.value })} />
        )}
        {build ? browseButton('sourceRoot', 'folder') : browseButton('indexRoot', 'index')}
      </div>
      {build && <IndexDepthControl value={depth} onChange={setDepth} disabled={busy !== null} ocr={ocr} note={meaningLastNote} />}
      <Button variant="ghost" size="sm" className="local-library-more" aria-expanded={more} onClick={() => setMore(m => !m)}>
        {more ? 'Fewer options' : 'More options'}
      </Button>
      {more && (
        <div className="settings-grid">
          {build ? (
            <div>
              <Input label="Index folder" hint="optional" mono placeholder="in the Studio's own data folder" spellCheck={false} value={form.indexRoot}
                description="Where the index is written: an empty or new folder, outside the source folder." onChange={e => set({ indexRoot: e.target.value })} />
              {browseButton('indexRoot', 'index folder')}
            </div>
          ) : (
            <div>
              <Input label="Source directory" hint="optional" mono placeholder="/path/to/files" spellCheck={false} value={form.sourceRoot}
                description="The indexed files, to open them. Without it, names and metadata only." onChange={e => set({ sourceRoot: e.target.value })} />
              {browseButton('sourceRoot', 'files')}
            </div>
          )}
          <Input label="Label" hint="optional" placeholder={(build ? sourceName : indexName) || 'Shown in the Library switcher'} value={form.label}
            onChange={e => set({ label: e.target.value })} />
          <Input label="Identifier" hint="optional" placeholder="made from the label" spellCheck={false} value={form.id}
            description="Lowercase letters, digits, dashes. Names the library's folder under All libraries."
            onChange={e => set({ id: e.target.value })} />
        </div>
      )}
      {browsing && (
        <div className="local-library-browse">
          <FolderBrowser key={browsing} start={form[browsing].trim() || '~'}
            pickLabel={build
              ? (browsing === 'indexRoot' ? 'Use as the index folder' : 'Use as the source folder')
              : (browsing === 'indexRoot' ? 'Use as the index directory' : 'Use as the source directory')}
            onPick={p => { set({ [browsing]: p }); setBrowsing(null) }} />
        </div>
      )}
      {build && check && <StepList steps={check.steps} label="Check before the build" />}
      {error && <Callout tone="error" live>{error}</Callout>}
      <div className="query-actions">
        {build ? (
          <>
            <Button variant="secondary" loading={busy === 'check'} disabled={!form.sourceRoot.trim() || busy !== null} onClick={() => void runCheck()}>Check</Button>
            <Button variant="primary" loading={busy === 'add'} disabled={!passed || busy !== null}
              onClick={() => (check?.create.length ? setConfirming(true) : void startBuild())}>
              Build the index
            </Button>
            {check && !passed && !busy && <span className="muted">{check.ok ? 'Fields changed: check again to build.' : 'Fix the failing step and check again.'}</span>}
          </>
        ) : (
          <Button variant="primary" loading={busy === 'add'} disabled={!form.indexRoot.trim() || busy !== null} onClick={() => void add()}>
            {busy === 'add' ? 'Checking…' : 'Add library'}
          </Button>
        )}
      </div>
      {build && (
        <CreateConfirm paths={check?.create ?? []} where="on this computer" open={confirming} onClose={() => setConfirming(false)}
          onConfirm={async () => { setConfirming(false); await startBuild() }} />
      )}
    </div>
  )
}
