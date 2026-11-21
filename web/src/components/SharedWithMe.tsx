import { useEffect, useState } from 'react'
import { Button, Callout, Loader, Table } from '@parallelworks/ui'
import { api, type FolderShare, type UserLibrary } from '../api'

/**
 * Folders other accounts shared with the viewer's account, or a group it
 * is in (GET /api/user-libraries/shared-with-me). Adding one makes a
 * library of the viewer's own, indexed and read as them, so it holds what
 * their account can read.
 */
export function SharedWithMe({ onAdded, system, compact = false, title }: {
  onAdded: (lib: UserLibrary) => void
  /** A heading over the list, shown only when it lists something. */
  title?: string
  /** Only the folders on this system (Add a library, A system). */
  system?: string
  /** Say nothing when nothing is shared, as a part of another form. */
  compact?: boolean
}) {
  const [shares, setShares] = useState<FolderShare[] | null>(null)
  const [why, setWhy] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState('')

  const load = () => api.sharedWithMe().then(r => { setShares(r.shares); setWhy(r.why ?? '') }, e => setError(String((e as Error).message ?? e)))
  useEffect(() => { void load() }, [])

  const add = async (s: FolderShare) => {
    setBusy(s.id); setError('')
    try {
      const lib = await api.addShared(s.id, {})
      onAdded(lib)
      void load()
    } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy('') }
  }

  if (!shares && !error) return compact ? null : <p className="muted"><Loader inline size={16} label="Loading…" /></p>
  const here = (shares ?? []).filter(s => system === undefined || s.site === undefined || s.site === system)
  if (compact && !error && (why || !here.length)) return null
  return (
    <div className="shared-with-me">
      {why && <Callout tone="info">{why}</Callout>}
      {shares && !why && !here.length && <p className="muted">Nothing is shared with your account yet. When someone shares a folder of their space with you, or with a group you are in, it shows here.</p>}
      {title && here.length > 0 && <h4 className="lib-add-shared-title">{title}</h4>}
      {here.length > 0 && (
        <Table density="compact" tableProps={{ 'aria-label': 'Shared with me' }}>
          <Table.Header>Folder</Table.Header>
          <Table.Header>From</Table.Header>
          <Table.Header>Access</Table.Header>
          <Table.Header />
          {here.map(s => (
            <tr key={s.id}>
              <Table.Item wrap><code title={s.folder}>{s.label}</code></Table.Item>
              <Table.Item wrap>{s.from}{s.kind === 'group' ? `, for ${s.name}` : s.kind === 'everyone' ? ', for every account' : ''}</Table.Item>
              <Table.Item>{s.access === 'write' ? 'Read and write' : 'Read'}</Table.Item>
              <Table.Item>{s.library
                ? <span className="muted">Added</span>
                : <Button size="sm" variant="secondary" loading={busy === s.id} disabled={!!busy} onClick={() => void add(s)}>Add</Button>}</Table.Item>
            </tr>
          ))}
        </Table>
      )}
      {error && <Callout tone="error" live>{error}</Callout>}
    </div>
  )
}
