import { useEffect, useState } from 'react'
import { api, IndexStatus } from '../api'

function ago(iso: string | null): string {
  if (!iso) return 'never'
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000)
  if (s < 90) return `${Math.round(s)}s ago`
  if (s < 5400) return `${Math.round(s / 60)}m ago`
  return `${Math.round(s / 3600)}h ago`
}

export function StatusFooter() {
  const [stats, setStats] = useState<{ files?: number; dirs?: number } | null>(null)
  const [idx, setIdx] = useState<IndexStatus | null>(null)
  const [syncing, setSyncing] = useState(false)

  const refresh = () => {
    api.stats().then(setStats).catch(() => {})
    api.indexStatus().then(setIdx).catch(() => {})
  }
  useEffect(() => {
    refresh()
    const t = setInterval(refresh, 60_000)
    return () => clearInterval(t)
  }, [])

  const sync = async () => {
    setSyncing(true)
    try { await api.sweepNow() } catch { /* surfaced via status */ }
    refresh()
    setSyncing(false)
  }

  return (
    <div className="sidenav-footer">
      <div className="status-line">
        <span className={`status-dot ${idx?.lastError ? 'warn' : 'ok'}`} />
        {stats?.files ? `${stats.files.toLocaleString()} files indexed` : 'index loading'}
      </div>
      <div className="status-line muted2">
        sync {ago(idx?.lastSweepAt ?? null)}
        <button className="link-btn" onClick={sync} disabled={syncing || idx?.running}>
          {syncing || idx?.running ? 'syncing…' : 'sync now'}
        </button>
      </div>
      <div className="powered">Powered by Parallel Works</div>
    </div>
  )
}
