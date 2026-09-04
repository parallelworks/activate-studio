import { useEffect, useState } from 'react'
import { api, SearchHit } from '../api'
import { TagMenu } from '../components/TagMenu'
import { RowCheck } from '../components/Explorer'

const MODE_LABEL: Record<SearchHit['mode'], string> = {
  fts: 'full text',
  vector: 'semantic',
  name: 'filename',
}

export function SearchView({ onOpen }: { onOpen: (path: string, q?: string) => void }) {
  const [q, setQ] = useState('')
  const [hits, setHits] = useState<SearchHit[] | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [vocab, setVocab] = useState<{ tag: string; count: number }[]>([])
  const [activeTags, setActiveTags] = useState<Set<string>>(new Set())
  const [sel, setSel] = useState<Set<string>>(new Set())
  const [tagMenuOpen, setTagMenuOpen] = useState(false)
  const [lastLimit, setLastLimit] = useState(50)

  useEffect(() => {
    api.tagVocabulary().then(v => setVocab(v.tags)).catch(() => {})
  }, [])

  const run = async (limit = 50) => {
    if (!q.trim()) return
    setBusy(true)
    setNote(null)
    setSel(new Set())
    setTagMenuOpen(false)
    try {
      const r = await api.search(q, [...activeTags], limit)
      setHits(r.hits)
      setLastLimit(limit)
      if (r.error) setNote(r.error)
    } catch (e) {
      setNote(String(e))
    } finally {
      setBusy(false)
    }
  }

  const toggleSel = (path: string) => {
    setSel(s => {
      const next = new Set(s)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  const allSelected = hits !== null && hits.length > 0 && sel.size === hits.length

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
          <button className="btn-primary" onClick={() => run()} disabled={busy}>{busy ? 'Searching…' : 'Search'}</button>
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
        <div className={`search-results card ${sel.size ? 'has-sel' : ''}`}>
          <div className="results-head results-toolbar">
            <span>{hits.length} result{hits.length === 1 ? '' : 's'}</span>
            {hits.length > 0 && (
              <span className="results-actions">
                <button
                  className="btn-secondary"
                  onClick={() => setSel(allSelected ? new Set() : new Set(hits.map(h => h.path)))}
                >{allSelected ? 'Clear selection' : 'Select all'}</button>
                <button
                  className="btn-secondary"
                  disabled={!sel.size}
                  onClick={() => setTagMenuOpen(o => !o)}
                >Labels…{sel.size ? ` (${sel.size})` : ''}</button>
              </span>
            )}
          </div>
          {hits.length === 0 && <p className="muted pad">No matches.</p>}
          {hits.map(h => (
            <div key={h.path} className={`result-row ${sel.has(h.path) ? 'multi' : ''}`} onClick={() => onOpen(h.path, q)}>
              <div className="result-top">
                <RowCheck checked={sel.has(h.path)} onToggle={() => toggleSel(h.path)} />
                <span className="result-path">{h.path}</span>
                <span className="result-pills">
                  {(h.tags ?? []).map(t => <span key={t} className="tag-pill on small">{t}</span>)}
                  <span className={`pill pill-${h.mode}`}>{MODE_LABEL[h.mode]}</span>
                </span>
              </div>
              {h.snippet && <div className="result-snippet">{h.snippet}</div>}
            </div>
          ))}
          {hits.length >= lastLimit && lastLimit < 1000 && (
            <div className="results-more">
              <button className="btn-secondary" disabled={busy} onClick={() => run(1000)}>
                {busy ? 'Loading…' : 'Load more matches (up to 1,000)'}
              </button>
            </div>
          )}
        </div>
      )}
      {tagMenuOpen && sel.size > 0 && (
        <TagMenu
          paths={[...sel]}
          onClose={() => setTagMenuOpen(false)}
          onApplied={updated => {
            setHits(hs => hs?.map(h => (updated[h.path] ? { ...h, tags: updated[h.path] } : h)) ?? hs)
          }}
        />
      )}
    </div>
  )
}
