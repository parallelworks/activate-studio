import { useEffect, useState } from 'react'
import { api, IndexStatus } from '../api'
import { SemanticMap, TopologyMap } from '../components/CorpusMaps'

interface ExtRow { ext: string; count: number; bytes: number }
interface QueryResult { columns: string[]; rows: string[][] }

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
  const [stats, setStats] = useState<{ files?: number; dirs?: number; totalBytes?: number; byExt?: ExtRow[] } | null>(null)
  const [idx, setIdx] = useState<IndexStatus | null>(null)
  const [extractors, setExtractors] = useState<{ name: string; available: boolean; covers: string }[]>([])
  const [tags, setTags] = useState<{ tag: string; count: number }[]>([])
  const [largest, setLargest] = useState<QueryResult | null>(null)
  const [recent, setRecent] = useState<QueryResult | null>(null)
  const [activity, setActivity] = useState<{ conversations: number; latest: string | null; transcripts: number; attachments: number } | null>(null)

  useEffect(() => {
    api.stats().then(setStats).catch(() => {})
    api.indexStatus().then(setIdx).catch(() => {})
    api.extractors().then(r => setExtractors(r.extractors)).catch(() => {})
    api.tagVocabulary().then(v => setTags(v.tags)).catch(() => {})
    const q = (body: unknown, set: (r: QueryResult) => void) =>
      fetch('/api/query', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
        // An error payload parses as JSON too; only a shape with rows is a result.
        .then(r => r.json()).then(r => { if (Array.isArray(r?.rows)) set(r) }).catch(() => {})
    q({ canned: 'largest', n: 8 }, setLargest)
    q({ canned: 'recent', n: 8, days: 7 }, setRecent)
    Promise.all([
      fetch('/api/chat/conversations').then(r => r.json()).catch(() => []),
      fetch('/api/kb/tree?path=chat-sessions').then(r => r.json()).catch(() => []),
      fetch('/api/chat/attachments?limit=1&offset=0').then(r => r.json()).catch(() => ({ total: 0 })),
    ]).then(([convs, transcripts, atts]) => {
      const list = Array.isArray(convs) ? convs : []
      setActivity({
        conversations: list.length,
        latest: list[0]?.title ?? null,
        transcripts: Array.isArray(transcripts) ? transcripts.length : 0,
        attachments: atts?.total ?? 0,
      })
    }).catch(() => {})
  }, [])

  const byExt = [...(stats?.byExt ?? [])].filter(e => e.ext !== "(none)").sort((a, b) => b.bytes - a.bytes).slice(0, 12)
  const maxBytes = Math.max(...byExt.map(e => e.bytes), 1)

  return (
    <div className="overview-view">
      <div className="card ov-head">
        <h1 className="view-title">Corpus statistics</h1>
        <p className="muted view-sub">State and health of the knowledge base: what it holds, how it is stored, what changed recently, and how the index is doing.</p>
      </div>
      <div className="ov-tiles">
        <div className="card ov-tile"><span className="stat-num">{stats?.files?.toLocaleString() ?? '…'}</span><span className="stat-label">files indexed</span></div>
        <div className="card ov-tile"><span className="stat-num">{stats?.dirs?.toLocaleString() ?? '…'}</span><span className="stat-label">directories</span></div>
        <div className="card ov-tile"><span className="stat-num">{stats?.totalBytes ? fmtBytes(stats.totalBytes) : '…'}</span><span className="stat-label">corpus size</span></div>
        <div className="card ov-tile">
          <span className="stat-num">{idx?.lastError ? 'attention' : 'healthy'}</span>
          <span className="stat-label">index · sync {ago(idx?.lastSweepAt ?? null)}</span>
        </div>
        <div className="card ov-tile"><span className="stat-num">{tags.length}</span><span className="stat-label">labels in use</span></div>
      </div>

      {idx?.lastError && (
        <div className="card ov-error">Last sync reported: {idx.lastError.slice(0, 300)}</div>
      )}

      {/* A format with no extractor indexes as its filename, which is worth
          saying out loud rather than leaving to be discovered. */}
      {extractors.some(x => !x.available) && (
        <div className="card ov-note">
          Not all document formats can be read on this host, so these index by filename only:{' '}
          {extractors.filter(x => !x.available).map(x => `${x.covers} (needs ${x.name})`).join(', ')}.
        </div>
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
          {largest?.rows.map(r => (
            <div className="ov-row" key={r[0]} onClick={() => onOpen(r[0])}>
              <span className="ov-row-path">{r[0]}</span>
              <span className="ov-row-meta">{fmtBytes(Number(r[1]))}</span>
            </div>
          )) ?? <p className="muted pad">Loading…</p>}
        </section>

        <section className="card ov-list">
          <h3>Changed in the last 7 days</h3>
          {recent?.rows.length === 0 && <p className="muted pad">No changes recorded.</p>}
          {recent?.rows.map(r => (
            <div className="ov-row" key={r[0]} onClick={() => onOpen(r[0])}>
              <span className="ov-row-path">{r[0]}</span>
              <span className="ov-row-meta">{r[2]?.slice(0, 10)}</span>
            </div>
          )) ?? <p className="muted pad">Loading…</p>}
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

      <div className="ov-grid ov-maps">
        <TopologyMap />
        <SemanticMap onOpen={onOpen} />
      </div>
    </div>
  )
}
