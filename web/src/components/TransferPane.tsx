import { useCallback, useEffect, useMemo, useRef, useState, type MouseEvent as ReactMouseEvent } from 'react'
import {
  Breadcrumbs, Button, Callout, Checkbox, Combobox, EmptyState, IconButton, Input, Loader, RefreshButton,
  SearchInput, StatusBadge, type ComboboxGroup,
} from '@parallelworks/ui'
import {
  KIND_ORDER, TransferError, bytesText, joinPath, rangeSelect, sortEntries, transferApi,
  type BrowseEntry, type BrowseResult, type TransferLocation,
} from '../transferApi'

/** One side of the transfer page: where it is, and what in it is selected. */
export interface PaneState {
  locationId: string | null
  path: string
  selected: string[]
}

export interface PaneHandle {
  location: TransferLocation | null
  entries: BrowseEntry[]
}

const FolderGlyph = () => (
  <svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.3" aria-hidden="true"><path d="M1.5 3.5A1 1 0 0 1 2.5 2.5h3l1.5 2h6a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1v-9z"/></svg>
)
const FileGlyph = () => (
  <svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.3" aria-hidden="true"><path d="M4 1.5h5.5L13 5v9.5H4z"/><path d="M9.5 1.5V5H13"/></svg>
)
const LinkGlyph = () => (
  <svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.3" aria-hidden="true"><path d="M6.5 9.5 9.5 6.5"/><path d="M7 4.5 8.5 3a2.5 2.5 0 0 1 3.5 3.5L10.5 8"/><path d="M9 11.5 7.5 13A2.5 2.5 0 0 1 4 9.5L5.5 8"/></svg>
)
/** rclone remotes: a cloud, since most are cloud storage. */
const CloudGlyph = () => (
  <svg viewBox="0 0 16 16" width="15" height="15" fill="none" stroke="currentColor" strokeWidth="1.3" aria-hidden="true"><path d="M4.5 12.5h7.25a2.75 2.75 0 0 0 .4-5.47A4 4 0 0 0 4.4 6.6 3 3 0 0 0 4.5 12.5z"/></svg>
)
const UpGlyph = () => (
  <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true"><path d="M8 13V3.5M4 7l4-4 4 4"/></svg>
)

function modifiedText(iso: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  // The year only when it is not this one, so the column stays narrow.
  const year = d.getFullYear() === new Date().getFullYear() ? undefined : 'numeric'
  return d.toLocaleString(undefined, { year, month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' })
}

/** The location chooser's groups, in the page's order, each location with
 *  its detail line, and unavailable ones disabled with their reason. */
function locationItems(locations: TransferLocation[]): ComboboxGroup<string>[] {
  return KIND_ORDER.map(({ kind, label }) => ({
    label,
    icon: kind === 'rclone' ? <CloudGlyph /> : undefined,
    items: locations.filter(l => l.kind === kind).map(l => ({
      value: l.id,
      label: l.label,
      description: l.available ? l.detail : `${l.detail ? `${l.detail}. ` : ''}Unavailable: ${l.why ?? 'not reachable now'}`,
      hint: [l.indexed ? 'Indexed' : '', l.writable ? '' : 'Read only'].filter(Boolean).join(', ') || undefined,
      disabled: !l.available,
    })),
  })).filter(g => g.items.length)
}

export function TransferPane({
  name, locations, state, onChange, onLoaded, refreshKey, role,
}: {
  /** Names the pane for assistive technology, such as "First location". */
  name: string
  locations: TransferLocation[]
  state: PaneState
  onChange: (next: PaneState) => void
  /** Reports the entries listed, which the page needs to tell files from folders. */
  onLoaded: (h: PaneHandle) => void
  /** Bumped by the page to list the folder again, as after a transfer lands here. */
  refreshKey: number
  /** Which way this pane faces on a narrow screen, shown as a tag. */
  role?: 'source' | 'destination'
}) {
  const location = locations.find(l => l.id === state.locationId) ?? null
  const [listing, setListing] = useState<BrowseResult | null>(null)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<{ status: number; text: string } | null>(null)
  const [filter, setFilter] = useState('')
  const [typedPath, setTypedPath] = useState(state.path)
  const anchor = useRef<string | null>(null)
  const seq = useRef(0)

  useEffect(() => { setTypedPath(state.path) }, [state.path])

  const load = useCallback(async () => {
    if (!location || !location.available || !location.browsable) {
      setListing(null); setError(null); onLoaded({ location, entries: [] })
      return
    }
    const mine = ++seq.current
    setLoading(true)
    try {
      const r = await transferApi.browse(location.id, state.path)
      if (mine !== seq.current) return
      setListing(r); setError(null)
      onLoaded({ location, entries: r.entries })
    } catch (e) {
      if (mine !== seq.current) return
      const err = e as TransferError
      setListing(null); setError({ status: err.status ?? 0, text: err.message })
      onLoaded({ location, entries: [] })
    } finally {
      if (mine === seq.current) setLoading(false)
    }
    // onLoaded is the page's setter for this pane; it does not change what is listed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [location?.id, location?.available, location?.browsable, state.path])

  useEffect(() => { void load() }, [load, refreshKey])
  useEffect(() => { setFilter(''); anchor.current = null }, [state.locationId, state.path])

  const go = (path: string) => onChange({ ...state, path, selected: [] })
  const entries = useMemo(() => sortEntries(listing?.entries ?? []), [listing])
  const q = filter.trim().toLowerCase()
  const visible = useMemo(() => (q ? entries.filter(e => e.name.toLowerCase().includes(q)) : entries), [entries, q])
  const visibleNames = visible.map(e => e.name)
  const selectedSet = new Set(state.selected)
  const selectedVisible = visibleNames.filter(n => selectedSet.has(n)).length
  const allState = selectedVisible === 0 ? false : selectedVisible === visible.length ? true : 'some'

  const setSelected = (selected: string[]) => onChange({ ...state, selected })
  const toggle = (name: string, shift: boolean) => {
    if (shift && anchor.current) setSelected(rangeSelect(visibleNames, state.selected, anchor.current, name))
    else setSelected(selectedSet.has(name) ? state.selected.filter(n => n !== name) : [...state.selected, name])
    anchor.current = name
  }
  const rowClick = (e: ReactMouseEvent, name: string) => {
    if ((e.target as HTMLElement).closest('button, a, input, [role="checkbox"]')) return
    if (e.shiftKey && anchor.current) { setSelected(rangeSelect(visibleNames, e.metaKey || e.ctrlKey ? state.selected : [], anchor.current, name)); return }
    if (e.metaKey || e.ctrlKey) toggle(name, false)
    else { setSelected([name]); anchor.current = name }
  }

  const crumbs = useMemo(() => {
    const parts = state.path.split('/').filter(Boolean)
    return [
      { label: location?.label ?? 'Location' },
      ...parts.map(p => ({ label: p })),
    ]
  }, [state.path, location?.label])
  const parent = listing ? listing.parent : state.path ? state.path.split('/').slice(0, -1).join('/') : null

  return (
    <section className="xfer-pane" aria-label={name}>
      <div className="xfer-pane-head">
        <div className="xfer-pane-picker">
          <Combobox<string>
            aria-label={`${name}: location`}
            placeholder="Choose a location"
            items={locationItems(locations)}
            value={state.locationId}
            onValueChange={v => { if (v) onChange({ locationId: v, path: '', selected: [] }) }}
            itemToStringLabel={v => locations.find(l => l.id === v)?.label ?? v}
            size="sm"
          />
          {role && <StatusBadge size="sm" variant={role === 'source' ? 'accent' : 'muted'}>{role === 'source' ? 'From' : 'To'}</StatusBadge>}
        </div>
        <a className="xfer-add-location" href="#view=settings:transfers">Add a location</a>
        {location && (
          <div className="xfer-pane-facts">
            <span className="xfer-pane-detail" title={location.detail}>{location.detail}</span>
            {location.indexed && <StatusBadge size="sm" variant="muted">Indexed</StatusBadge>}
            {!location.writable && <StatusBadge size="sm" variant="muted">Read only</StatusBadge>}
          </div>
        )}
      </div>

      {location && (
        <div className="xfer-pane-nav">
          <IconButton label="Up one folder" icon={<UpGlyph />} size="sm" disabled={parent === null || !location.available} onClick={() => { if (parent !== null) go(parent) }} />
          {location.browsable
            ? <Breadcrumbs className="xfer-crumbs" crumbs={crumbs} current="location" label={`${name}: path`}
                onSelect={(_c, i) => go(state.path.split('/').filter(Boolean).slice(0, i).join('/'))} />
            : <span className="xfer-crumbs muted">{location.label}</span>}
          {location.available && location.browsable && <RefreshButton size="sm" iconOnly onRefresh={load} refreshing={loading} />}
        </div>
      )}
      {location && (
        <div className="xfer-pane-path">
          {/* Enter goes to the path typed; a form would not submit on Enter
              with the filter box beside it and no submit button. */}
          <Input size="sm" aria-label={`${name}: path`} value={typedPath} placeholder="/" onChange={e => setTypedPath(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); go(typedPath.trim().replace(/^\/+|\/+$/g, '')) } }}
            onBlur={() => { if (!location.browsable && typedPath.trim() !== state.path) go(typedPath.trim().replace(/^\/+|\/+$/g, '')) }}
            disabled={!location.available} />
          {location.browsable && (
            <SearchInput size="sm" value={filter} onValueChange={setFilter} label={`Filter ${name}`} placeholder="Filter" className="xfer-filter" />
          )}
        </div>
      )}

      <div className="xfer-list" role="region" aria-label={`${name}: files`}>
        {!locations.length && (
          <EmptyState variant="bare" title="No locations" description="Libraries, folders on this computer, clusters and saved locations appear here when Studio can reach them." />
        )}
        {locations.length > 0 && !location && (
          <EmptyState variant="bare" title="Choose a location" description="Pick a library, a folder on this computer, a cluster or a saved location." />
        )}
        {location && !location.available && (
          <Callout tone="warning" size="sm" title={`${location.label} is not reachable`}>{location.why ?? 'Try again later.'}</Callout>
        )}
        {location?.available && !location.browsable && (
          <EmptyState variant="bare" title="This location cannot be listed" description="Type a path above. As a source, the path typed is what is sent; as a destination, files land in it." />
        )}
        {location?.available && location.browsable && error && (
          <Callout tone={error.status === 502 ? 'warning' : 'error'} size="sm"
            title={error.status === 404 ? 'This folder no longer exists' : error.status === 502 ? `Cannot reach ${location.label}` : 'Cannot list this folder'}
            action={state.path ? <Button size="sm" variant="secondary" onClick={() => go('')}>Go to the top</Button> : undefined}>
            {error.text}
          </Callout>
        )}
        {location?.available && location.browsable && !error && !listing && loading && (
          <div className="muted xfer-pad"><Loader inline size={16} label="Listing…" /></div>
        )}
        {listing && !error && (
          entries.length === 0
            ? <EmptyState variant="bare" title="This folder is empty" />
            : (
              <table className="xfer-table">
                <thead>
                  <tr>
                    <th className="xfer-col-check">
                      <Checkbox aria-label={`Select all in ${name}`} checked={allState === true} indeterminate={allState === 'some'}
                        disabled={!visible.length}
                        onCheckedChange={() => setSelected(allState === true
                          ? state.selected.filter(n => !visibleNames.includes(n))
                          : [...new Set([...state.selected, ...visibleNames])])} />
                    </th>
                    <th>Name</th>
                    <th className="xfer-col-size">Size</th>
                    <th className="xfer-col-mtime">Modified</th>
                  </tr>
                </thead>
                <tbody>
                  {visible.map(e => {
                    const on = selectedSet.has(e.name)
                    return (
                      <tr key={e.name} className={on ? 'on' : undefined} aria-selected={on}
                        onClick={ev => rowClick(ev, e.name)}
                        onDoubleClick={() => { if (e.type === 'dir') go(joinPath(state.path, e.name)) }}>
                        <td className="xfer-col-check">
                          <Checkbox aria-label={`Select ${e.name}`} checked={on}
                            onClick={ev => { ev.stopPropagation(); toggle(e.name, ev.shiftKey); ev.preventDefault() }} />
                        </td>
                        <td><span className="xfer-name">
                          <span className={`xfer-glyph ${e.type}`}>{e.type === 'dir' ? <FolderGlyph /> : e.type === 'link' ? <LinkGlyph /> : <FileGlyph />}</span>
                          {e.type === 'dir'
                            ? <button type="button" className="xfer-dir" onClick={() => go(joinPath(state.path, e.name))} title={`Open ${e.name}`}>{e.name}</button>
                            : <span className="xfer-file" title={e.type === 'link' ? `${e.name} (link)` : e.name}>{e.name}</span>}
                        </span></td>
                        <td className="xfer-col-size">{e.type === 'dir' ? '' : bytesText(e.size)}</td>
                        <td className="xfer-col-mtime">{modifiedText(e.mtime)}</td>
                      </tr>
                    )
                  })}
                </tbody>
              </table>
            )
        )}
        {listing && !error && entries.length > 0 && !visible.length && <p className="muted xfer-pad">Nothing here matches “{filter}”.</p>}
      </div>
      {listing && !error && (
        <div className="xfer-pane-foot muted">
          {state.selected.length ? `${state.selected.length.toLocaleString()} of ${entries.length.toLocaleString()} selected` : `${entries.length.toLocaleString()} items`}
          {listing.truncated && ' · showing the first 2,000 by name'}
          {state.selected.length > 0 && <Button size="xs" variant="ghost" onClick={() => setSelected([])}>Clear</Button>}
        </div>
      )}
    </section>
  )
}
