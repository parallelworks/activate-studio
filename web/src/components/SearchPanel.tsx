import { useState } from 'react'
import { api, SearchHit } from '../api'

export function SearchPanel({ onOpen }: { onOpen: (path: string) => void }) {
  const [q, setQ] = useState('')
  const [hits, setHits] = useState<SearchHit[] | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const run = async () => {
    if (!q.trim()) return
    setBusy(true)
    setNote(null)
    try {
      const r = await api.search(q)
      setHits(r.hits)
      if (r.error) setNote(r.error)
    } catch (e) {
      setNote(String(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="search-panel">
      <div className="search-bar">
        <input
          value={q}
          onChange={e => setQ(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && run()}
          placeholder="Search the knowledge base"
        />
        <button onClick={run} disabled={busy}>{busy ? '…' : 'Search'}</button>
      </div>
      {note && <p className="viewer-meta">{note}</p>}
      {hits && (
        <div className="search-results">
          {hits.length === 0 && <p className="viewer-meta">No matches.</p>}
          {hits.map(h => (
            <div key={h.path} className="search-hit" onClick={() => onOpen(h.path)}>
              <div className="hit-path">{h.path}</div>
              {h.snippet && <div className="hit-snippet">{h.snippet}</div>}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
