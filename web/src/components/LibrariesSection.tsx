import { Fragment, useEffect, useState } from 'react'
import { Button, DescriptionList, DescriptionListItem, Input, Table, toast } from '@parallelworks/ui'
import { api, type SettingsLibrary } from '../api'
import { UserLibrariesSection } from './UserLibrariesSection'

const failed = (e: unknown) => toast.error(String((e as Error).message ?? e))

/**
 * Settings > Libraries: what is mounted, what each supports, and adding
 * one by path. The server probes the tree when a library is added and
 * reports what it found, so a wrong path or a directory that is not a
 * GUFI index is refused with a reason rather than mounted and empty.
 */
export function LibrariesSection() {
  const [libs, setLibs] = useState<SettingsLibrary[]>([])
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ id: '', label: '', indexRoot: '', sourceRoot: '' })
  const load = () => api.libraries().then(r => setLibs(r.libraries)).catch(failed)
  useEffect(() => { void load() }, [])
  const add = async () => {
    setBusy(true)
    try {
      await api.addLibrary({ id: form.id.trim(), label: form.label.trim() || undefined, indexRoot: form.indexRoot.trim(), sourceRoot: form.sourceRoot.trim() || undefined })
      setForm({ id: '', label: '', indexRoot: '', sourceRoot: '' })
      toast.success('Added. It appears in the Library switcher now.')
      await load()
    } catch (e) { failed(e) } finally { setBusy(false) }
  }
  const remove = async (id: string) => {
    setBusy(true)
    try { await api.removeLibrary(id); await load() } catch (e) { failed(e) } finally { setBusy(false) }
  }
  return (
    <>
      <h1>Libraries</h1>
      <p className="muted view-sub">
        The indexes this Studio can browse and search. The knowledge base is the first and the only one it writes to;
        any other is read only: a site's index built by root, or one someone handed over. A library without files on
        this host can still be searched and described.
      </p>
      <UserLibrariesSection />
      <div className="tool-group">Libraries on this host</div>
      <Table density="compact" className="libraries-table" wrapperClassName="rag-calls-wrap libraries-wrap">
        <Table.Header>Library</Table.Header>
        <Table.Header>Access</Table.Header>
        <Table.Header>Files on this host</Table.Header>
        <Table.Header>Full-text search</Table.Header>
        <Table.Header>Semantic search</Table.Header>
        <Table.Header />
        {libs.map(l => (
          <tr key={l.id}>
            <Table.Item wrap>
              <div className="lib-name">{l.label}</div>
              <div className="lib-id">{l.id}{l.primary ? ', the knowledge base' : l.pinned ? ', set by the deployment' : ''}</div>
              <DescriptionList className="lib-paths">
                <DescriptionListItem label="Index" mono copy><PathText path={l.indexRoot} /></DescriptionListItem>
                <DescriptionListItem label="Source" mono={!!l.sourceRoot} copy={!!l.sourceRoot}>
                  {l.sourceRoot ? <PathText path={l.sourceRoot} /> : <span className="muted">not on this host</span>}
                </DescriptionListItem>
              </DescriptionList>
            </Table.Item>
            <Table.Item>{l.writable ? 'Read and write' : 'Read only'}</Table.Item>
            <Table.Item><Mark on={l.source} /></Table.Item>
            <Table.Item><Mark on={l.caps.fullText} /></Table.Item>
            <Table.Item><Mark on={l.caps.vectors} /></Table.Item>
            <Table.Item className="lib-actions">{!l.pinned && <Button variant="secondary" size="sm" disabled={busy} onClick={() => void remove(l.id)}>Remove</Button>}</Table.Item>
          </tr>
        ))}
      </Table>
      <div className="tool-group">Add a library</div>
      <p className="muted view-sub">The Studio checks the path before mounting it and says what it found.</p>
      <div className="settings-grid">
        <Input label="Identifier" placeholder="site-scratch" value={form.id} onChange={e => setForm({ ...form, id: e.target.value })} />
        <Input label="Label" placeholder="Scratch filesystem" value={form.label} onChange={e => setForm({ ...form, label: e.target.value })} />
        <Input label="Index root (a GUFI tree)" placeholder="/gufi/scratch" value={form.indexRoot} onChange={e => setForm({ ...form, indexRoot: e.target.value })} />
        <Input label="Source root, if the files are on this host" placeholder="/lustre/scratch" value={form.sourceRoot} onChange={e => setForm({ ...form, sourceRoot: e.target.value })} />
      </div>
      <div className="query-actions">
        <Button variant="primary" loading={busy} disabled={!form.id.trim() || !form.indexRoot.trim()} onClick={() => void add()}>{busy ? 'Probing…' : 'Add library'}</Button>
      </div>
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
