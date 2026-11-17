import { useEffect, useState } from 'react'
import { Button, Callout, Input, Loader } from '@parallelworks/ui'
import { api, type IndexDepth, type SpaceCheck, type SpaceStatus, type UserLibrary } from '../api'
import { DEFAULT_REMOTE_LIBRARY_DEPTH, IndexDepthControl, remoteMeaningNote } from './IndexDepth'
import { CreateConfirm, StepList } from './LibraryFormParts'

/**
 * The viewer's own space: a folder on the cluster the Studio runs beside,
 * owned by their own account, indexed and written as them (POST
 * /api/user-libraries/space). The check runs first, as them: the folder is
 * theirs and closed to other accounts, and so are the folders above it.
 * A folder that does not exist yet is made, closed to others, once
 * confirmed. Where the space cannot be made (no sign-in, no cluster), the
 * form says why.
 */
export function SpaceForm({ onAdded }: { onAdded: (lib: UserLibrary) => void }) {
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

  useEffect(() => {
    api.spaceStatus().then(s => { setStatus(s); setFolder(f => f || s.suggested) }, e => setStatusError(String((e as Error).message ?? e)))
  }, [])

  if (statusError) return <Callout tone="error">{statusError}</Callout>
  if (!status) return <p className="muted"><Loader inline size={16} label="Loading…" /></p>
  if (!status.available) return <Callout tone="info" title="A space cannot be made yet">{status.why}</Callout>

  const typed = folder.trim() || status.suggested
  const passed = !!check?.ok && tested === typed

  const test = async () => {
    setBusy('test'); setError('')
    try {
      const c = await api.testSpace({ sourceRoot: typed })
      setCheck(c); setTested(typed)
    } catch (e) { setError(String((e as Error).message ?? e)); setCheck(null) } finally { setBusy(null) }
  }
  const add = async (create: boolean) => {
    setBusy('add'); setError('')
    try {
      const lib = await api.addSpace({ sourceRoot: typed, label: label.trim() || undefined, depth, ...(create ? { create: true } : {}) })
      onAdded(lib)
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(null) }
  }

  return (
    <div className="ssh-library-form">
      <div className="ssh-library-fields">
        <Input label="Folder" mono placeholder={status.suggested} spellCheck={false} value={folder}
          description={`A folder of yours on ${status.cluster}, such as your home folder's ${status.suggested.replace(/^~\//, '')}. Only your account can open it until you share a folder in it. Made, closed to others, if it does not exist.`}
          onChange={e => { setFolder(e.target.value); setCheck(null) }} />
        <Input label="Label" hint="optional" placeholder="My space" value={label} onChange={e => setLabel(e.target.value)} />
      </div>
      <IndexDepthControl value={depth} onChange={setDepth} disabled={busy !== null} note={remoteMeaningNote} />
      {check && <StepList steps={check.steps} label="Space check" />}
      {check?.ok && !check.acl && (
        <p className="muted">This file system ({check.fs}) does not take POSIX ACLs, so folders in this space can be shared with every account for reading, but not with one person or group.</p>
      )}
      {error && <Callout tone="error" live>{error}</Callout>}
      <div className="query-actions">
        <Button variant="secondary" loading={busy === 'test'} disabled={busy !== null} onClick={() => void test()}>Check the folder</Button>
        <Button variant="primary" loading={busy === 'add'} disabled={!passed || busy !== null}
          onClick={() => (check?.create?.length ? setConfirming(true) : void add(false))}>Make my space</Button>
      </div>
      <CreateConfirm open={confirming} paths={check?.create ?? []} where={`on ${status.cluster}, as ${check?.account ?? 'you'}`}
        onClose={() => setConfirming(false)} onConfirm={() => { setConfirming(false); return add(true) }} />
    </div>
  )
}
