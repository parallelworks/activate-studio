import { ReactElement, useEffect, useRef, useState } from 'react'
import { ChatView } from './views/ChatView'
import { DagViewer } from './components/DagViewer'
import { ModelViewer } from './components/ModelViewer'
import { LibraryView } from './views/LibraryView'
import { SearchView } from './views/SearchView'
import { QueryView } from './views/QueryView'
import { OverviewView } from './views/OverviewView'
import { HelpView } from './views/HelpView'
import { SettingsView } from './views/SettingsView'
import { StatusFooter } from './components/StatusFooter'
import { useAppConfig } from './config'
import { applyAccent, applySurface } from './accents'

type ViewId = 'chat' | 'library' | 'search' | 'query' | 'overview' | 'settings' | 'help'
export type Display = { kind: 'file'; target: string } | { kind: 'workflow_dag'; target: string }

const NAV: { id: ViewId; label: string; icon: ReactElement }[] = [
  {
    id: 'chat',
    label: 'Chat',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M14 10.5a1.5 1.5 0 0 1-1.5 1.5H5l-3 3V3.5A1.5 1.5 0 0 1 3.5 2h9A1.5 1.5 0 0 1 14 3.5v7z"/></svg>,
  },
  {
    id: 'library',
    label: 'Library',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M1.5 3.5A1 1 0 0 1 2.5 2.5h3l1.5 2h6a1 1 0 0 1 1 1v7a1 1 0 0 1-1 1h-11a1 1 0 0 1-1-1v-9z"/></svg>,
  },
  {
    id: 'search',
    label: 'Search',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="7" cy="7" r="4.5"/><path d="m10.5 10.5 3.5 3.5"/></svg>,
  },
  {
    id: 'query',
    label: 'Query',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><ellipse cx="8" cy="3.5" rx="5.5" ry="2"/><path d="M2.5 3.5v9c0 1.1 2.5 2 5.5 2s5.5-.9 5.5-2v-9"/><path d="M2.5 8c0 1.1 2.5 2 5.5 2s5.5-.9 5.5-2"/></svg>,
  },
  {
    id: 'overview',
    label: 'Stats',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M2 13.5h12"/><path d="M3.5 13.5V8"/><path d="M7 13.5V4.5"/><path d="M10.5 13.5V6.5"/><path d="M14 13.5V2.5"/></svg>,
  },
  {
    id: 'settings',
    label: 'Settings',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="8" r="2.4"/><path d="M8 1.6v2M8 12.4v2M1.6 8h2M12.4 8h2M3.5 3.5l1.4 1.4M11.1 11.1l1.4 1.4M12.5 3.5l-1.4 1.4M4.9 11.1l-1.4 1.4"/></svg>,
  },
  {
    id: 'help',
    label: 'Help',
    icon: <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="8" r="6.5"/><path d="M6 6.2a2 2 0 0 1 3.9.6c0 1.2-1.9 1.5-1.9 2.7"/><circle cx="8" cy="11.8" r="0.3" fill="currentColor"/></svg>,
  },
]

export default function App() {
  const [view, setView] = useState<ViewId>('chat')
  const [display, setDisplay] = useState<Display | null>(null)
  const [navCollapsed, setNavCollapsed] = useState(() => localStorage.getItem('ade-nav-collapsed') === '1')
  const cfg = useAppConfig()

  const [theme, setTheme] = useState<'light' | 'dark' | null>(() => {
    const t = localStorage.getItem('ade-theme')
    return t === 'dark' || t === 'light' ? t : null
  })

  useEffect(() => { localStorage.setItem('ade-nav-collapsed', navCollapsed ? '1' : '0') }, [navCollapsed])
  useEffect(() => { if (cfg.loaded) document.title = cfg.appName }, [cfg])
  useEffect(() => {
    if (!cfg.loaded || !cfg.faviconUrl) return
    let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]')
    if (!link) {
      link = document.createElement('link')
      link.rel = 'icon'
      document.head.appendChild(link)
    }
    link.href = cfg.faviconUrl
  }, [cfg])
  useEffect(() => { if (cfg.loaded) { applyAccent(cfg.accent); applySurface(cfg.surface) } }, [cfg])
  useEffect(() => {
    const effective = theme ?? cfg.theme
    document.documentElement.dataset.theme = effective
    // Cached so the next load paints in this theme before config arrives.
    if (cfg.loaded) { try { localStorage.setItem('ade-theme-default', cfg.theme) } catch { /* ignore */ } }
  }, [theme, cfg])

  const toggleTheme = () => {
    const next = (theme ?? cfg.theme) === 'dark' ? 'light' : 'dark'
    setTheme(next)
    localStorage.setItem('ade-theme', next)
  }

  // Navigation state lives in the URL hash: #open=kind:target for an open
  // document, #view=<id> for a bare view, empty for the default chat view.
  // applyHash covers initial load, chat links, manual edits, and browser
  // back/forward (hash-only history moves fire hashchange).
  useEffect(() => {
    const applyHash = () => {
      const m = location.hash.match(/^#open=(file|workflow_dag):(.+)$/)
      if (m) {
        setDisplay({ kind: m[1] as Display['kind'], target: decodeURIComponent(m[2]) })
        setView('library')
        return
      }
      const vm = location.hash.match(/^#view=([a-z]+)(?::([a-z0-9-]+))?$/)
      const v = vm?.[1]
      if (v && NAV.some(n => n.id === v)) {
        setView(v as ViewId)
        if (vm?.[2]) window.dispatchEvent(new CustomEvent('ade-view-section', { detail: { view: v, section: vm[2] } }))
      } else if (!location.hash) setView('chat')
    }
    applyHash()
    // Chat links render with target=_blank; catch #open= clicks in the
    // capture phase and route them to the viewer in this tab instead.
    const onClick = (e: MouseEvent) => {
      const a = (e.target as HTMLElement).closest?.('a[href*="#open="]') as HTMLAnchorElement | null
      const m = a?.getAttribute('href')?.match(/#open=(file|workflow_dag):(.+)$/)
      if (!m) return
      e.preventDefault()
      e.stopPropagation()
      setDisplay({ kind: m[1] as Display['kind'], target: decodeURIComponent(m[2]) })
      setView('library')
    }
    window.addEventListener('hashchange', applyHash)
    window.addEventListener('click', onClick, true)
    return () => {
      window.removeEventListener('hashchange', applyHash)
      window.removeEventListener('click', onClick, true)
    }
  }, [])

  // Push a history entry whenever the navigation state changes, so the
  // browser's back/forward buttons walk opens and view switches. After a
  // back/forward the hash already matches the restored state, so the
  // comparison makes this a no-op instead of a re-push (no feedback loop).
  // The first run only arms the ref: on a deep-linked load the state update
  // from applyHash has not landed yet, and writing here would clobber the
  // incoming hash. Slashes stay literal in the hash for readable URLs.
  const navReady = useRef(false)
  useEffect(() => {
    if (!navReady.current) { navReady.current = true; return }
    const hash = display && view === 'library'
      ? `#open=${display.kind}:${encodeURIComponent(display.target).replace(/%2F/gi, '/')}`
      : view !== 'chat' ? `#view=${view}` : ''
    if ((location.hash || '') === hash) return
    // Views may own a :section suffix on their hash; leave it theirs.
    if (hash && location.hash.startsWith(`${hash}:`)) return
    history.pushState(null, '', location.pathname + location.search + hash)
  }, [view, display])

  const openFile = (path: string) => {
    setDisplay({ kind: 'file', target: path })
    setView('library')
  }

  // Embed mode: /?embed=dag&workflow=<name> or /?embed=model&path=<rel>
  // renders a single interactive viewer with no app chrome, for iframes
  // hosted inside chat replies (the ai-chat img override turns same-origin
  // /?embed= image sources into iframes).
  const embedParams = new URLSearchParams(location.search)
  const embedKind = embedParams.get('embed')
  if (embedKind === 'dag' && embedParams.get('workflow')) {
    return <div className="embed-root"><DagViewer workflow={embedParams.get('workflow')!} /></div>
  }
  if (embedKind === 'model' && embedParams.get('path')) {
    return <div className="embed-root"><ModelViewer path={embedParams.get('path')!} /></div>
  }

  return (
    <div className="app">
      <nav className={`sidenav ${navCollapsed ? 'collapsed' : ''}`}>
        <div className="sidenav-head">
          <div className="sidenav-brand" title={cfg.loaded ? cfg.appName : undefined}>
            {/* Hold the slot empty until config arrives so the generic
                default never flashes before the deployment brand. */}
            {cfg.loaded && (cfg.iconUrl
              ? <img className="brand-icon" src={cfg.iconUrl} alt="" />
              : <span className="brand-badge">{(cfg.appName[0] ?? 'S').toUpperCase()}</span>)}
            {cfg.loaded && <span className="brand-text"><b>{cfg.appName}</b></span>}
          </div>
          <button
            className="nav-collapse"
            title={navCollapsed ? 'Expand navigation' : 'Collapse navigation'}
            onClick={() => setNavCollapsed(c => !c)}
          >
            {navCollapsed ? '»' : '«'}
          </button>
        </div>
        <div className="sidenav-items">
          {NAV.filter(i => i.id !== 'settings' && i.id !== 'help').map(item => (
            <button
              key={item.id}
              className={`sidenav-item ${view === item.id ? 'active' : ''}`}
              title={item.label}
              onClick={() => setView(item.id)}
            >
              {item.icon}
              <span>{item.label}</span>
            </button>
          ))}
        </div>
        <div className="sidenav-items sidenav-secondary">
          {NAV.filter(i => i.id === 'settings' || i.id === 'help').map(item => (
            <button
              key={item.id}
              className={`sidenav-item ${view === item.id ? 'active' : ''}`}
              title={item.label}
              onClick={() => setView(item.id)}
            >
              {item.icon}
              <span>{item.label}</span>
            </button>
          ))}
          <button
            className="sidenav-item"
            title={(theme ?? cfg.theme) === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
            onClick={toggleTheme}
          >
            {(theme ?? cfg.theme) === 'dark'
              ? <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="8" r="3.2"/><path d="M8 1.2v1.8M8 13v1.8M1.2 8H3M13 8h1.8M3.2 3.2l1.3 1.3M11.5 11.5l1.3 1.3M12.8 3.2l-1.3 1.3M4.5 11.5l-1.3 1.3"/></svg>
              : <svg viewBox="0 0 16 16" width="16" height="16" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M13.5 9.5A6 6 0 0 1 6.5 2.5a6 6 0 1 0 7 7z"/></svg>}
            <span>{(theme ?? cfg.theme) === 'dark' ? 'Light mode' : 'Dark mode'}</span>
          </button>
        </div>
        <StatusFooter collapsed={navCollapsed} />
      </nav>
      <main className="content">
        <div className={view === 'chat' ? 'view' : 'view hidden'}><ChatView /></div>
        <div className={view === 'library' ? 'view' : 'view hidden'}>
          <LibraryView display={display} onDisplay={setDisplay} />
        </div>
        <div className={view === 'search' ? 'view' : 'view hidden'}><SearchView onOpen={openFile} /></div>
        <div className={view === 'query' ? 'view' : 'view hidden'}><QueryView onOpen={openFile} /></div>
        <div className={view === 'overview' ? 'view' : 'view hidden'}><OverviewView onOpen={openFile} /></div>
        <div className={view === 'settings' ? 'view' : 'view hidden'}><SettingsView /></div>
        <div className={view === 'help' ? 'view' : 'view hidden'}><HelpView /></div>
      </main>
    </div>
  )
}
