import { useEffect, useState } from 'react'
import { api, type PublicLibrary } from '../api'

/**
 * Settings > Libraries: what is mounted, what each supports, and adding
 * one by path. The server probes the tree when a library is added and
 * reports what it found, so a wrong path or a directory that is not a
 * GUFI index is refused with a reason rather than mounted and empty.
 */
export function LibrariesSection() {
  const [libs, setLibs] = useState<PublicLibrary[]>([])
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ id: '', label: '', indexRoot: '', sourceRoot: '' })
  const load = () => api.libraries().then(r => setLibs(r.libraries)).catch(e => setNote(String((e as Error).message ?? e)))
  useEffect(() => { void load() }, [])
  const add = async () => {
    setBusy(true); setNote('')
    try {
      await api.addLibrary({ id: form.id.trim(), label: form.label.trim() || undefined, indexRoot: form.indexRoot.trim(), sourceRoot: form.sourceRoot.trim() || undefined })
      setForm({ id: '', label: '', indexRoot: '', sourceRoot: '' })
      setNote('Added. It appears in the Library switcher now.')
      await load()
    } catch (e) { setNote(String((e as Error).message ?? e)) } finally { setBusy(false) }
  }
  const remove = async (id: string) => {
    setBusy(true); setNote('')
    try { await api.removeLibrary(id); await load() } catch (e) { setNote(String((e as Error).message ?? e)) } finally { setBusy(false) }
  }
  const yn = (b: boolean) => (b ? 'yes' : 'no')
  return (
    <>
      <h1>Libraries</h1>
      <p className="muted view-sub">
        The indexes this Studio can browse and search. The knowledge base is the first and the only one it writes to;
        any other is read only: a site's index built by root, or one someone handed over. A library without files on
        this host can still be searched and described.
      </p>
      <table className="settings-table">
        <thead><tr><th>Library</th><th>Writable</th><th>Files here</th><th>Full text</th><th>Vectors</th><th></th></tr></thead>
        <tbody>
          {libs.map(l => (
            <tr key={l.id}>
              <td><b>{l.label}</b> <span className="muted">{l.id}{l.pinned && !l.primary ? ', set by the deployment' : ''}</span></td>
              <td>{yn(l.writable)}</td><td>{yn(l.source)}</td><td>{yn(l.caps.fullText)}</td><td>{yn(l.caps.vectors)}</td>
              <td>{!l.pinned && <button className="btn-secondary" disabled={busy} onClick={() => void remove(l.id)}>Remove</button>}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <h2>Add a library</h2>
      <div className="settings-grid">
        <div>
          <label className="field-label">Identifier</label>
          <input className="field" placeholder="site-scratch" value={form.id} onChange={e => setForm({ ...form, id: e.target.value })} />
        </div>
        <div>
          <label className="field-label">Label</label>
          <input className="field" placeholder="Scratch filesystem" value={form.label} onChange={e => setForm({ ...form, label: e.target.value })} />
        </div>
        <div>
          <label className="field-label">Index root (a GUFI tree)</label>
          <input className="field" placeholder="/gufi/scratch" value={form.indexRoot} onChange={e => setForm({ ...form, indexRoot: e.target.value })} />
        </div>
        <div>
          <label className="field-label">Source root, if the files are on this host</label>
          <input className="field" placeholder="/lustre/scratch" value={form.sourceRoot} onChange={e => setForm({ ...form, sourceRoot: e.target.value })} />
        </div>
      </div>
      <div className="query-actions">
        <button className="btn-primary" disabled={busy || !form.id.trim() || !form.indexRoot.trim()} onClick={() => void add()}>{busy ? 'Probing…' : 'Add library'}</button>
        {note && <span className="muted">{note}</span>}
      </div>
    </>
  )
}
