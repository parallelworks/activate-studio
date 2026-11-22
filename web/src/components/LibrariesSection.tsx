import { type ReactNode, useEffect, useRef, useState } from 'react'
import {
  ActionMenu, Button, Callout, Card, CopyButton, IconButton, Loader, type MenuEntry, Modal, ProgressBar, Select, StatusBadge, toast, useConfirm,
} from '@parallelworks/ui'
import { AddIcon, CloseIcon, MoreIcon } from '@parallelworks/ui/icons'
import { BUCKET_EVERY, COPY_EVERY, api, bucketWhere, copyWhere, listingCost, textCost, type BucketTextEstimate, type BucketTextPass, type BuildProgress, type IndexDepth, type ReadThrough, type SettingsLibrary, type UserLibrary } from '../api'
import { IndexDepthSelect, UNSTORED_LIBRARY_DEPTH, deeper, depthLabel } from './IndexDepth'
import { reloadAppConfig, useAppConfig } from '../config'
import { reloadWhenLibrariesBuilt } from '../libraryWatch'
import { libraryAccess } from './LibraryScope'
import { AddLibrary } from './AddLibrary'
import { LibraryExport } from './LibraryExport'
import { LibraryImport } from './LibraryImport'
import { CLUSTER_STARTED, IMPORT_ABOUT, addOptions, addedMessage } from './LibraryAddDialogs'
import { FolderPicker, errText, json, type SetupStatus } from './PrimaryFolder'
import { DeleteIndexDialog, IndexControls, IndexJobView, IndexLogButton, LastBuild, useIndexControls } from './IndexActivity'
import { useIndexStatus } from '../indexActivity'
import { FOCUS_LIBRARY_EVENT, FOCUS_LIBRARY_KEY } from './OmittedLine'
import { ReadThroughControl } from './ReadThroughControl'
import { bytesText } from '../transferApi'
import { plural } from '../plural'

/** A user library as listed, with how it is reached. */
type UserLib = UserLibrary
interface UserList { libraries: UserLib[]; available: boolean; ssh: boolean; pw: boolean; pwOffered: boolean; workflow: boolean; space?: boolean; home?: string }

/** The kinds of library, in the words the setup wizard's Other libraries
 *  step uses for the forms it shares with this page. */
export type LibraryKind = 'local' | 'ssh' | 'pw' | 'cluster'
export const KINDS: Record<LibraryKind, { label: string; description: string }> = {
  local: { label: 'A GUFI index on this machine', description: 'An index already built here. Read only.' },
  ssh: { label: 'A GUFI index on a remote computer over ssh', description: 'Read with your own ssh keys. Nothing is written there.' },
  pw: { label: 'A GUFI index on a remote computer, over pw ssh', description: 'An index already on one of the remote computers the pw CLI reaches, read over pw ssh. Nothing is written there.' },
  cluster: { label: 'An index built on a cluster', description: 'Built on one of your clusters with your own key. Only you see it.' },
}

const isBuilding = (state: string | undefined) => state === 'building' || state === 'updating'

/** The note on the row of a library built on a remote system whose depth its
 *  index does not have yet. A library with no recorded depth has none: what
 *  its index holds is not known. */
function depthNote(l: UserLib): string | null {
  if (!l.depth || !l.builtDepth) return null
  if (isBuilding(l.state)) {
    if ((l.buildingDepth ?? l.builtDepth) !== l.depth) return 'Depth changed, applies at the next Rebuild'
    return l.builtDepth !== l.depth ? 'Depth changed, applies when this build finishes' : null
  }
  return l.builtDepth !== l.depth ? 'Depth changed, Rebuild to apply' : null
}

/** What a rebuild at a deeper depth costs. */
const DEPTH_TIME: Record<IndexDepth, string> = {
  names: '',
  text: 'Reading the files takes longer than indexing their names.',
  ocr: 'Reading images and scanned pages adds to the time.',
  'meaning-documents': 'Search by meaning is the slowest part of indexing, so it takes much longer than a build without it.',
  'meaning-full': 'Search by meaning is the slowest part of indexing, and on all files it takes about three times as long as on documents.',
}

/**
 * Under the row of a library built on a remote system, after its depth
 * changed: the choice to rebuild now or later, or, with a build running,
 * that the change waits for the next one. `from` is the depth its index has,
 * as far as is known.
 */
function DepthRebuild({ l, host, from, busy, onRebuild, onClose }: {
  l: UserLib; host: string; from: IndexDepth; busy: boolean; onRebuild: () => void; onClose: () => void
}) {
  const to = l.depth!
  const name = depthLabel(to)
  if (isBuilding(l.state)) {
    if (l.buildingDepth === to) return null
    return (
      <Callout tone="info" size="sm" live title={`${name} applies to the next build`} onDismiss={onClose}>
        A build is running on {host} and finishes at the depth it started with. Rebuild once it is done.
      </Callout>
    )
  }
  return (
    <Callout tone="info" size="sm" live title={`Rebuild at ${name}?`}>
      {deeper(to, from) || !l.indexRoot
        ? <>The index on {host} is built again at this depth. {l.indexRoot && 'The current index keeps answering searches until the new one replaces it. '}{DEPTH_TIME[to]}</>
        : <>Search keeps working without a rebuild. A rebuild drops what the index holds past {name}, which makes it smaller.</>}
      <div style={{ display: 'flex', gap: 6, marginTop: 8 }}>
        <Button variant="primary" size="sm" loading={busy} onClick={onRebuild}>Rebuild now</Button>
        <Button variant="secondary" size="sm" disabled={busy} onClick={onClose}>Later</Button>
      </div>
    </Callout>
  )
}

/** A build's progress under its row: the phase, and folders so far. */
function BuildLine({ progress }: { progress: BuildProgress | null | undefined }) {
  return (
    <div className="lib-build">
      <ProgressBar label={progress?.phase ?? 'Starting'} value={null} detail={progress?.current ?? undefined} />
    </div>
  )
}

type Tone = 'success' | 'warning' | 'error' | 'info'
/** One of a library's details: a label and its value. A path shows on one
 *  line, cut in the middle where it is too long, whole on hover, with Copy. */
interface Detail { label: string; value?: ReactNode; path?: string; host?: string; mono?: boolean }
interface Row {
  key: string
  name: string
  /** What it is, in a few words. Its access and where it is stand beside its name. */
  kind: string
  /** Its access as the Library switcher names it, for a library the app's
   *  list does not carry yet. */
  access: string
  /** Its state, with its counts and when it was last indexed (`facts`, on
   *  the state's line) and anything else to know (`notes`, under it). */
  state: { tone: Tone; text: string; pulse?: boolean; facts: ReactNode[]; notes: ReactNode[] }
  details: (Detail | null | false | undefined)[]
  /** What the index has: its full-text tables, and vectors for search by meaning. */
  fullText: boolean
  meaning: boolean
  /** For a library the Studio builds: its depth, which can be changed here,
   *  and when a change applies: at the sync, in the background, for one
   *  built here; at the next Rebuild for one built on a remote system. */
  depth?: { value: IndexDepth; reload: () => Promise<unknown>; applies: 'sync' | 'rebuild'; built?: IndexDepth | null; building?: boolean
    /** Why this viewer may not set it: one of the deployment's libraries on
     *  a shared Studio, set by its administrators. */
    locked?: string }
  /** The everyday actions, at the card's right: the one that keeps its
   *  index current first in every kind (Update index, Rebuild, List again,
   *  Check, Refresh). */
  actions: ReactNode
  /** What takes it away (Delete index, Remove), behind More; each asks first. */
  more: MenuEntry[]
  /** Under the details: a build's progress, a question about its depth. */
  detail?: ReactNode
  /** The card's quiet last line: how the last run went, and the index log. */
  footer?: ReactNode
}

const when = (iso: string | null) => (iso ? new Date(iso).toLocaleString() : '')


/** Whether a bucket library's objects' text is read, as offered. */
const TEXT_CHOICES: { value: 'off' | 'on'; label: string }[] = [
  { value: 'off', label: 'Off' }, { value: 'on', label: 'After each listing' },
]

/** What reading a bucket library's text would read and cost, for the
 *  question before it is turned on. */
function TextEstimate({ e }: { e: BucketTextEstimate }) {
  const { money, time } = textCost(e)
  const left = [
    e.over > 0 && `${plural(e.over, 'object')} over ${bytesText(e.maxBytes)} (${bytesText(e.overBytes)})`,
    e.other > 0 && `${plural(e.other, 'object')} of kinds no reader reads, such as images and data files`,
  ].filter(Boolean)
  return (
    <>
      <p>
        {e.objects > 0 ? <>Reads {plural(e.objects, 'object')}, {bytesText(e.bytes)}: text files and documents.</> : <>There is nothing new to read.</>}
        {e.done > 0 && <> {plural(e.done, 'object')} read before ({bytesText(e.doneBytes)}) are not read again.</>}
        {left.length > 0 && <> Left out: {left.join('; ')}.</>}
      </p>
      <p>{money}{time && <> {time}</>}</p>
      <p>After this, each listing reads only the objects that are new or changed. Turning it off stops reading; the text read so far stays searchable.</p>
    </>
  )
}

/** What a bucket library's last text pass did, in the row's notes. */
function textNotes(on: boolean, p: BucketTextPass | null | undefined): string[] {
  if (!p) return []
  const read = Object.values(p.read ?? {}).reduce((a, b) => a + b, 0)
  const out = [on
    ? `${plural(p.files, 'object')} with text; the last pass ${read ? `read ${plural(read, 'object')}, ${bytesText(p.bytes)} in ${plural(p.requests, 'request')}` : 'had nothing new to read'}, ${when(p.at)}`
    : `${plural(p.files, 'object')} keep the text read before; reading text is off`]
  const unread = (p.read?.failed ?? 0) + (p.read?.missing ?? 0) + (p.read?.changed ?? 0)
  if (unread > 0) out.push(`${plural(unread, 'object')} could not be read (gone, changed since the listing, or unreadable); see the index log`)
  if (p.stopped) out.push('the last pass was stopped; the next goes on from there')
  for (const m of p.missing ?? []) out.push(m)
  if (p.error) out.push(`the last pass failed: ${p.error}`)
  return out
}
/** A transfer's details on the Transfers page (TransfersView's transferHash). */
const transferHash = (id: string) => `#view=transfers?transfer=${encodeURIComponent(id)}`
/** Why a library the deployment set has no Remove, and where it is removed. */
const PINNED_NOTE = 'Named in the server\'s STUDIO_LIBRARIES setting; remove it there and restart the Studio.'
const many = (n: number, one: string, more: string) => `${n.toLocaleString()} ${n === 1 ? one : more}`
const counts = (dirs: number | null, files: number | null) =>
  dirs != null ? `${many(dirs, 'folder', 'folders')}, ${many(files ?? 0, 'file', 'files')}` : null

/**
 * Settings > Indexing > Libraries: one list of every library the Studio searches, and
 * one control to add another (AddLibrary). The main folder is the one the
 * Studio indexes itself and writes to; on a single-user install it can be
 * changed and its index rebuilt here. Every other library is read only: an
 * index the Studio builds of a folder on this machine, or a GUFI index
 * already there (/api/libraries); an index it builds on a remote system
 * over ssh or pw ssh, one already there, or one built on a cluster through
 * the platform (/api/user-libraries). Builds show their progress under
 * their row.
 */
export function LibrariesSection({ pollMs = 1000, heading = true }: {
  pollMs?: number
  /** Off where the page around it already names it, as a tab of Settings, Indexing does. */
  heading?: boolean
}) {
  const cfg = useAppConfig()
  const [libs, setLibs] = useState<SettingsLibrary[] | null>(null)
  const [loadError, setLoadError] = useState('')
  const [user, setUser] = useState<UserList>({ libraries: [], available: false, ssh: false, pw: false, pwOffered: false, workflow: false })
  const [canBuild, setCanBuild] = useState(false)
  const [status, setStatus] = useState<SetupStatus | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  // Opened by its address with the form already showing
  // (#view=settings:libraries?add=1, ADD_LIBRARY_HASH).
  const [adding, setAdding] = useState(() => /^#view=settings:libraries\?(?:.*&)?add=1(?:&|$)/.test(location.hash))
  const openedByLink = useRef(adding)
  const addForm = useRef<HTMLElement | null>(null)
  // Brought into view as the lists above it load, for the first moments
  // after the page opens: scrolled once at once, it was pushed down again by
  // the libraries arriving.
  const openedAt = useRef(Date.now())
  const [linked, setLinked] = useState(0)
  // That address while this page is already open (Settings visited
  // earlier, the section kept): the address changes, the page does not load.
  useEffect(() => {
    const onHash = () => {
      if (!/^#view=settings:libraries\?(?:.*&)?add=1(?:&|$)/.test(location.hash)) return
      openedByLink.current = true
      openedAt.current = Date.now()
      setAdding(true)
      setLinked(n => n + 1)
    }
    window.addEventListener('hashchange', onHash)
    return () => window.removeEventListener('hashchange', onHash)
  }, [])
  // Add a library opens its form under the list, below the fold on a
  // smaller window, so the form is brought into view as a link brings it.
  const openAdd = () => {
    openedByLink.current = true
    openedAt.current = Date.now()
    setAdding(true)
    setLinked(n => n + 1)
  }
  useEffect(() => {
    if (!openedByLink.current || libs === null || !addForm.current) return
    if (Date.now() - openedAt.current > 3000) { openedByLink.current = false; return }
    addForm.current.scrollIntoView?.({ block: 'start' })
  }, [libs, user, status, adding, linked])
  const [changing, setChanging] = useState(false)
  const [restarting, setRestarting] = useState(false)
  // Library lists: a file of libraries to import, and one to export.
  const [importing, setImporting] = useState(false)
  const [exporting, setExporting] = useState(false)
  // The library just given a depth its index does not have, and the depth
  // that index has: what the choice under its row is about.
  const [asked, setAsked] = useState<{ id: string; from: IndexDepth } | null>(null)
  const ready = useRef(new Set<string>())
  const readyHere = useRef<Set<string> | null>(null)

  const loadLibs = () => api.libraries().then(r => {
    setLibs(r.libraries); setCanBuild(!!r.canBuild); setLoadError('')
    // A library built here that just finished joins the switcher.
    const now = new Set(r.libraries.filter(l => l.build && !isBuilding(l.build.state) && l.caps.index).map(l => l.id))
    if (readyHere.current && [...now].some(id => !readyHere.current!.has(id))) reloadAppConfig()
    readyHere.current = now
  }).catch(e => setLoadError(errText(e)))
  // The list reloads while a build runs; one toast stands for a failure
  // that repeats rather than stacking another each time.
  const loadUser = () => Promise.all([api.userLibraries(), api.spaceStatus().then(s => ({ space: !!(s?.available || s?.cluster), home: s?.cluster ?? '' }), () => ({ space: false, home: '' }))]).then(([r, { space, home }]) => {
    const list = r as Partial<UserList>
    const libraries = list.libraries ?? []
    setUser({ libraries, available: !!list.available, ssh: !!list.ssh, pw: !!list.pw, pwOffered: !!(list.pwOffered ?? list.pw), workflow: !!list.workflow, space, home })
    // A library that just finished building joins the switcher.
    const now = new Set(libraries.filter(l => l.state === 'ready').map(l => l.id))
    if ([...now].some(id => !ready.current.has(id)) && ready.current.size + now.size > 0) reloadAppConfig()
    ready.current = now
  }).catch(e => toast.error(errText(e), { id: 'user-libraries-load' }))
  // The main folder's details on a single-user install; the file count walks
  // the index, so it follows in a second request.
  const loadStatus = async (): Promise<SetupStatus | null> => {
    const s = await json<SetupStatus>('/api/setup/status').catch(() => null)
    setStatus(s)
    if (s?.standalone && s.primary?.indexed) void json<SetupStatus>('/api/setup/status?files=1').then(setStatus).catch(() => {})
    return s
  }
  // The main folder's indexing, as every other view shows it.
  const ix = useIndexStatus()
  const indexing = !!ix?.running
  const wasIndexing = useRef(false)
  useEffect(() => {
    // A job just ended: the counts and what the index supports changed.
    if (wasIndexing.current && !indexing) { void Promise.all([loadLibs(), loadStatus()]).then(() => reloadAppConfig()) }
    wasIndexing.current = indexing
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [indexing])

  useEffect(() => {
    void loadLibs()
    void loadUser()
    void loadStatus()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  // A library named in a search's or a query's "left out" line: its row is
  // brought into view and marked for a few seconds.
  const [focused, setFocused] = useState<string | null>(null)
  const loaded = !!libs
  useEffect(() => {
    if (!loaded) return
    let timer = 0
    const focus = (id: string | null) => {
      try { sessionStorage.removeItem(FOCUS_LIBRARY_KEY) } catch { /* none */ }
      if (!id) return
      setFocused(id)
      window.setTimeout(() => document.querySelector(`[data-library="${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'center' }), 50)
      window.clearTimeout(timer)
      timer = window.setTimeout(() => setFocused(null), 4000)
    }
    let stored: string | null = null
    try { stored = sessionStorage.getItem(FOCUS_LIBRARY_KEY) } catch { /* none */ }
    focus(stored)
    const on = (e: Event) => focus(String((e as CustomEvent).detail ?? '') || null)
    window.addEventListener(FOCUS_LIBRARY_EVENT, on)
    return () => { window.removeEventListener(FOCUS_LIBRARY_EVENT, on); window.clearTimeout(timer) }
  }, [loaded])
  // Follow running builds: every few seconds for those the Studio runs
  // itself (here, or on a remote system over the session), every 15 s for
  // the platform's workflow runs.
  const followHere = (libs ?? []).some(l => isBuilding(l.build?.state) || l.copyOf?.looking || l.bucket?.reading)
  const followRemote = user.libraries.some(l => l.built && isBuilding(l.state))
  const followRuns = user.libraries.some(l => !l.built && isBuilding(l.state))
  useEffect(() => {
    if (!followHere && !followRemote && !followRuns) return
    const fast = followHere || followRemote
    const t = window.setInterval(() => {
      if (followHere) void loadLibs()
      if (followRemote || followRuns) void loadUser()
    }, fast ? Math.max(pollMs * 2, 2000) : 15_000)
    return () => window.clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [followHere, followRemote, followRuns])

  // Removing asks first, and says what goes and what stays. It is red, and
  // the dialog an alert, only where it deletes something: the index the
  // Studio built in a folder of its own.
  const { confirm, dialog } = useConfirm()
  // The main folder's Delete index, from its More menu.
  const mainControls = useIndexControls()
  const askRemove = (label: string, what: ReactNode, deletes: boolean, run: () => Promise<unknown>) => void confirm({
    title: `Remove ${label}?`, description: what, confirmLabel: 'Remove', destructive: deletes, onConfirm: run,
  })
  const act = async (key: string, fn: () => Promise<unknown>, done?: string) => {
    setBusy(key)
    try { await fn(); if (done) toast.success(done) } catch (e) { toast.error(errText(e)) } finally { setBusy(null) }
  }
  // Reading a bucket's text asks first, with what it would read and cost.
  const askText = (id: string, label: string) => void act(id, async () => {
    const e = await api.bucketTextEstimate(id)
    confirm({
      title: `Read the text of ${label}?`, description: <TextEstimate e={e} />,
      confirmLabel: e.objects > 0 ? `Read ${plural(e.objects, 'object')}` : 'Turn on',
      onConfirm: () => act(id, async () => { await api.setBucketText(id, true); await loadLibs() }, `Listing ${label}, then reading its text.`),
    })
  })
  const added = (message: string) => {
    toast.success(message)
    setAdding(false)
    void loadLibs()
    void loadUser()
    reloadAppConfig()
    reloadWhenLibrariesBuilt()
  }
  // After an import the dialog stays open with each entry's result.
  const imported = (count: number) => {
    if (!count) return
    void loadLibs()
    void loadUser()
    reloadAppConfig()
    reloadWhenLibrariesBuilt()
  }
  const capsOf = (id: string) => cfg.libraries?.find(l => l.id === id)?.caps
  // Another host that has a library's files, where its originals are also
  // read (the assistant's read_original, and the viewer). Shown where it is
  // set, and where it can be set: the ssh keys it uses are this account's,
  // which only a Studio with sign-in off lends.
  const throughRow = (id: string, label: string, current: ReadThrough | null | undefined, canEdit: boolean, reload: () => Promise<unknown>): Detail | null =>
    current || canEdit
      ? { label: 'Originals via', value: <ReadThroughControl id={id} label={label} current={current} canEdit={canEdit} onChanged={async () => { await reload() }} /> }
      : null
  // Remove, behind More: it asks first, and says what goes and what stays.
  const removeEntry = (label: string, disabled: boolean, deletes: boolean, onClick: () => void): MenuEntry => ({
    label: 'Remove', description: deletes ? 'Deletes its index; the files are not touched' : 'Stops searching it; its index stays',
    destructive: deletes, disabled, disabledReason: disabled ? 'Wait for the build or the action running now to finish.' : undefined, onClick,
    keywords: label,
  })

  if (loadError && !libs) {
    return (
      <>
        {heading && <h1>Libraries</h1>}
        <Callout tone="error" title="The libraries could not be listed" action={<Button size="sm" variant="secondary" onClick={() => void loadLibs()}>Try again</Button>}>
          {loadError}
        </Callout>
      </>
    )
  }

  const standalone = status?.standalone ? status : null
  const rows: Row[] = []
  // On a shared Studio the main library is read only for everyone but its
  // administrators, and so is how the deployment's libraries are indexed.
  const deploymentLocked = libs?.find(l => l.primary)?.readOnlyWhy
    ? 'On a Studio with sign-in, the deployment\'s libraries are indexed as its administrators set them.'
    : undefined
  for (const l of libs ?? []) {
    if (l.primary) {
      const p = standalone?.primary
      const facts: ReactNode[] = []
      const notes: ReactNode[] = []
      const job = ix?.job ?? null
      const indexed = ix?.indexed ?? (p ? p.indexed : l.caps.index)
      let state: Row['state']
      if (job) state = { tone: 'info', text: job.kind === 'delete' ? 'Deleting' : job.kind === 'move' ? 'Moving' : job.kind === 'build' ? 'Indexing' : 'Syncing', pulse: true, facts, notes }
      else if (p && !p.exists) state = { tone: 'error', text: 'Folder not found', facts, notes }
      else if (indexed) {
        // Ready, as every other library that can be searched says.
        state = ix?.incomplete ? { tone: 'warning', text: 'Incomplete', facts, notes } : { tone: 'success', text: 'Ready', facts, notes }
        if (p?.files !== undefined) facts.push(plural(p.files, 'file'))
      } else {
        state = { tone: 'warning', text: 'Not indexed yet', facts, notes }
        if (p?.earlierIndex) notes.push(`An index of this folder from ${p.earlierIndex.builtAt ? new Date(p.earlierIndex.builtAt).toLocaleDateString() : 'before'} is kept; building updates it.`)
      }
      const gufi = standalone?.gufi
      if (gufi && !gufi.found) notes.push(`GUFI was not found in ${gufi.bin}, so the index cannot be built.`)
      // A Studio that signs people in is shared: its deployment builds this
      // index, and removing it here would take search away from everyone.
      if (!standalone) notes.push('On a Studio that signs people in, the deployment builds and deletes this index.')
      if (l.readOnlyWhy) notes.push(l.readOnlyWhy)
      const canDelete = !!standalone && !!indexed && !ix?.running
      rows.push({
        key: l.id, name: l.label, kind: l.readOnlyWhy ? 'Main folder, read only for you' : 'Main folder', access: libraryAccess(l),
        details: [
          { label: 'Folder', path: l.sourceRoot ?? undefined },
          { label: 'Index', path: l.indexRoot },
        ],
        state, fullText: l.caps.fullText, meaning: l.caps.vectors,
        depth: { value: l.depth ?? 'meaning-documents', reload: loadLibs, applies: 'sync', locked: deploymentLocked },
        actions: (
          <>
            {standalone && <Button variant="secondary" size="sm" disabled={indexing} onClick={() => setChanging(true)}>Change folder</Button>}
            {/* A hosted Studio's deployment builds its index; this page shows it and its log. */}
            <IndexControls part="primary" canDelete={!!standalone} canBuild={!!standalone && !!gufi?.found && !!p?.exists} folder={l.sourceRoot} />
          </>
        ),
        // Delete index, on a computer of its own: never the folder's files.
        more: standalone
          ? [{ label: 'Delete index', description: 'The files in the folder are not touched', destructive: true, disabled: !canDelete,
            disabledReason: !indexed ? 'There is no index to delete.' : 'Wait for indexing to finish, or stop it.', onClick: () => mainControls.setConfirming(true) }]
          : [],
        // The build as it runs; how the last one went, with the log, at the foot.
        detail: job ? <IndexJobView job={job} /> : undefined,
        footer: <><LastBuild status={ix} /><IndexLogButton /></>,
      })
      continue
    }
    if (l.build && l.bucket) {
      const b = l.build
      const k = l.bucket
      const from = bucketWhere(k)
      const facts: ReactNode[] = [k.listedAt && `${k.objects.toLocaleString()} ${k.objects === 1 ? 'object' : 'objects'}, ${bytesText(k.bytes)}, listed ${when(k.listedAt)}`].filter(Boolean)
      const notes: ReactNode[] = [
        k.listedAt && listingCost(k),
        k.skipped > 0 && `${k.skipped.toLocaleString()} ${k.skipped === 1 ? 'entry' : 'entries'} could not be indexed (a path longer than GUFI holds, or a name "." or "..")`,
        k.error && `the last listing failed: ${k.error}`,
        b.error && b.error !== k.error && b.error,
        ...textNotes(k.text, k.textPass),
      ].filter(Boolean)
      const busyHere = busy !== null || isBuilding(b.state)
      rows.push({
        key: l.id, name: l.label, kind: `Index of ${from}, from its listing`, access: libraryAccess(l),
        details: [
          { label: 'Listed from', value: from, mono: true },
          { label: 'Index', path: l.indexRoot },
          { label: 'List again', value: (
            <Select<number> aria-label={`How often ${l.label} is listed again`} size="sm" items={BUCKET_EVERY} value={k.every}
              disabled={busy !== null}
              onValueChange={v => void act(l.id, async () => { await api.setBucketEvery(l.id, v); await loadLibs() })} />
          ) },
          { label: 'Read text', value: (
            <Select<'off' | 'on'> aria-label={`Whether the text of ${l.label}'s objects is read`} size="sm" items={TEXT_CHOICES} value={k.text ? 'on' : 'off'}
              disabled={busy !== null}
              onValueChange={v => v === 'on'
                ? askText(l.id, l.label)
                : void act(l.id, async () => { await api.setBucketText(l.id, false); await loadLibs() }, `No more text is read from ${from}; what was read stays searchable.`)} />
          ) },
        ],
        state: b.state === 'building' ? { tone: 'info', text: 'Listing and indexing', pulse: true, facts, notes }
          : b.state === 'updating' ? { tone: 'info', text: 'Listing again', pulse: true, facts, notes }
          : k.reading ? { tone: 'info', text: 'Reading text', pulse: true, facts, notes }
          : b.state === 'failed' ? { tone: 'error', text: 'Failed', facts, notes }
          : !l.caps.index ? { tone: 'error', text: 'Index not found', facts, notes: [...notes, 'List again makes it.'] }
          : k.error ? { tone: 'warning', text: 'Ready; the last listing failed', facts, notes }
          : { tone: 'success', text: 'Ready', facts, notes },
        fullText: l.caps.fullText, meaning: l.caps.vectors,
        actions: (
          <>
            {k.reading && (
              <Button variant="secondary" size="sm" disabled={busy !== null}
                onClick={() => void act(l.id, async () => { await api.stopBucketText(l.id); await loadLibs() }, 'Stopped; the next pass goes on from there.')}>Stop reading</Button>
            )}
            <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busy !== null || isBuilding(b.state) || k.reading}
              onClick={() => void act(l.id, async () => { await api.rebuildLibrary(l.id); await loadLibs() }, `Listing ${from} again.`)}>List again</Button>
          </>
        ),
        more: [removeEntry(l.label, busyHere, true, () => askRemove(l.label,
          <>Its index, which {cfg.appName} keeps in a folder of its own, is deleted. The objects in {from} are not touched.</>,
          true,
          () => act(l.id, async () => { await api.removeLibrary(l.id); await loadLibs(); reloadAppConfig() }, `Removed ${l.label} and its index.`)))],
        detail: isBuilding(b.state) || k.reading ? <BuildLine progress={b.progress} /> : undefined,
      })
      continue
    }
    if (l.build && l.copyOf) {
      const b = l.build
      const c = l.copyOf
      const from = copyWhere(c)
      const facts: ReactNode[] = [counts(b.dirs, b.files), c.checkedAt && `looked for changes ${when(c.checkedAt)}`].filter(Boolean)
      const notes: ReactNode[] = [
        c.changedAt && c.transfer && <>last brought {c.files} {c.files === 1 ? 'file' : 'files'}{c.deleted ? ` and removed ${c.deleted}` : ''} <a href={transferHash(c.transfer)}>{when(c.changedAt)}</a></>,
        c.error && `the last look failed: ${c.error}`,
        b.error,
      ].filter(Boolean)
      const looking = c.looking || !!c.running
      const busyHere = busy !== null || isBuilding(b.state) || looking
      rows.push({
        key: l.id, name: l.label, kind: `Copy of ${from}, kept in step with it`, access: libraryAccess(l),
        details: [
          { label: 'Copied from', value: from, mono: true },
          { label: 'Copy', path: l.sourceRoot ?? undefined },
          { label: 'Look for changes', value: (
            <Select<number> aria-label={`How often ${l.label} looks for changes`} size="sm" items={COPY_EVERY} value={c.every}
              disabled={busy !== null}
              onValueChange={v => void act(l.id, async () => { await api.setCopyEvery(l.id, v); await loadLibs() })} />
          ) },
        ],
        state: b.state === 'building' ? { tone: 'info', text: 'Copying and indexing', pulse: true, facts, notes }
          : b.state === 'updating' || c.running ? { tone: 'info', text: 'Updating', pulse: true, facts, notes }
          : looking ? { tone: 'info', text: 'Looking for changes', pulse: true, facts, notes }
          : b.state === 'failed' ? { tone: 'error', text: 'Failed', facts, notes }
          : !l.caps.index ? { tone: 'error', text: 'Index not found', facts, notes: [...notes, 'Rebuild makes it again.'] }
          : c.error || b.error ? { tone: 'warning', text: 'Ready; the last pass failed', facts, notes }
          : { tone: 'success', text: 'Ready', facts, notes },
        fullText: l.caps.fullText, meaning: l.caps.vectors,
        depth: { value: l.depth ?? UNSTORED_LIBRARY_DEPTH, reload: loadLibs, applies: 'sync', locked: deploymentLocked },
        actions: (
          <>
            <Button variant="secondary" size="sm" disabled={busyHere}
              onClick={() => void act(l.id, async () => { await api.rebuildLibrary(l.id); await loadLibs() }, `Rebuilding ${l.label}.`)}>Rebuild</Button>
            <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busyHere}
              onClick={() => void act(l.id, async () => { await api.updateCopy(l.id); await loadLibs() }, `Looking for changes at ${from}.`)}>Update now</Button>
          </>
        ),
        more: [removeEntry(l.label, busyHere, true, () => askRemove(l.label,
          <>Its copy and its index, which {cfg.appName} keeps in a folder of its own, are deleted. The files at {from} are not touched.</>,
          true,
          () => act(l.id, async () => { await api.removeLibrary(l.id); await loadLibs(); reloadAppConfig() }, `Removed ${l.label}, its copy and its index.`)))],
        detail: isBuilding(b.state) ? <BuildLine progress={b.progress} />
          : c.running ? <p className="muted">Bringing changes from {from}. <a href={transferHash(c.running)}>Open the transfer</a></p>
          : looking ? <p className="muted">Looking for changes at {from}.</p>
          : undefined,
      })
      continue
    }
    if (l.build) {
      const b = l.build
      const facts: ReactNode[] = [counts(b.dirs, b.files), b.builtAt && `indexed ${when(b.builtAt)}`].filter(Boolean)
      const notes: ReactNode[] = [b.error].filter(Boolean)
      const busyHere = busy !== null || isBuilding(b.state)
      rows.push({
        key: l.id, name: l.label, kind: 'Index of a folder on this machine, kept in step with it', access: libraryAccess(l),
        details: [
          { label: 'Folder', path: l.sourceRoot ?? undefined },
          { label: 'Index', path: l.indexRoot },
        ],
        state: b.state === 'building' ? { tone: 'info', text: 'Building', pulse: true, facts, notes }
          : b.state === 'updating' ? { tone: 'info', text: 'Updating', pulse: true, facts, notes }
          : b.state === 'failed' ? { tone: 'error', text: 'Failed', facts, notes }
          // Its index was removed outside the Studio: search names it as left out.
          : !l.caps.index ? { tone: 'error', text: 'Index not found', facts, notes: [...notes, 'Rebuild makes it again.'] }
          : b.error ? { tone: 'warning', text: 'Ready; the last pass failed', facts, notes }
          : { tone: 'success', text: 'Ready', facts, notes },
        fullText: l.caps.fullText, meaning: l.caps.vectors,
        depth: { value: l.depth ?? UNSTORED_LIBRARY_DEPTH, reload: loadLibs, applies: 'sync', locked: deploymentLocked },
        actions: (
          <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busyHere}
            onClick={() => void act(l.id, async () => { await api.rebuildLibrary(l.id); await loadLibs() }, `Rebuilding ${l.label}.`)}>Rebuild</Button>
        ),
        more: [removeEntry(l.label, busyHere, b.managed, () => askRemove(l.label, b.managed
          ? <>Its index, which {cfg.appName} keeps in a folder of its own, is deleted. The files in {l.sourceRoot ? <code>{l.sourceRoot}</code> : 'the folder'} are not touched.</>
          : <>{cfg.appName} stops searching it. Its index stays in <code>{b.indexDir}</code>, and the files in {l.sourceRoot ? <code>{l.sourceRoot}</code> : 'the folder'} are not touched.</>,
          b.managed,
          () => act(l.id, async () => { await api.removeLibrary(l.id); await loadLibs(); reloadAppConfig() },
            b.managed ? `Removed ${l.label} and its index.` : `Removed ${l.label}. Its index stays in ${b.indexDir}.`)))],
        detail: isBuilding(b.state) ? <BuildLine progress={b.progress} /> : undefined,
      })
      continue
    }
    rows.push({
      key: l.id, name: l.label, kind: `GUFI index on this machine, connected as it is${l.pinned ? ', set by the deployment' : ''}`, access: libraryAccess(l),
      details: [
        { label: 'Index', path: l.indexRoot },
        l.sourceRoot ? { label: 'Files', path: l.sourceRoot } : { label: 'Holds', value: holds(l.caps.fullText, l.caps.vectors) },
        throughRow(l.id, l.label, l.readThrough, user.ssh && !l.pinned, loadLibs),
      ],
      state: l.caps.index
        ? { tone: 'success', text: 'Ready', facts: [], notes: l.pinned ? [PINNED_NOTE] : [] }
        : { tone: 'error', text: 'Index not found', facts: [], notes: ['There is no db.db at the index directory.', ...(l.pinned ? [PINNED_NOTE] : [])] },
      fullText: l.caps.fullText, meaning: l.caps.vectors,
      actions: null,
      more: l.pinned ? [] : [removeEntry(l.label, busy !== null, false, () => askRemove(l.label, <>{cfg.appName} stops searching it. The index stays where it is, in <code>{l.indexRoot}</code>.</>, false,
        () => act(l.id, async () => { await api.removeLibrary(l.id); await loadLibs(); reloadAppConfig() })))],
    })
  }
  for (const l of user.libraries) {
    const caps = capsOf(l.id)
    const where = l.transport === 'ssh' ? (l.reached ?? l.destination ?? l.site) : l.site
    const remove = [removeEntry(l.label, busy !== null, false, () => askRemove(l.label, <>{cfg.appName} stops searching it. The index stays where it is, on {where}.</>, false,
      () => act(l.id, async () => { await api.removeUserLibrary(l.id); await loadUser(); reloadAppConfig() })))]
    const access = libraryAccess({ label: l.label, site: where, writable: !!(l.mine || l.space || l.shared?.access === 'write'), sharedFrom: l.shared?.from, access: l.access })
    const contents = counts(l.directories, l.files)
    const sshDetails: Detail[] = [
      ...(l.port ? [{ label: 'Port', value: String(l.port), mono: true }] : []),
      ...(l.sshOptions ? [{ label: 'SSH options', value: l.sshOptions, mono: true }] : []),
    ]
    if (l.built) {
      const host = where
      const facts: ReactNode[] = [contents, l.builtAt && `indexed ${when(l.builtAt)}`].filter(Boolean)
      const notes: ReactNode[] = [l.error, depthNote(l), ...(l.gaps ?? [])].filter(Boolean)
      const choice = asked?.id === l.id && (
        <DepthRebuild l={l} host={host} from={asked.from} busy={busy === l.id} onClose={() => setAsked(null)}
          onRebuild={() => void act(l.id, async () => { await api.refreshUserLibrary(l.id); await loadUser(); setAsked(null) }, `Rebuilding the index on ${host}.`)} />
      )
      rows.push({
        key: l.id, name: l.label, access,
        // Its access and its system are beside its name; this says what it is and whose account reads it.
        kind: l.space ? `${cfg.signIn ? 'My library' : 'Your folder'}, as ${l.space.account}, read and written as you`
          : l.shared ? `Shared by ${l.shared.from}, ${l.shared.access === 'write' ? 'read and written' : 'read'} as you`
          : `Index built there over ${l.transport === 'ssh' ? 'ssh' : 'pw ssh'}${l.mine ? ', read and written as you' : ''}`,
        details: [
          { label: 'Folder', host, path: l.sourceRoot ?? l.sourceDir },
          { label: 'Index', host, path: l.indexRoot ?? l.indexDir ?? '' },
          ...sshDetails,
          throughRow(l.id, l.label, l.readThrough, user.ssh, loadUser),
        ],
        state: l.state === 'building' ? { tone: 'info', text: 'Building', pulse: true, facts, notes }
          : l.state === 'updating' ? { tone: 'info', text: 'Updating', pulse: true, facts, notes }
          : l.state === 'failed' ? { tone: 'error', text: 'Failed', facts, notes }
          : l.error ? { tone: 'warning', text: 'Ready; the last build failed', facts, notes }
          : l.gaps?.length ? { tone: 'warning', text: 'Ready; some files were not read', facts, notes }
          : { tone: 'success', text: 'Ready', facts, notes },
        fullText: !!l.fullText, meaning: !!l.vectors,
        depth: { value: l.depth ?? UNSTORED_LIBRARY_DEPTH, reload: loadUser, applies: 'rebuild', built: l.builtDepth, building: isBuilding(l.state) },
        actions: (
          <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busy !== null || isBuilding(l.state)}
            onClick={() => void act(l.id, async () => { await api.refreshUserLibrary(l.id); await loadUser(); setAsked(null) }, `Rebuilding the index on ${host}.`)}>Rebuild</Button>
        ),
        more: remove,
        detail: isBuilding(l.state) || choice
          ? <>{isBuilding(l.state) && <BuildLine progress={l.progress} />}{choice && <div className="lib-build">{choice}</div>}</>
          : undefined,
      })
      continue
    }
    if (l.transport === 'ssh') {
      const dest = where
      const facts: ReactNode[] = [contents, l.builtAt && `index written ${when(l.builtAt)}`].filter(Boolean)
      const notes: ReactNode[] = [l.error].filter(Boolean)
      rows.push({
        key: l.id, name: l.label, kind: 'GUFI index over ssh, connected as it is', access,
        details: [
          { label: 'Index', host: dest, path: l.indexRoot ?? '' },
          l.sourceRoot ? { label: 'Files', host: dest, path: l.sourceRoot } : { label: 'Holds', value: holds(!!caps?.fullText, !!caps?.vectors) },
          ...sshDetails,
          throughRow(l.id, l.label, l.readThrough, user.ssh, loadUser),
        ],
        state: l.error ? { tone: 'warning', text: 'Check failed', facts, notes } : { tone: 'success', text: 'Ready', facts, notes },
        fullText: !!caps?.fullText, meaning: !!caps?.vectors,
        actions: (
          <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busy !== null}
            onClick={() => void act(l.id, async () => { await api.refreshUserLibrary(l.id); await loadUser() }, `Checked the index on ${dest}.`)}>Check</Button>
        ),
        more: remove,
      })
      continue
    }
    if (l.existing) {
      const facts: ReactNode[] = [contents, l.builtAt && `index written ${when(l.builtAt)}`].filter(Boolean)
      const notes: ReactNode[] = [l.error].filter(Boolean)
      rows.push({
        key: l.id, name: l.label, kind: 'GUFI index over pw ssh, connected as it is', access,
        details: [
          { label: 'Index', host: l.site, path: l.indexRoot ?? '' },
          l.sourceRoot ? { label: 'Files', host: l.site, path: l.sourceRoot } : { label: 'Holds', value: holds(!!caps?.fullText, !!caps?.vectors) },
          throughRow(l.id, l.label, l.readThrough, user.ssh, loadUser),
        ],
        state: l.error ? { tone: 'warning', text: 'Check failed', facts, notes } : { tone: 'success', text: 'Ready', facts, notes },
        fullText: !!caps?.fullText, meaning: !!caps?.vectors,
        actions: (
          <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busy !== null}
            onClick={() => void act(l.id, async () => { await api.refreshUserLibrary(l.id); await loadUser() }, `Checked the index on ${l.site}.`)}>Check</Button>
        ),
        more: remove,
      })
      continue
    }
    const building = l.state === 'building' || l.state === 'updating'
    const facts: ReactNode[] = [building && l.run ? `run ${l.run.slug}` : null, contents, l.builtAt && `indexed ${when(l.builtAt)}`].filter(Boolean)
    const notes: ReactNode[] = [l.error].filter(Boolean)
    rows.push({
      key: l.id, name: l.label, kind: 'Index built on a cluster by a workflow job, yours alone', access,
      details: [
        l.indexRoot ? { label: 'Index', host: l.site, path: l.indexRoot } : { label: 'Index', value: <span className="muted">being built</span> },
        { label: 'Files', host: l.site, path: l.sourceRoot ?? l.sourceDir },
        throughRow(l.id, l.label, l.readThrough, user.ssh, loadUser),
      ],
      state: l.state === 'building' ? { tone: 'info', text: 'Building', pulse: true, facts, notes }
        : l.state === 'updating' ? { tone: 'info', text: 'Updating', pulse: true, facts, notes }
        : l.state === 'failed' ? { tone: 'error', text: 'Failed', facts, notes }
        : l.error ? { tone: 'warning', text: 'Ready; the last update failed', facts, notes }
        : { tone: 'success', text: 'Ready', facts, notes },
      fullText: !!caps?.fullText, meaning: !!caps?.vectors,
      actions: (
        <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busy !== null || building}
          onClick={() => void act(l.id, async () => { await api.refreshUserLibrary(l.id); await loadUser() }, 'Updating the index on the cluster.')}>Refresh</Button>
      ),
      more: remove,
    })
  }

  // An index from before GUFI 0.8.0 that reads wrongly says so, whatever kind of row it is.
  const formatNotes = new Map<string, string>([...(libs ?? []).map(l => [l.id, l.formatNote] as const), ...user.libraries.map(l => [l.id, l.formatNote] as const)]
    .filter((e): e is readonly [string, string] => !!e[1]))
  for (const r of rows) {
    const note = formatNotes.get(r.key)
    if (note) r.state.notes.push(note)
  }

  const primary = libs?.find(l => l.primary)
  const primaryPath = standalone?.primary?.path ?? primary?.sourceRoot ?? null
  // pw ssh is a choice wherever it is offered; the form says why when it cannot be used yet.
  const options = addOptions(canBuild, user, !!cfg.platform)

  return (
    <>
      {dialog}
      {heading && <h1>Libraries</h1>}
      <p className="muted view-sub">
        Every library searched here. {cfg.signIn
          ? <>{cfg.kbLabel} is written here, unless its card says it is read only for you. My library and the
            libraries you index on your remote computers are written as your account there, where it may write.</>
          : <>{cfg.kbLabel}, your folder on this computer, is written here. The libraries you index on your remote
            computers are written as your account there, where it may write.</>}
        {' '}The others are read only: folders indexed on this computer, indexes connected as they
        are, and kept copies.
      </p>
      {!libs ? <Loader inline size={16} label="Loading…" /> : (
        <div className="lib-cards">
          {rows.map(r => {
            // Its access, as the Library switcher names it.
            const listed = cfg.libraries?.find(x => x.id === r.key)
            const access = listed ? libraryAccess(listed) : r.access
            const details = r.details.filter((d): d is Detail => !!d)
            return (
              <Card key={r.key} size="fill" padding="none" interactive={false}
                className={['lib-card', focused === r.key ? 'lib-focus' : ''].filter(Boolean).join(' ')}
                render={<section aria-label={r.name} data-library={r.key} />}>
                <div className="lib-card-head">
                  <div className="lib-card-title">
                    <h2 className="lib-name">{r.name}<span className="lib-access"> · {access}</span></h2>
                    <div className="lib-kind">{r.kind}</div>
                    <div className="lib-state">
                      <StatusBadge variant={r.state.tone} dot={!r.state.pulse} pulse={r.state.pulse} size="sm">{r.state.text}</StatusBadge>
                      {r.state.facts.length > 0 && (
                        <span className="lib-facts">{r.state.facts.map((f, i) => <span key={i}>{i > 0 && ' · '}{f}</span>)}</span>
                      )}
                    </div>
                  </div>
                  <div className="lib-card-actions">
                    {r.actions}
                    {r.more.length > 0 && (
                      <ActionMenu label={`More actions for ${r.name}`} align="end" items={r.more}>
                        <MoreIcon />
                      </ActionMenu>
                    )}
                  </div>
                </div>
                {r.state.notes.length > 0 && (
                  <ul className="lib-notes">{r.state.notes.map((n, i) => <li key={i}>{n}</li>)}</ul>
                )}
                <dl className="lib-fields">
                  {details.map(d => (
                    <div key={d.label} className={d.path !== undefined ? 'lib-field lib-field-path' : 'lib-field'}>
                      <dt>{d.label}</dt>
                      <dd>{d.path !== undefined
                        ? (d.path ? <PathLine host={d.host} path={d.path} what={`${d.label.toLowerCase()} path of ${r.name}`} /> : <span className="muted">none</span>)
                        : d.mono ? <code>{d.value}</code> : d.value}</dd>
                    </div>
                  ))}
                  <div className="lib-field">
                    <dt>Depth</dt>
                    <dd>
                      {r.depth
                        ? r.depth.locked
                          ? <span title={r.depth.locked}>{depthLabel(r.depth.value)} <span className="muted">{r.depth.locked}</span></span>
                          : <IndexDepthSelect library={r.name} value={r.depth.value} disabled={busy !== null}
                            onChange={v => {
                              const d = r.depth!
                              // Back at the depth its index has, there is nothing to rebuild.
                              const from = d.built ?? (asked?.id === r.key ? asked.from : d.value)
                              const same = from === v && !d.building
                              void act(r.key, async () => {
                                await api.setLibraryDepth(r.key, v); await d.reload(); reloadAppConfig()
                                if (d.applies === 'rebuild') setAsked(same ? null : { id: r.key, from })
                              }, d.applies === 'sync' ? `${r.name}: ${depthLabel(v)}. It is applied in the background now.`
                                : same ? `${r.name}: ${depthLabel(v)}, the depth its index has.` : undefined)
                            }} />
                        : <AsBuilt fullText={r.fullText} meaning={r.meaning} />}
                    </dd>
                  </div>
                </dl>
                {r.detail && <div className="lib-card-detail">{r.detail}</div>}
                {r.footer && <div className="lib-card-foot">{r.footer}</div>}
              </Card>
            )
          })}
        </div>
      )}
      {standalone && <DeleteIndexDialog controls={mainControls} folder={primary?.sourceRoot} />}
      {libs && (
        <p className="muted lib-depth-note">
          Depth is how far an index goes, each stop adding to the one before: Names; Text, the words inside files; Text + OCR,
          text in images and scanned pages; Meaning on documents, search by meaning over documents and images; and Meaning on
          all files, about three times the indexing time of Meaning on documents. For a library {cfg.appName} builds it is a
          choice: one built here applies a new depth in the background at once. One built on a remote computer applies it when
          it is rebuilt, which is offered as the depth changes, and its card says so until then. An index connected as it is
          stays as it was built.
        </p>
      )}

      {!adding && (
        <div className="query-actions lib-add">
          <Button variant="primary" icon={<AddIcon />} onClick={openAdd}>Add a library</Button>
          <Button variant="secondary" onClick={() => setImporting(true)}>Import…</Button>
          {libs && <Button variant="secondary" onClick={() => setExporting(true)}>Export…</Button>}
        </div>
      )}
      <Modal open={importing} onClose={() => setImporting(false)} size="lg" title="Import libraries"
        description={IMPORT_ABOUT}
        footer={<Button variant="secondary" onClick={() => setImporting(false)}>Close</Button>}>
        {importing && <LibraryImport onDone={imported} />}
      </Modal>
      {libs && <LibraryExport open={exporting} onClose={() => setExporting(false)} libraries={libs} userLibraries={user.libraries} />}

      {adding && (
        <section className="lib-add-form" aria-label="Add a library"
          ref={el => { addForm.current = el }}>
          <div className="lib-add-head">
            <div className="tool-group">Add a library</div>
            <IconButton icon={<CloseIcon />} label="Close" size="sm" onClick={() => setAdding(false)} />
          </div>
          <AddLibrary options={options} browse={!!standalone} onImported={imported}
            onAdded={(lib, started, here) => added(addedMessage(lib, started, here))}
            onStarted={() => added(CLUSTER_STARTED)} />
        </section>
      )}

      {standalone && (
        <Modal open={changing} onClose={() => { if (!restarting) setChanging(false) }} dismissible={!restarting} size="lg"
          title="Change the main folder"
          description={`${cfg.appName} restarts to open the folder you choose. Build its index afterwards from this page.`}
          footer={<Button variant="secondary" disabled={restarting} onClick={() => setChanging(false)}>Close</Button>}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <FolderPicker current={primaryPath} start={standalone.primary?.exists && primaryPath ? primaryPath : '~'}
              disabled={indexing} pollMs={pollMs} onRestarting={setRestarting}
              onChanged={async () => {
                const [s] = await Promise.all([loadStatus(), loadLibs(), loadUser()])
                reloadAppConfig()
                return s?.primary?.path
              }} />
          </div>
        </Modal>
      )}
    </>
  )
}

/** What an index with no files reachable from here can be searched by. */
function holds(fullText: boolean, vectors: boolean): string {
  return vectors ? 'Names, metadata, text and meaning' : fullText ? 'Names, metadata and text' : 'Names and metadata'
}

/**
 * A path on one line: cut in the middle where it is too long, so its start
 * and its last folders both show, whole on hover and to a screen reader,
 * with Copy beside it. `host` prefixes host: for a path on another system.
 */
function PathLine({ host, path, what }: { host?: string; path: string; what: string }) {
  const full = host ? `${host}:${path}` : path
  // The tail kept whole: the last two names where they are short, or the last one.
  const parts = full.split('/')
  const keep = parts.length > 2 && parts.slice(-2).join('/').length <= 28 ? 2 : 1
  const tail = parts.length > 1 ? `/${parts.slice(-keep).join('/')}` : ''
  const head = parts.length > 1 ? parts.slice(0, -keep).join('/') : full
  return (
    <span className="lib-path">
      <code className="lib-path-text" title={full} aria-label={full}>
        <span className="lib-path-head" aria-hidden="true">{head}</span>
        <span className="lib-path-tail" aria-hidden="true">{tail}</span>
      </code>
      <CopyButton text={full} label={`the ${what}`} size="xs" quiet={false} />
    </span>
  )
}

/** What an index connected as it is has, in the depth's words. */
function AsBuilt({ fullText, meaning }: { fullText: boolean; meaning: boolean }) {
  return <span>{meaning ? 'Text and meaning' : fullText ? 'Text' : 'Names'} <span className="muted">as built</span></span>
}
