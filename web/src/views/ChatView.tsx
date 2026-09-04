import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { rememberHash } from '../lastLocation'
import {
  ChatProvider, ChatLayout, ChatThread, ChatEmptyState, AttachmentManager, useChat,
} from '@parallelworks/ai-chat'
import { createStudioAdapter, getChatListFilter, setChatListFilter, setViewerUsername } from '../adapter'
import { useAppConfig } from '../config'
import { api } from '../api'
import { setLabelScope, setPersona } from '../labelScope'
import { PersonaIcon } from '../components/PersonaIcon'
import { ConversationScrubber } from '../components/ConversationScrubber'
import { EmbedFrame } from '../components/EmbedFrame'

const MODEL_STORAGE_KEY = 'studio.chat.lastModel'

/** Re-lists the conversation rail when the scope toggle changes, without
 *  remounting the provider and tearing down an open stream. */
function FilterReloader() {
  const { loadConversations } = useChat()
  useEffect(() => {
    const on = () => { void loadConversations() }
    window.addEventListener('ade:chat-filter-changed', on)
    return () => window.removeEventListener('ade:chat-filter-changed', on)
  }, [loadConversations])
  return null
}

/**
 * Selection persistence moved into the chat package (0.4.3+ remembers the
 * chosen model under its own key and validates it against the catalog),
 * which is what the studio's guard did. This carries a choice remembered
 * under the studio's old key across the upgrade, once, and retires the key.
 */
function migrateRememberedModel(): void {
  try {
    const legacy = localStorage.getItem(MODEL_STORAGE_KEY)
    if (legacy && !localStorage.getItem('aiChatSelectedModel')) localStorage.setItem('aiChatSelectedModel', legacy)
    if (legacy) localStorage.removeItem(MODEL_STORAGE_KEY)
  } catch { /* storage unavailable */ }
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
  // Off means the server will not return anyone else's conversations,
  // so the show-all toggle has nothing to show and is hidden.
  const [sharedHistory, setSharedHistory] = useState(true)
  const [scopeOpen, setScopeOpen] = useState(false)
  // Personas: a standing stance for the conversation, distinct from
  // skills, which the assistant loads per task. Remembered per browser so
  // a reload keeps the chosen one.
  const [personaList, setPersonaList] = useState<{ name: string; description: string; shared: boolean; icon: string }[]>([])
  const [persona, setPersonaState] = useState<string | null>(() => localStorage.getItem('ade-persona'))
  const [personaOpen, setPersonaOpen] = useState(false)
  const [personaFilter, setPersonaFilter] = useState('')
  useEffect(() => {
    fetch('/api/extensions').then(r => r.json()).then(d => setPersonaList(d.personas ?? [])).catch(() => {})
  }, [])
  useEffect(() => {
    setPersona(persona)
    if (persona) localStorage.setItem('ade-persona', persona)
    else localStorage.removeItem('ade-persona')
  }, [persona])

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
    migrateRememberedModel()
    api.tagVocabulary().then(v => setVocab(v.tags)).catch(() => {})
    // The model picker is the package's and renders labels from the id
    // alone, so a locked provider cannot be marked inside it (upstream
    // issue filed). The warning therefore lives here, above the thread,
    // where it cannot be missed or truncated.
    fetch('/api/chat/models').then(r => r.json()).then(d => {
      const impaired = (d.impaired ?? []) as { id: string; locked: boolean }[]
      if (impaired.length) {
        const locked = impaired.some(m => m.locked)
        setCredNote(`${impaired.length} model${impaired.length === 1 ? ' is' : 's are'} marked [${locked ? 'locked' : 'unavailable'}] in the model list${locked
          ? ': the provider reports the key is locked'
          : ' (for GenAI this usually means the key is locked on its 8-hour schedule)'}. Unlock and Re-check under Settings, Model access; the marks clear on the next listing.`)
      }
    }).catch(() => {})
    fetch('/api/me/model-key').then(r => r.json())
      .then(d => {
        setMultiUser(!!d.authEnabled)
        const shared = d.sharedHistory !== false
        setSharedHistory(shared)
        // A browser that stored 'all' before the setting was turned
        // off would otherwise sit on a scope it can no longer have.
        if (!shared && getChatListFilter() === 'all') {
          setChatListFilter('mine')
          setChatFilterState('mine')
          window.dispatchEvent(new Event('ade:chat-filter-changed'))
        }
      })
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


  // The empty state freezes its greeting and starter prompts at mount, so
  // wait for the deployment config before mounting the chat tree.
  if (!cfg.loaded) return <div className="chat-wrap"><div className="chat-canvas card" /></div>

  setViewerUsername(cfg.user?.username ?? '')

  const toggleChatFilter = () => {
    const next = chatFilter === 'all' ? 'mine' : 'all'
    setChatListFilter(next)
    setChatFilterState(next)
    // A list refresh, not a remount: bumping the provider epoch here tore
    // down the open thread and aborted any stream just to refilter the
    // rail. FilterReloader inside the provider re-lists instead.
    window.dispatchEvent(new Event('ade:chat-filter-changed'))
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
        suggestedPrompts: cfg.suggestedPrompts,
        LinkComponent,
        // Same-origin /?embed= image sources render as live viewers (DAGs,
        // 3D models) instead of broken images; the server emits them in
        // chat replies. Formerly a dist patch, now the supported hook.
        markdownComponents: {
          img: ({ node: _node, ...props }) =>
            typeof props.src === 'string' && props.src.startsWith('/?embed=') ? (
              <EmbedFrame src={props.src} title={String(props.alt ?? 'embedded viewer')} />
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
        <div ref={canvasRef} className={`chat-canvas card${railOpen ? ' rail-open' : ''}`} style={{ ['--ade-think-rail' as string]: `${thinkRail}px` }}>
          {credNote && (
            <div className="cred-banner">
              <span className="cred-banner-text">{credNote}</span>
              <button className="btn-primary" onClick={() => { location.hash = '#view=settings:access' }}>Open Settings</button>
              <button className="btn-secondary" onClick={() => setCredNote(null)}>Dismiss</button>
            </div>
          )}
                    <FilterReloader />
          <ChatLayout>
            {showAttachments ? <AttachmentManager /> : activeId ? <ChatThread conversationId={activeId} /> : (() => {
              // The same dark-aware pick the sidebar makes, so the two never
              // disagree about which mark to show. The icon rides as a
              // pseudo-element on the package's greeting heading, so it sits
              // directly above "what's on your mind" wherever the package
              // centers it, rather than pinned to the top of the canvas.
              const dark = document.documentElement.dataset.theme === 'dark'
              const icon = dark ? (cfg.iconUrlDark ?? cfg.iconUrl) : cfg.iconUrl
              return (
                <div
                  className={`empty-with-brand${icon ? ' has-icon' : ''}`}
                  style={icon ? { ['--empty-brand-icon' as string]: `url("${icon}")` } : undefined}
                >
                  <ChatEmptyState />
                </div>
              )
            })()}
          </ChatLayout>
          {activeId && !showAttachments && <ConversationScrubber />}
          {railOpen && <div className="chat-rail-backdrop" onClick={() => setRailOpen(false)} />}
          <div className="chat-think-handle" onMouseDown={onThinkDrag} title="Drag to resize the activity panel" />
          {multiUser && sharedHistory && !showAttachments && (
            <button
              className={`chat-filter-btn ${chatFilter === 'mine' ? 'active' : ''}`}
              title="Show every conversation on this deployment, or only yours"
              onClick={toggleChatFilter}
            >
              {chatFilter === 'mine' ? 'Showing my chats' : 'Showing all chats'}
            </button>
          )}
          <div className="chat-controls">
            {(
              <div className={`chat-persona-anchor ${personaOpen ? 'open' : ''}`}>
                <button
                  className={`scope-btn ${persona ? 'active' : ''}`}
                  title="The standing persona for this conversation, from the shared library"
                  onClick={() => setPersonaOpen(o => !o)}
                >
                  {persona
                    ? <PersonaIcon icon={personaList.find(p => p.name === persona)?.icon} name={persona} size={16} />
                    : <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="5.5" r="2.8"/><path d="M2.6 14c.6-3 2.8-4.5 5.4-4.5s4.8 1.5 5.4 4.5"/></svg>}
                  <span className="persona-label">{persona ? personaList.find(p => p.name === persona)?.name ?? persona : 'Persona'}</span>
                </button>
                {personaOpen && (
                  <>
                    <div className="scope-overlay" onClick={() => setPersonaOpen(false)} />
                    <div className="scope-menu card">
                      <div className="scope-menu-head">Persona for this conversation</div>
                      <button className={`persona-row ${!persona ? 'on' : ''}`} onClick={() => { setPersonaState(null); setPersonaOpen(false) }}>
                        <span className="persona-name">Default</span>
                        <span className="muted">how this workspace normally answers</span>
                      </button>
                      {personaList.length > 8 && (
                        <input
                          className="field scope-filter"
                          placeholder={`Filter ${personaList.length} personas`}
                          value={personaFilter}
                          onChange={e => setPersonaFilter(e.target.value)}
                          autoFocus
                        />
                      )}
                      {personaList.length === 0 && (
                        <p className="muted pad">
                          No personas yet. Add one in the Agents tab and it appears here.
                        </p>
                      )}
                      {personaList
                        .filter(p => {
                          const q = personaFilter.trim().toLowerCase()
                          return !q || p.name.toLowerCase().includes(q) || (p.description ?? '').toLowerCase().includes(q)
                        })
                        .map(p => (
                        <button key={p.name} className={`persona-row ${persona === p.name ? 'on' : ''}`}
                          onClick={() => { setPersonaState(p.name); setPersonaOpen(false) }}>
                          <span className="persona-name"><PersonaIcon icon={p.icon} name={p.name} size={18} />{p.name}{p.shared && <span className="persona-shared">shared</span>}</span>
                          {p.description && <span className="muted">{p.description}</span>}
                        </button>
                      ))}
                    </div>
                  </>
                )}
              </div>
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
      </div>
    </ChatProvider>
  )
}
