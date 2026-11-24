import { useCallback, useEffect, useState } from 'react'
import { Button, Callout, Checkbox, Field, Input, Loader, SegmentedControl, toast } from '@parallelworks/ui'
import { api, type FolderShare, type IndexDepth, type SpaceCheck, type SpaceStatus, type UserLibrary } from '../api'
import { DEFAULT_REMOTE_LIBRARY_DEPTH, IndexDepthControl, remoteMeaningNote } from './IndexDepth'
import { CreateConfirm, StepList } from './LibraryFormParts'
import { PlatformSignInPanel } from './PlatformSignInPanel'
import { NeedsActivate } from './NeedsActivate'
import { SIGN_IN_TO_USE, needsActivate } from '../accounts'
import { sshOptionsProblem } from '../sshOptions'
import { useAppConfig } from '../config'

/** A library's name from its folder, as the server names one not named. */
export const folderName = (dir: string): string => {
  const name = dir.replace(/\/+$/, '').split('/').pop() ?? ''
  return !name || name === '~' || name === '.' ? 'Home folder' : name
}

type Kind = FolderShare['kind']
type Access = FolderShare['access']
const KIND_LABEL: Record<Kind, string> = { user: 'A person', group: 'A group', everyone: 'Everyone' }

/**
 * Index a folder on a remote computer (POST /api/user-libraries/space): one
 * of the viewer's clusters, reached over pw ssh with their ACTIVATE
 * sign-in, or a host over plain ssh with their own keys on a Studio one
 * person uses. It is indexed, read and written there as them. The form
 * around this one (Add a library) chooses the system; a person without a
 * sign-in is offered it here first, and again where the platform refuses
 * their credential.
 *
 * The check runs first, as them. What it finds that another account could
 * change is a warning: the folder is indexed and read, and the Library does
 * not write in it. A folder that does not exist yet is made, closed to
 * other accounts, once confirmed.
 *
 * Who sees it: only the viewer, or it is shared as it is added, for reading
 * or for reading and writing, where they own the folder; otherwise the form
 * says why it is not. Where people sign in, a folder only they see can be
 * their My library, where their chats and the assistant's files are saved.
 */
export function IndexFolderForm({ onAdded, cluster, destination, holdHeight }: {
  onAdded: (lib: UserLibrary) => void
  /** The system: a cluster over pw ssh, or a host over plain ssh. */
  cluster?: string
  destination?: string
  /** Called before a choice that changes the form's height. */
  holdHeight?: () => void
}) {
  const signIn = !!useAppConfig().signIn
  const [status, setStatus] = useState<SpaceStatus | null>(null)
  const [statusError, setStatusError] = useState('')
  // Until it is typed, the folder follows My library: the one suggested
  // for it, and none otherwise.
  const [typedFolder, setFolder] = useState<string | null>(null)
  const [label, setLabel] = useState('')
  const [depth, setDepth] = useState<IndexDepth>(DEFAULT_REMOTE_LIBRARY_DEPTH)
  const [more, setMore] = useState(false)
  const [indexRoot, setIndexRoot] = useState('')
  const [gufiBin, setGufiBin] = useState('')
  const [sshOptions, setSshOptions] = useState('')
  const [who, setWho] = useState<'me' | 'shared'>('me')
  const [kind, setKind] = useState<Kind>('user')
  const [name, setName] = useState('')
  const [access, setAccess] = useState<Access>('read')
  const [myPicked, setMy] = useState<boolean | null>(null)
  const [check, setCheck] = useState<SpaceCheck | null>(null)
  const [tested, setTested] = useState('')
  const [busy, setBusy] = useState<'test' | 'add' | null>(null)
  const [error, setError] = useState('')
  const [confirming, setConfirming] = useState(false)

  const readStatus = useCallback(() => {
    api.spaceStatus().then(s => { setStatus(s); setStatusError('') }, e => setStatusError(String((e as Error).message ?? e)))
  }, [])
  useEffect(() => { readStatus() }, [readStatus])
  // Signing in here, or again after the platform refused a credential,
  // reads the status again and clears the refusal.
  const signedIn = useCallback(() => { setError(''); setCheck(null); readStatus() }, [readStatus])

  if (statusError) return <Callout tone="error">{statusError}</Callout>
  if (!status) return <p className="muted"><Loader inline size={16} label="Loading…" /></p>
  const ssh = destination !== undefined
  // A host over ssh needs no sign-in.
  if (!ssh && !status.available) {
    if (status.cluster && status.signInOffered && !status.credential) {
      return (
        <div className="space-sign-in">
          <Callout tone="info" title={SIGN_IN_TO_USE}>
            A folder on {status.cluster} is indexed and read as your own account. Sign in with ACTIVATE here and approve
            the code on the platform, then choose the folder. The sign-in is kept under Settings, Accounts.
          </Callout>
          <PlatformSignInPanel onChanged={signedIn} />
        </div>
      )
    }
    return needsActivate(status.why)
      ? <NeedsActivate>{status.why}</NeedsActivate>
      : <Callout tone="info" title="A folder on a remote computer is not available">{status.why}</Callout>
  }

  // A cluster not chosen yet stays unchosen: nothing is checked or made
  // until the person picks one.
  const system = (ssh ? destination : cluster ?? '').trim()
  const where = system || 'that remote computer'
  const options = ssh ? sshOptions.trim() : ''
  const optionsProblem = options ? sshOptionsProblem(options) : null

  // My library is offered for a folder only the viewer sees, where people
  // sign in, and is chosen to begin with for someone who has none.
  const suggestMy = signIn && (myPicked ?? !status.spaces.length)
  const folder = typedFolder ?? (suggestMy && who === 'me' ? status.suggested : '')
  const source = folder.trim()
  const asked = {
    ...(ssh ? { destination: system, ...(options ? { sshOptions: options } : {}) } : { cluster: system }),
    sourceRoot: source,
    ...(indexRoot.trim() ? { indexRoot: indexRoot.trim() } : {}),
    ...(gufiBin.trim() ? { gufiBin: gufiBin.trim() } : {}),
  }
  const current = !!check && tested === JSON.stringify(asked)
  const passed = current && check.ok

  const noShare = ssh ? 'It is reached over ssh on a Studio only you use, so there is nobody here to share it with.' : current && check.ok && !check.canShare ? check.shareWhy : null
  const shared = who === 'shared' && !noShare
  const noACL = passed && check.owned && !check.acl
  const kinds: Kind[] = noACL ? ['everyone'] : ['user', 'group', 'everyone']
  const shownKind: Kind = kinds.includes(kind) ? kind : kinds[0]
  const shownAccess: Access = shownKind === 'everyone' ? 'read' : access
  const noMy = passed ? check.myLibraryWhy ?? null : null
  const myLibrary = signIn && !shared && suggestMy && !noMy
  const ready = passed && (!shared || shownKind === 'everyone' || !!name.trim())

  const test = async () => {
    setBusy('test'); setError('')
    try {
      const c = await api.testSpace(asked)
      setCheck(c); setTested(JSON.stringify(asked))
    } catch (e) { setError(String((e as Error).message ?? e)); setCheck(null) } finally { setBusy(null) }
  }
  const add = async (create: boolean) => {
    setBusy('add'); setError('')
    try {
      const lib = await api.addSpace({
        ...asked, label: label.trim() || undefined, depth,
        ...(signIn ? { myLibrary } : {}),
        ...(shared ? { share: { kind: shownKind, ...(shownKind === 'everyone' ? {} : { name: name.trim() }), access: shownAccess } } : {}),
        ...(create ? { create: true } : {}),
      })
      if (lib.shareError) toast.error(`${lib.label} is added, and not shared: ${lib.shareError}`)
      onAdded(lib)
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(null) }
  }

  return (
    <div className="ssh-library-form">
      {!ssh && status.credential === 'key' && (
        <p className="muted view-sub">Reached with the API key you added under Settings, Accounts, in place of your sign-in. Signing in with ACTIVATE there instead renews itself.</p>
      )}
      <div className="settings-grid">
        <Input label="Folder" mono placeholder="~/data" spellCheck={false} value={folder}
          description={`The folder to index on ${where}. Made, closed to other accounts, if it does not exist.`}
          onChange={e => { setFolder(e.target.value); setError('') }} />
        <Input label="Label" hint="optional" placeholder={myLibrary ? 'My library' : source ? folderName(source) : 'Shown in the Library switcher'} value={label}
          onChange={e => setLabel(e.target.value)} />
      </div>
      <div className="lib-add-sees">
        <Field label="Who sees it">
          <SegmentedControl label="Who sees it" value={shared ? 'shared' : 'me'} onValueChange={v => { holdHeight?.(); setWho(v) }}
            options={[{ value: 'me', label: 'Only me' }, ...(noShare ? [] : [{ value: 'shared' as const, label: 'Shared' }])]} />
        </Field>
        {noShare && <p className="muted lib-add-about" data-testid="no-share">Shared is not offered here. {noShare}</p>}
        {!shared && signIn && (
          <div className="lib-add-my">
            <label className="lib-add-my-line">
              <Checkbox checked={myLibrary} disabled={!!noMy || busy !== null} onCheckedChange={v => { holdHeight?.(); setMy(v) }} aria-label="My library" />
              <span>My library: my chats and the assistant&apos;s files are saved here</span>
            </label>
            {noMy && suggestMy && <p className="muted lib-add-about">{noMy}</p>}
          </div>
        )}
        {shared && (
          <>
            <div className="lib-add-choices">
              <Field label="Shared with">
                <SegmentedControl label="Shared with" value={shownKind} onValueChange={setKind} options={kinds.map(k => ({ value: k, label: KIND_LABEL[k] }))} />
              </Field>
              <Field label="They can">
                <SegmentedControl label="They can" value={shownAccess} onValueChange={setAccess}
                  options={[{ value: 'read', label: 'Read only' }, ...(shownKind === 'everyone' ? [] : [{ value: 'write' as Access, label: 'Read and write' }])]} />
              </Field>
            </div>
            {shownKind !== 'everyone' && (
              <div className="settings-grid">
                <Input label={shownKind === 'user' ? 'Account' : 'Group'} mono spellCheck={false} autoComplete="off" value={name}
                  placeholder={shownKind === 'user' ? `their account on ${where}` : 'a group you are in'}
                  description={shownKind === 'user' ? `Their account name on ${where}, as id -un prints it.` : `A group on ${where} you belong to, as id -Gn lists it.`}
                  onChange={e => setName(e.target.value)} />
              </div>
            )}
            <p className="muted lib-add-about">
              {noACL
                ? `This file system (${check.fs}) does not take POSIX ACLs, so the folder can be shared with everyone for reading, but not with one person or group.`
                : shownKind === 'everyone'
                  ? `Every account on ${where} can read the folder. They find it under Shared with you, and it is indexed and read as them.`
                  : 'They find it under Shared with you, and it is indexed and read as them.'}
            </p>
          </>
        )}
      </div>
      <IndexDepthControl value={depth} onChange={setDepth} disabled={busy !== null} note={remoteMeaningNote} />
      <Button variant="ghost" size="sm" className="local-library-more" aria-expanded={more} onClick={() => { holdHeight?.(); setMore(m => !m) }}>
        {more ? 'Fewer options' : 'More options'}
      </Button>
      {more && (
        <div className="settings-grid">
          {ssh && (
            <Input label="SSH options" hint="optional" mono placeholder="-J user@jumphost -o ConnectTimeout=20" autoComplete="off" spellCheck={false} value={sshOptions}
              error={optionsProblem ?? undefined}
              description="For a jump host or other settings: -J, -p, -i, -l, -F, and -o Keyword=value. Used on every ssh to this host." onChange={e => { setSshOptions(e.target.value); setError('') }} />
          )}
          <Input label="Index folder" hint="optional" mono placeholder="~/.activate-studio/indexes/…" spellCheck={false} value={indexRoot}
            description="Where the index is written there: an empty or new folder, outside the folder to index." onChange={e => { setIndexRoot(e.target.value); setError('') }} />
          <Input label="GUFI bin directory" hint="optional" mono placeholder="found by the check" spellCheck={false} value={gufiBin}
            description="Where GUFI's tools are there. Without it, the Studio uses its own copy, or looks for one installed there." onChange={e => { setGufiBin(e.target.value); setError('') }} />
        </div>
      )}
      {check && <StepList steps={check.steps} label="Folder check" />}
      {error && <Callout tone="error" live>{error}</Callout>}
      {error.startsWith('The platform refused') && status.signInOffered && <PlatformSignInPanel onChanged={signedIn} />}
      <div className="query-actions">
        <Button variant="secondary" loading={busy === 'test'} disabled={busy !== null || !system || !source || !!optionsProblem} onClick={() => void test()}>Check the folder</Button>
        <Button variant="primary" loading={busy === 'add'} disabled={!ready || busy !== null}
          onClick={() => (check?.create?.length ? setConfirming(true) : void add(false))}>Index the folder</Button>
        {check && !passed && !busy && <span className="muted">{check.ok ? 'Fields changed: check again to index it.' : 'Fix the failing step and check again.'}</span>}
      </div>
      <CreateConfirm open={confirming} paths={check?.create ?? []} where={`on ${where}, as ${check?.account ?? 'you'}`}
        onClose={() => setConfirming(false)} onConfirm={() => { setConfirming(false); return add(true) }} />
    </div>
  )
}
