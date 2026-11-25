import { useEffect, useState } from 'react'
import { Button, Callout, Field, Input, Loader, Modal, SegmentedControl, Table, toast } from '@parallelworks/ui'
import { api, type FolderShare } from '../api'

type Kind = FolderShare['kind']
type Access = FolderShare['access']

const KIND_LABEL: Record<Kind, string> = { user: 'A person', group: 'A group', everyone: 'Every account' }

function whom(s: FolderShare): string {
  return s.kind === 'everyone' ? 'Every account' : s.kind === 'group' ? `Group ${s.name}` : s.name
}

/**
 * Share a folder of the viewer's space with an account, a host group they
 * are in, or every account on the cluster (POST
 * /api/user-libraries/{id}/shares). The Studio sets it as them: a POSIX
 * ACL on the folder and a default ACL so new files inherit, and pass-through
 * access on the folders above it. Shares already set on the folder are
 * listed, each with Stop sharing.
 */
export function ShareDialog({ open, onClose, library, path }: {
  open: boolean
  onClose: () => void
  /** The space's library id. */
  library: string
  /** The folder within the space, '' for all of it. */
  path: string
}) {
  const [shares, setShares] = useState<FolderShare[] | null>(null)
  const [acl, setAcl] = useState(true)
  const [fs, setFs] = useState('')
  const [kind, setKind] = useState<Kind>('user')
  const [name, setName] = useState('')
  const [access, setAccess] = useState<Access>('read')
  const [busy, setBusy] = useState('')
  const [error, setError] = useState('')

  const load = () => api.spaceShares(library).then(r => {
    setShares(r.shares.filter(s => s.path === path)); setAcl(r.acl); setFs(r.fs)
    if (!r.acl) setKind('everyone')
  }, e => setError(String((e as Error).message ?? e)))
  useEffect(() => { if (open) { setError(''); setShares(null); void load() } }, [open, library, path])

  const kinds: Kind[] = acl ? ['user', 'group', 'everyone'] : ['everyone']
  const shownAccess: Access = kind === 'everyone' ? 'read' : access
  const ready = kind === 'everyone' || !!name.trim()

  const share = async () => {
    setBusy('add'); setError('')
    try {
      await api.addShare(library, { path, kind, name: kind === 'everyone' ? undefined : name.trim(), access: shownAccess })
      toast.success(`Shared ${path || 'the space'} with ${kind === 'everyone' ? 'every account' : name.trim()}.`)
      setName('')
      void load()
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy('') }
  }
  const stop = async (s: FolderShare) => {
    setBusy(s.id); setError('')
    try {
      await api.removeShare(library, s.id)
      toast.success(`Stopped sharing with ${whom(s)}.`)
      void load()
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy('') }
  }

  return (
    <Modal open={open} onClose={onClose} size="lg" title={`Share ${path || 'this space'}`}
      description="Gives an account on the cluster, a group you are in, or every account access to this folder and everything in it, as a POSIX ACL set as you. Whoever it names, or is in the group, and uses this Studio gets a notice with Add to my libraries. It is also under Add a library, Shared with me, and is indexed and read as them."
      footer={<Button variant="secondary" onClick={onClose}>Close</Button>}>
      <div className="share-dialog">
        {!shares && !error && <p className="muted"><Loader inline size={16} label="Loading…" /></p>}
        {shares && shares.length > 0 && (
          <Table density="compact" tableProps={{ 'aria-label': 'Shared with' }}>
            <Table.Header>With</Table.Header>
            <Table.Header>Access</Table.Header>
            <Table.Header>Added</Table.Header>
            <Table.Header />
            {shares.map(s => (
              <tr key={s.id}>
                <Table.Item wrap>{whom(s)}</Table.Item>
                <Table.Item>{s.access === 'write' ? 'Read and write' : 'Read'}</Table.Item>
                <Table.Item>{s.recipients === 1 ? '1 library' : `${s.recipients} libraries`}</Table.Item>
                <Table.Item><Button size="sm" variant="danger" loading={busy === s.id} disabled={!!busy} onClick={() => void stop(s)}>Stop sharing</Button></Table.Item>
              </tr>
            ))}
          </Table>
        )}
        {shares && !shares.length && <p className="muted">Only your account can open this folder now.</p>}
        {!acl && <p className="muted">This file system ({fs}) does not take POSIX ACLs, so the folder can be shared with every account for reading only.</p>}
        <Field label="Share with">
          <SegmentedControl label="Share with" value={kind} onValueChange={setKind} options={kinds.map(k => ({ value: k, label: KIND_LABEL[k] }))} />
        </Field>
        {kind !== 'everyone' && (
          <Input label={kind === 'user' ? 'Account' : 'Group'} mono spellCheck={false} autoComplete="off" value={name}
            placeholder={kind === 'user' ? 'their account on the cluster' : 'a group you are in'}
            description={kind === 'user' ? 'Their account name on the cluster, as id -un prints it.' : 'A group on the cluster you belong to, as id -Gn lists it.'}
            onChange={e => setName(e.target.value)} />
        )}
        <Field label="Access">
          <SegmentedControl label="Access" value={shownAccess} onValueChange={setAccess}
            options={[{ value: 'read', label: 'Read' }, ...(kind === 'everyone' ? [] : [{ value: 'write' as Access, label: 'Read and write' }])]} />
        </Field>
        {kind === 'everyone' && <p className="muted">Every account on the cluster can read the folder. The folders above it are opened for passing through only, not for listing.</p>}
        {error && <Callout tone="error" live>{error}</Callout>}
        <div className="query-actions">
          <Button variant="primary" loading={busy === 'add'} disabled={!ready || !!busy} onClick={() => void share()}>Share</Button>
        </div>
      </div>
    </Modal>
  )
}
