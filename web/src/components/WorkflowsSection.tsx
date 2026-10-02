import { useEffect, useMemo, useState } from 'react'

/**
 * Settings > Workflows: which platform workflows this Studio offers on its
 * Workflows tab. The list to pick from is every workflow on the account
 * the Studio is deployed under, so a site deployment offers from the
 * site's own workflows. Order is the order of the tiles.
 */

interface CatalogRow { name: string; displayName: string; description: string; tags: string[]; curated: boolean }

export function WorkflowsSection() {
  const [catalog, setCatalog] = useState<CatalogRow[] | null>(null)
  const [source, setSource] = useState<'deployment' | 'viewer'>('deployment')
  const [picked, setPicked] = useState<string[]>([])
  const [filter, setFilter] = useState('')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [dirty, setDirty] = useState(false)

  useEffect(() => {
    fetch('/api/settings').then(r => r.json()).then(d => setPicked(d.effective?.workflowCollection ?? [])).catch(() => {})
    fetch('/api/workflows/catalog').then(async r => {
      const d = await r.json()
      if (!r.ok) throw new Error(d.error ?? `${r.status}`)
      setCatalog(d.workflows)
      setSource(d.source === 'viewer' ? 'viewer' : 'deployment')
    }).catch(e => setNote(`Cannot list the platform's workflows: ${(e as Error).message}`))
  }, [])

  const byName = useMemo(() => new Map((catalog ?? []).map(w => [w.name, w])), [catalog])
  const shown = (catalog ?? []).filter(w => {
    const q = filter.trim().toLowerCase()
    return !q || w.name.toLowerCase().includes(q) || w.displayName.toLowerCase().includes(q) || w.tags.some(t => t.toLowerCase().includes(q))
  })
  const toggle = (name: string) => { setDirty(true); setPicked(p => p.includes(name) ? p.filter(n => n !== name) : [...p, name]) }
  const move = (i: number, d: -1 | 1) => {
    const j = i + d
    if (j < 0 || j >= picked.length) return
    setDirty(true)
    setPicked(p => { const n = [...p]; [n[i], n[j]] = [n[j], n[i]]; return n })
  }
  const save = async () => {
    setBusy(true); setNote('')
    try {
      const r = await fetch('/api/settings', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ workflowCollection: picked }) })
      const d = await r.json()
      if (!r.ok) throw new Error(d.error ?? `${r.status}`)
      setDirty(false); setNote('Saved. The Workflows tab shows the new set on its next load.')
    } catch (e) { setNote(String((e as Error).message)) } finally { setBusy(false) }
  }

  return (
    <>
      <h1>Workflows</h1>
      <p className="muted view-sub">
        The workflows this Studio offers as tiles on its Workflows tab, each run from its own form under the viewer's
        account. Pick them from the workflows on the platform account this Studio is deployed under. A viewer who does
        not have one yet can add a copy to their account from the tile.
      </p>

      <div className="tool-group">Offered here ({picked.length})</div>
      {picked.length === 0 && <p className="muted">None yet. Tick workflows below.</p>}
      {picked.length > 0 && (
        <ol className="wf-picked">
          {picked.map((n, i) => (
            <li key={n}>
              <span className="wf-picked-name">{byName.get(n)?.displayName ?? n}</span>
              <code className="muted">{n}</code>
              {catalog && !byName.has(n) && <span className="wf-fail">not on the platform account</span>}
              <span className="wf-picked-actions">
                <button className="btn-link" disabled={i === 0} onClick={() => move(i, -1)} aria-label="Move up">&uarr;</button>
                <button className="btn-link" disabled={i === picked.length - 1} onClick={() => move(i, 1)} aria-label="Move down">&darr;</button>
                <button className="btn-link" onClick={() => toggle(n)}>Remove</button>
              </span>
            </li>
          ))}
        </ol>
      )}
      <div className="query-actions">
        <button className="btn-primary" disabled={busy || !dirty} onClick={() => void save()}>{busy ? 'Saving…' : 'Save'}</button>
        {note && <span className="muted">{note}</span>}
      </div>

      <div className="tool-group">On the platform{catalog ? ` (${catalog.length})` : ''}</div>
      {catalog && source === 'viewer' && (
        <p className="muted">Listed from your own account, because the deployment's platform credential is not usable. Until it is renewed, other viewers can run a workflow picked here only if it is already in their own account.</p>
      )}
      <input className="field" placeholder="Filter by name or tag" value={filter} onChange={e => setFilter(e.target.value)} />
      {!catalog && !note && <p className="muted">Loading…</p>}
      {catalog && (
        <div className="rag-calls-wrap">
          <table className="rag-calls-table wf-catalog">
            <thead><tr><th></th><th>Workflow</th><th>Description</th></tr></thead>
            <tbody>
              {shown.map(w => (
                <tr key={w.name}>
                  <td><input type="checkbox" checked={picked.includes(w.name)} onChange={() => toggle(w.name)} aria-label={`Offer ${w.displayName}`} /></td>
                  <td><div>{w.displayName}</div><code className="muted">{w.name}</code></td>
                  <td className="muted">{w.description}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  )
}
