import { useEffect, useState } from 'react'
import {
  Button, Callout, Field, Loader, Modal, NumberInput, Select, type SelectItem, Switch, Table,
} from '@parallelworks/ui'
import { api, type IndexSettings, type IndexSettingsInfo } from '../api'
import { useIndexStatus } from '../indexActivity'
import { IndexControls, IndexJobView, LastBuild, useIndexControls } from './IndexActivity'
import { FolderBrowser, errText, waitForRestart } from './PrimaryFolder'
import { MeaningLevelControl } from './MeaningLevel'

/**
 * Settings, General, Indexing (below the sync settings): how the main
 * folder's index is built. The threads every phase uses, which readers run
 * (documents, the PDF fallback, LibreOffice for older Office files, OCR,
 * image descriptions when AI features are on), the main folder's level of search by meaning,
 * and where the index is kept. Each setting saves as it changes and applies
 * to the next build or sync. The vision model is chosen under AI features;
 * this says which one describes images.
 */

const PDF_READERS: SelectItem<IndexSettings['pdfReader']>[] = [
  { value: 'auto', label: 'pdftotext where installed, then pypdf' },
  { value: 'pdftotext', label: 'pdftotext only' },
  { value: 'pypdf', label: 'pypdf only' },
]

export function IndexingSection({ visionModel = '', deploymentCred = null }: {
  /** The vision model chosen under AI features, as the General form has it. */
  visionModel?: string
  /** Whether the deployment has a credential of its own for background calls. */
  deploymentCred?: boolean | null
}) {
  const [info, setInfo] = useState<IndexSettingsInfo | null>(null)
  const [error, setError] = useState('')
  const [saving, setSaving] = useState<string | null>(null)
  const [threads, setThreads] = useState<number | null>(null)
  const [moving, setMoving] = useState(false)
  const [moveNote, setMoveNote] = useState('')
  const [restarting, setRestarting] = useState(false)
  const status = useIndexStatus()
  const controls = useIndexControls()

  const load = () => api.indexSettings().then(i => {
    // A server without these routes (an older one) answers something else.
    if (!i?.settings || !i.readers || !i.location) throw new Error('This server does not offer indexing settings.')
    setInfo(i); setThreads(i.settings.threads); setError('')
  }).catch(e => setError(errText(e)))
  useEffect(() => { void load() }, [])

  const save = async (patch: Partial<IndexSettings>, key: string) => {
    if (!info) return
    setSaving(key)
    setError('')
    try {
      const r = await api.saveIndexSettings(patch)
      setInfo({ ...info, settings: r.settings, threads: r.threads, readers: r.readers })
    } catch (e) { setError(errText(e)) } finally { setSaving(null) }
  }
  const move = async (path: string) => {
    setMoveNote('')
    try {
      const before = await fetch('/api/version').then(r => r.json()).then((v: { startedAt?: string }) => v.startedAt ?? '').catch(() => '')
      const r = await api.moveIndex(path)
      if (r.restarting) {
        setRestarting(true)
        const back = await waitForRestart(before, 1000, () => true)
        setRestarting(false)
        if (back) { location.reload(); return }
        setMoveNote('The Studio did not come back after the move. Open it again from the app.')
      } else {
        setMoveNote(r.note ?? '')
        setMoving(false)
        await load()
      }
    } catch (e) { setMoveNote(errText(e)) }
  }

  if (!info) {
    return (
      <>
        <h2 className="ix-h2">How the index is built</h2>
        {error ? <Callout tone="error">{error}</Callout> : <Loader inline size={16} label="Loading…" />}
      </>
    )
  }
  const vision = visionModel || info.ai?.visionModel || ''
  const s = info.settings
  const reader = (name: string) => info.readers.extractors.find(x => x.name === name)
  const soffice = reader('LibreOffice')
  const tesseract = reader('tesseract')
  const running = !!status?.running

  return (
    <>
      <h2 className="ix-h2">How the index is built</h2>
      <p className="muted view-sub">
        The main folder&apos;s index. Each setting here saves as you change it and applies to the next build or sync;
        Update index applies it now.
      </p>
      {error && <Callout tone="error">{error}</Callout>}

      <section className="settings-field ix-section" aria-label="Now">
        {status?.job
          ? <IndexJobView job={status.job} onStop={() => void controls.stop()} stopping={controls.busy === 'stop'} />
          : <LastBuild status={status} />}
        <IndexControls canDelete={info.single} canBuild={info.single} folder={info.location.folder} />
      </section>

      <div className="settings-grid">
        <Field label="Threads"
          description={`Each phase runs this many workers at once. Empty uses one per processor core: ${info.cpus} here.`}>
          <div className="ix-inline">
            <NumberInput min={1} max={256} value={threads ?? undefined} placeholder={String(info.cpus)} aria-label="Threads"
              onValueChange={v => setThreads(v ?? null)} />
            <Button variant="secondary" loading={saving === 'threads'} disabled={threads === s.threads}
              onClick={() => void save({ threads }, 'threads')}>Save</Button>
          </div>
        </Field>
      </div>

      <h2 className="ix-h2">What is read from files</h2>
      <div className="ix-switches">
        <label className="ix-switch">
          <Switch checked={s.documents} disabled={saving !== null} onCheckedChange={v => void save({ documents: v }, 'documents')} />
          <span>
            <span className="ix-switch-label">Text of PDFs and Office documents</span>
            <span className="muted">Converted by downmark, which comes with the Studio. Off, they are found by name only.</span>
          </span>
        </label>
        <Field label="PDFs downmark cannot read"
          description={['pdftotext', 'pypdf'].map(n => `${n}: ${reader(n)?.note.replace(/^./, c => c.toLowerCase()) ?? 'unknown'}`).join(' ')}>
          <Select items={PDF_READERS} value={s.pdfReader} disabled={!s.documents || saving !== null}
            onValueChange={v => v && void save({ pdfReader: v }, 'pdfReader')} />
        </Field>
        <label className="ix-switch">
          <Switch checked={s.legacyOffice} disabled={!s.documents || saving !== null} onCheckedChange={v => void save({ legacyOffice: v }, 'legacyOffice')} />
          <span>
            <span className="ix-switch-label">Older Office files (.ppt, .xls) and OpenDocument, through LibreOffice</span>
            <span className="muted">{soffice?.note}</span>
          </span>
        </label>
        <label className="ix-switch">
          <Switch checked={s.ocr} disabled={saving !== null} onCheckedChange={v => void save({ ocr: v }, 'ocr')} />
          <span>
            <span className="ix-switch-label">Text in images and scanned pages (OCR)</span>
            <span className="muted">{tesseract?.note}</span>
          </span>
        </label>
        {info.ai?.enabled && (
          <>
            <label className="ix-switch">
              <Switch checked={s.imageDescriptions} disabled={saving !== null} onCheckedChange={v => void save({ imageDescriptions: v }, 'imageDescriptions')} />
              <span>
                <span className="ix-switch-label">Describe images with a vision model</span>
                <span className="muted">Each image is sent to the model below, once; the description is searchable.</span>
              </span>
            </label>
            <p className="ix-note ix-indent">
              {vision ? <>Images are described by <code>{vision}</code>, chosen under <a href="#view=settings:ai">AI features</a>.</>
                : <>No vision model is chosen; choose one under <a href="#view=settings:ai">AI features</a>.</>}
              {vision && deploymentCred === false && ' Descriptions are made during background indexing, with no user present, so they need the deployment credential or the pw CLI login on this computer; there is neither, so images are read by OCR and name only.'}
            </p>
          </>
        )}
        <div className="ix-meaning">
          <MeaningLevelControl label="Search by meaning in the main folder" value={s.meaning} disabled={saving !== null}
            onChange={v => void save({ meaning: v }, 'meaning')} />
          <p className="ix-note">
            Made after the text, as the last phase; search by name and words works before it ends. Each other library has its
            own level, under <a href="#view=settings:libraries">Settings, Libraries</a>.
          </p>
        </div>
      </div>

      <h2 className="ix-h2">What this computer can read</h2>
      <Table density="compact" wrapperClassName="ix-formats">
        <Table.Header>Files</Table.Header>
        <Table.Header>Read by</Table.Header>
        <Table.Header>Note</Table.Header>
        {info.readers.formats.map(f => (
          <tr key={f.formats}>
            <Table.Item>{f.formats}</Table.Item>
            <Table.Item>{f.readers.length ? f.readers.join(', ') : <span className="lib-no">name only</span>}</Table.Item>
            <Table.Item wrap className="muted">{f.note}</Table.Item>
          </tr>
        ))}
      </Table>

      <h2 className="ix-h2">Where the index is kept</h2>
      <div className="settings-field">
        <p className="ix-path"><code>{info.location.current}</code></p>
        <p className="muted view-sub">
          Separate from the folder being indexed ({info.location.folder}), whose files are never written to by indexing.
          {info.location.current !== info.location.default && ` The default is ${info.location.default}.`}
        </p>
        {info.location.canChange ? (
          <div className="ix-actions">
            <Button variant="secondary" disabled={running} onClick={() => { setMoveNote(''); setMoving(true) }}>Choose another place…</Button>
            {info.location.current !== info.location.default && (
              <Button variant="ghost" disabled={running} onClick={() => void move('')}>Back to the default</Button>
            )}
            {running && <span className="muted">Wait for indexing to finish, or stop it, to move the index.</span>}
          </div>
        ) : <p className="muted">Set STUDIO_INDEX_DIR in the Studio&apos;s environment to keep it elsewhere.</p>}
        {moveNote && !moving && <Callout tone="info">{moveNote}</Callout>}
      </div>

      <Modal open={moving} onClose={() => { if (!restarting) setMoving(false) }} dismissible={!restarting} size="lg"
        title="Keep the index somewhere else"
        description="The index and its caches move to the folder you choose, and the Studio restarts there. Not inside the folder being indexed."
        footer={<Button variant="secondary" disabled={restarting} onClick={() => setMoving(false)}>Close</Button>}>
        {restarting
          ? <Loader inline size={16} label="Moving the index and restarting…" />
          : (
            <>
              <FolderBrowser start={info.location.current} pickLabel="Keep the index here" onPick={p => void move(p)} />
              {moveNote && <Callout tone="error">{moveNote}</Callout>}
            </>
          )}
      </Modal>
    </>
  )
}
