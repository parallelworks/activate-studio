import { useEffect, useState } from 'react'
import { api, IndexStatus } from '../api'

interface ExtRow { ext: string; count: number; bytes: number }
interface QueryResult { columns: string[]; rows: string[][] }

function fmtBytes(n: number): string {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

function ago(iso: string | null): string {
  if (!iso) return 'never'
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000)
  if (s < 90) return `${Math.round(s)}s ago`
  if (s < 5400) return `${Math.round(s / 60)}m ago`
  return `${Math.round(s / 3600)}h ago`
}

export function OverviewView({ onOpen }: { onOpen: (path: string) => void }) {
  const [stats, setStats] = useState<{ files?: number; dirs?: number; totalBytes?: number; byExt?: ExtRow[] } | null>(null)
  const [idx, setIdx] = useState<IndexStatus | null>(null)
  const [tags, setTags] = useState<{ tag: string; count: number }[]>([])
  const [largest, setLargest] = useState<QueryResult | null>(null)
  const [recent, setRecent] = useState<QueryResult | null>(null)

  useEffect(() => {
    api.stats().then(setStats).catch(() => {})
    api.indexStatus().then(setIdx).catch(() => {})
    api.tagVocabulary().then(v => setTags(v.tags)).catch(() => {})
    const q = (body: unknown, set: (r: QueryResult) => void) =>
      fetch('/api/query', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
        .then(r => r.json()).then(set).catch(() => {})
    q({ canned: 'largest', n: 8 }, setLargest)
    q({ canned: 'recent', n: 8, days: 7 }, setRecent)
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

      <div className="ov-grid">
        <section className="card ov-chart">
          <h3>Storage by file type</h3>
          <div className="ov-bars">
            {byExt.map(e => (
              <div className="ov-bar-row" key={e.ext} title={`${e.ext}: ${fmtBytes(e.bytes)} across ${e.count.toLocaleString()} files`}>
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
              {tags.map(t => <span key={t.tag} className="tag-pill on">{t.tag} <span className="tag-count">{t.count}</span></span>)}
            </div>
          </section>
        )}
      </div>
    </div>
  )
}
