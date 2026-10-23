import { useEffect, useState } from 'react'
import { Button, EmptyState, SearchInput, StatusBadge, ToggleGroup, type BadgeVariant } from '@parallelworks/ui'
import { api, type PublicLibrary, SearchHit } from '../api'
import { TagMenu } from '../components/TagMenu'
import { RowCheck } from '../components/Explorer'
import { ALL_LIBRARIES, LibrarySwitch } from '../components/LibrarySwitch'
import { currentLibrary, onLibraryChange } from '../api'
import { useAppConfig } from '../config'
import { SEARCH_PLACEHOLDER, librariesInScope, meaningOn, searchReach, useSearchScope } from '../searchReach'
import { GufiCommands, type GufiCommand } from '../components/GufiCommands'

const MODE_LABEL: Record<SearchHit['mode'], string> = {
  fts: 'words',
  vector: 'meaning',
  name: 'name',
}
// The badge tones have no purple; matches by meaning keep theirs from .pill-vector.
const MODE_TONE: Record<SearchHit['mode'], BadgeVariant> = {
  fts: 'info',
  vector: 'default',
  name: 'muted',
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
      // Among several libraries, one that had not answered in time is left out.
      else if (r.slow?.length) setNote(`Still waiting for ${r.slow.join(', ')}, so ${r.slow.length === 1 ? 'it is' : 'they are'} left out of these results. Choose ${r.slow.length === 1 ? 'it' : 'one'} alone in Searching to wait for it.`)
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
  const scope = useSearchScope()
  // A new library means new results: run the search again there.
  useEffect(() => onLibraryChange(() => { if (q.trim()) void run(lastLimit) }), [q, lastLimit])
  return (
    <div className="search-view">
      <div className="search-hero card">
        {(cfg.libraries?.length ?? 0) > 1 && <div className="scope-bar"><span className="muted">Searching</span><LibrarySwitch libraries={cfg.libraries ?? []} /></div>}
        <div className="search-bar">
          <SearchInput
            className="search-box"
            value={q}
            onValueChange={setQ}
            onKeyDown={e => e.key === 'Enter' && run()}
            placeholder={SEARCH_PLACEHOLDER[searchReach(cfg.libraries ?? [], scope)]}
            autoFocus
          />
          <Button variant="primary" loading={busy} onClick={() => run()}>{busy ? 'Searching…' : 'Search'}</Button>
        </div>
        {vocab.length > 0 && (
          <div className="search-label-filter">
            <span className="lbl">Labels</span>
            <ToggleGroup
              label="Labels"
              options={vocab.map(v => ({ value: v.tag, label: v.tag, count: v.count }))}
              value={[...activeTags]}
              onValueChange={next => setActiveTags(new Set(next))}
            />
          </div>
        )}
        <SearchHelp libraries={cfg.libraries ?? []} />
        {note && <p className="muted">{note}</p>}
        {hits && <GufiCommands commands={gufi} />}
      </div>
      {hits && (
        <div className={`search-results card ${sel.size ? 'has-sel' : ''}`}>
          <div className="results-head results-toolbar">
            <span>{hits.length} result{hits.length === 1 ? '' : 's'}</span>
            {hits.length > 0 && (
              <span className="results-actions">
                <Button size="sm" onClick={() => setSel(allSelected ? new Set() : new Set(hits.map(h => h.path)))}>
                  {allSelected ? 'Clear selection' : 'Select all'}
                </Button>
                <Button size="sm" disabled={!sel.size} onClick={() => setTagMenuOpen(o => !o)}>
                  Labels…{sel.size ? ` (${sel.size})` : ''}
                </Button>
              </span>
            )}
          </div>
          {hits.length === 0 && <EmptyState variant="bare" title="No matches." />}
          {hits.map(h => (
            <div key={`${h.library?.id ?? ''}/${h.path}`} className={`result-row ${sel.has(h.path) ? 'multi' : ''}`}
              onClick={() => onOpen(currentLibrary() === ALL_LIBRARIES && h.library ? `${h.library.id}/${h.path}` : h.path, q)}>
              <div className="result-top">
                <RowCheck checked={sel.has(h.path)} onToggle={() => toggleSel(h.path)} />
                <span className="result-path">{h.path}</span>
                <span className="result-pills">
                  {currentLibrary() === ALL_LIBRARIES && h.library && (
                    <span title="The library this match is in"><StatusBadge>{h.library.label}</StatusBadge></span>
                  )}
                  {(h.tags ?? []).map(t => <span key={t} className="tag-pill on small">{t}</span>)}
                  <StatusBadge variant={MODE_TONE[h.mode]} className={h.mode === 'vector' ? 'pill-vector' : undefined}>{MODE_LABEL[h.mode]}</StatusBadge>
                </span>
              </div>
              {h.snippet && <div className="result-snippet">{h.snippet}</div>}
            </div>
          ))}
          {hits.length >= lastLimit && lastLimit < 1000 && (
            <div className="results-more">
              <Button loading={busy} onClick={() => run(1000)}>
                {busy ? 'Loading…' : 'Load more matches (up to 1,000)'}
              </Button>
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

/**
 * What a search here matches: names and the words in files always, and
 * meaning in the libraries that have it, said in one line under the box,
 * with how each kind works a click away. Search by meaning is a choice per
 * library, so the line names where it is on.
 */
export function SearchHelp({ libraries }: { libraries: PublicLibrary[] }) {
  const [open, setOpen] = useState(false)
  const scope = useSearchScope()
  const inScope = librariesInScope(libraries, scope)
  const on = inScope.filter(meaningOn)
  const names = (ls: PublicLibrary[]) => ls.map(l => l.label).join(', ')
  const summary = inScope.length && searchReach(libraries, scope) === 'names'
    ? 'Matches file and folder names: nothing inside the files is indexed here.'
    : !inScope.length || !on.length
    ? 'Matches file names and the words inside files. Search by meaning is off here, so use words the files contain.'
    : on.length === inScope.length
      ? 'Matches file names, the words inside files, and their meaning: a plain question also finds passages that say it in other words.'
      : `Matches file names and the words inside files, and also meaning in ${names(on)}.`
  return (
    <div className="search-help">
      <p className="muted search-help-line">
        {summary}{' '}
        <Button variant="ghost" size="sm" aria-expanded={open} onClick={() => setOpen(o => !o)}>{open ? 'Hide' : 'How search works'}</Button>
      </p>
      {open && (
        <div className="search-help-detail muted">
          <p><i>By name:</i> any part of a file or folder name.</p>
          <p>
            <i>By words:</i> whole words in the text of documents, notes and code. <code>"exact phrase"</code>, <code>OR</code>,{' '}
            <code>-exclude</code> and <code>prefix*</code> narrow it.
          </p>
          <p>
            <i>By meaning:</i> passages that say the same thing in other words, so a question such as "why did the blades
            crack" finds a report on fatigue failure that never uses those words. It runs for a few plain words; quotes,
            operators, one short word, or a code such as ABC-12 match words only. Each library&apos;s depth sets it, under{' '}
            <a href="#view=settings:libraries">Settings, Libraries</a>: Meaning on documents covers documents and images,
            Meaning on all files every file with text, and the stops before them leave it off. Here it is {inScope.map((l, i) => (
              <span key={l.id}>{i ? ', ' : ''}{l.label}: {meaningOn(l) ? (l.meaning === 'full' ? 'all files' : l.meaning === 'documents' ? 'documents' : 'its own vectors') : 'off'}</span>
            ))}.
          </p>
          <p>Each result says how it matched: name, words, or meaning.</p>
        </div>
      )}
    </div>
  )
}

