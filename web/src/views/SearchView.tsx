import { useState } from 'react'
import { api, SearchHit } from '../api'

const MODE_LABEL: Record<SearchHit['mode'], string> = {
  fts: 'full text',
  vector: 'semantic',
  name: 'filename',
}

export function SearchView({ onOpen }: { onOpen: (path: string) => void }) {
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
    <div className="search-view">
      <div className="search-hero card">
        <h1>Search the knowledge base</h1>
        <p className="muted">Full-text and semantic search across the corpus. Office document and PDF content is included.</p>
        <div className="search-bar">
          <input
            value={q}
            onChange={e => setQ(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && run()}
            placeholder="Search files, documents, and topics"
            autoFocus
          />
          <button className="btn-primary" onClick={run} disabled={busy}>{busy ? 'Searching…' : 'Search'}</button>
        </div>
        {note && <p className="muted">{note}</p>}
      </div>
      {hits && (
        <div className="search-results card">
          <div className="results-head">{hits.length} result{hits.length === 1 ? '' : 's'}</div>
          {hits.length === 0 && <p className="muted pad">No matches.</p>}
          {hits.map(h => (
            <div key={h.path} className="result-row" onClick={() => onOpen(h.path)}>
              <div className="result-top">
                <span className="result-path">{h.path}</span>
                <span className={`pill pill-${h.mode}`}>{MODE_LABEL[h.mode]}</span>
              </div>
              {h.snippet && <div className="result-snippet">{h.snippet}</div>}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
