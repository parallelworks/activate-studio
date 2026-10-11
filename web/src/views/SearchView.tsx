import { useEffect, useState } from 'react'
import { api, SearchHit } from '../api'
import { TagMenu } from '../components/TagMenu'
import { RowCheck } from '../components/Explorer'
import { ALL_LIBRARIES, LibrarySwitch } from '../components/LibrarySwitch'
import { currentLibrary, onLibraryChange } from '../api'
import { useAppConfig } from '../config'
import { GufiCommands, type GufiCommand } from '../components/GufiCommands'

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
  const [gufi, setGufi] = useState<GufiCommand[] | undefined>(undefined)

  useEffect(() => {
    api.tagVocabulary().then(v => setVocab(v.tags)).catch(() => {})
  }, [])

  const run = async (limit = 50, query = q) => {
    if (!query.trim()) return
    setBusy(true)
    setNote(null)
    setSel(new Set())
    setTagMenuOpen(false)
    try {
      const r = await api.search(query, [...activeTags], limit)
      setHits(r.hits)
      setGufi(r.gufi)
      setLastLimit(limit)
      if (r.error) setNote(r.error)
    } catch (e) {
      setNote(String(e))
    } finally {
      setBusy(false)
    }
  }

  // The sidebar's search box hands its query here and opens this view.
  useEffect(() => {
    const on = (e: Event) => {
      const query = String((e as CustomEvent).detail?.q ?? '').trim()
      if (!query) return
      setQ(query)
      void run(50, query)
    }
    window.addEventListener('ade:search', on)
    return () => window.removeEventListener('ade:search', on)
  })

  const toggleSel = (path: string) => {
    setSel(s => {
      const next = new Set(s)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  const allSelected = hits !== null && hits.length > 0 && sel.size === hits.length

  const cfg = useAppConfig()
  // A new library means new results: run the search again there.
  useEffect(() => onLibraryChange(() => { if (q.trim()) void run(lastLimit) }), [q, lastLimit])
  return (
    <div className="search-view">
      {(cfg.libraries?.length ?? 0) > 1 && <div className="scope-bar"><span className="muted">Searching</span><LibrarySwitch libraries={cfg.libraries ?? []} /></div>}
      <div className="search-hero card">
        <div className="search-bar">
          <input
            value={q}
            onChange={e => setQ(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && run()}
            placeholder={'Whole words; "exact phrase", OR, -exclude, prefix*'}
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
        {hits && <GufiCommands commands={gufi} />}
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
            <div key={`${h.library?.id ?? ''}/${h.path}`} className={`result-row ${sel.has(h.path) ? 'multi' : ''}`}
              onClick={() => onOpen(currentLibrary() === ALL_LIBRARIES && h.library ? `${h.library.id}/${h.path}` : h.path, q)}>
              <div className="result-top">
                <RowCheck checked={sel.has(h.path)} onToggle={() => toggleSel(h.path)} />
                <span className="result-path">{h.path}</span>
                <span className="result-pills">
                  {currentLibrary() === ALL_LIBRARIES && h.library && <span className="pill pill-library" title="The library this match is in">{h.library.label}</span>}
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
