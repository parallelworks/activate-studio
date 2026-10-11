import { useEffect, useRef, useState } from 'react'
import { api, type UserLibrary } from '../api'
import { reloadAppConfig } from '../config'

/**
 * Settings > Libraries > Your libraries: indexes of directories on the
 * user's own clusters. Adding one runs the index workflow there with the
 * user's key; when it finishes, the library appears in the Library switcher
 * for this user alone, read only, browsed and searched on the cluster.
 */
export function UserLibrariesSection() {
  const [libs, setLibs] = useState<UserLibrary[]>([])
  const [available, setAvailable] = useState(true)
  const [sites, setSites] = useState<string[]>([])
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState({ site: '', sourceDir: '$WORKDIR', label: '', slurm: false, partition: '', account: '' })
  const ready = useRef(new Set<string>())

  const load = () => api.userLibraries().then(r => {
    setAvailable(r.available); setLibs(r.libraries)
    // A library that just finished joins the switcher.
    const now = new Set(r.libraries.filter(l => l.state === 'ready').map(l => l.id))
    if ([...now].some(id => !ready.current.has(id)) && ready.current.size + now.size > 0) reloadAppConfig()
    ready.current = now
  }).catch(e => setNote(String((e as Error).message ?? e)))

  useEffect(() => {
    void load()
    api.agentSites().then(r => {
      const names = r.sites.filter(s => s.kind === 'cluster').map(s => s.name)
      setSites(names); setForm(f => (f.site || !names.length ? f : { ...f, site: names[0] }))
    }).catch(() => {})
  }, [])
  // Follow running builds.
  const running = libs.some(l => l.state === 'building' || l.state === 'updating')
  useEffect(() => {
    if (!running) return
    const t = window.setInterval(() => { void load() }, 15_000)
    return () => window.clearInterval(t)
  }, [running])

  const act = async (fn: () => Promise<unknown>, done?: string) => {
    setBusy(true); setNote('')
    try { await fn(); if (done) setNote(done); await load() } catch (e) { setNote(String((e as Error).message ?? e)) } finally { setBusy(false) }
  }
  const add = () => act(async () => {
    await api.addUserLibrary({
      site: form.site, sourceDir: form.sourceDir.trim() || '$WORKDIR', label: form.label.trim() || undefined,
      scheduler: form.slurm && form.partition.trim() ? { partition: form.partition.trim(), account: form.account.trim() || undefined } : null,
    })
    setForm(f => ({ ...f, label: '' }))
  }, 'Building the index on the cluster. It appears in the Library switcher when it is ready.')

  if (!available) return null
  const when = (iso: string | null) => (iso ? new Date(iso).toLocaleString() : '')
  return (
    <>
      <div className="tool-group">Your libraries</div>
      <p className="muted view-sub">
        Directories on your clusters, indexed there with your own key and searched there as you. Only you see them.
      </p>
      {libs.length > 0 && (
        <div className="rag-calls-wrap">
          <table className="rag-calls-table libraries-table">
            <thead><tr><th>Library</th><th>State</th><th>Contents</th><th></th></tr></thead>
            <tbody>
              {libs.map(l => (
                <tr key={l.id}>
                  <td>
                    <div className="lib-name">{l.label}</div>
                    <div className="lib-id">{l.site}, <code>{l.sourceRoot ?? l.sourceDir}</code></div>
                  </td>
                  <td>
                    <span className={`status-dot ${l.state === 'ready' ? 'ok' : l.state === 'failed' ? 'warn' : 'ok pulse'}`} />{' '}
                    {l.state === 'building' ? 'Building' : l.state === 'updating' ? 'Updating' : l.state === 'failed' ? 'Failed' : 'Ready'}
                    {l.run && (l.state === 'building' || l.state === 'updating') && <div className="lib-id">run {l.run.slug}</div>}
                    {l.error && <div className="lib-id">{l.error}</div>}
                  </td>
                  <td>
                    {l.directories != null && <div>{l.directories.toLocaleString()} folders, {(l.files ?? 0).toLocaleString()} files</div>}
                    {l.builtAt && <div className="lib-id">indexed {when(l.builtAt)}</div>}
                  </td>
                  <td className="lib-actions">
                    <button className="btn-secondary" disabled={busy || l.state === 'building' || l.state === 'updating'} onClick={() => void act(() => api.refreshUserLibrary(l.id), 'Updating the index on the cluster.')}>Refresh</button>
                    <button className="btn-secondary" disabled={busy} onClick={() => void act(async () => { await api.removeUserLibrary(l.id); reloadAppConfig() })}>Remove</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="settings-grid">
        <div>
          <label className="field-label">Cluster</label>
          <select className="field" value={form.site} onChange={e => setForm({ ...form, site: e.target.value })}>
            {!sites.length && <option value="">No active clusters</option>}
            {sites.map(s => <option key={s} value={s}>{s}</option>)}
          </select>
        </div>
        <div>
          <label className="field-label">Directory</label>
          <input className="field" placeholder="$WORKDIR" value={form.sourceDir} onChange={e => setForm({ ...form, sourceDir: e.target.value })} />
        </div>
        <div>
          <label className="field-label">Label</label>
          <input className="field" placeholder={`${form.site || 'cluster'} ${form.sourceDir || '$WORKDIR'}`} value={form.label} onChange={e => setForm({ ...form, label: e.target.value })} />
        </div>
        <div>
          <label className="field-label">
            <input type="checkbox" checked={form.slurm} onChange={e => setForm({ ...form, slurm: e.target.checked })} /> Build as a Slurm job (otherwise on the login node)
          </label>
          {form.slurm && (
            <div className="user-lib-slurm">
              <input className="field" placeholder="Partition" value={form.partition} onChange={e => setForm({ ...form, partition: e.target.value })} />
              <input className="field" placeholder="Account, if the site requires one" value={form.account} onChange={e => setForm({ ...form, account: e.target.value })} />
            </div>
          )}
        </div>
      </div>
      <div className="query-actions">
        <button className="btn-primary" disabled={busy || !form.site || (form.slurm && !form.partition.trim())} onClick={() => void add()}>{busy ? 'Starting…' : 'Add from cluster'}</button>
        {note && <span className="muted">{note}</span>}
      </div>
    </>
  )
}
