import { ReactElement, useEffect, useState } from 'react'
import { Streamdown } from 'streamdown'
import { api, IndexStatus } from '../api'
import { useAppConfig } from '../config'

const SECTION_ICONS: Record<string, ReactElement> = {
  chat: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M14 10.5a1.5 1.5 0 0 1-1.5 1.5H5l-3 3V3.5A1.5 1.5 0 0 1 3.5 2h9A1.5 1.5 0 0 1 14 3.5v7z"/></svg>,
  library: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M1.5 3.5A1 1 0 0 1 2.5 2.5h3l1.5 2h6a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1v-9z"/></svg>,
  search: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="7" cy="7" r="4.5"/><path d="m10.5 10.5 3.5 3.5"/></svg>,
  query: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><ellipse cx="8" cy="3.5" rx="5.5" ry="2"/><path d="M2.5 3.5v9c0 1.1 2.5 2 5.5 2s5.5-.9 5.5-2v-9"/><path d="M2.5 8c0 1.1 2.5 2 5.5 2s5.5-.9 5.5-2"/></svg>,
  adding: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M8 10V2.5"/><path d="m5 5.5 3-3 3 3"/><path d="M2.5 10.5v2a1 1 0 0 0 1 1h9a1 1 0 0 0 1-1v-2"/></svg>,
  index: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><rect x="2" y="2" width="5" height="5" rx="1"/><rect x="9" y="2" width="5" height="5" rx="1"/><rect x="2" y="9" width="5" height="5" rx="1"/><rect x="9" y="9" width="5" height="5" rx="1"/></svg>,
  label: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M2 2h5.2L14 8.8a1 1 0 0 1 0 1.4L10.2 14a1 1 0 0 1-1.4 0L2 7.2V2z"/><circle cx="5.2" cy="5.2" r="1" fill="currentColor" stroke="none"/></svg>,
  stats: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M2 13.5h12"/><path d="M3.5 13.5V8"/><path d="M7 13.5V4.5"/><path d="M10.5 13.5V6.5"/><path d="M14 13.5V2.5"/></svg>,
  around: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="8" r="6.5"/><path d="M8 1.5v3M8 11.5v3M1.5 8h3M11.5 8h3"/></svg>,
}

function iconFor(title: string): ReactElement | null {
  const key = Object.keys(SECTION_ICONS).find(k => title.toLowerCase().includes(k))
  return key ? SECTION_ICONS[key] : null
}

interface Section { title: string; body: string }

/** Split the help markdown: intro is everything before the first `## `,
 *  each `## Title` becomes a card. */
function parseHelp(md: string): { intro: string; sections: Section[] } {
  const parts = md.split(/\n(?=## )/)
  const intro = parts[0]?.startsWith('## ') ? '' : (parts.shift() ?? '')
  const sections = parts.map(p => {
    const nl = p.indexOf('\n')
    return { title: p.slice(3, nl < 0 ? undefined : nl).trim(), body: nl < 0 ? '' : p.slice(nl + 1).trim() }
  })
  return { intro: intro.trim(), sections }
}

function ago(iso: string | null): string {
  if (!iso) return 'pending'
  const s = Math.max(0, (Date.now() - Date.parse(iso)) / 1000)
  if (s < 90) return `${Math.round(s)} seconds ago`
  if (s < 5400) return `${Math.round(s / 60)} minutes ago`
  return `${Math.round(s / 3600)} hours ago`
}

const slugOf = (t: string) => t.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '')

export function HelpView() {
  const cfg = useAppConfig()
  const [md, setMd] = useState<string>('')
  const [stats, setStats] = useState<{ files?: number; dirs?: number; totalBytes?: number } | null>(null)
  const [idx, setIdx] = useState<IndexStatus | null>(null)
  const [active, setActive] = useState<string>('Overview')
  const [pendingSlug, setPendingSlug] = useState<string | null>(() => location.hash.match(/^#view=help:([a-z0-9-]+)/)?.[1] ?? null)

  const goSection = (title: string) => {
    setActive(title)
    history.replaceState(null, '', `${location.pathname}${location.search}#view=help:${slugOf(title)}`)
  }

  useEffect(() => {
    fetch('/api/help').then(r => r.text()).then(setMd).catch(() => setMd('Help content unavailable.'))
    api.stats().then(setStats).catch(() => {})
    api.indexStatus().then(setIdx).catch(() => {})
  }, [])

  const { intro, sections } = parseHelp(md)

  // Restore the section from the hash once the markdown has arrived.
  useEffect(() => {
    if (!pendingSlug || !sections.length) return
    if (pendingSlug === 'overview') { setActive('Overview'); setPendingSlug(null); return }
    const hit = sections.find(s => slugOf(s.title) === pendingSlug)
    if (hit) setActive(hit.title)
    setPendingSlug(null)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [md])

  const current = (active === 'Overview' ? null : sections.find(s => s.title === active)) ?? null

  return (
    <div className="help-view">
      <div className="help-docs card">
        <nav className="help-nav">
          <div className="help-nav-head">
            {cfg.iconUrl ? <img className="help-nav-icon" src={cfg.iconUrl} alt="" /> : null}
            <span>User guide</span>
          </div>
          <button className={active === 'Overview' ? 'active' : ''} onClick={() => goSection('Overview')}>Overview</button>
          {sections.map(s => (
            <button key={s.title} className={active === s.title ? 'active' : ''} onClick={() => goSection(s.title)}>
              {iconFor(s.title)} <span>{s.title}</span>
            </button>
          ))}
        </nav>
        <article className="help-content">
          {current === null ? (
            <>
              <h1>{cfg.appName}</h1>
              <div className="md-body"><Streamdown>{intro}</Streamdown></div>
              <p className="muted help-hint">Each section in the left rail covers one part of the application. The chat assistant reads this same guide, so asking it "how do I…" works too.</p>
            </>
          ) : (
            <>
              <h1>{current.title}</h1>
              <div className="md-body"><Streamdown>{current.body}</Streamdown></div>
              {current.title.toLowerCase().includes('index') && (
                <div className="help-stats">
                  <div><span className="stat-num">{stats?.files?.toLocaleString() ?? '…'}</span><span className="stat-label">files indexed</span></div>
                  <div><span className="stat-num">{stats?.dirs?.toLocaleString() ?? '…'}</span><span className="stat-label">directories</span></div>
                  <div><span className="stat-num">{stats?.totalBytes ? `${(stats.totalBytes / 1e9).toFixed(1)} GB` : '…'}</span><span className="stat-label">corpus size</span></div>
                  <div><span className="stat-num">{ago(idx?.lastSweepAt ?? null)}</span><span className="stat-label">last sync</span></div>
                </div>
              )}
            </>
          )}
        </article>
      </div>
    </div>
  )
}
