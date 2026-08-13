import { useCallback, useEffect, useRef, useState } from 'react'
import { Explorer } from './components/Explorer'
import { Viewer } from './components/Viewer'
import { SearchPanel } from './components/SearchPanel'
import { ChatPanel } from './components/ChatPanel'
import { api } from './api'

const CHAT_MIN = 380
const readWidth = () => {
  const saved = Number(localStorage.getItem('ade-chat-width'))
  const fallback = Math.round(window.innerWidth * 0.45)
  return Math.max(CHAT_MIN, saved || fallback)
}

export default function App() {
  const [selected, setSelected] = useState<string | null>(null)
  const [tab, setTab] = useState<'browse' | 'search'>('browse')
  const [gufi, setGufi] = useState<boolean | null>(null)
  const [chatWidth, setChatWidth] = useState(readWidth)
  const [chatFull, setChatFull] = useState(() => localStorage.getItem('ade-chat-full') === '1')
  const dragState = useRef<{ startX: number; startWidth: number } | null>(null)

  useEffect(() => {
    api.health().then(h => setGufi(h.gufi)).catch(() => setGufi(false))
  }, [])

  useEffect(() => { localStorage.setItem('ade-chat-width', String(chatWidth)) }, [chatWidth])
  useEffect(() => { localStorage.setItem('ade-chat-full', chatFull ? '1' : '0') }, [chatFull])

  const onDividerDown = useCallback((e: React.MouseEvent) => {
    e.preventDefault()
    dragState.current = { startX: e.clientX, startWidth: chatWidth }
    const onMove = (ev: MouseEvent) => {
      if (!dragState.current) return
      const delta = dragState.current.startX - ev.clientX
      const max = Math.round(window.innerWidth * 0.75)
      setChatWidth(Math.min(max, Math.max(CHAT_MIN, dragState.current.startWidth + delta)))
    }
    const onUp = () => {
      dragState.current = null
      document.body.classList.remove('resizing')
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
    document.body.classList.add('resizing')
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }, [chatWidth])

  return (
    <div className="app">
      <header className="topbar">
        <span className="brand">ade-studio</span>
        <span className="sub">PW_KNOWLEDGE_BASE</span>
        {gufi === false && <span className="warn">index not built; search limited</span>}
      </header>
      <div className="columns">
        {!chatFull && (
          <aside className="left">
            <div className="tabs">
              <button className={tab === 'browse' ? 'active' : ''} onClick={() => setTab('browse')}>Browse</button>
              <button className={tab === 'search' ? 'active' : ''} onClick={() => setTab('search')}>Search</button>
            </div>
            {tab === 'browse'
              ? <Explorer onOpen={p => setSelected(p)} selected={selected} />
              : <SearchPanel onOpen={p => { setSelected(p) }} />}
          </aside>
        )}
        {!chatFull && (
          <main className="center">
            <Viewer path={selected} />
          </main>
        )}
        {!chatFull && <div className="divider" onMouseDown={onDividerDown} title="Drag to resize chat" />}
        <aside className="right" style={chatFull ? { flex: 1 } : { width: chatWidth }}>
          <ChatPanel fullScreen={chatFull} onToggleFull={() => setChatFull(f => !f)} />
        </aside>
      </div>
    </div>
  )
}
