import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { rememberHash } from '../lastLocation'
import {
  ChatProvider, ChatLayout, ChatThread, ChatEmptyState, AttachmentManager, useChat,
} from '@parallelworks/ai-chat'
import { createStudioAdapter, getChatListFilter, setChatListFilter, setViewerUsername } from '../adapter'
import { useAppConfig } from '../config'
import { api } from '../api'
import { setLabelScope } from '../labelScope'

const MODEL_STORAGE_KEY = 'studio.chat.lastModel'

/** Keeps the model selection valid and remembered. A conversation restores
 *  the model its last reply used, which may be a serve that no longer exists;
 *  the composer then lets a message go out and the send fails. Whenever the
 *  selection is missing or absent from the live model list, this swaps in the
 *  remembered model, or the first available one, and every valid selection is
 *  persisted for the next visit. */
function ModelSelectionGuard() {
  const { models, selectedProvider, setSelectedProvider, hasLoadedModels } = useChat()
  useEffect(() => {
    if (selectedProvider && models.some(m => m.id === selectedProvider)) {
      try { localStorage.setItem(MODEL_STORAGE_KEY, selectedProvider) } catch { /* storage unavailable */ }
    }
  }, [selectedProvider, models])
  useEffect(() => {
    if (!hasLoadedModels || models.length === 0) return
    if (selectedProvider && models.some(m => m.id === selectedProvider)) return
    let stored: string | null = null
    try { stored = localStorage.getItem(MODEL_STORAGE_KEY) } catch { /* storage unavailable */ }
    setSelectedProvider(stored && models.some(m => m.id === stored) ? stored : models[0].id)
  }, [hasLoadedModels, models, selectedProvider, setSelectedProvider])
  return null
}

export function ChatView() {
  // The open conversation lives in the URL, so a refresh (or a shared link)
  // returns to the same session instead of an empty chat.
  const [activeId, setActiveIdState] = useState<string | null>(() => {
    const m = location.hash.match(/^#chat=([0-9a-f-]{8,})$/i)
    return m ? m[1] : null
  })
  const setActiveId = useCallback((id: string | null) => {
    setActiveIdState(id)
    const next = id ? `#chat=${id}` : ''
    if (location.hash !== next) {
      history.replaceState(null, '', `${location.pathname}${location.search}${next}`)
    }
    // Refreshing inside the platform frame reloads this app without its
    // hash, so the open conversation is remembered separately.
    rememberHash(next)
  }, [])
  const [showAttachments, setShowAttachments] = useState(false)
  const [rail, setRail] = useState(() => Number(localStorage.getItem('ade-chat-rail')) || 260)
  // Width of the Activity (thinking) drawer on the right; the package
  // renders it fixed at 400px, and our CSS reads this variable over it.
  const [thinkRail, setThinkRail] = useState(() => Number(localStorage.getItem('ade-think-rail')) || 400)

  // The Activity drawer belongs to the chat package; its open state is
  // observed from the DOM and mirrored as a data attribute the CSS keys on,
  // which holds up across package markup changes better than a :has()
  // selector on utility classes.
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return
    const sync = () => {
      const drawer = canvas.querySelector('div[class*="transition-[width]"]')
      const open = !!drawer && !(drawer.className.split(/\s+/).includes('w-0'))
      canvas.dataset.thinkOpen = open ? '1' : '0'
    }
    sync()
    const mo = new MutationObserver(sync)
    mo.observe(canvas, { subtree: true, attributes: true, attributeFilter: ['class'], childList: true })
    return () => mo.disconnect()
  }, [])
  // Phone slide-over state for the conversations rail; the bottom-bar
  // Chat item toggles it, and choosing a conversation closes it.
  const [railOpen, setRailOpen] = useState(false)
  useEffect(() => {
    const t = () => setRailOpen(o => !o)
    window.addEventListener('ade:toggle-chat-rail', t)
    return () => window.removeEventListener('ade:toggle-chat-rail', t)
  }, [])
  useEffect(() => { setRailOpen(false) }, [activeId])

  const adapter = useMemo(() => createStudioAdapter(), [])
  const [credNote, setCredNote] = useState<string | null>(null)
  const [vocab, setVocab] = useState<{ tag: string; count: number }[]>([])
  const [scope, setScope] = useState<Set<string>>(new Set())
  const [chatFilter, setChatFilterState] = useState<'all' | 'mine'>(getChatListFilter())
  const [chatEpoch, setChatEpoch] = useState(0)
  const [multiUser, setMultiUser] = useState(false)
  const [scopeOpen, setScopeOpen] = useState(false)

  // A key added or removed in Settings changes what the chat may list and
  // say: remount the provider so models and the credential notice refetch.
  useEffect(() => {
    const onAccessChange = () => {
      setCredNote(null)
      fetch('/api/chat/models').then(r => r.json())
        .then(d => { if (!d.models?.length && d.error) setCredNote(String(d.error)) })
        .catch(() => { /* the remount below refetches regardless */ })
      setChatEpoch(e => e + 1)
    }
    window.addEventListener('ade:model-access-changed', onAccessChange)
    return () => window.removeEventListener('ade:model-access-changed', onAccessChange)
  }, [])

  // "Add to chat" from the Library hands a corpus path to the composer, the
  // way an editor drops a file reference into a prompt. The composer belongs
  // to the chat package, so the text goes in through the DOM: React tracks
  // the value on the element, and only the native setter plus an input event
  // makes it notice a change from outside.
  useEffect(() => {
    const onInsert = (e: Event) => {
      const text = (e as CustomEvent<{ text: string }>).detail?.text
      if (!text) return
      const put = (tries: number) => {
        const box = document.querySelector<HTMLTextAreaElement>('.chat-canvas textarea')
        if (!box) { if (tries > 0) setTimeout(() => put(tries - 1), 100); return }
        const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set
        const next = box.value ? `${box.value.replace(/\s*$/, '')} ${text} ` : `${text} `
        setter?.call(box, next)
        box.dispatchEvent(new Event('input', { bubbles: true }))
        box.focus()
        box.setSelectionRange(next.length, next.length)
      }
      put(20)
    }
    window.addEventListener('ade-chat-insert', onInsert)
    return () => window.removeEventListener('ade-chat-insert', onInsert)
  }, [])
  const [scopeFilter, setScopeFilter] = useState('')

  useEffect(() => {
    api.tagVocabulary().then(v => setVocab(v.tags)).catch(() => {})
    fetch('/api/me/model-key').then(r => r.json())
      .then(d => setMultiUser(!!d.authEnabled))
      .catch(() => {})
    // Both the no-models state and mid-conversation credential failures
    // surface as a toast with a click path to Settings; the empty state
    // text explains, the toast provides the button.
    fetch('/api/chat/models').then(r => r.json())
      .then(d => { if (!d.models?.length && d.error) setCredNote(String(d.error)) })
      .catch(() => {})
    const onCredError = (e: Event) => setCredNote(String((e as CustomEvent).detail ?? 'Model credential needed.'))
    window.addEventListener('ade-credential-error', onCredError)
    // Back and forward between conversations.
    const onHash = () => {
      const m = location.hash.match(/^#chat=([0-9a-f-]{8,})$/i)
      setActiveIdState(m ? m[1] : null)
    }
    window.addEventListener('hashchange', onHash)
    return () => {
      window.removeEventListener('ade-credential-error', onCredError)
      window.removeEventListener('hashchange', onHash)
    }
  }, [])

  const toggleScope = (tag: string) => {
    setScope(s => {
      const next = new Set(s)
      if (next.has(tag)) next.delete(tag)
      else next.add(tag)
      setLabelScope([...next])
      return next
    })
  }

  const visibleVocab = scopeFilter
    ? vocab.filter(v => v.tag.toLowerCase().includes(scopeFilter.toLowerCase()))
    : vocab

  // The package's default link is a real anchor (/chat/<id>) meant for a
  // routed host; without interception it full-page navigates and loses the
  // SPA state, which read as "sessions do not load".
  const LinkComponent = useCallback(function StudioChatLink({ target, className, title, onClick, children }: {
    target: { kind: 'conversation'; id: string } | { kind: 'attachments' } | { kind: 'external'; href: string }
    className?: string
    title?: string
    onClick?: (e: React.MouseEvent) => void
    children?: React.ReactNode
  }) {
    return (
      <a
        href="#"
        className={className}
        title={title}
        onClick={e => {
          e.preventDefault()
          onClick?.(e)
          if (target.kind === 'conversation') { setActiveId(target.id); setShowAttachments(false) }
          else if (target.kind === 'attachments') setShowAttachments(true)
          else if (target.kind === 'external') window.open(target.href, '_blank', 'noopener')
        }}
      >{children}</a>
    )
  }, [])
  const cfg = useAppConfig()

  const canvasRef = useRef<HTMLDivElement>(null)
  const handleRef = useRef<HTMLDivElement>(null)

  // Drag writes straight to the DOM; React state commits once on release,
  // so the provider subtree does not re-render per mousemove.
  const onThinkDrag = (e: React.MouseEvent) => {
    e.preventDefault()
    const startX = e.clientX
    const startW = thinkRail
    let w = startW
    const onMove = (ev: MouseEvent) => {
      // The drawer hangs off the right edge, so dragging left widens it.
      w = Math.min(700, Math.max(280, startW + (startX - ev.clientX)))
      canvasRef.current?.style.setProperty('--ade-think-rail', `${w}px`)
    }
    const onUp = () => {
      document.body.classList.remove('resizing')
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
      setThinkRail(w)
      localStorage.setItem('ade-think-rail', String(w))
    }
    document.body.classList.add('resizing')
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }

  const onRailDrag = (e: React.MouseEvent) => {
    e.preventDefault()
    const startX = e.clientX
    const startW = rail
    let w = startW
    const onMove = (ev: MouseEvent) => {
      w = Math.min(460, Math.max(180, startW + (ev.clientX - startX)))
      canvasRef.current?.style.setProperty('--ade-chat-rail', `${w}px`)
      if (handleRef.current) handleRef.current.style.left = `${w - 3}px`
    }
    const onUp = () => {
      document.body.classList.remove('resizing')
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
      setRail(w)
      localStorage.setItem('ade-chat-rail', String(w))
    }
    document.body.classList.add('resizing')
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }

  // The empty state freezes its greeting and starter prompts at mount, so
  // wait for the deployment config before mounting the chat tree.
  if (!cfg.loaded) return <div className="chat-wrap"><div className="chat-canvas card" /></div>

  setViewerUsername(cfg.user?.username ?? '')

  const toggleChatFilter = () => {
    const next = chatFilter === 'all' ? 'mine' : 'all'
    setChatListFilter(next)
    setChatFilterState(next)
    setChatEpoch(e => e + 1)
  }

  return (
    <ChatProvider
      key={chatEpoch}
      adapter={adapter}
      currentUser={cfg.user}
      navigation={{
        toConversation: id => { setActiveId(id); setShowAttachments(false) },
        toNewChat: () => {
          setActiveId(null); setShowAttachments(false)
          // Land the cursor in the composer so typing starts immediately.
          const focus = (tries: number) => {
            const box = document.querySelector<HTMLTextAreaElement>('.chat-canvas textarea')
            if (box) box.focus()
            else if (tries > 0) setTimeout(() => focus(tries - 1), 100)
          }
          setTimeout(() => focus(5), 50)
        },
        toAttachments: () => setShowAttachments(true),
      }}
      notify={{
        success: () => {},
        error: (m: string) => console.error(m),
        info: () => {},
      }}
      activeConversationId={activeId}
      config={{
        variant: 'modern',
        suggestedPrompts: cfg.suggestedPrompts,
        LinkComponent,
        // Same-origin /?embed= image sources render as live viewers (DAGs,
        // 3D models) instead of broken images; the server emits them in
        // chat replies. Formerly a dist patch, now the supported hook.
        markdownComponents: {
          img: ({ node: _node, ...props }) =>
            typeof props.src === 'string' && props.src.startsWith('/?embed=') ? (
              <iframe src={props.src} title={String(props.alt ?? 'embedded viewer')} className="chat-embed-frame" />
            ) : (
              <img {...props} />
            ),
        },
        strings: {
          thinking: {
            thoughtFor: (duration: string) => `Worked for ${duration}`,
          },
          emptyState: {
            noProvidersTitle: 'Model credential needed',
            noProvidersBody: 'Add your API key or platform token under Settings, Model access. Browsing, search, and adding material work without one.',
          },
          input: {
            placeholderNoProviders: 'Add your model credential to start chatting',
          },
        },
      }}
    >
      <div className="chat-wrap">
        <div ref={canvasRef} className={`chat-canvas card${railOpen ? ' rail-open' : ''}`} style={{ ['--ade-chat-rail' as string]: `${rail}px`, ['--ade-think-rail' as string]: `${thinkRail}px` }}>
          {credNote && (
            <div className="cred-banner">
              <span className="cred-banner-text">{credNote}</span>
              <button className="btn-primary" onClick={() => { location.hash = '#view=settings:access' }}>Open Settings</button>
              <button className="btn-secondary" onClick={() => setCredNote(null)}>Dismiss</button>
            </div>
          )}
          <ModelSelectionGuard />
          <ChatLayout>
            {showAttachments ? <AttachmentManager /> : activeId ? <ChatThread conversationId={activeId} /> : <ChatEmptyState />}
          </ChatLayout>
          {railOpen && <div className="chat-rail-backdrop" onClick={() => setRailOpen(false)} />}
          <div className="chat-think-handle" onMouseDown={onThinkDrag} title="Drag to resize the activity panel" />
          {multiUser && !showAttachments && (
            <button
              className={`chat-filter-btn ${chatFilter === 'mine' ? 'active' : ''}`}
              style={{ width: rail - 24 }}
              title="Show every conversation on this deployment, or only yours"
              onClick={toggleChatFilter}
            >
              {chatFilter === 'mine' ? 'Showing my chats' : 'Showing all chats'}
            </button>
          )}
          {vocab.length > 0 && (
            <div className={`chat-scope-anchor ${scopeOpen ? 'open' : ''}`}>
              <button
                className={`scope-btn ${scope.size ? 'active' : ''}`}
                title="Limit knowledge base retrieval in this chat to items carrying the selected labels"
                onClick={() => setScopeOpen(o => !o)}
              >
                <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M1.5 3h13l-5 5.5V13l-3 1.5V8.5z"/></svg>
                Scope{scope.size > 0 && <span className="scope-count">{scope.size}</span>}
              </button>
              {scopeOpen && (
                <>
                  <div className="scope-overlay" onClick={() => setScopeOpen(false)} />
                  <div className="scope-menu card">
                    <div className="scope-menu-head">Limit this chat to labels</div>
                    {vocab.length > 8 && (
                      <input
                        className="field scope-filter"
                        value={scopeFilter}
                        onChange={e => setScopeFilter(e.target.value)}
                        placeholder="Filter labels"
                        autoFocus
                      />
                    )}
                    <div className="scope-list">
                      {visibleVocab.map(v => (
                        <label key={v.tag} className="scope-item">
                          <input
                            type="checkbox"
                            checked={scope.has(v.tag)}
                            onChange={() => toggleScope(v.tag)}
                          />
                          <span className="scope-tag">{v.tag}</span>
                          <span className="scope-n">{v.count}</span>
                        </label>
                      ))}
                      {visibleVocab.length === 0 && <div className="muted scope-empty">No labels match.</div>}
                    </div>
                    <div className="scope-menu-foot">
                      <button
                        className="scope-clear"
                        disabled={!scope.size}
                        onClick={() => { setScope(new Set()); setLabelScope([]) }}
                      >Clear all</button>
                      <button className="btn-secondary" onClick={() => setScopeOpen(false)}>Done</button>
                    </div>
                  </div>
                </>
              )}
            </div>
          )}
        </div>
      </div>
    </ChatProvider>
  )
}
