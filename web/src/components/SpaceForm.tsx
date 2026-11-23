import { useCallback, useEffect, useState } from 'react'
import { Button, Callout, Input, Loader } from '@parallelworks/ui'
import { api, type IndexDepth, type SpaceCheck, type SpaceStatus, type UserLibrary } from '../api'
import { DEFAULT_REMOTE_LIBRARY_DEPTH, IndexDepthControl, remoteMeaningNote } from './IndexDepth'
import { CreateConfirm, StepList } from './LibraryFormParts'
import { PlatformSignInPanel } from './PlatformSignInPanel'
import { PwSystemField } from './PwSystemField'
import { useAppConfig } from '../config'

/** A library's name from its folder, as the server names one not named. */
export const folderName = (dir: string): string => {
  const name = dir.replace(/\/+$/, '').split('/').pop() ?? ''
  return !name || name === '~' || name === '.' ? 'Home folder' : name
}

/**
 * A folder of the viewer's own on one of their systems, owned
 * by their own account, indexed and written as them (POST
 * /api/user-libraries/space). The system is one of their clusters, reached
 * over pw ssh with their ACTIVATE sign-in, the Studio's own cluster chosen
 * first; on a Studio one person uses with sign-in off, also a host over
 * plain ssh with their own keys. A person without a sign-in is offered it
 * here first. The check runs first, as them: the folder is theirs and
 * closed to other accounts, and so are the folders above it. A folder that
 * does not exist yet is made, closed to others, once confirmed. Where it
 * cannot be made, the form says why; where the platform refuses their
 * credential, the sign-in is offered again.
 *
 * Where people sign in it is My library, theirs beside the main library
 * they share. On a Studio only they use, the main library is already their
 * library, so this is a library on that system named by its folder.
 */
export function SpaceForm({ onAdded, cluster: given, destination }: {
  onAdded: (lib: UserLibrary) => void
  /** The system, chosen by the form around this one (Add a library): a
   *  cluster over pw ssh, or a host over plain ssh. The form then shows no
   *  System field of its own. */
  cluster?: string
  destination?: string
}) {
  const signIn = !!useAppConfig().signIn
  const [status, setStatus] = useState<SpaceStatus | null>(null)
  const [statusError, setStatusError] = useState('')
  const [folder, setFolder] = useState('')
  const [label, setLabel] = useState('')
  const [depth, setDepth] = useState<IndexDepth>(DEFAULT_REMOTE_LIBRARY_DEPTH)
  const [check, setCheck] = useState<SpaceCheck | null>(null)
  const [tested, setTested] = useState('')
  const [busy, setBusy] = useState<'test' | 'add' | null>(null)
  const [error, setError] = useState('')
  const [confirming, setConfirming] = useState(false)
  // The system: one of the person's clusters, or a host over plain ssh.
  const [cluster, setCluster] = useState('')
  const [overSSH, setOverSSH] = useState(false)
  const [host, setHost] = useState('')

  const readStatus = useCallback(() => {
    // The Studio's own cluster is the system to begin with, before the
    // list of the person's systems answers.
    api.spaceStatus().then(s => { setStatus(s); setFolder(f => f || s.suggested); setCluster(c => c || s.cluster); setStatusError('') }, e => setStatusError(String((e as Error).message ?? e)))
  }, [])
  useEffect(() => { readStatus() }, [readStatus])
  // Signing in here, or again after the platform refused a credential,
  // reads the status again and clears the refusal.
  const signedIn = useCallback(() => { setError(''); setCheck(null); readStatus() }, [readStatus])

  if (statusError) return <Callout tone="error">{statusError}</Callout>
  if (!status) return <p className="muted"><Loader inline size={16} label="Loading…" /></p>
  if (!status.available && status.cluster && status.signInOffered && !status.credential) {
    return (
      <div className="space-sign-in">
        <Callout tone="info" title={signIn ? 'My library needs your ACTIVATE sign-in' : 'A folder on a remote computer needs your ACTIVATE sign-in'}>
          {signIn ? 'My library is a folder of yours' : 'This is a folder of yours'} on {status.cluster}, which the Studio reaches as your own account. Sign in with
          ACTIVATE and approve the code on the platform, then choose the folder here.
        </Callout>
        <PlatformSignInPanel onChanged={signedIn} />
      </div>
    )
  }
  if (!status.available) return <Callout tone="info" title={signIn ? 'My library is not available' : 'A folder on a remote computer is not available'}>{status.why}</Callout>

  const pwOK = status.pw !== false
  const chosen = given !== undefined || destination !== undefined
  const ssh = destination !== undefined || (!chosen && (overSSH || !pwOK) && !!status.ssh)
  // A cluster the form around this one has not chosen yet stays unchosen:
  // nothing is made until the person picks one.
  const system = destination !== undefined ? destination.trim() : given !== undefined ? given : ssh ? host.trim() : cluster
  const where = system || 'that remote computer'
  const typed = folder.trim() || status.suggested
  const at = ssh ? { destination: system } : { cluster: system }
  const asked = JSON.stringify({ ...at, sourceRoot: typed })
  const passed = !!check?.ok && tested === asked
  const reset = () => { setCheck(null); setError('') }

  const test = async () => {
    setBusy('test'); setError('')
    try {
      const c = await api.testSpace({ ...at, sourceRoot: typed })
      setCheck(c); setTested(asked)
    } catch (e) { setError(String((e as Error).message ?? e)); setCheck(null) } finally { setBusy(null) }
  }
  const add = async (create: boolean) => {
    setBusy('add'); setError('')
    try {
      const lib = await api.addSpace({ ...at, sourceRoot: typed, label: label.trim() || undefined, depth, ...(create ? { create: true } : {}) })
      onAdded(lib)
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(null) }
  }
  const SSH = '\u0000ssh'

  return (
    <div className="ssh-library-form">
      {/* Where the form around this one chose the system, it already says
          whose account reads it; this adds only what it does not say. */}
      {chosen
        ? !ssh && status.credential === 'key' && (
          <p className="muted view-sub">Reached with the API key you added under Settings, Model access, in place of your sign-in. Signing in with ACTIVATE there instead renews itself.</p>
        )
        : (
          <p className="muted view-sub">
            {ssh
              ? `Reached on ${where} as you over ssh with your own keys, agent and ~/.ssh/config.`
              : status.credential === 'key'
                ? `Reached on ${where} as you with the API key you added under Settings, Model access. Signing in with ACTIVATE there instead renews itself.`
                : `Reached on ${where} as you with your ACTIVATE sign-in.`}
          </p>
        )}
      <div className="settings-grid">
        {pwOK && !chosen && (
          <PwSystemField value={cluster} home={status.cluster || undefined}
            onChange={c => { setCluster(c); setOverSSH(false); reset() }}
            extra={status.ssh ? [{ value: SSH, label: 'A host over ssh…', description: 'user@host, with your own keys' }] : []}
            extraValue={overSSH ? SSH : null} onExtra={() => { setOverSSH(true); reset() }} />
        )}
        {ssh && !chosen && (
          <Input label="Host" placeholder="user@host or user@host:port" autoComplete="off" spellCheck={false} value={host}
            description="As you would type it after ssh. A Host from your ~/.ssh/config works, including ProxyJump."
            onChange={e => { setHost(e.target.value); reset() }} />
        )}
        <Input label="Folder" mono placeholder={status.suggested} spellCheck={false} value={folder}
          description={`A folder of yours on ${where}, such as your home folder's ${status.suggested.replace(/^~\//, '')}. Only your account can open it${ssh ? '' : ' until you share a folder in it'}. Made, closed to others, if it does not exist.`}
          onChange={e => { setFolder(e.target.value); setCheck(null) }} />
        <Input label="Label" hint="optional" placeholder={signIn ? 'My library' : folderName(typed)} value={label} onChange={e => setLabel(e.target.value)} />
      </div>
      <IndexDepthControl value={depth} onChange={setDepth} disabled={busy !== null} note={remoteMeaningNote} />
      {check && <StepList steps={check.steps} label="Folder check" />}
      {check?.ok && !check.acl && (
        <p className="muted">This file system ({check.fs}) does not take POSIX ACLs, so folders in this library can be shared with every account for reading, but not with one person or group.</p>
      )}
      {error && <Callout tone="error" live>{error}</Callout>}
      {error.startsWith('The platform refused') && status.signInOffered && <PlatformSignInPanel onChanged={signedIn} />}
      <div className="query-actions">
        <Button variant="secondary" loading={busy === 'test'} disabled={busy !== null || !system} onClick={() => void test()}>Check the folder</Button>
        <Button variant="primary" loading={busy === 'add'} disabled={!passed || busy !== null}
          onClick={() => (check?.create?.length ? setConfirming(true) : void add(false))}>{signIn ? 'Make my library' : 'Make the folder'}</Button>
      </div>
      <CreateConfirm open={confirming} paths={check?.create ?? []} where={`on ${where}, as ${check?.account ?? 'you'}`}
        onClose={() => setConfirming(false)} onConfirm={() => { setConfirming(false); return add(true) }} />
    </div>
  )
}
