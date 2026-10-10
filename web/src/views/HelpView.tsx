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
  workflow: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="3.5" cy="4" r="1.8"/><circle cx="12.5" cy="4" r="1.8"/><circle cx="8" cy="12" r="1.8"/><path d="M5.3 4h5.4M4.5 5.6l2.6 4.8M11.5 5.6l-2.6 4.8"/></svg>,
  agent: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="5" r="2.5"/><path d="M3 14c0-2.8 2.2-4.5 5-4.5s5 1.7 5 4.5"/></svg>,
  tools: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M6 4 2 8l4 4M10 4l4 4-4 4"/></svg>,
  around: <svg viewBox="0 0 16 16" width="18" height="18" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="8" r="6.5"/><path d="M8 1.5v3M8 11.5v3M1.5 8h3M11.5 8h3"/></svg>,
}

function iconFor(title: string): ReactElement | null {
  const key = Object.keys(SECTION_ICONS).find(k => title.toLowerCase().includes(k))
  return key ? SECTION_ICONS[key] : null
}

interface Section { title: string; body: string; group: string | null }

/** Split the help markdown: the intro is everything before the first
 *  heading, each `## Title` is a page, and a `# Group` heading groups the
 *  pages after it in the rail. A guide without `#` headings is one flat
 *  list, so a deployment's own HELP_FILE keeps working. */
export function parseHelp(md: string): { intro: string; sections: Section[] } {
  const intro: string[] = []
  const sections: Section[] = []
  let group: string | null = null
  let current: Section | null = null
  for (const line of md.split('\n')) {
    const g = /^# (.+)$/.exec(line)
    const h = /^## (.+)$/.exec(line)
    if (g) { group = g[1].trim(); current = null; continue }
    if (h) { current = { title: h[1].trim(), body: '', group }; sections.push(current); continue }
    if (current) current.body += line + '\n'
    else if (group === null) intro.push(line)
  }
  for (const sec of sections) sec.body = sec.body.trim()
  return { intro: intro.join('\n').trim(), sections }
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
  // The rail's groups, in order. Only the group being read starts open, so
  // the guide opens as a few headings, not a wall of pages.
  const groups = sections.reduce<{ name: string | null; items: Section[] }[]>((acc, sec) => {
    const last = acc[acc.length - 1]
    if (last && last.name === sec.group) last.items.push(sec); else acc.push({ name: sec.group, items: [sec] })
    return acc
  }, [])
  // A header click is remembered for this visit; otherwise a group is open
  // when it holds the page being read, or is the first group on Overview.
  const [chosen, setChosen] = useState<Record<string, boolean>>({})
  const isOpen = (name: string, items: Section[], i: number) =>
    chosen[name] ?? (items.some(x => x.title === active) || (active === 'Overview' && i === 0))

  return (
    <div className="help-view">
      <div className="help-docs card">
        <nav className="help-nav">
          <div className="help-nav-head">
            {cfg.iconUrl ? <img className="help-nav-icon" src={cfg.iconUrl} alt="" /> : null}
            <span>User guide</span>
          </div>
          <button className={active === 'Overview' ? 'active' : ''} onClick={() => goSection('Overview')}>Overview</button>
          {groups.map((g, i) => {
            const pages = g.items.map(s => (
              <button key={s.title} className={active === s.title ? 'active' : ''} onClick={() => goSection(s.title)}>
                {iconFor(s.title)} <span>{s.title}</span>
              </button>
            ))
            if (!g.name) return <div key={`flat-${i}`}>{pages}</div>
            const open = isOpen(g.name, g.items, i)
            return (
              <div key={g.name} className="help-nav-group">
                <button className="help-nav-group-head" aria-expanded={open} onClick={() => setChosen(c => ({ ...c, [g.name!]: !open }))}>
                  <span>{g.name}</span>
                  <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.6"><path d="m6 4 4 4-4 4"/></svg>
                </button>
                {open && <div className="help-nav-group-pages">{pages}</div>}
              </div>
            )
          })}
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
