import { useEffect, useRef, useState } from 'react'
import { Callout, Loader, RefreshButton, StatusDot, Table } from '@parallelworks/ui'
import { api, type FormatReadiness } from '../api'
import { indexSummary, phaseLine, useIndexStatus } from '../indexActivity'
import { useShared } from '../statusShare'
import { IndexControls, IndexJobView, IndexRuns, LastBuild, useIndexControls } from '../components/IndexActivity'
import { SemanticMap, TopologyMap } from '../components/CorpusMaps'
import { currentLibrary, onLibraryChange } from '../api'
import type { LibraryStats } from '../api'
import { LibrarySwitch } from '../components/LibrarySwitch'
import { useAppConfig } from '../config'

interface ExtRow { ext: string; count: number; bytes: number }
interface QueryResult { columns: string[]; rows: string[][]; rowLibraries?: string[] }

/**
 * A file list's rows read by column name. An answer across libraries leads
 * with the library's name, which shifted every position by one; rowLibraries
 * says which library each row came from, and such a file opens as
 * <library>/<path>, as the Query page opens it.
 */
export function fileRows(r: QueryResult): { key: string; path: string; bytes: number; modified: string; library: string | null; open: string }[] {
  const at = (name: string) => r.columns.indexOf(name)
  const [path, bytes, modified, library] = [at('path'), at('bytes'), at('modified'), at('library')]
  return r.rows.map((row, i) => {
    const lib = r.rowLibraries?.[i]
    const p = row[path] ?? ''
    return {
      key: `${lib ?? ''}:${p}`, path: p, bytes: Number(row[bytes]), modified: row[modified] ?? '',
      library: library >= 0 ? row[library] : null,
      open: library >= 0 && lib && lib !== 'all' ? `${lib}/${p}` : p,
    }
  })
}

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

function ago(iso: string | null): string {
  // Null just means no sweep has completed since the server started.
  if (!iso) return 'pending'
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000)
  if (s < 90) return `${Math.round(s)}s ago`
  if (s < 5400) return `${Math.round(s / 60)}m ago`
  return `${Math.round(s / 3600)}h ago`
}

/** Hand a builder query to the Query view and switch to it. */
function jumpToQuery(filters: { field: string; op: string; value: string }[], sort = 'size') {
  window.dispatchEvent(new CustomEvent('ade-run-query', {
    detail: { builder: { source: 'files', filters, groupBy: 'none', sort, dir: 'desc', limit: 100, subtree: '' } },
  }))
  location.hash = '#view=query'
}

export function OverviewView({ onOpen }: { onOpen: (path: string) => void }) {
  const [stats, setStats] = useState<{ files?: number; dirs?: number; totalBytes?: number; byExt?: ExtRow[]; libraries?: LibraryStats[] } | null>(null)
  const cfg = useAppConfig()
  // The index status every view shows; the page follows a running job.
  const idx = useIndexStatus()
  const controls = useIndexControls()
  const { authEnabled } = useShared()
  const [missing, setMissing] = useState<FormatReadiness[]>([])
  const [updatedAt, setUpdatedAt] = useState<number | null>(null)
  const [tags, setTags] = useState<{ tag: string; count: number }[]>([])
  const [largest, setLargest] = useState<QueryResult | null>(null)
  const [recent, setRecent] = useState<QueryResult | null>(null)
  const [activity, setActivity] = useState<{ conversations: number; latest: string | null; transcripts: number; attachments: number } | null>(null)

  // Loaded again when the library changes or the index does (a sync, a
  // delete, an upload), so the page never shows a stale count.
  const [epoch, setEpoch] = useState(0)
  useEffect(() => {
    const bump = () => setEpoch(e => e + 1)
    const off = onLibraryChange(bump)
    window.addEventListener('ade-kb-changed', bump)
    return () => { off(); window.removeEventListener('ade-kb-changed', bump) }
  }, [])
  // A library on a cluster takes seconds to count. Its figures are cleared
  // while they load, so the last library's numbers never stand in for it,
  // and an answer for a library no longer selected is dropped.
  const [loading, setLoading] = useState(true)
  const waiters = useRef<(() => void)[]>([])
  useEffect(() => {
    let live = true
    const keep = <T,>(set: (v: T) => void) => (v: T) => { if (live) set(v) }
    setStats(null); setLargest(null); setRecent(null); setLoading(true)
    api.stats().then(keep(setStats)).catch(() => {}).finally(() => {
      if (!live) return
      setLoading(false)
      setUpdatedAt(Date.now())
      for (const done of waiters.current.splice(0)) done()
    })
    api.extractors().then(r => { if (live) setMissing(r.missing ?? []) }).catch(() => {})
    api.tagVocabulary().then(v => { if (live) setTags(v.tags) }).catch(() => {})
    const q = (body: unknown, set: (r: QueryResult) => void) =>
      fetch('/api/query', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ ...(body as object), ...(currentLibrary() !== 'kb' ? { library: currentLibrary() } : {}) }) })
        // An error payload parses as JSON too; only a shape with rows is a result.
        .then(r => r.json()).then(r => { if (live && Array.isArray(r?.rows)) set(r) }).catch(() => {})
    q({ canned: 'largest', n: 8 }, setLargest)
    q({ canned: 'recent', n: 8, days: 7 }, setRecent)
    Promise.all([
      fetch('/api/chat/conversations').then(r => r.json()).catch(() => []),
      fetch('/api/kb/tree?path=chat-sessions').then(r => r.json()).catch(() => []),
      fetch('/api/chat/attachments?limit=1&offset=0').then(r => r.json()).catch(() => ({ total: 0 })),
    ]).then(([convs, transcripts, atts]) => {
      if (!live) return
      const list = Array.isArray(convs) ? convs : []
      setActivity({
        conversations: list.length,
        latest: list[0]?.title ?? null,
        transcripts: Array.isArray(transcripts) ? transcripts.length : 0,
        attachments: atts?.total ?? 0,
      })
    }).catch(() => {})
    return () => { live = false }
  }, [epoch])

  // While a job runs, the counts are read again every few seconds, so the
  // page grows with the index; the whole page reloads when the job ends.
  const running = !!idx?.running
  useEffect(() => {
    if (!running) return
    const t = window.setInterval(() => {
      api.stats().then(s => { setStats(s); setUpdatedAt(Date.now()) }).catch(() => {})
    }, 5000)
    return () => window.clearInterval(t)
  }, [running])
  // Resolves once the counts are back, so the button spins until then.
  const refresh = () => new Promise<void>(resolve => { waiters.current.push(resolve); setEpoch(e => e + 1) })
  const ix = indexSummary(idx, stats?.files)

  const byExt = [...(stats?.byExt ?? [])].filter(e => e.ext !== "(none)").sort((a, b) => b.bytes - a.bytes).slice(0, 12)
  const maxBytes = Math.max(...byExt.map(e => e.bytes), 1)

  return (
    <div className="overview-view">
      <div className="scope-bar">
        {(cfg.libraries?.length ?? 0) > 1 && (
          <>
            <span className="muted">Statistics for</span>
            <LibrarySwitch libraries={cfg.libraries ?? []} />
          </>
        )}
        {loading && <Loader inline size={14} label="Counting…" />}
        <span className="ov-refresh"><RefreshButton onRefresh={refresh} updatedAt={updatedAt ?? undefined} size="sm" /></span>
      </div>
      {idx?.job && (
        <section className="card ov-index-job" aria-label="Indexing">
          <IndexJobView job={idx.job} onStop={() => void controls.stop()} stopping={controls.busy === 'stop'} />
        </section>
      )}
      <div className="ov-tiles">
        <div className="card ov-tile"><span className="stat-num">{stats?.files?.toLocaleString() ?? '…'}</span><span className="stat-label">files indexed</span></div>
        <div className="card ov-tile"><span className="stat-num">{stats?.dirs?.toLocaleString() ?? '…'}</span><span className="stat-label">directories</span></div>
        <div className="card ov-tile"><span className="stat-num">{stats?.totalBytes ? fmtBytes(stats.totalBytes) : '…'}</span><span className="stat-label">corpus size</span></div>
        <div className="card ov-tile" title={ix.detail ?? undefined}>
          <span className="stat-num ov-index-state">
            <StatusDot variant={ix.tone} pulse={ix.pulse} size={9} />
            {ix.running ? (phaseLine(idx?.job) ?? 'indexing') : ix.tone === 'success' ? 'healthy' : ix.tone === 'muted' ? 'not built' : 'attention'}
          </span>
          <span className="stat-label">{ix.running ? 'indexing now' : `index · sync ${ago(idx?.lastSweepAt ?? null)}`}</span>
        </div>
        <div className="card ov-tile"><span className="stat-num">{tags.length}</span><span className="stat-label">labels in use</span></div>
      </div>
      {stats?.libraries && (
        <Table density="compact" wrapperClassName="ov-libraries">
          <Table.Header>Library</Table.Header>
          <Table.Header>Files</Table.Header>
          <Table.Header>Directories</Table.Header>
          <Table.Header>Size</Table.Header>
          {stats.libraries.map(l => (
            <tr key={l.id}>
              <Table.Item>{l.label}{l.site ? <span className="muted"> on {l.site}</span> : null}</Table.Item>
              {l.available
                ? <><Table.Item>{(l.files ?? 0).toLocaleString()}</Table.Item><Table.Item>{(l.dirs ?? 0).toLocaleString()}</Table.Item><Table.Item>{fmtBytes(l.totalBytes ?? 0)}</Table.Item></>
                : <Table.Item colSpan={3} wrap className="muted">{l.error ?? 'not available'}</Table.Item>}
            </tr>
          ))}
        </Table>
      )}

      {idx?.lastError && !idx.running && (
        <Callout tone="error">Last index job reported: {idx.lastError.slice(0, 300)}</Callout>
      )}
      {idx?.incomplete && !idx.running && !idx.lastError && <Callout tone="warning">{ix.detail}</Callout>}

      {/* A format with no reader indexes as its filename, which is worth
          saying out loud rather than leaving to be discovered. Optional
          readers (pdftotext where downmark reads PDFs) are not listed. */}
      {missing.length > 0 && (
        <Callout tone="info" title="Some files index by name only">
          {missing.map(m => <div key={m.formats}><strong>{m.formats}</strong>: {m.note}</div>)}
        </Callout>
      )}

      <div className="ov-grid">
        <section className="card ov-chart">
          <h3>Storage by file type</h3>
          <div className="ov-bars">
            {byExt.map(e => (
              <div className="ov-bar-row ov-click" key={e.ext}
                title={`${e.ext}: ${fmtBytes(e.bytes)} across ${e.count.toLocaleString()} files. Click to list them in Query.`}
                onClick={() => jumpToQuery([{ field: 'extension', op: 'eq', value: e.ext }])}>
                <span className="ov-bar-label">.{e.ext}</span>
                <div className="ov-bar-track">
                  <div className="ov-bar-fill" style={{ width: `${Math.max((e.bytes / maxBytes) * 100, 1)}%` }} />
                </div>
                <span className="ov-bar-value">{fmtBytes(e.bytes)} · {e.count.toLocaleString()} files</span>
              </div>
            ))}
          </div>
        </section>

        <section className="card ov-list">
          <h3>Largest files</h3>
          {largest ? fileRows(largest).map(f => (
            <div className="ov-row" key={f.key} onClick={() => onOpen(f.open)} title={f.library ?? undefined}>
              <span className="ov-row-path">{f.path}</span>
              <span className="ov-row-meta">{fmtBytes(f.bytes)}</span>
            </div>
          )) : <Loader size={24} />}
        </section>

        <section className="card ov-list">
          <h3>Changed in the last 7 days</h3>
          {recent?.rows.length === 0 && <p className="muted pad">No changes recorded.</p>}
          {recent ? fileRows(recent).map(f => (
            <div className="ov-row" key={f.key} onClick={() => onOpen(f.open)} title={f.library ?? undefined}>
              <span className="ov-row-path">{f.path}</span>
              <span className="ov-row-meta">{f.modified.slice(0, 10)}</span>
            </div>
          )) : <Loader size={24} />}
        </section>

        {tags.length > 0 && (
          <section className="card ov-list">
            <h3>Labels</h3>
            <div className="tag-row ov-tags">
              {tags.map(t => (
                <button key={t.tag} className="tag-pill on" title={`List everything labeled ${t.tag}`}
                  onClick={() => jumpToQuery([{ field: 'label', op: 'eq', value: t.tag }], 'modified')}>
                  {t.tag} <span className="tag-count">{t.count}</span>
                </button>
              ))}
            </div>
          </section>
        )}

        <section className="card ov-list">
          <h3>Studio activity</h3>
          <div className="ov-row ov-click" onClick={() => { location.hash = '' }}>
            <span className="ov-row-path">Chat conversations{activity?.latest ? `, latest: ${activity.latest.slice(0, 48)}` : ''}</span>
            <span className="ov-row-meta">{activity?.conversations ?? '…'}</span>
          </div>
          <div className="ov-row ov-click" onClick={() => jumpToQuery([{ field: 'label', op: 'eq', value: 'chat-session' }], 'modified')}>
            <span className="ov-row-path">Exported session transcripts, searchable in the corpus</span>
            <span className="ov-row-meta">{activity?.transcripts ?? '…'}</span>
          </div>
          <div className="ov-row">
            <span className="ov-row-path">Chat attachments stored under uploads/chat</span>
            <span className="ov-row-meta">{activity?.attachments ?? '…'}</span>
          </div>
        </section>
      </div>

      <section className="card ov-list ov-runs">
        <div className="ov-runs-head">
          <h3>Index runs</h3>
          {/* Building is for a single-user install; a deployment that signs
              users in builds its own index. The log is for everyone. */}
          <IndexControls canDelete={false} canBuild={authEnabled === false} />
        </div>
        <LastBuild status={idx} />
        <IndexRuns runs={idx?.runs ?? []} />
      </section>

      <div className="ov-grid ov-maps">
        <TopologyMap libraries={cfg.libraries ?? []} />
        <SemanticMap onOpen={onOpen} libraries={cfg.libraries ?? []} />
      </div>
    </div>
  )
}
