import { Suspense, lazy, useEffect, useMemo, useRef, useState, type ReactElement } from 'react'
import { Button, Callout, Checkbox, Field, IconButton, Input, Popover, SegmentedControl, Select, toast } from '@parallelworks/ui'
import { ChevronDownIcon, FolderIcon, HelpIcon } from '@parallelworks/ui/icons'
import { SIGN_IN_TO_USE, needsActivate } from '../accounts'
import {
  api, type FolderCount, type FolderShare, type IndexDepth, type PwSystems, type SettingsLibrary, type SpaceAsk, type SpaceCheck, type SpaceStatus, type UserLibrary,
} from '../api'
import { useAppConfig } from '../config'
import { plural } from '../plural'
import { sshOptionsProblem } from '../sshOptions'
import { BucketLibraryForm } from './BucketLibraryForm'
import { ClusterLibraryForm } from './ClusterLibraryForm'
import { FolderCheckCard, countLine, stepWarnings, type CheckWarning } from './FolderCheckCard'
import { DEFAULT_LIBRARY_DEPTH, DEFAULT_REMOTE_LIBRARY_DEPTH, DEPTHS, depthHelp, readsImages, useOcrHere } from './IndexDepth'
import { CreateConfirm, postJson, type CheckStep } from './LibraryFormParts'
import { LibraryImport } from './LibraryImport'
import { useFolderSuggestions, type FolderSuggestion } from './LocalLibraryForm'
import { NeedsActivate } from './NeedsActivate'
import { PwBucketForm } from './PwBucketForm'
import { PlatformSignInPanel } from './PlatformSignInPanel'
import { SharedWithMe } from './SharedWithMe'
import { PwBlocked } from './SshLibraryForm'
import { SystemChip } from './SystemChip'
import { TargetIcon, addChoices, firstTarget, targetKey, targetMenu, targetName, type AddOptions, type PwBuckets, type Target } from './addLibraryTarget'
import { useCheck } from './useCheck'

export type { AddOptions } from './addLibraryTarget'

/**
 * Add a library, in two steps: where (a computer and a folder on it), and
 * a short review with defaults (docs/LIBRARIES.md, What a library carries,
 * access).
 *
 * - The computer: this one (sign-in off: with sign-in on, the libraries here
 *   are the deployment's), one of the viewer's clusters over pw ssh, a host
 *   over plain ssh on a Studio one person uses, one of their ACTIVATE
 *   buckets, read with their own sign-in, or a bucket of an rclone remote.
 *   A folder on a remote computer is always listed, checked, indexed
 *   and read as the viewer, with their own sign-in or ssh keys; here and in
 *   a bucket, with the account the Studio runs as.
 * - The folder is checked by itself once chosen, and the check is cancelled
 *   when the choice changes. What it found is one card (FolderCheckCard).
 * - The review: a name, who sees it, and the index depth, each with a
 *   default; the rest is under More options.
 *
 * An index that is there already is connected through the same two steps,
 * read only. The platform's index workflow is offered only where it is the
 * one way to build (addChoices, workflowOnly); libraries it built before are
 * refreshed and removed as always.
 */

const FolderChoice = lazy(() => import('./addLibraryFolder'))

/** A library's name from its folder, as the server names one not named. */
export const folderName = (dir: string): string => {
  const name = dir.replace(/\/+$/, '').split('/').pop() ?? ''
  return !name || name === '~' || name === '.' ? 'Home folder' : name
}

type Kind = FolderShare['kind']
type Access = FolderShare['access']
const KIND_LABEL: Record<Kind, string> = { user: 'A person', group: 'A group', everyone: 'Everyone' }

interface IndexCheck { ok: boolean; steps: CheckStep[]; directories?: number; files?: number; fullText?: boolean; vectors?: boolean }
interface BuildCheck { ok: boolean; steps: CheckStep[]; create: string[] }
type AnyCheck = Partial<SpaceCheck> & Partial<IndexCheck> & { ok: boolean; steps: CheckStep[] }

const EMPTY = { indexRoot: '', sourceRoot: '', gufiBin: '', sshOptions: '', id: '' }
const say = (e: unknown) => String((e as Error)?.message ?? e)

export function AddLibrary({ options, browse = false, onAdded, onStarted, onImported }: {
  options: AddOptions
  /** Suggested folders on this computer are offered (a single-user install). */
  browse?: boolean
  /** A library was added, or its build started (`started`); `here` is a
   *  build on this computer, searchable by name while its text and meaning
   *  are read. */
  onAdded: (lib: { id: string; label: string }, started: boolean, here?: boolean, count?: number) => void
  /** A build through the platform's index workflow started. */
  onStarted?: (lib: UserLibrary) => void
  /** Offers importing a library list in place of the form; called with
   *  how many were added once the import is done. */
  onImported?: (added: number) => void
}) {
  const [importing, setImporting] = useState(false)
  const flow = useRef<HTMLDivElement>(null)
  // A choice that shows a shorter form would shorten the page, and where
  // the form ends the page the browser scrolls up to fit, moving the
  // choices under the pointer: the form keeps the tallest height it had.
  const holdHeight = () => {
    const el = flow.current
    if (el) el.style.minHeight = `${Math.max(el.offsetHeight, parseFloat(el.style.minHeight) || 0)}px`
  }

  const asksSystems = options.pw || !!options.space
  const [systems, setSystems] = useState<PwSystems | null>(null)
  const [round, setRound] = useState(0)
  useEffect(() => {
    if (!asksSystems) return
    let live = true
    api.pwSystems().then(r => { if (live) setSystems(r) }, e => { if (live) setSystems({ usable: true, why: null, systems: [], error: say(e) }) })
    return () => { live = false }
  }, [asksSystems, round])
  const [buckets, setBuckets] = useState<PwBuckets>({ buckets: [], loading: true })
  useEffect(() => {
    if (!options.buckets) return
    let live = true
    api.pwBuckets().then(
      r => { if (live) setBuckets({ buckets: r.buckets ?? [], available: r.available, why: r.why, loading: false }) },
      e => { if (live) setBuckets({ buckets: [], why: say(e), loading: false }) })
    return () => { live = false }
  }, [options.buckets, round])
  const [hosts, setHosts] = useState<string[]>([])
  useEffect(() => {
    if (!options.ssh) return
    let live = true
    api.sshHosts().then(r => { if (live) setHosts(r.hosts ?? []) }, () => {})
    return () => { live = false }
  }, [options.ssh])

  const offered = addChoices(options, systems)
  const [picked, setPicked] = useState<Target | null>(null)
  const target = picked ?? firstTarget(options, systems)
  const [mode, setMode] = useState<'build' | 'connect'>('build')
  const connect = mode === 'connect' && offered.connect(target)
  const [naming, setNaming] = useState<'ssh' | 'cluster' | null>(null)
  const pick = (t: Target) => { holdHeight(); setNaming(null); setPicked(t) }

  if (importing) {
    return (
      <div className="lib-add-flow">
        <div><Button variant="ghost" size="sm" onClick={() => setImporting(false)}>Back to adding one library</Button></div>
        <p className="muted lib-add-about">A library list exported from another Studio, such as one your administrator shares. Each library is checked and added as this form adds it.</p>
        <LibraryImport onDone={n => onImported?.(n)} />
      </div>
    )
  }
  const importLink = onImported && <Button variant="ghost" size="sm" onClick={() => setImporting(true)}>Import a library list from a file…</Button>

  if (offered.workflowOnly) {
    return <div className="lib-add-flow" ref={flow}><WorkflowOnly onStarted={onStarted} />{importLink && <div className="lib-where-below">{importLink}</div>}</div>
  }

  const chosen = targetName(target, systems, buckets.buckets)
  const chip = chosen || (naming === 'ssh' ? 'Another host…' : options.ssh || options.buildHere ? 'Choose a computer' : options.buckets ? 'Choose a place' : 'Choose a cluster')
  const items = targetMenu({ options, systems, buckets, hosts, target, onPick: pick, onName: k => { holdHeight(); setNaming(k) } })
  // Buckets can be chosen while the clusters are still being found.
  const finding = offered.finding && !target && !naming && !buckets.buckets.length
  return (
    <div className="lib-add-flow" ref={flow}>
      <FolderForm key={`${targetKey(target)}/${connect ? 'connect' : 'build'}`} target={naming ? null : target} place={chosen} connect={connect}
        options={options} browse={browse} systems={systems} buckets={buckets} holdHeight={holdHeight}
        onSignedIn={() => setRound(n => n + 1)}
        chip={(
          <SystemChip label={chosen && !naming ? `${target?.kind === 'bucket' || target?.kind === 'pwbucket' ? 'Bucket' : 'Computer'}: ${chosen}` : options.buckets ? 'Choose a computer or a bucket' : 'Choose a computer'}
            text={naming ? (naming === 'ssh' ? 'Another host…' : 'Another cluster…') : finding ? 'Finding your clusters…' : chip}
            icon={target && !naming ? <TargetIcon target={target} systems={systems} /> : undefined}
            items={items} finding={finding} search={hosts.length + (systems?.systems?.length ?? 0) + buckets.buckets.length > 10}
            testId="add-library-computer" findingTestId="add-library-finding" />
        )}
        naming={naming && (
          <NameField key={naming} kind={naming} onCancel={() => setNaming(null)}
            onName={name => pick(naming === 'ssh' ? { kind: 'ssh', destination: name } : { kind: 'cluster', name })} />
        )}
        below={(
          <>
            {offered.connect(target) && !naming && (
              <Button variant="ghost" size="sm" onClick={() => { holdHeight(); setMode(connect ? 'build' : 'connect') }}>
                {connect ? 'Index a folder instead' : 'Connect an existing index'}
              </Button>
            )}
            {importLink}
            <span className="lib-help-at"><AddHelp options={options} clusters={offered.clusters || offered.blocked} /></span>
          </>
        )}
        onAdded={onAdded} />
    </div>
  )
}

/** The host or cluster typed by name, taken on Enter or when it is left. */
function NameField({ kind, onName, onCancel }: { kind: 'ssh' | 'cluster'; onName: (name: string) => void; onCancel: () => void }) {
  const [name, setName] = useState('')
  const use = () => (name.trim() ? onName(name.trim()) : onCancel())
  return (
    <Input size="sm" mono autoFocus autoComplete="off" autoCapitalize="off" spellCheck={false} value={name}
      aria-label={kind === 'ssh' ? 'Host' : 'Cluster'} placeholder={kind === 'ssh' ? 'user@host or user@host:port' : 'my-cluster'}
      onChange={e => setName(e.target.value)} onBlur={use}
      onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); use() } else if (e.key === 'Escape') { e.stopPropagation(); onCancel() } }} />
  )
}

/** What the form does not say: whose account reads, what is left where, and
 *  the other ways in. */
function AddHelp({ options, clusters }: { options: AddOptions; clusters: boolean }) {
  const remote = clusters || options.ssh
  return (
    <Popover align="end" className="lib-help" aria-label="About adding a library"
      trigger={<IconButton icon={<HelpIcon />} label="About adding a library" size="sm" variant="ghost" />}>
      {options.buildHere && <p><b>This computer</b> is read with the account the Studio runs as. The index follows the folder as it changes, and the folder is read only here.{options.home ? ` To read it as your own account instead, choose ${options.home}.` : ''}</p>}
      {remote && <p><b>A remote computer</b> is read as you, with {[...(clusters ? ['your ACTIVATE sign-in (or the API key you added under Settings, Accounts)'] : []), ...(options.ssh ? ['your own ssh keys, agent and ~/.ssh/config'] : [])].join(', or ')}. The index stays there, in your ~/.activate-studio. The Library writes there as you where your account may.</p>}
      <p><b>Index depth</b> says what search covers. Search by meaning is the slowest part and runs last: names are searchable first, then words.</p>
      {offeredConnect(options) && <p><b>An existing index</b> is one GUFI made already. It is searched as it is, and never written to.</p>}
      {options.buckets && <p><b>An ACTIVATE bucket</b> is one you can read on the platform. It is listed and read with your own ACTIVATE sign-in, with no rclone and no keys to enter: searched by name, size and time, from its listing, and an object is read only when you open it.</p>}
      {options.bucket && <p><b>{options.buckets ? 'Another bucket' : 'A bucket'}</b> is of an rclone remote set up on this computer: any S3 compatible store, AWS or on premises. It is searched by name, size and time, from its listing, and an object is read only when you open it.</p>}
      {options.copy && <p>A host that takes SFTP only, or a cloud drive: <a href="#view=transfers">Transfers</a> keeps a local copy of a folder there and indexes it.</p>}
    </Popover>
  )
}
const offeredConnect = (o: AddOptions) => o.buildHere || o.ssh || o.pw

/** Where the platform's index workflow is the only way to build. */
function WorkflowOnly({ onStarted }: { onStarted?: (lib: UserLibrary) => void }) {
  const [more, setMore] = useState(true)
  return (
    <>
      <p className="muted lib-add-about">This Studio cannot reach your clusters over pw ssh, so a folder on one is indexed by a job of the platform&apos;s index workflow, with your own key.</p>
      <MoreOptions open={more} onToggle={() => setMore(m => !m)}>
        <ClusterLibraryForm compact onStarted={lib => onStarted?.(lib)} />
      </MoreOptions>
    </>
  )
}

// What opened is below the fold of a short dialog.
const intoView = (el: HTMLDivElement | null) => { el?.scrollIntoView?.({ block: 'nearest' }) }

function MoreOptions({ open, onToggle, children }: { open: boolean; onToggle: () => void; children: ReactElement | ReactElement[] }) {
  return (
    <div className="lib-more">
      <Button variant="ghost" size="sm" aria-expanded={open} onClick={onToggle}>{open ? 'Fewer options' : 'More options'}</Button>
      {open && <div className="lib-more-body" ref={intoView}>{children}</div>}
    </div>
  )
}

/** The field that opens a computer's folders, with the whole path chosen. */
function FolderField({ place, sshOptions, value, empty, label, anew, onPick }: {
  place: Target
  sshOptions?: string
  value: string
  empty: string
  label: string
  anew?: boolean
  onPick: (path: string) => void
}) {
  const trigger = (
    <button type="button" className="lib-where-folder" aria-label={value ? `${label}: ${value}` : label} title={value || undefined} data-testid="add-library-folder">
      <FolderIcon aria-hidden className="code-chip-icon" />
      <span className={`lib-where-path${value ? ' code-mono' : ' muted'}`}>{value ? <bdi>{value}</bdi> : empty}</span>
      <ChevronDownIcon aria-hidden className="code-chip-caret" />
    </button>
  )
  return (
    <Suspense fallback={trigger}>
      <FolderChoice place={place} sshOptions={sshOptions} value={value} label={label} trigger={trigger} anew={anew} onPick={onPick} />
    </Suspense>
  )
}

/** An optional path under More options, typed or chosen from the folders. */
function PathOption({ label, description, placeholder, place, value, onChange }: {
  label: string; description: string; placeholder: string; place: Target; value: string; onChange: (v: string) => void
}) {
  return (
    <div className="lib-path-option">
      <Input label={label} hint="optional" size="sm" mono spellCheck={false} placeholder={placeholder} description={description} value={value} onChange={e => onChange(e.target.value)} />
      <Suspense fallback={null}>
        <FolderChoice place={place} value={value} label={`Choose the ${label.toLowerCase()}`} anew onPick={onChange}
          trigger={<button type="button" className="code-chip" aria-label={`Browse for the ${label.toLowerCase()}`}><FolderIcon aria-hidden className="code-chip-icon" />Browse</button>} />
      </Suspense>
    </div>
  )
}

/**
 * The folder on the computer chosen, its check, and the review under it. One
 * per computer and per way of adding, so nothing chosen for one is carried
 * to another.
 */
function FolderForm({ target, place, connect, options, browse, systems, buckets, holdHeight, chip, naming, below, onSignedIn, onAdded }: {
  target: Target | null
  place: string
  connect: boolean
  options: AddOptions
  browse: boolean
  systems: PwSystems | null
  buckets: PwBuckets
  holdHeight: () => void
  chip: ReactElement
  naming: ReactElement | null
  below: ReactElement
  /** The person signed in here: what their sign-in reaches is read again. */
  onSignedIn: () => void
  onAdded: (lib: { id: string; label: string }, started: boolean, here?: boolean, count?: number) => void
}) {
  const signIn = !!useAppConfig().signIn
  const here = target?.kind === 'here'
  const cluster = target?.kind === 'cluster' ? target.name : ''
  const ssh = target?.kind === 'ssh' ? target.destination : ''
  const remote = !!cluster || !!ssh
  const offered = addChoices(options, systems)

  // A cluster is reached with the viewer's sign-in: whether they have one,
  // and the folder offered first for their My library.
  const [status, setStatus] = useState<SpaceStatus | null>(null)
  const [statusError, setStatusError] = useState('')
  const [statusRound, setStatusRound] = useState(0)
  const asksStatus = (!!cluster && !connect) || offered.blocked
  useEffect(() => {
    if (!asksStatus) return
    let live = true
    api.spaceStatus().then(s => { if (live) { setStatus(s); setStatusError('') } }, e => { if (live) setStatusError(say(e)) })
    return () => { live = false }
  }, [asksStatus, statusRound])
  const signedIn = () => { setError(''); setStatusRound(n => n + 1); onSignedIn() }

  const [typedFolder, setFolder] = useState<string | null>(null)
  const [form, setForm] = useState(EMPTY)
  const set = (patch: Partial<typeof EMPTY>) => { setForm(f => ({ ...f, ...patch })); setError('') }
  const [typedName, setName] = useState<string | null>(null)
  const [depth, setDepth] = useState<IndexDepth>(here ? DEFAULT_LIBRARY_DEPTH : DEFAULT_REMOTE_LIBRARY_DEPTH)
  const [who, setWho] = useState<'me' | 'shared'>('me')
  const [kind, setKind] = useState<Kind>('user')
  const [account, setAccount] = useState('')
  const [access, setAccess] = useState<Access>('read')
  const [myPicked, setMy] = useState<boolean | null>(null)
  const [more, setMore] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [confirming, setConfirming] = useState(false)
  const ocr = useOcrHere(here && !connect)
  const [suggestions, reloadSuggestions] = useFolderSuggestions(here && !connect && browse)
  const [suggested, setSuggested] = useState<FolderSuggestion | null>(null)

  // My library is offered for a folder only the viewer sees, where people
  // sign in, and is chosen to begin with for someone who has none.
  const mine = signIn && !!cluster && !connect && (myPicked ?? (!!status?.available && !status.spaces.length))
  const folder = (typedFolder ?? (mine && who === 'me' ? status?.suggested ?? '' : '')).trim()
  const sshOptions = ssh ? form.sshOptions.trim() : ''
  const sshProblem = sshOptions ? sshOptionsProblem(sshOptions) : null
  const trimmed = { indexRoot: form.indexRoot.trim(), sourceRoot: form.sourceRoot.trim(), gufiBin: form.gufiBin.trim(), id: form.id.trim() }
  const reach = cluster ? { cluster } : { destination: ssh, ...(sshOptions ? { sshOptions } : {}) }

  // What is asked, for each way of adding; the check runs whenever it changes.
  const asked = useMemo(() => {
    const given = (o: Record<string, string>) => Object.fromEntries(Object.entries(o).filter(([, v]) => v))
    if (here) {
      return connect
        ? { url: '/api/libraries/check', body: { indexRoot: folder, ...given({ sourceRoot: trimmed.sourceRoot }) } }
        : { url: '/api/libraries/build/check', body: { sourceRoot: folder, ...given({ indexRoot: trimmed.indexRoot, id: trimmed.id }) } }
    }
    return connect
      ? { url: `/api/user-libraries/${cluster ? 'pw' : 'ssh'}/test`, body: { ...reach, indexRoot: folder, ...given({ sourceRoot: trimmed.sourceRoot, gufiBin: trimmed.gufiBin }) } }
      : { url: '/api/user-libraries/space/test', body: { ...reach, sourceRoot: folder, ...given({ indexRoot: trimmed.indexRoot, gufiBin: trimmed.gufiBin }) } }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [here, connect, cluster, ssh, sshOptions, folder, trimmed.indexRoot, trimmed.sourceRoot, trimmed.gufiBin, trimmed.id])
  const gone = !!cluster && !connect && !!status && !status.available
  const checks = (here || remote) && !!folder && !sshProblem && !gone && !naming
  const check = useCheck<AnyCheck>(checks ? JSON.stringify(asked) : null, signal => postJson<AnyCheck>(asked.url, { ...asked.body, ...(here && !connect ? { depth } : {}) }, signal))
  const counted = useCheck<{ count: FolderCount | null }>(checks && !connect ? JSON.stringify([here, reach, folder]) : null, signal =>
    postJson(here ? '/api/libraries/build/count' : '/api/user-libraries/space/count', here ? { sourceRoot: folder } : { ...reach, sourceRoot: folder }, signal))

  if (target?.kind === 'bucket') {
    return <><Where chip={chip} naming={naming} below={below} /><BucketLibraryForm onAdded={lib => onAdded(lib, true, true)} /></>
  }

  if (target?.kind === 'pwbucket') {
    return (
      <>
        <Where chip={chip} naming={naming} below={below} folder={<span className="lib-where-none muted" title={target.uri}><FolderIcon aria-hidden className="code-chip-icon" />{target.uri}</span>} />
        <PwBucketForm uri={target.uri} buckets={buckets.buckets} holdHeight={holdHeight} onAdded={(lib, count) => onAdded(lib, true, true, count)} />
      </>
    )
  }

  // Clusters that cannot be reached yet: the sign-in, or what is missing.
  if (!target && offered.blocked && (!status || status.available || !status.cluster)) {
    return <><Where chip={offered.buckets ? chip : null} naming={null} below={below} /><PwBlocked why={systems?.why} /></>
  }
  if (status && !status.available && (gone || (!target && offered.blocked))) {
    return (
      <>
        <Where chip={gone || offered.buckets ? chip : null} naming={naming} below={below} />
        {status.cluster && status.signInOffered && !status.credential ? (
          <div className="space-sign-in">
            <Callout tone="info" title={SIGN_IN_TO_USE}>
              A folder on {status.cluster} is indexed and read as your own account. Sign in with ACTIVATE here and approve
              the code on the platform, then choose the folder. The sign-in is kept under Settings, Accounts.
            </Callout>
            <PlatformSignInPanel onChanged={signedIn} />
          </div>
        ) : needsActivate(status.why)
          ? <NeedsActivate>{status.why}</NeedsActivate>
          : <Callout tone="info" title="A folder on a remote computer is not available">{status.why}</Callout>}
      </>
    )
  }

  const found = check.result
  const passed = !!found?.ok
  const failing = found && !found.ok ? found.steps.find(s => !s.ok)?.detail ?? 'The check did not pass.' : ''
  const blocked = check.error || failing
  const refused = check.error.startsWith('The platform refused') || error.startsWith('The platform refused')
  const create = found?.create ?? []
  const isNew = !connect && create.some(c => c === found?.sourceRoot || c === folder)
  const meaning = depth.startsWith('meaning')

  const warnings: CheckWarning[] = !passed ? [] : [
    ...stepWarnings(found.steps),
    ...(remote && !connect && (found.readOnly || found.writable === false)
      ? [{ short: 'Read only for you', detail: found.readOnly ?? 'Your account cannot write in the folder itself, so the Library writes only in what it holds that your account can change.' }] : []),
    ...(!connect && remote && meaning && found.embedMB ? [{ short: `Search by meaning sends its model there (about ${found.embedMB} MB, once)`, detail: 'The first build sends the Studio\'s embedding model and its packages to ~/.activate-studio/embed there, as you. Names and words are searchable first.' }] : []),
    ...(!connect && remote && meaning && !found.embedMB && found.embedWhy ? [{ short: 'No search by meaning there', detail: found.embedWhy }] : []),
    ...(!connect && here && ocr === false && readsImages(depth) ? [{ short: 'No text from images: tesseract is not installed here', detail: 'Images and scanned pages are found by name until it is.' }] : []),
  ]
  const facts = connect
    ? (passed ? [found.directories !== undefined ? plural(found.directories, 'folder') : '', found.files !== undefined ? plural(found.files, 'file') : '',
        found.vectors ? 'searched by words and meaning' : found.fullText ? 'searched by words' : here ? 'searched by name' : ''].filter(Boolean).join(' · ') : undefined)
    : counted.result?.count ? countLine(counted.result.count) : counted.result || counted.error ? 'Not counted' : undefined

  const noShare = !cluster ? '' : passed && !found.canShare ? found.shareWhy ?? '' : ''
  const shared = who === 'shared' && !!cluster && !noShare
  const noACL = passed && !!found.owned && found.acl === false
  const kinds: Kind[] = noACL ? ['everyone'] : ['user', 'group', 'everyone']
  const shownKind: Kind = kinds.includes(kind) ? kind : kinds[0]
  const shownAccess: Access = shownKind === 'everyone' ? 'read' : access
  const noMy = passed ? found.myLibraryWhy ?? null : null
  const myLibrary = mine && !shared && !noMy
  const ready = passed && !sshProblem && (!shared || shownKind === 'everyone' || !!account.trim())
  const prefilled = myLibrary ? 'My library' : folder ? suggested && suggested.path === folder ? suggested.label : folderName(folder) : ''
  const name = typedName ?? prefilled
  // Not typed, the server names it as the form shows.
  const label = typedName?.trim() || (suggested && suggested.path === folder && !myLibrary ? suggested.label : undefined)

  const choose = (path: string) => { holdHeight(); setError(''); setFolder(path) }
  const add = async (made: boolean) => {
    setBusy(true); setError('')
    try {
      if (here && connect) {
        const r = await api.addLibrary({ id: trimmed.id, label, indexRoot: folder, sourceRoot: trimmed.sourceRoot || undefined })
        onAdded(r.library, false)
      } else if (here) {
        const r = await postJson<{ library: SettingsLibrary }>('/api/libraries/build', { ...asked.body, label, depth, create: made })
        reloadSuggestions()
        onAdded(r.library, true, true)
      } else if (connect) {
        onAdded(await postJson<{ id: string; label: string }>(`/api/user-libraries/${cluster ? 'pw' : 'ssh'}`, { ...asked.body, label }), false)
      } else {
        const lib = await api.addSpace({
          ...(asked.body as unknown as SpaceAsk), label, depth,
          ...(signIn ? { myLibrary } : {}),
          ...(shared ? { share: { kind: shownKind, ...(shownKind === 'everyone' ? {} : { name: account.trim() }), access: shownAccess } } : {}),
          ...(made ? { create: true } : {}),
        })
        if (lib.shareError) toast.error(`${lib.label} is added, and not shared: ${lib.shareError}`)
        onAdded(lib, true)
      }
    } catch (e) { setError(say(e)) } finally { setBusy(false) }
  }

  return (
    <>
      <Where chip={chip} naming={naming} below={below}
        folder={target && !naming && (
          <FolderField place={target} sshOptions={sshOptions} value={folder} empty={connect ? 'Choose the index' : 'Choose a folder'}
            label={connect ? `Index on ${place}` : `Folder on ${place}`} anew={!connect} onPick={choose} />
        )} />
      {systems?.error && (
        <Callout tone="warning">Your clusters could not be listed: {systems.error.replace(/[.\s]*$/, '.')} Choose Another cluster… to name one that is running.</Callout>
      )}
      {statusError && <Callout tone="error">{statusError}</Callout>}
      {suggestions.length > 0 && (
        <div className="lib-suggest" role="group" aria-label="Suggested folders">
          <span className="muted lib-suggest-label">Suggested:</span>
          {suggestions.map(sg => (
            <Button key={sg.id} size="sm" variant={suggested?.id === sg.id && folder === sg.path ? 'secondary' : 'outline'} title={sg.path}
              onClick={() => { setSuggested(sg); choose(sg.path) }}>{sg.label}</Button>
          ))}
        </div>
      )}
      {suggested && folder === suggested.path && suggested.note && <p className="muted lib-suggest-note">{suggested.note}</p>}
      {suggested && folder === suggested.path && suggested.access === 'full-disk-access' && (
        <Callout tone="warning" title="Needs Full Disk Access">
          macOS lets ACTIVATE Studio read {suggested.label} only with Full Disk Access. Turn it on for ACTIVATE Studio in System Settings,
          Privacy &amp; Security, Full Disk Access, then press Check again.{' '}
          <Button size="sm" variant="outline" onClick={() => {
            fetch('/api/libraries/suggestions/full-disk-access', { method: 'POST' })
              .then(r => (r.ok ? null : r.json().then((d: { error?: string }) => setError(d.error ?? `${r.status}`))))
              .catch(e => setError(say(e)))
          }}>Open System Settings</Button>
          {' '}<Button size="sm" variant="ghost" onClick={check.again}>Check again</Button>
        </Callout>
      )}
      {checks && (
        <FolderCheckCard path={folder} place={place} facts={facts} counting={counted.busy} isNew={isNew} busy={check.busy} blocked={blocked}
          warnings={warnings} steps={found?.steps ?? []} onAgain={check.again} />
      )}
      {refused && status?.signInOffered && <PlatformSignInPanel onChanged={() => { check.again(); signedIn() }} />}
      {target && !naming && !gone && (folder || ssh) && (
        <div className="lib-review" aria-label="Review" role="group">
          {folder && !blocked && <>
            <Field label="Name" layout="inline" size="sm"><Input size="sm" value={name} onChange={e => setName(e.target.value)} /></Field>
            {connect && (
              <Field label="Files" layout="inline" size="sm" description="The files it was made from, to open them. Without them, names and details only.">
                <FolderField place={target!} sshOptions={sshOptions} value={form.sourceRoot} empty="Not given" label="Files folder" onPick={p => set({ sourceRoot: p })} />
              </Field>
            )}
            {cluster && !connect && (
              <Field label="Who sees it" layout="inline" size="sm" group description={noShare ? <span data-testid="no-share">Shared is not offered here. {noShare}</span> : undefined}>
                <div className="lib-review-row">
                  <SegmentedControl size="sm" label="Who sees it" value={shared ? 'shared' : 'me'} onValueChange={v => { holdHeight(); setWho(v) }}
                    options={[{ value: 'me', label: 'Only me' }, ...(noShare ? [] : [{ value: 'shared' as const, label: 'Shared' }])]} />
                </div>
              </Field>
            )}
            {shared && (
              <>
                <Field label="Shared with" layout="inline" size="sm" group>
                  <div className="lib-review-row">
                    <SegmentedControl size="sm" label="Shared with" value={shownKind} onValueChange={setKind} options={kinds.map(k => ({ value: k, label: KIND_LABEL[k] }))} />
                    {shownKind !== 'everyone' && (
                      <Input size="sm" mono spellCheck={false} autoComplete="off" aria-label={shownKind === 'user' ? `Account on ${place}` : 'Group'} value={account}
                        placeholder={shownKind === 'user' ? 'their account' : 'a group you are in'} onChange={e => setAccount(e.target.value)} />
                    )}
                  </div>
                </Field>
                <Field label="They can" layout="inline" size="sm" group description={noACL
                  ? `This file system (${found!.fs}) does not take POSIX ACLs, so it is shared with everyone, for reading.`
                  : shownKind === 'everyone' ? `Every account on ${place} can read it, under Shared with you.` : 'They find it under Shared with you.'}>
                  <div className="lib-review-row">
                    <SegmentedControl size="sm" label="They can" value={shownAccess} onValueChange={setAccess}
                      options={[{ value: 'read', label: 'Read only' }, ...(shownKind === 'everyone' ? [] : [{ value: 'write' as Access, label: 'Read and write' }])]} />
                  </div>
                </Field>
              </>
            )}
            {!connect && (
              <Field label="Index depth" layout="inline" size="sm" description={depthHelp(depth)}>
                <Select size="sm" width="content" items={DEPTHS.map(d => ({ value: d.value, label: d.label }))} value={depth} disabled={busy} onValueChange={v => { if (v) setDepth(v) }} />
              </Field>
            )}
            {mine && !shared && <p className="muted lib-add-about" data-testid="my-library-line">{noMy ?? 'It becomes My library, where your chats and the assistant\'s files are saved.'}</p>}
          </>}
          <MoreOptions open={more} onToggle={() => { holdHeight(); setMore(m => !m) }}>
            <div className="lib-more-fields">
              {signIn && cluster && !connect && !shared ? (
                <div className="lib-add-my">
                  <label className="lib-add-my-line">
                    <Checkbox checked={myLibrary} disabled={!!noMy || busy} onCheckedChange={v => { holdHeight(); setMy(v) }} aria-label="Use as My library" />
                    <span>Use as My library: my chats and the assistant&apos;s files are saved here</span>
                  </label>
                  {noMy && !mine && <p className="muted lib-add-about">{noMy}</p>}
                </div>
              ) : <></>}
              {ssh ? (
                <Input label="SSH options" hint="optional" size="sm" mono placeholder="-J user@jumphost -o ConnectTimeout=20" autoComplete="off" spellCheck={false} value={form.sshOptions}
                  error={sshProblem ?? undefined}
                  description="For a jump host or other settings: -J, -p, -i, -l, -F, and -o Keyword=value. Used on every ssh to this host." onChange={e => set({ sshOptions: e.target.value })} />
              ) : <></>}
              {!connect ? (
                <PathOption label="Index folder" place={target!} placeholder={here ? 'in the Studio\'s own data folder' : '~/.activate-studio/indexes/…'} value={form.indexRoot}
                  description="Where the index is written: an empty or new folder, outside the folder to index." onChange={indexRoot => set({ indexRoot })} />
              ) : <></>}
              {remote ? (
                <Input label="GUFI bin directory" hint="optional" size="sm" mono placeholder="found by the check" spellCheck={false} value={form.gufiBin}
                  description="Where GUFI's tools are there. Without it, the Studio uses its own copy, or looks for one installed there." onChange={e => set({ gufiBin: e.target.value })} />
              ) : (
                <Input label="Identifier" hint="optional" size="sm" placeholder="made from the name" spellCheck={false} value={form.id}
                  description="Lowercase letters, digits, dashes. Names the library's folder under All libraries." onChange={e => set({ id: e.target.value })} />
              )}
            </div>
          </MoreOptions>
        </div>
      )}
      {error && <Callout tone="error" live>{error}</Callout>}
      {cluster && !connect && options.space && (
        <section className="lib-add-shared" aria-label={`Shared with you on ${place}`}>
          <SharedWithMe compact system={cluster} title={`Shared with you on ${place}`} onAdded={lib => onAdded(lib, true)} />
        </section>
      )}
      <div className="query-actions">
        <Button variant="primary" loading={busy} disabled={!ready || busy || check.busy}
          onClick={() => (create.length ? setConfirming(true) : void add(false))}>{connect ? 'Connect' : 'Index'}</Button>
      </div>
      <CreateConfirm open={confirming} paths={create} where={here ? 'on this computer' : `on ${place}, as ${found?.account ?? 'you'}`}
        onClose={() => setConfirming(false)} onConfirm={() => { setConfirming(false); return add(true) }} />
    </>
  )
}

/** Step one: the computer and the folder on one row, with what else can be
 *  done under it. */
function Where({ chip, naming, folder, below }: { chip: ReactElement | null; naming: ReactElement | null; folder?: ReactElement | null | false; below: ReactElement }) {
  return (
    <div className="lib-where-step">
      {chip && (
        <div className="lib-where" role="group" aria-label="Where">
          {chip}
          {naming ?? (folder || <span className="lib-where-none muted"><FolderIcon aria-hidden className="code-chip-icon" />Choose a folder</span>)}
        </div>
      )}
      <div className="lib-where-below">{below}</div>
    </div>
  )
}
