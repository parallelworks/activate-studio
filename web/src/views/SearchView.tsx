import { useEffect, useState } from 'react'
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
  const [vocab, setVocab] = useState<{ tag: string; count: number }[]>([])
  const [activeTags, setActiveTags] = useState<Set<string>>(new Set())

  useEffect(() => {
    api.tagVocabulary().then(v => setVocab(v.tags)).catch(() => {})
  }, [])

  const run = async () => {
    if (!q.trim()) return
    setBusy(true)
    setNote(null)
    try {
      const r = await api.search(q, [...activeTags])
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
        <h1 className="view-title">Search the knowledge base</h1>
        <p className="muted view-sub">Full-text and semantic search across the corpus. Office document and PDF content is included.</p>
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
        {vocab.length > 0 && (
          <div className="search-label-filter">
            <span className="lbl">Labels</span>
            {vocab.map(v => (
              <button
                key={v.tag}
                className={`tag-pill ${activeTags.has(v.tag) ? 'on' : ''}`}
                onClick={() => setActiveTags(s => {
                  const next = new Set(s)
                  if (next.has(v.tag)) next.delete(v.tag)
                  else next.add(v.tag)
                  return next
                })}
              >{v.tag}</button>
            ))}
          </div>
        )}
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
                <span className="result-pills">
                  {(h.tags ?? []).map(t => <span key={t} className="tag-pill on small">{t}</span>)}
                  <span className={`pill pill-${h.mode}`}>{MODE_LABEL[h.mode]}</span>
                </span>
              </div>
              {h.snippet && <div className="result-snippet">{h.snippet}</div>}
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
