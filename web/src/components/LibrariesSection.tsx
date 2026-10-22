import { Fragment, type ReactNode, useEffect, useRef, useState } from 'react'
import {
  Button, Callout, DescriptionList, DescriptionListItem, IconButton, Loader, Modal, ProgressBar, StatusBadge, Table, toast,
} from '@parallelworks/ui'
import { AddIcon, CloseIcon } from '@parallelworks/ui/icons'
import { api, type BuildProgress, type MeaningLevel, type SettingsLibrary, type UserLibrary } from '../api'
import { MeaningLevelSelect, meaningPhrase } from './MeaningLevel'
import { reloadAppConfig, useAppConfig } from '../config'
import { AddLibrary } from './AddLibrary'
import { FolderPicker, errText, json, type SetupStatus } from './PrimaryFolder'
import { IndexControls, IndexJobView, LastBuild } from './IndexActivity'
import { useIndexStatus } from '../indexActivity'

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
  fullText: boolean
  meaning: boolean
  /** For a library the Studio builds: its level of search by meaning, which can be changed here. */
  level?: { value: MeaningLevel; reload: () => Promise<unknown> }
  actions: ReactNode
  /** A line under the row across the whole table, such as a build's progress. */
  detail?: ReactNode
}

const when = (iso: string | null) => (iso ? new Date(iso).toLocaleString() : '')
const counts = (dirs: number | null, files: number | null) =>
  dirs != null ? `${dirs.toLocaleString()} folders, ${(files ?? 0).toLocaleString()} files` : null

/**
 * Settings > Libraries: one list of every library the Studio searches, and
 * one control to add another (AddLibrary). The main folder is the one the
 * Studio indexes itself and writes to; on a single-user install it can be
 * changed and its index rebuilt here. Every other library is read only: an
 * index the Studio builds of a folder on this machine, or a GUFI index
 * already there (/api/libraries); an index it builds on a remote system
 * over ssh or pw ssh, one already there, or one built on a cluster through
 * the platform (/api/user-libraries). Builds show their progress under
 * their row.
 */
export function LibrariesSection({ pollMs = 1000 }: { pollMs?: number }) {
  const cfg = useAppConfig()
  const [libs, setLibs] = useState<SettingsLibrary[] | null>(null)
  const [loadError, setLoadError] = useState('')
  const [user, setUser] = useState<UserList>({ libraries: [], available: false, ssh: false, pw: false, pwOffered: false, workflow: false })
  const [canBuild, setCanBuild] = useState(false)
  const [status, setStatus] = useState<SetupStatus | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [adding, setAdding] = useState(false)
  const [changing, setChanging] = useState(false)
  const [restarting, setRestarting] = useState(false)
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

  if (loadError && !libs) {
    return (
      <>
        <h1>Libraries</h1>
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
      rows.push({
        key: l.id, name: l.label, kind: 'Main folder, read and write',
        where: (
          <DescriptionList className="lib-paths">
            <DescriptionListItem label="Folder" mono={!!l.sourceRoot} copy={!!l.sourceRoot}>{l.sourceRoot ? <PathText path={l.sourceRoot} /> : null}</DescriptionListItem>
            <DescriptionListItem label="Index" mono copy><PathText path={l.indexRoot} /></DescriptionListItem>
          </DescriptionList>
        ),
        state, fullText: l.caps.fullText, meaning: l.caps.vectors,
        level: { value: l.meaning ?? 'documents', reload: loadLibs },
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
          <DescriptionList className="lib-paths">
            <DescriptionListItem label="Folder" mono copy>{l.sourceRoot ? <PathText path={l.sourceRoot} /> : null}</DescriptionListItem>
            <DescriptionListItem label="Index" mono copy><PathText path={l.indexRoot} /></DescriptionListItem>
          </DescriptionList>
        ),
        state: b.state === 'building' ? { tone: 'info', text: 'Building', pulse: true, notes }
          : b.state === 'updating' ? { tone: 'info', text: 'Updating', pulse: true, notes }
          : b.state === 'failed' ? { tone: 'error', text: 'Failed', notes }
          : b.error ? { tone: 'warning', text: 'Ready; the last pass failed', notes }
          : { tone: 'success', text: 'Ready', notes },
        fullText: l.caps.fullText, meaning: l.caps.vectors,
        level: { value: l.meaning ?? 'off', reload: loadLibs },
        actions: (
          <>
            <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busy !== null || isBuilding(b.state)}
              onClick={() => void act(l.id, async () => { await api.rebuildLibrary(l.id); await loadLibs() }, `Rebuilding ${l.label}.`)}>Rebuild</Button>
            <Button variant="secondary" size="sm" aria-label={`Remove ${l.label}`} disabled={busy !== null || isBuilding(b.state)}
              onClick={() => void act(l.id, async () => { await api.removeLibrary(l.id); await loadLibs(); reloadAppConfig() },
                b.managed ? `Removed ${l.label} and its index.` : `Removed ${l.label}. Its index stays in ${b.indexDir}.`)}>Remove</Button>
          </>
        ),
        detail: isBuilding(b.state) ? <BuildLine progress={b.progress} /> : undefined,
      })
      continue
    }
    rows.push({
      key: l.id, name: l.label, kind: `GUFI index on this machine, read only${l.pinned ? ', set by the deployment' : ''}`,
      where: (
        <DescriptionList className="lib-paths">
          <DescriptionListItem label="Index" mono copy><PathText path={l.indexRoot} /></DescriptionListItem>
          <DescriptionListItem label="Files" mono={!!l.sourceRoot} copy={!!l.sourceRoot}>
            {l.sourceRoot ? <PathText path={l.sourceRoot} /> : <span className="muted">not on this machine: names and metadata only</span>}
          </DescriptionListItem>
        </DescriptionList>
      ),
      state: l.caps.index
        ? { tone: 'success', text: 'Ready', notes: [] }
        : { tone: 'error', text: 'Index not found', notes: ['There is no db.db at the index directory.'] },
      fullText: l.caps.fullText, meaning: l.caps.vectors,
      actions: !l.pinned && (
        <Button variant="secondary" size="sm" aria-label={`Remove ${l.label}`} disabled={busy !== null}
          onClick={() => void act(l.id, async () => { await api.removeLibrary(l.id); await loadLibs(); reloadAppConfig() })}>Remove</Button>
      ),
    })
  }
  for (const l of user.libraries) {
    const caps = capsOf(l.id)
    const remove = (
      <Button variant="secondary" size="sm" aria-label={`Remove ${l.label}`} disabled={busy !== null}
        onClick={() => void act(l.id, async () => { await api.removeUserLibrary(l.id); await loadUser(); reloadAppConfig() })}>Remove</Button>
    )
    const contents = counts(l.directories, l.files)
    if (l.built) {
      const host = l.transport === 'ssh' ? (l.destination ?? l.site) : l.site
      const notes: ReactNode[] = [contents, l.builtAt && `indexed ${when(l.builtAt)}`, l.error].filter(Boolean)
      rows.push({
        key: l.id, name: l.label, kind: `Index built on ${host} over ${l.transport === 'ssh' ? 'ssh' : 'pw ssh'}, read only`,
        where: (
          <DescriptionList className="lib-paths">
            <DescriptionListItem label="Folder" mono copy><PathText path={`${host}:${l.sourceRoot ?? l.sourceDir}`} /></DescriptionListItem>
            <DescriptionListItem label="Index" mono copy><PathText path={`${host}:${l.indexRoot ?? l.indexDir ?? ''}`} /></DescriptionListItem>
            {l.port ? <DescriptionListItem label="Port" mono>{String(l.port)}</DescriptionListItem> : null}
          </DescriptionList>
        ),
        state: l.state === 'building' ? { tone: 'info', text: 'Building', pulse: true, notes }
          : l.state === 'updating' ? { tone: 'info', text: 'Updating', pulse: true, notes }
          : l.state === 'failed' ? { tone: 'error', text: 'Failed', notes }
          : l.error ? { tone: 'warning', text: 'Ready; the last build failed', notes }
          : { tone: 'success', text: 'Ready', notes },
        fullText: !!l.fullText, meaning: !!l.vectors,
        level: { value: l.meaning ?? 'off', reload: loadUser },
        actions: (
          <>
            <Button variant="secondary" size="sm" loading={busy === l.id} disabled={busy !== null || isBuilding(l.state)}
              onClick={() => void act(l.id, async () => { await api.refreshUserLibrary(l.id); await loadUser() }, `Rebuilding the index on ${host}.`)}>Rebuild</Button>
            {remove}
          </>
        ),
        detail: isBuilding(l.state) ? <BuildLine progress={l.progress} /> : undefined,
      })
      continue
    }
    if (l.transport === 'ssh') {
      const dest = l.destination ?? l.site
      const notes: ReactNode[] = [contents, l.builtAt && `index written ${when(l.builtAt)}`, l.error].filter(Boolean)
      rows.push({
        key: l.id, name: l.label, kind: 'GUFI index over ssh, read only',
        where: (
          <DescriptionList className="lib-paths">
            <DescriptionListItem label="Index" mono copy>{`${dest}:${l.indexRoot ?? ''}`}</DescriptionListItem>
            {l.port ? <DescriptionListItem label="Port" mono>{String(l.port)}</DescriptionListItem> : null}
            {l.sourceRoot
              ? <DescriptionListItem label="Files" mono copy>{`${dest}:${l.sourceRoot}`}</DescriptionListItem>
              : <DescriptionListItem label="Files"><span className="muted">none: names and metadata only</span></DescriptionListItem>}
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
          <DescriptionList className="lib-paths">
            <DescriptionListItem label="Index" mono copy>{`${l.site}:${l.indexRoot ?? ''}`}</DescriptionListItem>
            {l.sourceRoot
              ? <DescriptionListItem label="Files" mono copy>{`${l.site}:${l.sourceRoot}`}</DescriptionListItem>
              : <DescriptionListItem label="Files"><span className="muted">none: names and metadata only</span></DescriptionListItem>}
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
        <DescriptionList className="lib-paths">
          <DescriptionListItem label="Index" mono={!!l.indexRoot} copy={!!l.indexRoot}>
            {l.indexRoot ? `${l.site}:${l.indexRoot}` : <span className="muted">being built</span>}
          </DescriptionListItem>
          <DescriptionListItem label="Files" mono copy>{`${l.site}:${l.sourceRoot ?? l.sourceDir}`}</DescriptionListItem>
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

  const primary = libs?.find(l => l.primary)
  const primaryPath = standalone?.primary?.path ?? primary?.sourceRoot ?? null
  // pw ssh is a choice wherever it is offered; the form says why when it cannot be used yet.
  const options = { buildHere: canBuild, ssh: user.ssh, pw: user.pwOffered, workflow: user.workflow && !!cfg.platform && user.available }

  return (
    <>
      <h1>Libraries</h1>
      <p className="muted view-sub">
        Every library searched here. The main folder is the only one written to; the others are indexes read where
        they are, built here or connected as they are.
      </p>
      {!libs ? <Loader inline size={16} label="Loading…" /> : (
        <Table density="compact" className="libraries-table" wrapperClassName="rag-calls-wrap libraries-wrap">
          <Table.Header>Library</Table.Header>
          <Table.Header>State</Table.Header>
          <Table.Header>Full text</Table.Header>
          <Table.Header>Meaning</Table.Header>
          <Table.Header />
          {rows.map(r => (
            <Fragment key={r.key}>
              <tr className={r.detail ? 'lib-has-detail' : undefined}>
                <Table.Item wrap>
                  <div className="lib-name">{r.name}</div>
                  <div className="lib-id">{r.kind}</div>
                  {r.where}
                </Table.Item>
                <Table.Item wrap>
                  <StatusBadge variant={r.state.tone} pulse={r.state.pulse} dot={!r.state.pulse} size="sm">{r.state.text}</StatusBadge>
                  {r.state.notes.map((n, i) => <div key={i} className="lib-id">{n}</div>)}
                </Table.Item>
                <Table.Item className="lib-cap" data-label="Full text"><Mark on={r.fullText} /></Table.Item>
                <Table.Item className="lib-cap" data-label="Meaning">
                  {r.level
                    ? <MeaningLevelSelect library={r.name} value={r.level.value} disabled={busy !== null}
                        onChange={v => { const lv = r.level!; void act(r.key, async () => { await api.setLibraryMeaning(r.key, v); await lv.reload(); reloadAppConfig() },
                          `Search by meaning in ${r.name}: ${meaningPhrase(v)}. It applies at the next build or sync; Rebuild applies it now.`) }} />
                    : <Mark on={r.meaning} />}
                </Table.Item>
                <Table.Item className="lib-actions">{r.actions}</Table.Item>
              </tr>
              {r.detail && <tr className="lib-detail"><Table.Item colSpan={5} wrap>{r.detail}</Table.Item></tr>}
            </Fragment>
          ))}
        </Table>
      )}
      {libs && (
        <p className="muted lib-meaning-note">
          Meaning is search by meaning: passages that say the same thing in other words. For a library {cfg.appName} builds it
          is a choice: Off (names and words only, fastest), Documents (documents and images), or Full (every file with text,
          about three times the indexing time of Documents). A change applies at the next build or sync. An index connected as
          it is uses the vectors it has.
        </p>
      )}

      {!adding && (
        <div className="query-actions lib-add">
          <Button variant="primary" icon={<AddIcon />} onClick={() => setAdding(true)}>Add a library</Button>
        </div>
      )}

      {adding && (
        <section className="lib-add-form" aria-label="Add a library">
          <div className="lib-add-head">
            <div className="tool-group">Add a library</div>
            <IconButton icon={<CloseIcon />} label="Close" size="sm" onClick={() => setAdding(false)} />
          </div>
          <AddLibrary options={options} browse={!!standalone}
            onAdded={(lib, started) => added(started
              ? `Building ${lib.label}. Its progress shows in the list; it is searchable once the build is done.`
              : `Added ${lib.label}. It is in the Library switcher.`)}
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

/** Yes or no for a capability, as a word a screen reader says and a color the eye finds. */
function Mark({ on }: { on: boolean }) {
  return <span className={on ? 'lib-yes' : 'lib-no'}>{on ? 'Yes' : 'No'}</span>
}
