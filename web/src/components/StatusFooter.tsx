import { useEffect, useState } from 'react'
import { api, IndexStatus } from '../api'

function ago(iso: string | null): string {
  // Null just means no sweep has completed since the server started.
  if (!iso) return 'pending'
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000)
  if (s < 90) return `${Math.round(s)}s ago`
  if (s < 5400) return `${Math.round(s / 60)}m ago`
  return `${Math.round(s / 3600)}h ago`
}

function IndexPanel({ idx, gufi, stats, onClose, onSync, syncing }: {
  idx: IndexStatus | null
  gufi: boolean | null
  stats: { files?: number; dirs?: number; totalBytes?: number } | null
  onClose: () => void
  onSync: () => void
  syncing: boolean
}) {
  const gb = stats?.totalBytes ? `${(stats.totalBytes / 1e9).toFixed(2)} GB` : ''
  const rows: [string, string, 'ok' | 'warn' | 'off' | null][] = [
    ['GUFI engine', gufi === null ? 'checking…' : gufi ? 'available' : 'unavailable (search falls back to grep)', gufi === null ? 'off' : gufi ? 'ok' : 'warn'],
    ['Indexed', stats?.files ? `${stats.files.toLocaleString()} files in ${stats.dirs?.toLocaleString()} directories, ${gb}` : 'loading…', null],
    ['Sync loop', idx ? (idx.running ? 'sweep running now' : `every ${idx.sweepIntervalSec}s, last ${ago(idx.lastSweepAt ?? null)}${idx.lastSweepMs != null ? ` (${idx.lastSweepMs} ms)` : ''}`) : 'loading…', idx?.running ? 'ok' : null],
    ['Last result', idx?.lastError ? idx.lastError : idx?.lastSweepAt ? 'clean' : 'no sweep yet this server start', idx?.lastError ? 'warn' : idx?.lastSweepAt ? 'ok' : 'off'],
  ]
  return (
    <>
      <div className="scope-overlay" onClick={onClose} />
      <div className="identity-panel card">
        <div className="scope-menu-head">Index health</div>
        <table className="identity-table">
          <tbody>
            {rows.map(([k, v, dot]) => (
              <tr key={k}>
                <td className="k">{k}</td>
                <td className="v">{dot && <span className={`status-dot inline ${dot}`} />}{v}</td>
              </tr>
            ))}
          </tbody>
        </table>
        {(idx?.lastChanges?.length ?? 0) > 0 && (
          <>
            <div className="scope-menu-head">Last sweep re-indexed</div>
            <pre className="identity-claims">{idx!.lastChanges.slice(0, 10).join('\n')}</pre>
          </>
        )}
        <div className="scope-menu-foot">
          <span className="muted identity-src">GET /api/index/status</span>
          <span>
            <button className="btn-secondary" onClick={onSync} disabled={syncing || idx?.running}>{syncing || idx?.running ? 'syncing…' : 'Sync now'}</button>
            {' '}
            <button className="btn-secondary" onClick={onClose}>Close</button>
          </span>
        </div>
      </div>
    </>
  )
}

interface AiHealth {
  ok: boolean
  status: 'ok' | 'auth' | 'unreachable' | 'unconfigured' | 'key-required'
  models: number
  gatewayHost: string
  message: string | null
  lastCallFailure: { at: string; message: string; auth: boolean } | null
  checkedAt: string
}

const AI_LABEL: Record<AiHealth['status'], string> = {
  ok: 'models ready',
  auth: 'model key rejected',
  unreachable: 'models unreachable',
  unconfigured: 'models not configured',
  'key-required': 'add your model key',
}

function AiPanel({ ai, onClose, onRecheck }: { ai: AiHealth; onClose: () => void; onRecheck: () => void }) {
  const rows: [string, string][] = [
    ['Status', AI_LABEL[ai.status] + (ai.status === 'ok' ? ` (${ai.models} models)` : '')],
    ['Gateway', ai.gatewayHost],
    ['Checked', new Date(ai.checkedAt).toLocaleTimeString()],
  ]
  if (ai.message) rows.push(['Detail', ai.message])
  if (ai.lastCallFailure) rows.push(['Last call failure', `${new Date(ai.lastCallFailure.at).toLocaleTimeString()}: ${ai.lastCallFailure.message.slice(0, 160)}`])
  return (
    <>
      <div className="scope-overlay" onClick={onClose} />
      <div className="identity-panel card">
        <div className="scope-menu-head">Model availability</div>
        <table className="identity-table"><tbody>
          {rows.map(([k, v]) => <tr key={k}><td className="k">{k}</td><td className="v">{v}</td></tr>)}
        </tbody></table>
        {ai.status === 'auth' && (
          <p className="identity-note">The gateway rejected the credential. Some providers lock keys on a schedule; unlock the key at your provider, then re-check.</p>
        )}
        <p className="identity-note">Checked via the model listing; a key can also fail at call time, which shows under last call failure.</p>
        <div className="scope-menu-foot">
          <span className="muted identity-src">GET /api/ai/health</span>
          <span>
            <button className="btn-secondary" onClick={onRecheck}>Re-check</button>
            {' '}
            <button className="btn-secondary" onClick={onClose}>Close</button>
          </span>
        </div>
      </div>
    </>
  )
}

interface WhoAmI {
  authEnabled: boolean
  header: string
  verified: boolean
  user: { id: string; username: string; name?: string } | null
  token: { issuer: string | null; audience: unknown; issuedAt: string | null; expiresAt: string | null } | null
  claims: Record<string, unknown> | null
}

function IdentityPanel({ who, onClose }: { who: WhoAmI; onClose: () => void }) {
  const rows: [string, string][] = who.verified
    ? [
        ['Username', who.user?.username ?? ''],
        ['Name', who.user?.name ?? '(not set)'],
        ['Subject', String(who.claims?.sub ?? '')],
        ['Issuer', who.token?.issuer ?? ''],
        ['Audience', Array.isArray(who.token?.audience) ? (who.token?.audience as string[]).join('\n') : String(who.token?.audience ?? '')],
        ['Issued', who.token?.issuedAt ? new Date(who.token.issuedAt).toLocaleTimeString() : ''],
        ['Expires', who.token?.expiresAt ? `${new Date(who.token.expiresAt).toLocaleTimeString()} (${Math.max(0, Math.round((Date.parse(who.token.expiresAt) - Date.now()) / 1000))}s)` : ''],
      ]
    : []
  return (
    <>
      <div className="scope-overlay" onClick={onClose} />
      <div className="identity-panel card">
        <div className="scope-menu-head">{who.verified ? 'Platform-verified identity' : 'Identity not verified'}</div>
        {who.verified ? (
          <>
            <table className="identity-table">
              <tbody>
                {rows.map(([k, v]) => (
                  <tr key={k}><td className="k">{k}</td><td className="v">{v}</td></tr>
                ))}
              </tbody>
            </table>
            <div className="scope-menu-head">Raw token claims</div>
            <pre className="identity-claims">{JSON.stringify(who.claims, null, 2)}</pre>
          </>
        ) : (
          <p className="identity-note">
            This connection carried no valid platform token. Verification is enabled and the
            server expects a short-lived JWT in the <code>{who.header}</code> header, which the
            platform session proxy adds when the app is reached through it. Direct access
            (localhost, the bare subdomain outside the proxy) has no such header. If this shows
            while using the platform, check the server log for a token verification warning.
          </p>
        )}
        <div className="scope-menu-foot">
          <span className="muted identity-src">GET /api/whoami</span>
          <button className="btn-secondary" onClick={onClose}>Close</button>
        </div>
      </div>
    </>
  )
}

export function StatusFooter({ collapsed = false }: { collapsed?: boolean }) {
  const [stats, setStats] = useState<{ files?: number; dirs?: number; totalBytes?: number } | null>(null)
  const [idx, setIdx] = useState<IndexStatus | null>(null)
  const [syncing, setSyncing] = useState(false)
  const [who, setWho] = useState<WhoAmI | null>(null)
  const [idOpen, setIdOpen] = useState(false)
  const [idxOpen, setIdxOpen] = useState(false)
  const [gufi, setGufi] = useState<boolean | null>(null)
  const [ai, setAi] = useState<AiHealth | null>(null)
  const [aiOpen, setAiOpen] = useState(false)

  const refresh = () => {
    api.stats().then(s => setStats(s as typeof stats)).catch(() => {})
    api.indexStatus().then(setIdx).catch(() => {})
    fetch('/api/whoami').then(r => r.json()).then(setWho).catch(() => {})
    api.health().then(h => setGufi(h.gufi)).catch(() => setGufi(null))
    fetch('/api/ai/health').then(r => r.json()).then(setAi).catch(() => setAi(null))
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
      {who?.authEnabled && (
        <div className="status-line identity-line" title="Click for identity details">
          <button className="identity-btn" onClick={() => setIdOpen(o => !o)}>
            <span className={`status-dot ${who.verified ? 'ok' : 'off'}`} />
            {who.verified ? who.user?.username : 'identity not verified'}
          </button>
          {idOpen && <IdentityPanel who={who} onClose={() => setIdOpen(false)} />}
        </div>
      )}
      {ai && (
        <div className="status-line identity-line" title="Click for model availability details">
          <button className="identity-btn" onClick={() => setAiOpen(o => !o)}>
            <span className={`status-dot ${ai.status === 'ok' ? 'ok' : ai.status === 'unconfigured' ? 'off' : 'warn'}`} />
            {ai.status === 'ok' ? `${ai.models} models ready` : AI_LABEL[ai.status]}
          </button>
          {aiOpen && (
            <AiPanel
              ai={ai}
              onClose={() => setAiOpen(false)}
              onRecheck={() => { fetch('/api/ai/health').then(r => r.json()).then(setAi).catch(() => {}) }}
            />
          )}
        </div>
      )}
      <div className="status-line identity-line" title="Click for index health details">
        <button className="identity-btn" onClick={() => setIdxOpen(o => !o)}>
          <span className={`status-dot ${idx?.lastError ? 'warn' : 'ok'}`} />
          {stats?.files ? `${stats.files.toLocaleString()} files indexed` : 'index loading'}
        </button>
        {idxOpen && (
          <IndexPanel
            idx={idx}
            gufi={gufi}
            stats={stats}
            onClose={() => setIdxOpen(false)}
            onSync={() => { void sync() }}
            syncing={syncing}
          />
        )}
      </div>
      <div className="status-line muted2">
        sync {ago(idx?.lastSweepAt ?? null)}
        <button className="link-btn" onClick={sync} disabled={syncing || idx?.running}>
          {syncing || idx?.running ? 'syncing…' : 'sync now'}
        </button>
      </div>
    </div>
  )
}
