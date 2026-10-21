import { Fragment, type ReactNode, useEffect, useRef, useState } from 'react'
import {
  Button, Callout, DescriptionList, DescriptionListItem, IconButton, Loader, MethodButton, Modal, Popover, StatusBadge, Table, toast,
} from '@parallelworks/ui'
import { AddIcon, CloseIcon, ClusterIcon, DesktopIcon, TerminalIcon } from '@parallelworks/ui/icons'
import { api, type SettingsLibrary, type UserLibrary } from '../api'
import { reloadAppConfig, useAppConfig } from '../config'
import { ClusterLibraryForm } from './ClusterLibraryForm'
import { LocalLibraryForm } from './LocalLibraryForm'
import { FolderPicker, IndexBuildProgress, errText, json, useIndexBuild, type SetupStatus } from './PrimaryFolder'
import { SshLibraryForm } from './SshLibraryForm'

/** A user library as listed, with how it is reached. */
type UserLib = UserLibrary & { transport?: 'pw' | 'ssh'; destination?: string | null; port?: number | null }
interface UserList { libraries: UserLib[]; available: boolean; ssh: boolean }

/** The kinds of library a user can add, as the Add a library menu offers them. */
export type LibraryKind = 'local' | 'ssh' | 'cluster'
const KINDS: Record<LibraryKind, { label: string; description: string; icon: ReactNode }> = {
  local: { label: 'A GUFI index on this machine', description: 'An index already built here. Read only.', icon: <DesktopIcon /> },
  ssh: { label: 'A GUFI index on another machine over ssh', description: 'Read with your own ssh keys. Nothing is written there.', icon: <TerminalIcon /> },
  cluster: { label: 'An index built on a cluster', description: 'Built on one of your clusters with your own key. Only you see it.', icon: <ClusterIcon /> },
}

/** The kinds this install supports: an index on this machine always, one
 *  over ssh where the server allows it (sign-in off), and one built on a
 *  cluster only where the Studio reaches a platform. */
export function libraryKinds(opts: { ssh: boolean; platform: boolean; userLibraries: boolean }): LibraryKind[] {
  return ['local', ...(opts.ssh ? ['ssh' as const] : []), ...(opts.platform && opts.userLibraries ? ['cluster' as const] : [])]
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
  actions: ReactNode
  /** A line under the row across the whole table, such as a build's progress. */
  detail?: ReactNode
}

const when = (iso: string | null) => (iso ? new Date(iso).toLocaleString() : '')
const counts = (dirs: number | null, files: number | null) =>
  dirs != null ? `${dirs.toLocaleString()} folders, ${(files ?? 0).toLocaleString()} files` : null

/**
 * Settings > Libraries: one list of every library the Studio searches, and
 * one control to add another. The main folder is the one the Studio indexes
 * itself and writes to; on a single-user install it can be changed and its
 * index rebuilt here. Every other library is read only: a GUFI index on
 * this machine (/api/libraries), one on another machine over ssh, or one
 * built on a cluster through the platform (both /api/user-libraries).
 */
export function LibrariesSection({ pollMs = 1000 }: { pollMs?: number }) {
  const cfg = useAppConfig()
  const [libs, setLibs] = useState<SettingsLibrary[] | null>(null)
  const [loadError, setLoadError] = useState('')
  const [user, setUser] = useState<UserList>({ libraries: [], available: false, ssh: false })
  const [status, setStatus] = useState<SetupStatus | null>(null)
  const [busy, setBusy] = useState<string | null>(null)
  const [adding, setAdding] = useState<LibraryKind | null>(null)
  const [menuOpen, setMenuOpen] = useState(false)
  const [changing, setChanging] = useState(false)
  const [restarting, setRestarting] = useState(false)
  const ready = useRef(new Set<string>())

  const loadLibs = () => api.libraries().then(r => { setLibs(r.libraries); setLoadError('') }).catch(e => setLoadError(errText(e)))
  // The list reloads every 15s during a cluster build; one toast stands for
  // a failure that repeats rather than stacking another each time.
  const loadUser = () => api.userLibraries().then(r => {
    const list = r as Partial<UserList>
    const libraries = list.libraries ?? []
    setUser({ libraries, available: !!list.available, ssh: !!list.ssh })
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
  const build = useIndexBuild({ pollMs, onDone: async () => { await Promise.all([loadLibs(), loadStatus()]); reloadAppConfig() } })

  useEffect(() => {
    void loadLibs()
    void loadUser()
    // A build started elsewhere, such as in the setup wizard.
    void loadStatus().then(s => { if (s?.job) void build.follow(s.job) })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  // Follow running cluster builds.
  const running = user.libraries.some(l => l.state === 'building' || l.state === 'updating')
  useEffect(() => {
    if (!running) return
    const t = window.setInterval(() => { void loadUser() }, 15_000)
    return () => window.clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [running])

  const act = async (key: string, fn: () => Promise<unknown>, done?: string) => {
    setBusy(key)
    try { await fn(); if (done) toast.success(done) } catch (e) { toast.error(errText(e)) } finally { setBusy(null) }
  }
  const added = (message: string) => {
    toast.success(message)
    setAdding(null)
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
      let state: Row['state']
      if (build.building) state = { tone: 'info', text: 'Indexing', pulse: true, notes }
      else if (p && !p.exists) state = { tone: 'error', text: 'Folder not found', notes }
      else if (p ? p.indexed : l.caps.index) {
        state = { tone: 'success', text: 'Indexed', notes }
        if (p?.files !== undefined) notes.push(`${p.files.toLocaleString()} files`)
      } else state = { tone: 'warning', text: 'Not indexed yet', notes }
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
        actions: standalone && (
          <>
            <Button variant="secondary" size="sm" disabled={build.building} onClick={() => setChanging(true)}>Change folder</Button>
            <Button variant="secondary" size="sm" loading={build.building} disabled={build.building || !gufi?.found || !p?.exists}
              onClick={() => void build.start()}>
              {p?.indexed ? 'Rebuild index' : 'Build index'}
            </Button>
          </>
        ),
        detail: (build.job || build.error) ? <div className="lib-build"><IndexBuildProgress job={build.job} error={build.error} /></div> : undefined,
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

  const kinds = libraryKinds({ ssh: user.ssh, platform: !!cfg.platform, userLibraries: user.available })
  const primary = libs?.find(l => l.primary)
  const primaryPath = standalone?.primary?.path ?? primary?.sourceRoot ?? null
  const addButton = (
    <Button variant="primary" icon={<AddIcon />} onClick={kinds.length === 1 ? () => setAdding(kinds[0]) : undefined}>Add a library</Button>
  )

  return (
    <>
      <h1>Libraries</h1>
      <p className="muted view-sub">
        Every library searched here. The main folder is the only one written to; the others are indexes read where
        they are.
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
                <Table.Item className="lib-cap" data-label="Meaning"><Mark on={r.meaning} /></Table.Item>
                <Table.Item className="lib-actions">{r.actions}</Table.Item>
              </tr>
              {r.detail && <tr className="lib-detail"><Table.Item colSpan={5} wrap>{r.detail}</Table.Item></tr>}
            </Fragment>
          ))}
        </Table>
      )}

      <div className="query-actions lib-add">
        {kinds.length > 1 ? (
          <Popover trigger={addButton} open={menuOpen} onOpenChange={setMenuOpen} align="start" initialFocus="popup"
            aria-label="Kinds of library" className="lib-kinds">
            {kinds.map(k => (
              <MethodButton key={k} icon={KINDS[k].icon} label={KINDS[k].label} description={KINDS[k].description}
                onClick={() => { setAdding(k); setMenuOpen(false) }} />
            ))}
          </Popover>
        ) : addButton}
      </div>

      {adding && (
        <section className="lib-add-form" aria-label={KINDS[adding].label}>
          <div className="lib-add-head">
            <div className="tool-group">{KINDS[adding].label}</div>
            <IconButton icon={<CloseIcon />} label="Close" size="sm" onClick={() => setAdding(null)} />
          </div>
          {adding === 'local' && <LocalLibraryForm onAdded={lib => added(`Added ${lib.label}. It is in the Library switcher.`)} />}
          {adding === 'ssh' && <SshLibraryForm onAdded={lib => added(`Added ${lib.label}. It is in the Library switcher.`)} />}
          {adding === 'cluster' && (
            <ClusterLibraryForm onStarted={() => added('Building the index on the cluster. It appears in the Library switcher when it is ready.')} />
          )}
        </section>
      )}

      {standalone && (
        <Modal open={changing} onClose={() => { if (!restarting) setChanging(false) }} dismissible={!restarting} size="lg"
          title="Change the main folder"
          description={`${cfg.appName} restarts to open the folder you choose. Build its index afterwards from this page.`}
          footer={<Button variant="secondary" disabled={restarting} onClick={() => setChanging(false)}>Close</Button>}>
          <div style={{ display: 'flex', flexDirection: 'column', gap: 12 }}>
            <FolderPicker current={primaryPath} start={standalone.primary?.exists && primaryPath ? primaryPath : '~'}
              disabled={build.building} pollMs={pollMs} onRestarting={setRestarting}
              onChanged={async () => {
                build.reset()
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
