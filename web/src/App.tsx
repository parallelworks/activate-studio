import { useState } from 'react'
import { ChatView } from './views/ChatView'
import { LibraryView } from './views/LibraryView'
import { SearchView } from './views/SearchView'
import { StatusFooter } from './components/StatusFooter'

type ViewId = 'chat' | 'library' | 'search'

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
]

export default function App() {
  const [view, setView] = useState<ViewId>('chat')
  const [selected, setSelected] = useState<string | null>(null)

  const openFile = (path: string) => {
    setSelected(path)
    setView('library')
  }

  return (
    <div className="app">
      <nav className="sidenav">
        <div className="sidenav-brand">
          <span className="brand-mark">ADE</span>
          <span className="brand-name">Studio</span>
        </div>
        <div className="sidenav-sub">PW Knowledge Base</div>
        <div className="sidenav-items">
          {NAV.map(item => (
            <button
              key={item.id}
              className={`sidenav-item ${view === item.id ? 'active' : ''}`}
              onClick={() => setView(item.id)}
            >
              {item.icon}
              <span>{item.label}</span>
            </button>
          ))}
        </div>
        <StatusFooter />
      </nav>
      <main className="content">
        <div className={view === 'chat' ? 'view' : 'view hidden'}><ChatView /></div>
        <div className={view === 'library' ? 'view' : 'view hidden'}>
          <LibraryView selected={selected} onSelect={setSelected} />
        </div>
        <div className={view === 'search' ? 'view' : 'view hidden'}><SearchView onOpen={openFile} /></div>
      </main>
    </div>
  )
}
