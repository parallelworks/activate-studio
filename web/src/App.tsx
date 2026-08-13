import { useEffect, useState } from 'react'
import { Explorer } from './components/Explorer'
import { Viewer } from './components/Viewer'
import { SearchPanel } from './components/SearchPanel'
import { ChatPanel } from './components/ChatPanel'
import { api } from './api'

export default function App() {
  const [selected, setSelected] = useState<string | null>(null)
  const [tab, setTab] = useState<'browse' | 'search'>('browse')
  const [gufi, setGufi] = useState<boolean | null>(null)

  useEffect(() => {
    api.health().then(h => setGufi(h.gufi)).catch(() => setGufi(false))
  }, [])

  return (
    <div className="app">
      <header className="topbar">
        <span className="brand">ade-studio</span>
        <span className="sub">PW_KNOWLEDGE_BASE</span>
        {gufi === false && <span className="warn">index not built; search limited</span>}
      </header>
      <div className="columns">
        <aside className="left">
          <div className="tabs">
            <button className={tab === 'browse' ? 'active' : ''} onClick={() => setTab('browse')}>Browse</button>
            <button className={tab === 'search' ? 'active' : ''} onClick={() => setTab('search')}>Search</button>
          </div>
          {tab === 'browse'
            ? <Explorer onOpen={p => setSelected(p)} selected={selected} />
            : <SearchPanel onOpen={p => { setSelected(p) }} />}
        </aside>
        <main className="center">
          <Viewer path={selected} />
        </main>
        <aside className="right">
          <ChatPanel />
        </aside>
      </div>
    </div>
  )
}
