import { useEffect, useState } from 'react'
import { api, IndexStatus } from '../api'

function ago(iso: string | null): string {
  if (!iso) return 'never'
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000)
  if (s < 90) return `${Math.round(s)}s ago`
  if (s < 5400) return `${Math.round(s / 60)}m ago`
  return `${Math.round(s / 3600)}h ago`
}

export function StatusFooter({ collapsed = false, onToggleTheme, theme }: {
  collapsed?: boolean
  onToggleTheme?: () => void
  theme?: 'light' | 'dark'
}) {
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
    try {
      await api.sweepNow()
      // Tell the library to drop its cached tree state.
      window.dispatchEvent(new Event('ade-kb-changed'))
    } catch { /* surfaced via status */ }
    refresh()
    setSyncing(false)
  }

  if (collapsed) {
    return (
      <div className="sidenav-footer">
        <div className="status-line" title={stats?.files ? `${stats.files.toLocaleString()} files indexed, sync ${ago(idx?.lastSweepAt ?? null)}` : 'index loading'}>
          <span className={`status-dot ${idx?.lastError ? 'warn' : 'ok'}`} />
        </div>
      </div>
    )
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
      {onToggleTheme && (
        <div className="status-line muted2">
          <button className="link-btn theme-btn" onClick={onToggleTheme}>
            {theme === 'dark' ? 'light mode' : 'dark mode'}
          </button>
        </div>
      )}
    </div>
  )
}
