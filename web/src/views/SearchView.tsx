import { useEffect, useRef, useState } from 'react'
import { Button, CopyButton, EmptyState, Field, Input, SearchInput, StatusBadge, Switch, ToggleGroup, type BadgeVariant } from '@parallelworks/ui'
import { api, type PublicLibrary, SearchHit } from '../api'
import { TagMenu } from '../components/TagMenu'
import { RowCheck } from '../components/Explorer'
import { LibraryScope } from '../components/LibraryScope'
import { warmRemoteHosts } from '../api'
import { useAppConfig } from '../config'
import { SEARCH_PLACEHOLDER, librariesInScope, meaningOn, searchReach, useSearchScope } from '../searchReach'
import { openInLibrary, readScope, saveScope, scopeIsSeveral, scopeParam, useScope, type Scope } from '../scope'
import { GufiCommands, type GufiCommand } from '../components/GufiCommands'
import { OmittedLine, type Omission } from '../components/OmittedLine'
import { exportName, saveText, toCsv, toTsv } from '../tableExport'

/** A search as the address carries it, after #view=search?: the single
 *  box's q, or a refined search's name, text and match, and the libraries
 *  when not all of them (in=). */
export interface SearchState { q: string; name: string; text: string; match: 'any' | 'all'; scope: Scope | null }

export function searchFromHash(hash: string): SearchState | null {
  const at = hash.indexOf('?')
  if (!hash.startsWith('#view=search') || at < 0) return null
  const p = new URLSearchParams(hash.slice(at + 1))
  const ids = (p.get('in') ?? '').split(',').map(s => s.trim()).filter(Boolean)
  const st: SearchState = {
    q: p.get('q') ?? '', name: p.get('name') ?? '', text: p.get('text') ?? '',
    match: p.get('match') === 'all' ? 'all' : 'any',
    scope: ids.length ? ids : null,
  }
  return st.q || st.name || st.text ? st : null
}

export function searchHash(st: SearchState): string {
  const p = new URLSearchParams()
  if (st.name || st.text) {
    if (st.name) p.set('name', st.name)
    if (st.text) p.set('text', st.text)
    if (st.match === 'all') p.set('match', 'all')
  } else if (st.q) p.set('q', st.q)
  if (st.scope && st.scope !== 'all') p.set('in', st.scope.join(','))
  return `#view=search?${p}`
}

const MODE_LABEL: Record<SearchHit['mode'], string> = {
  fts: 'words',
  vector: 'meaning',
  name: 'name',
  path: 'folder',
}
// The badge tones have no purple; matches by meaning keep theirs from .pill-vector.
const MODE_TONE: Record<SearchHit['mode'], BadgeVariant> = {
  fts: 'info',
  vector: 'default',
  name: 'muted',
  path: 'muted',
}

/** Every way a hit was found, for its badge's tooltip: the badge names the
 *  most exact one. */
export function foundBy(h: Pick<SearchHit, 'mode' | 'kinds'>): string {
  const what: Record<SearchHit['mode'], string> = { fts: 'its words', name: 'its name', path: 'a folder it is in', vector: 'its meaning' }
  const kinds = h.kinds?.length ? h.kinds : [h.mode]
  const parts = kinds.map(k => what[k])
  return `Found by ${parts.length > 1 ? `${parts.slice(0, -1).join(', ')} and ${parts[parts.length - 1]}` : parts[0]}`
}

/** Search results as a table for export: one row per result, in the order
 *  shown, with how it was found, its size in bytes and its modified time in
 *  UTC. The library comes first when several were searched. */
export function hitTable(hits: SearchHit[], several: boolean): { columns: string[]; rows: string[][] } {
  const columns = [...(several ? ['library'] : []), 'path', 'name', 'found by', 'size', 'modified', 'labels', 'snippet']
  const rows = hits.map(h => [
    ...(several ? [h.library?.label ?? ''] : []),
    h.path, h.name,
    (h.kinds?.length ? h.kinds : [h.mode]).map(k => MODE_LABEL[k]).join(' '),
    String(h.size ?? ''),
    h.mtime ? new Date(h.mtime * 1000).toISOString().replace('.000Z', 'Z') : '',
    (h.tags ?? []).join(' '),
    h.snippet ?? '',
  ])
  return { columns, rows }
}

export function SearchView({ onOpen }: { onOpen: (path: string, q?: string) => void }) {
  // Libraries on other hosts: their ssh login happens now, not on the first search.
  useEffect(() => { warmRemoteHosts() }, [])
  const cfg = useAppConfig()
  const libraries = cfg.libraries ?? []
  const [scope, setScope] = useScope('search', libraries)
  const initial = useRef(searchFromHash(location.hash))
  const [q, setQ] = useState(initial.current?.q ?? '')
  // The refined form: names and paths in one field, text and meaning in the other.
  const [refineOpen, setRefineOpen] = useState(!!(initial.current?.name || initial.current?.text))
  const [name, setName] = useState(initial.current?.name ?? '')
  const [text, setText] = useState(initial.current?.text ?? '')
  const [match, setMatch] = useState<'any' | 'all'>(initial.current?.match ?? 'any')
  const [hits, setHits] = useState<SearchHit[] | null>(null)
  const [note, setNote] = useState<string | null>(null)
  const [omitted, setOmitted] = useState<Omission[] | null>(null)
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

  const refined = refineOpen && !!(name.trim() || text.trim())
  const run = async (limit = 50, query = q, over: Scope = scope) => {
    if (refineOpen ? !(name.trim() || text.trim()) : !query.trim()) return
    // The address carries the search, so it can be shared or kept.
    history.replaceState(null, '', location.pathname + location.search + searchHash({ q: refineOpen ? '' : query, name: refineOpen ? name : '', text: refineOpen ? text : '', match, scope: over }))
    setBusy(true)
    setNote(null)
    setOmitted(null)
    setSel(new Set())
    setTagMenuOpen(false)
    try {
      const r = refineOpen
        ? await api.search('', [...activeTags], limit, { library: scopeParam(over), name: name.trim(), text: text.trim(), match })
        : await api.search(query, [...activeTags], limit, { library: scopeParam(over) })
      setHits(r.hits)
      setGufi(r.gufi)
      setLastLimit(limit)
      // Every library left out, or searched in part, with the reason; one
      // that had not answered in time is among them.
      setOmitted(r.omitted ?? null)
      if (r.error) setNote(r.error)
      else if (r.slow?.length) setNote(`Choose ${r.slow.length === 1 ? 'a library that did not answer in time' : 'one of the libraries that did not answer in time'} alone in Searching to wait for it.`)
    } catch (e) {
      setHits(null)
      setNote(String((e as Error).message ?? e))
    } finally {
      setBusy(false)
    }
  }

  // The sidebar's search box hands its query here and opens this view.
  useEffect(() => {
    const on = (e: Event) => {
      const query = String((e as CustomEvent).detail?.q ?? '').trim()
      if (!query) return
      setRefineOpen(false)
      setQ(query)
      void run(50, query)
    }
    window.addEventListener('ade:search', on)
    return () => window.removeEventListener('ade:search', on)
  })

  // A search in the address runs on arrival, in the libraries it names.
  useEffect(() => {
    const st = initial.current
    if (!st) return
    if (st.scope) saveScope('search', st.scope)
    // The libraries the address names, else this page's own choice: the
    // configuration may not have arrived to settle it yet.
    void run(50, st.q, st.scope ?? readScope('search'))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const toggleSel = (path: string) => {
    setSel(s => {
      const next = new Set(s)
      if (next.has(path)) next.delete(path)
      else next.add(path)
      return next
    })
  }

  const allSelected = hits !== null && hits.length > 0 && sel.size === hits.length

  // Other libraries chosen mean other results: run the search again there.
  const pick = (next: Scope) => {
    setScope(next)
    if (refined || (!refineOpen && q.trim())) void run(lastLimit, q, next)
  }
  const several = scopeIsSeveral(scope, libraries)
  const enter = (e: { key: string }) => { if (e.key === 'Enter') void run() }
  return (
    <div className="search-view">
      <div className="search-hero card">
        {libraries.length > 1 && <div className="scope-bar"><span className="muted">Searching</span><LibraryScope libraries={libraries} value={scope} onChange={pick} hint="Which libraries to search" /></div>}
        <div className="search-bar">
          {!refineOpen && (
            <SearchInput
              className="search-box"
              value={q}
              onValueChange={setQ}
              onKeyDown={enter}
              placeholder={SEARCH_PLACEHOLDER[searchReach(libraries, scope)]}
              autoFocus
            />
          )}
          {refineOpen && <span className="search-refine-title">Refined search</span>}
          <Button variant="primary" loading={busy} onClick={() => run()}>{busy ? 'Searching…' : 'Search'}</Button>
          <Button variant="ghost" size="sm" aria-expanded={refineOpen} onClick={() => setRefineOpen(o => !o)}
            title={refineOpen ? 'Back to one search box' : 'Search names and paths apart from the text and meaning of files'}>
            {refineOpen ? 'One box' : 'Refine'}
          </Button>
        </div>
        {refineOpen && (
          <div className="search-refine">
            <Input
              label="Name or path"
              size="sm"
              value={name}
              onChange={e => setName(e.target.value)}
              onKeyDown={enter}
              placeholder="Words or patterns, such as budget *.xlsx"
              autoFocus
            />
            <Input
              label="Text or meaning"
              size="sm"
              value={text}
              onChange={e => setText(e.target.value)}
              onKeyDown={enter}
              placeholder={'Words, "exact phrase", AND, OR, NOT, prefix*'}
            />
            <Field label="Search text and meaning only in files whose name or path matches" controlFirst size="sm" className="search-refine-match"
              description={match === 'all' ? 'Lists the files that match both.' : 'Shows matches of either kind, each to its own limit.'}>
              <Switch checked={match === 'all'} onCheckedChange={v => setMatch(v ? 'all' : 'any')} aria-label="Only in files whose name or path matches" />
            </Field>
          </div>
        )}
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
        <SearchHelp libraries={libraries} />
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
                <CopyButton size="sm" variant="outline" showLabel label="results" text={() => { const t = hitTable(hits, several); return toTsv(t.columns, t.rows) }}
                  title="Copy the results as tab-separated text, which a spreadsheet pastes into columns" />
                <Button size="sm" variant="outline" title="Save the results as a CSV file"
                  onClick={() => { const t = hitTable(hits, several); saveText(exportName('search', refineOpen ? `${name} ${text}` : q), toCsv(t.columns, t.rows)) }}>
                  Export CSV
                </Button>
              </span>
            )}
          </div>
          <OmittedLine omitted={omitted} />
          {hits.length === 0 && <EmptyState variant="bare" title="No matches." />}
          {hits.map(h => (
            <div key={`${h.library?.id ?? ''}/${h.path}`} className={`result-row ${sel.has(h.path) ? 'multi' : ''}`}
              onClick={() => openInLibrary(p => onOpen(p, refineOpen ? text || name : q), h.library?.id, h.path)}>
              <div className="result-top">
                <RowCheck checked={sel.has(h.path)} onToggle={() => toggleSel(h.path)} />
                <span className="result-path">{h.path}</span>
                <span className="result-pills">
                  {several && h.library && (
                    <span title="The library this match is in"><StatusBadge>{h.library.label}</StatusBadge></span>
                  )}
                  {(h.tags ?? []).map(t => <span key={t} className="tag-pill on small">{t}</span>)}
                  <span title={foundBy(h)}><StatusBadge variant={MODE_TONE[h.mode]} className={h.mode === 'vector' ? 'pill-vector' : undefined}>{MODE_LABEL[h.mode]}</StatusBadge></span>
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
          <p><i>By name:</i> any part of a file&apos;s name.</p>
          <p><i>By folder:</i> a folder the file is in has the search in its name.</p>
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
          <p>
            Each result says how it matched: name, folder, words, or meaning; hover it for every way it did. Files found more than one way
            come first.
          </p>
          <p>
            <i>Refine:</i> searches names and paths apart from the text. Name or path takes words, which match any part of a name or a
            folder, and patterns such as <code>*.docx</code>. Text or meaning takes the words in files, with <code>AND</code>,{' '}
            <code>OR</code>, <code>NOT</code>, <code>"phrases"</code> and <code>prefix*</code>, and is searched by meaning without them.
            With its switch on, words and meaning are looked for only in the files whose name or path matches.
          </p>
        </div>
      )}
    </div>
  )
}

