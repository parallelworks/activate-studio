import { Fragment, type ReactNode, useEffect, useRef, useState } from 'react'
import {
  Button, Callout, DescriptionList, DescriptionListItem, IconButton, Loader, Modal, ProgressBar, StatusLabel, Table, toast, useConfirm,
} from '@parallelworks/ui'
import { AddIcon, CloseIcon } from '@parallelworks/ui/icons'
import { api, type BuildProgress, type IndexDepth, type ReadThrough, type SettingsLibrary, type UserLibrary } from '../api'
import { IndexDepthSelect, UNSTORED_LIBRARY_DEPTH, deeper, depthLabel } from './IndexDepth'
import { reloadAppConfig, useAppConfig } from '../config'
import { AddLibrary } from './AddLibrary'
import { FolderPicker, errText, json, type SetupStatus } from './PrimaryFolder'
import { IndexControls, IndexJobView, LastBuild } from './IndexActivity'
import { useIndexStatus } from '../indexActivity'
import { FOCUS_LIBRARY_EVENT, FOCUS_LIBRARY_KEY } from './OmittedLine'
import { ReadThroughControl } from './ReadThroughControl'

/** A user library as listed, with how it is reached. */
type UserLib = UserLibrary
interface UserList { libraries: UserLib[]; available: boolean; ssh: boolean; pw: boolean; pwOffered: boolean; workflow: boolean }

/** The kinds of library, in the words the setup wizard's Other libraries
 *  step uses for the forms it shares with this page. */
export type LibraryKind = 'local' | 'ssh' | 'pw' | 'cluster'
export const KINDS: Record<LibraryKind, { label: string; description: string }> = {
  local: { label: 'A GUFI index on this machine', description: 'An index already built here. Read only.' },
  ssh: { label: 'A GUFI index on another machine over ssh', description: 'Read with your own ssh keys. Nothing is written there.' },
  pw: { label: 'A GUFI index on a remote system, over pw ssh', description: 'An index already on one of the systems the pw CLI reaches, read over pw ssh. Nothing is written there.' },
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
interface Row {
  key: string
  name: string
  /** What it is and what the Studio may do with it, in plain words. */
  kind: string
  where: ReactNode
  state: { tone: Tone; text: string; pulse?: boolean; notes: ReactNode[] }
  /** What the index has: its full-text tables, and vectors for search by meaning. */
  fullText: boolean
  meaning: boolean
  /** For a library the Studio builds: its depth, which can be changed here,
   *  and when a change applies: at the sync, in the background, for one
   *  built here; at the next Rebuild for one built on a remote system. */
  depth?: { value: IndexDepth; reload: () => Promise<unknown>; applies: 'sync' | 'rebuild'; built?: IndexDepth | null; building?: boolean }
  actions: ReactNode
  /** A line under the row across the whole table, such as a build's progress. */
  detail?: ReactNode
}

const when = (iso: string | null) => (iso ? new Date(iso).toLocaleString() : '')
/** Why a library the deployment set has no Remove, and where it is removed. */
const PINNED_NOTE = 'Named in the server\'s STUDIO_LIBRARIES setting; remove it there and restart the Studio.'
const counts = (dirs: number | null, files: number | null) =>
  dirs != null ? `${dirs.toLocaleString()} folders, ${(files ?? 0).toLocaleString()} files` : null

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
  // Opened from the Library's + Add menu (#view=settings:libraries?add=1)
  // with the form already showing.
  const [adding, setAdding] = useState(() => /^#view=settings:libraries\?(?:.*&)?add=1(?:&|$)/.test(location.hash))
  const openedByLink = useRef(adding)
  const addForm = useRef<HTMLElement | null>(null)
  // Brought into view as the lists above it load, for the first moments
  // after the page opens: scrolled once at once, it was pushed down again by
  // the libraries arriving.
  const openedAt = useRef(Date.now())
  useEffect(() => {
    if (!openedByLink.current || libs === null || !addForm.current) return
    if (Date.now() - openedAt.current > 3000) { openedByLink.current = false; return }
    addForm.current.scrollIntoView?.({ block: 'start' })
  }, [libs, user, status, adding])
  const [changing, setChanging] = useState(false)
  const [restarting, setRestarting] = useState(false)
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
  const loadUser = () => api.userLibraries().then(r => {
    const list = r as Partial<UserList>
    const libraries = list.libraries ?? []
    setUser({ libraries, available: !!list.available, ssh: !!list.ssh, pw: !!list.pw, pwOffered: !!(list.pwOffered ?? list.pw), workflow: !!list.workflow })
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
      window.setTimeout(() => document.querySelector(`tr[data-library="${CSS.escape(id)}"]`)?.scrollIntoView({ block: 'center' }), 50)
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
  const followHere = (libs ?? []).some(l => isBuilding(l.build?.state))
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
  const askRemove = (label: string, what: ReactNode, deletes: boolean, run: () => Promise<unknown>) => void confirm({
    title: `Remove ${label}?`, description: what, confirmLabel: 'Remove', destructive: deletes, onConfirm: run,
  })
  const act = async (key: string, fn: () => Promise<unknown>, done?: string) => {
    setBusy(key)
    try { await fn(); if (done) toast.success(done) } catch (e) { toast.error(errText(e)) } finally { setBusy(null) }
  }
  const added = (message: string) => {
    toast.success(message)
    setAdding(false)
    void loadLibs()
    void loadUser()
    reloadAppConfig()
  }
  const capsOf = (id: string) => cfg.libraries?.find(l => l.id === id)?.caps
  // Another host that has a library's files, where its originals are also
  // read (the assistant's read_original, and the viewer). Shown where it is
  // set, and where it can be set: the ssh keys it uses are this account's,
  // which only a Studio with sign-in off lends.
  const throughRow = (id: string, label: string, current: ReadThrough | null | undefined, canEdit: boolean, reload: () => Promise<unknown>) =>
    current || canEdit
      ? (
        <DescriptionListItem label="Originals via">
          <ReadThroughControl id={id} label={label} current={current} canEdit={canEdit} onChanged={async () => { await reload() }} />
        </DescriptionListItem>
      )
      : null

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
  for (const l of libs ?? []) {
    if (l.primary) {
      const p = standalone?.primary
      const notes: ReactNode[] = []
      const job = ix?.job ?? null
      const indexed = ix?.indexed ?? (p ? p.indexed : l.caps.index)
      let state: Row['state']
      if (job) state = { tone: 'info', text: job.kind === 'delete' ? 'Deleting' : job.kind === 'move' ? 'Moving' : job.kind === 'build' ? 'Indexing' : 'Syncing', pulse: true, notes }
      else if (p && !p.exists) state = { tone: 'error', text: 'Folder not found', notes }
      else if (indexed) {
        state = ix?.incomplete ? { tone: 'warning', text: 'Incomplete', notes } : { tone: 'success', text: 'Indexed', notes }
        if (p?.files !== undefined) notes.push(`${p.files.toLocaleString()} files`)
      } else {
        state = { tone: 'warning', text: 'Not indexed yet', notes }
        if (p?.earlierIndex) notes.push(`An index of this folder from ${p.earlierIndex.builtAt ? new Date(p.earlierIndex.builtAt).toLocaleDateString() : 'before'} is kept; building updates it.`)
      }
      const gufi = standalone?.gufi
      if (gufi && !gufi.found) notes.push(`GUFI was not found in ${gufi.bin}, so the index cannot be built.`)
      // A Studio that signs people in is shared: its deployment builds this
      // index, and removing it here would take search away from everyone.
      if (!standalone) notes.push('On a Studio that signs people in, the deployment builds and deletes this index.')
      rows.push({
        key: l.id, name: l.label, kind: 'Main folder, read and write',
        where: (
          <DescriptionList className="lib-paths" stacked>
            <DescriptionListItem label="Folder" mono={!!l.sourceRoot} copy={!!l.sourceRoot}>{l.sourceRoot ? <PathText path={l.sourceRoot} /> : null}</DescriptionListItem>
            <DescriptionListItem label="Index" mono copy><PathText path={l.indexRoot} /></DescriptionListItem>
          </DescriptionList>
        ),
        state, fullText: l.caps.fullText, meaning: l.caps.vectors,
        depth: { value: l.depth ?? 'meaning-documents', reload: loadLibs, applies: 'sync' },
        actions: (
          <>
            {standalone && <Button variant="secondary" size="sm" disabled={indexing} onClick={() => setChanging(true)}>Change folder</Button>}
            {/* A hosted Studio's deployment builds its index; this page shows it and its log. */}
            <IndexControls part="primary" canDelete={!!standalone} canBuild={!!standalone && !!gufi?.found && !!p?.exists} folder={l.sourceRoot} />
          </>
        ),
        // Under the row: the build as it runs, or how the last one went, and
        // the index's other controls, the delete's confirmation among them.
        detail: (
          <div className="lib-build">
            {job ? <IndexJobView job={job} /> : <LastBuild status={ix} />}
            <IndexControls part="secondary" canDelete={!!standalone} folder={l.sourceRoot} />
          </div>
        ),
      })
      continue
    }
    if (l.build) {
      const b = l.build
      const notes: ReactNode[] = [counts(b.dirs, b.files), b.builtAt && `indexed ${when(b.builtAt)}`, b.error].filter(Boolean)
      rows.push({
        key: l.id, name: l.label, kind: 'Index of a folder on this machine, kept in step with it, read only',
        where: (
          <DescriptionList className="lib-paths" stacked>
            <DescriptionListItem label="Folder" mono copy>{l.sourceRoot ? <PathText path={l.sourceRoot} /> : null}</DescriptionListItem>
            <DescriptionListItem label="Index" mono copy><PathText path={l.indexRoot} /></DescriptionListItem>
          </DescriptionList>
        ),
        state: b.state === 'building' ? { tone: 'info', text: 'Building', pulse: true, notes }
          : b.state === 'updating' ? { tone: 'info', text: 'Updating', pulse: true, notes }
          : b.state === 'failed' ? { tone: 'error', text: 'Failed', notes }
          // Its index was removed outside the Studio: search names it as left out.
          : !l.caps.index ? { tone: 'error', text: 'Index not found', notes: [...notes, 'Rebuild makes it again.'] }
          : b.error ? { tone: 'warning', text: 'Ready; the last pass failed', notes }
          : { tone: 'success', text: 'Ready', notes },
        fullText: l.caps.fullText, meaning: l.caps.vectors,
        depth: { value: l.depth ?? UNSTORED_LIBRARY_DEPTH, reload: loadLibs, applies: 'sync' },
        actions: (
          <>
            <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busy !== null || isBuilding(b.state)}
              onClick={() => void act(l.id, async () => { await api.rebuildLibrary(l.id); await loadLibs() }, `Rebuilding ${l.label}.`)}>Rebuild</Button>
            <Button variant={b.managed ? 'danger-outline' : 'secondary'} size="sm" aria-label={`Remove ${l.label}`} disabled={busy !== null || isBuilding(b.state)}
              onClick={() => askRemove(l.label, b.managed
                ? <>Its index, which {cfg.appName} keeps in a folder of its own, is deleted. The files in {l.sourceRoot ? <code>{l.sourceRoot}</code> : 'the folder'} are not touched.</>
                : <>{cfg.appName} stops searching it. Its index stays in <code>{b.indexDir}</code>, and the files in {l.sourceRoot ? <code>{l.sourceRoot}</code> : 'the folder'} are not touched.</>,
                b.managed,
                () => act(l.id, async () => { await api.removeLibrary(l.id); await loadLibs(); reloadAppConfig() },
                  b.managed ? `Removed ${l.label} and its index.` : `Removed ${l.label}. Its index stays in ${b.indexDir}.`))}>Remove</Button>
          </>
        ),
        detail: isBuilding(b.state) ? <BuildLine progress={b.progress} /> : undefined,
      })
      continue
    }
    rows.push({
      key: l.id, name: l.label, kind: `GUFI index on this machine, read only${l.pinned ? ', set by the deployment' : ''}`,
      where: (
        <DescriptionList className="lib-paths" stacked>
          <DescriptionListItem label="Index" mono copy><PathText path={l.indexRoot} /></DescriptionListItem>
          {l.sourceRoot
            ? <DescriptionListItem label="Files" mono copy><PathText path={l.sourceRoot} /></DescriptionListItem>
            : <DescriptionListItem label="Holds">{holds(l.caps.fullText, l.caps.vectors)}</DescriptionListItem>}
          {throughRow(l.id, l.label, l.readThrough, user.ssh && !l.pinned, loadLibs)}
        </DescriptionList>
      ),
      state: l.caps.index
        ? { tone: 'success', text: 'Ready', notes: l.pinned ? [PINNED_NOTE] : [] }
        : { tone: 'error', text: 'Index not found', notes: ['There is no db.db at the index directory.', ...(l.pinned ? [PINNED_NOTE] : [])] },
      fullText: l.caps.fullText, meaning: l.caps.vectors,
      actions: !l.pinned && (
        <Button variant="secondary" size="sm" aria-label={`Remove ${l.label}`} disabled={busy !== null}
          onClick={() => askRemove(l.label, <>{cfg.appName} stops searching it. The index stays where it is, in <code>{l.indexRoot}</code>.</>, false,
            () => act(l.id, async () => { await api.removeLibrary(l.id); await loadLibs(); reloadAppConfig() }))}>Remove</Button>
      ),
    })
  }
  for (const l of user.libraries) {
    const caps = capsOf(l.id)
    const remove = (
      <Button variant="secondary" size="sm" aria-label={`Remove ${l.label}`} disabled={busy !== null}
        onClick={() => askRemove(l.label, <>{cfg.appName} stops searching it. The index stays where it is, on {l.transport === 'ssh' ? (l.reached ?? l.destination ?? l.site) : l.site}.</>, false,
          () => act(l.id, async () => { await api.removeUserLibrary(l.id); await loadUser(); reloadAppConfig() }))}>Remove</Button>
    )
    const contents = counts(l.directories, l.files)
    if (l.built) {
      const host = l.transport === 'ssh' ? (l.reached ?? l.destination ?? l.site) : l.site
      const notes: ReactNode[] = [contents, l.builtAt && `indexed ${when(l.builtAt)}`, l.error, depthNote(l), ...(l.gaps ?? [])].filter(Boolean)
      const choice = asked?.id === l.id && (
        <DepthRebuild l={l} host={host} from={asked.from} busy={busy === l.id} onClose={() => setAsked(null)}
          onRebuild={() => void act(l.id, async () => { await api.refreshUserLibrary(l.id); await loadUser(); setAsked(null) }, `Rebuilding the index on ${host}.`)} />
      )
      rows.push({
        key: l.id, name: l.label, kind: `Index built on ${host} over ${l.transport === 'ssh' ? 'ssh' : 'pw ssh'}, read only`,
        where: (
          <DescriptionList className="lib-paths" stacked>
            <DescriptionListItem label="Folder" mono copy><HostPath host={host} path={l.sourceRoot ?? l.sourceDir} /></DescriptionListItem>
            <DescriptionListItem label="Index" mono copy><HostPath host={host} path={l.indexRoot ?? l.indexDir ?? ''} /></DescriptionListItem>
            {l.port ? <DescriptionListItem label="Port" mono>{String(l.port)}</DescriptionListItem> : null}
            {l.sshOptions ? <DescriptionListItem label="SSH options" mono>{l.sshOptions}</DescriptionListItem> : null}
            {throughRow(l.id, l.label, l.readThrough, user.ssh, loadUser)}
          </DescriptionList>
        ),
        state: l.state === 'building' ? { tone: 'info', text: 'Building', pulse: true, notes }
          : l.state === 'updating' ? { tone: 'info', text: 'Updating', pulse: true, notes }
          : l.state === 'failed' ? { tone: 'error', text: 'Failed', notes }
          : l.error ? { tone: 'warning', text: 'Ready; the last build failed', notes }
          : l.gaps?.length ? { tone: 'warning', text: 'Ready; some files were not read', notes }
          : { tone: 'success', text: 'Ready', notes },
        fullText: !!l.fullText, meaning: !!l.vectors,
        depth: { value: l.depth ?? UNSTORED_LIBRARY_DEPTH, reload: loadUser, applies: 'rebuild', built: l.builtDepth, building: isBuilding(l.state) },
        actions: (
          <>
            <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busy !== null || isBuilding(l.state)}
              onClick={() => void act(l.id, async () => { await api.refreshUserLibrary(l.id); await loadUser(); setAsked(null) }, `Rebuilding the index on ${host}.`)}>Rebuild</Button>
            {remove}
          </>
        ),
        detail: isBuilding(l.state) || choice
          ? <>{isBuilding(l.state) && <BuildLine progress={l.progress} />}{choice && <div className="lib-build">{choice}</div>}</>
          : undefined,
      })
      continue
    }
    if (l.transport === 'ssh') {
      const dest = l.reached ?? l.destination ?? l.site
      const notes: ReactNode[] = [contents, l.builtAt && `index written ${when(l.builtAt)}`, l.error].filter(Boolean)
      rows.push({
        key: l.id, name: l.label, kind: 'GUFI index over ssh, read only',
        where: (
          <DescriptionList className="lib-paths" stacked>
            <DescriptionListItem label="Index" mono copy><HostPath host={dest} path={l.indexRoot ?? ''} /></DescriptionListItem>
            {l.port ? <DescriptionListItem label="Port" mono>{String(l.port)}</DescriptionListItem> : null}
            {l.sshOptions ? <DescriptionListItem label="SSH options" mono>{l.sshOptions}</DescriptionListItem> : null}
            {l.sourceRoot
              ? <DescriptionListItem label="Files" mono copy><HostPath host={dest} path={l.sourceRoot} /></DescriptionListItem>
              : <DescriptionListItem label="Holds">{holds(!!caps?.fullText, !!caps?.vectors)}</DescriptionListItem>}
            {throughRow(l.id, l.label, l.readThrough, user.ssh, loadUser)}
          </DescriptionList>
        ),
        state: l.error ? { tone: 'warning', text: 'Check failed', notes } : { tone: 'success', text: 'Ready', notes },
        fullText: !!caps?.fullText, meaning: !!caps?.vectors,
        actions: (
          <>
            <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busy !== null}
              onClick={() => void act(l.id, async () => { await api.refreshUserLibrary(l.id); await loadUser() }, `Checked the index on ${dest}.`)}>Check</Button>
            {remove}
          </>
        ),
      })
      continue
    }
    if (l.existing) {
      const notes: ReactNode[] = [contents, l.builtAt && `index written ${when(l.builtAt)}`, l.error].filter(Boolean)
      rows.push({
        key: l.id, name: l.label, kind: 'GUFI index on a cluster over pw ssh, read only',
        where: (
          <DescriptionList className="lib-paths" stacked>
            <DescriptionListItem label="Index" mono copy><HostPath host={l.site} path={l.indexRoot ?? ''} /></DescriptionListItem>
            {l.sourceRoot
              ? <DescriptionListItem label="Files" mono copy><HostPath host={l.site} path={l.sourceRoot} /></DescriptionListItem>
              : <DescriptionListItem label="Holds">{holds(!!caps?.fullText, !!caps?.vectors)}</DescriptionListItem>}
            {throughRow(l.id, l.label, l.readThrough, user.ssh, loadUser)}
          </DescriptionList>
        ),
        state: l.error ? { tone: 'warning', text: 'Check failed', notes } : { tone: 'success', text: 'Ready', notes },
        fullText: !!caps?.fullText, meaning: !!caps?.vectors,
        actions: (
          <>
            <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busy !== null}
              onClick={() => void act(l.id, async () => { await api.refreshUserLibrary(l.id); await loadUser() }, `Checked the index on ${l.site}.`)}>Check</Button>
            {remove}
          </>
        ),
      })
      continue
    }
    const building = l.state === 'building' || l.state === 'updating'
    const notes: ReactNode[] = [
      building && l.run ? `run ${l.run.slug}` : null, l.error, contents, l.builtAt && `indexed ${when(l.builtAt)}`,
    ].filter(Boolean)
    rows.push({
      key: l.id, name: l.label, kind: 'Index built on a cluster, read only, yours alone',
      where: (
        <DescriptionList className="lib-paths" stacked>
          <DescriptionListItem label="Index" mono={!!l.indexRoot} copy={!!l.indexRoot}>
            {l.indexRoot ? <HostPath host={l.site} path={l.indexRoot} /> : <span className="muted">being built</span>}
          </DescriptionListItem>
          <DescriptionListItem label="Files" mono copy><HostPath host={l.site} path={l.sourceRoot ?? l.sourceDir} /></DescriptionListItem>
          {throughRow(l.id, l.label, l.readThrough, user.ssh, loadUser)}
        </DescriptionList>
      ),
      state: l.state === 'building' ? { tone: 'info', text: 'Building', pulse: true, notes }
        : l.state === 'updating' ? { tone: 'info', text: 'Updating', pulse: true, notes }
        : l.state === 'failed' ? { tone: 'error', text: 'Failed', notes }
        : l.error ? { tone: 'warning', text: 'Ready; the last update failed', notes }
        : { tone: 'success', text: 'Ready', notes },
      fullText: !!caps?.fullText, meaning: !!caps?.vectors,
      actions: (
        <>
          <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busy !== null || building}
            onClick={() => void act(l.id, async () => { await api.refreshUserLibrary(l.id); await loadUser() }, 'Updating the index on the cluster.')}>Refresh</Button>
          {remove}
        </>
      ),
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
  const options = { buildHere: canBuild, ssh: user.ssh, pw: user.pwOffered, workflow: user.workflow && !!cfg.platform && user.available }

  return (
    <>
      {dialog}
      {heading && <h1>Libraries</h1>}
      <p className="muted view-sub">
        Every library searched here. The main folder is the only one written to; the others are indexes read where
        they are, built here or connected as they are.
      </p>
      {!libs ? <Loader inline size={16} label="Loading…" /> : (
        <Table density="compact" className="libraries-table" wrapperClassName="rag-calls-wrap libraries-wrap">
          <Table.Header>Library</Table.Header>
          <Table.Header>State</Table.Header>
          <Table.Header>Depth</Table.Header>
          <Table.Header />
          {rows.map(r => (
            <Fragment key={r.key}>
              <tr className={[r.detail ? 'lib-has-detail' : '', focused === r.key ? 'lib-focus' : ''].filter(Boolean).join(' ') || undefined} data-library={r.key}>
                <Table.Item wrap>
                  <div className="lib-name">{r.name}</div>
                  <div className="lib-id">{r.kind}</div>
                  {r.where}
                </Table.Item>
                <Table.Item wrap>
                  <StatusLabel variant={r.state.tone} pulse={r.state.pulse}>{r.state.text}</StatusLabel>
                  {r.state.notes.map((n, i) => <div key={i} className="lib-id">{n}</div>)}
                </Table.Item>
                <Table.Item className="lib-cap" data-label="Depth">
                  {r.depth
                    ? <IndexDepthSelect library={r.name} value={r.depth.value} disabled={busy !== null}
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
                </Table.Item>
                <Table.Item className="lib-actions">{r.actions}</Table.Item>
              </tr>
              {r.detail && <tr className="lib-detail"><Table.Item colSpan={4} wrap>{r.detail}</Table.Item></tr>}
            </Fragment>
          ))}
        </Table>
      )}
      {libs && (
        <p className="muted lib-depth-note">
          Depth is how far an index goes, each stop adding to the one before: Names; Text, the words inside files; Text + OCR,
          text in images and scanned pages; Meaning on documents, search by meaning over documents and images; and Meaning on
          all files, about three times the indexing time of Meaning on documents. For a library {cfg.appName} builds it is a
          choice: one built here applies a new depth in the background at once. One built on a remote system applies it when
          it is rebuilt, which is offered as the depth changes, and its row says so until then. An index connected as it is
          stays as it was built.
        </p>
      )}

      {!adding && (
        <div className="query-actions lib-add">
          <Button variant="primary" icon={<AddIcon />} onClick={() => setAdding(true)}>Add a library</Button>
        </div>
      )}

      {adding && (
        <section className="lib-add-form" aria-label="Add a library"
          ref={el => { addForm.current = el }}>
          <div className="lib-add-head">
            <div className="tool-group">Add a library</div>
            <IconButton icon={<CloseIcon />} label="Close" size="sm" onClick={() => setAdding(false)} />
          </div>
          <AddLibrary options={options} browse={!!standalone}
            onAdded={(lib, started, here) => added(!started ? `Added ${lib.label}. It is in the Library switcher.`
              : here ? `Building ${lib.label}. Its progress shows in the list. It is searchable by name once its folders are indexed; text and meaning, where its depth has them, follow in that order.`
              : `Building ${lib.label}. Its progress shows in the list; it is searchable once the build is done.`)}
            onStarted={() => added('Building the index on the cluster. It appears in the Library switcher when it is ready.')} />
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

/** host:path for an index on another system: the host wraps after its dots
 *  and the path after its slashes, never inside a name, so a long host or
 *  path does not squeeze to a few characters a line. */
function HostPath({ host, path }: { host: string; path: string }) {
  const parts = host.split('.')
  return (
    <code title={`${host}:${path}`}>
      {parts.map((p, i) => <Fragment key={i}>{i > 0 && <>.<wbr /></>}<span className="lib-seg">{p}</span></Fragment>)}
      :<wbr />
      {path.split('/').map((seg, i) => <Fragment key={`p${i}`}>{i > 0 && <>/<wbr /></>}<span className="lib-seg">{seg}</span></Fragment>)}
    </code>
  )
}

/** A path that wraps only after a slash, never inside a directory name
 *  (a hyphenated name would otherwise break at its hyphen). The break
 *  points add no characters, so a selected path copies as written. */
function PathText({ path }: { path: string }) {
  return (
    <code title={path}>
      {path.split('/').map((seg, i) => (
        <Fragment key={i}>{i > 0 && <>/<wbr /></>}<span className="lib-seg">{seg}</span></Fragment>
      ))}
    </code>
  )
}

/** What an index connected as it is has, in the depth's words. */
function AsBuilt({ fullText, meaning }: { fullText: boolean; meaning: boolean }) {
  return (
    <>
      <span className="lib-yes">{meaning ? 'Text and meaning' : fullText ? 'Text' : 'Names'}</span>
      <div className="lib-id">as built</div>
    </>
  )
}
