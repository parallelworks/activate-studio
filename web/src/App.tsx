import { useEffect, useState } from 'react'
import { ChatView } from './views/ChatView'
import { LibraryView } from './views/LibraryView'
import { SearchView } from './views/SearchView'
import { QueryView } from './views/QueryView'
import { StatusFooter } from './components/StatusFooter'
import { useAppConfig } from './config'

type ViewId = 'chat' | 'library' | 'search' | 'query'
export type Display = { kind: 'file'; target: string } | { kind: 'workflow_dag'; target: string }

const NAV: { id: ViewId; label: string; icon: JSX.Element }[] = [
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
]

export default function App() {
  const [view, setView] = useState<ViewId>('chat')
  const [display, setDisplay] = useState<Display | null>(null)
  const [navCollapsed, setNavCollapsed] = useState(() => localStorage.getItem('ade-nav-collapsed') === '1')
  const cfg = useAppConfig()

  useEffect(() => { localStorage.setItem('ade-nav-collapsed', navCollapsed ? '1' : '0') }, [navCollapsed])
  useEffect(() => { if (cfg.loaded) document.title = cfg.appName }, [cfg])

  useEffect(() => {
    const onDisplay = (e: Event) => {
      const detail = (e as CustomEvent).detail as Display
      if (detail?.kind && detail?.target != null) {
        setDisplay(detail)
        setView('library')
      }
    }
    window.addEventListener('ade-display', onDisplay)
    return () => window.removeEventListener('ade-display', onDisplay)
  }, [])

  const openFile = (path: string) => {
    setDisplay({ kind: 'file', target: path })
    setView('library')
  }

  return (
    <div className="app">
      <nav className={`sidenav ${navCollapsed ? 'collapsed' : ''}`}>
        <div className="sidenav-head">
          <div className="sidenav-brand" title={cfg.appName}>
            <span className="brand-badge">{(cfg.appName[0] ?? 'S').toUpperCase()}</span>
            <span className="brand-text"><b>{cfg.appName}</b></span>
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
          {NAV.map(item => (
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
        <StatusFooter collapsed={navCollapsed} />
      </nav>
      <main className="content">
        <div className={view === 'chat' ? 'view' : 'view hidden'}><ChatView /></div>
        <div className={view === 'library' ? 'view' : 'view hidden'}>
          <LibraryView display={display} onDisplay={setDisplay} />
        </div>
        <div className={view === 'search' ? 'view' : 'view hidden'}><SearchView onOpen={openFile} /></div>
        <div className={view === 'query' ? 'view' : 'view hidden'}><QueryView onOpen={openFile} /></div>
      </main>
    </div>
  )
}
