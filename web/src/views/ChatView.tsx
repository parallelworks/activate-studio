import React, { Activity, useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { accountsHref } from '../accounts'
import {
  Button, Callout, Checkbox, EmptyState, Popover, PopoverClose, SearchInput, SegmentedControl,
} from '@parallelworks/ui'
import { useEffectiveTheme } from '../theme'
import { SlashPalette } from '../components/SlashPalette'
import { EffortPicker } from '../components/EffortPicker'
import { NewChatModelSlot } from '../pwcode/NewChatModel'
import { PwCodeOwnModel, useModelHeld } from '../pwcode/ownModel'
import { takeHanded, onFirstMessage } from '../pwcode/firstMessage'
import { NewChatTargetSlot } from '../pwcode/NewChatTarget'
import { dropLibraryStart } from '../pwcode/newChatStart'
import { CHAT_HERE_EVENT } from '../pwcode/chatHere'
import { setNewChatEngine } from '../chatEngine'
import { MentionPalette } from '../components/MentionPalette'
import { MentionLinks } from '../components/MentionLinks'
import { breakable } from '../components/breakable'
import { NextUp } from '../components/NextUp'
import { VoiceOverlay } from '../components/VoiceOverlay'
import { rememberHash } from '../lastLocation'
import {
  ChatProvider, ChatThread, ChatEmptyState, AttachmentManager, useChat, type ChatLinkProps,
} from '@parallelworks/ui/ai'
import { StudioChatLayout } from '../components/ChatList'
import { ChatsManager } from '../components/ChatsManager'
import { ManageChatsRailItem } from '../components/ManageChatsRailItem'
import { createStudioAdapter, getChatListFilter, setChatListFilter, setViewerUsername } from '../adapter'
import { useAppConfig } from '../config'
import { api, fetchModels, forgetModels, type ModelsResponse } from '../api'
import { setLabelScope, setPersona } from '../labelScope'
import { LibraryTicks, scopeLabel } from '../components/LibraryScope'
import { useChatScope } from '../chatScope'
import type { Scope } from '../scope'
import { useLabelVocabulary } from '../labelVocabulary'
import { PersonaIcon } from '../components/PersonaIcon'
import { ConversationScrubber } from '../components/ConversationScrubber'
import { LibraryImage, isLibrarySource } from '../components/LibraryImage'
import { EmbedFrame } from '../components/EmbedFrame'
import { SideViewHost } from '../components/RunPane'
import { PanelToggle } from '../components/StatusPanel'
import { modelLabel, share } from '../statusShare'
import { ToolApproval } from '../components/ToolApproval'
import { TransferNoteWatcher } from '../components/TransferNoteWatcher'
import { GUFI_TOOL_RENDERERS, gufiToolPresentation } from '../components/GufiCommands'
import { EmptyChatBrand } from '../components/EmptyChatBrand'
import { SaveChatButton } from '../components/SaveChatButton'
import { EngineMarks, EngineNoteReader, PWCodeApprovals, PWCodeEngine, PWCodeModelNotice, PWCodeNotice, PWCodeWatcher } from '../components/PWCodeEngine'
import { fitControls, watchNotices } from '../chatTop'
import { PWCodeSession } from '../pwcode/PWCodeSession'
import { useNames } from '../pwcode/chatNames'
import { BackgroundCommandHint } from '../pwcode/session/BackgroundCommandHint'
import { BACKGROUND_TOOL } from '@parallelworks/code'

import { type ChatLink, useChatLink } from '../pwcode/useChatLink'
import { ChatMarkdownLink } from '../chatLinks'
import { CitedPaths, citedMarkdown } from '../citedPaths'
import { VIEWER_CACHES_CLEARED } from '../viewerCache'
import { prefetchChat } from '../chatRecord'
import { setFrontChat } from '../notices'
import { PWCodeToolsNote } from '../pwcode/ToolsNote'

const PWCODE_TOOL_FOOTERS = { [BACKGROUND_TOOL]: BackgroundCommandHint }

const MODEL_STORAGE_KEY = 'studio.chat.lastModel'

/** Re-lists the conversation rail when the scope toggle changes, or a chat
 *  was made elsewhere (Continue in Studio on the Code page), without
 *  remounting the provider and tearing down an open stream. */
function FilterReloader() {
  const { loadConversations } = useChat()
  // A chat's title follows its pw code session's name, wherever that is
  // changed.
  useNames()
  useEffect(() => {
    const on = () => { void loadConversations() }
    window.addEventListener('ade:chat-filter-changed', on)
    window.addEventListener('ade:chats-changed', on)
    return () => {
      window.removeEventListener('ade:chat-filter-changed', on)
      window.removeEventListener('ade:chats-changed', on)
    }
  }, [loadConversations])
  return null
}

/**
 * "Retry with" links under a reply the model could not give (an empty
 * reply, or a provider still rate limiting after the server's waits):
 * `#retry-with=<model>&reply-to=<user message id>`. A click selects that
 * model and asks the same question again as a new branch, the way the
 * thread's own retry does. The model is selected first and the question
 * sent once the selection has taken, since the package's send reads the
 * selection it was rendered with.
 */
export function RetryWithModel() {
  const { currentConversation, selectedProvider, setSelectedProvider, sendMessage, isStreaming } = useChat()
  const [pending, setPending] = useState<{ model: string; content: string; attachmentIds?: string[]; parentId?: string } | null>(null)
  useEffect(() => {
    const on = (e: MouseEvent) => {
      const a = (e.target as HTMLElement | null)?.closest?.('a[href^="#retry-with="]') as HTMLAnchorElement | null
      if (!a) return
      e.preventDefault()
      e.stopPropagation()
      const m = /^#retry-with=([^&]+)(?:&reply-to=(.+))?$/.exec(a.getAttribute('href') ?? '')
      if (!m) return
      const msgs = currentConversation?.messages ?? []
      const replyTo = m[2] ? decodeURIComponent(m[2]) : null
      const asked = (replyTo && msgs.find(x => x.id === replyTo && x.role === 'user')) || [...msgs].reverse().find(x => x.role === 'user')
      if (!asked) return
      const model = decodeURIComponent(m[1])
      setPending({ model, content: asked.content || '', attachmentIds: asked.attachments?.map(x => x.id), parentId: asked.parentId || undefined })
      setSelectedProvider(model)
    }
    document.addEventListener('click', on, true)
    return () => document.removeEventListener('click', on, true)
  }, [currentConversation, setSelectedProvider])
  useEffect(() => {
    if (!pending || isStreaming || selectedProvider !== pending.model) return
    setPending(null)
    void sendMessage(pending.content, pending.attachmentIds, pending.parentId)
  }, [pending, isStreaming, selectedProvider, sendMessage])
  return null
}

/**
 * A pw code session draws itself, so the chat's provider is not told of the
 * chat that shows one: it went on holding the chat open before it, and the
 * parts that read it (the copied reply, the suggestions, the engine's state,
 * a reply still being written) went on being that chat's. It lets go of
 * that chat before the session is painted; the reply runs on, and its chat
 * shows it when it is opened again. It lets go, too, of a chat whose first
 * message is the session's to run: the provider has it as sent, with a
 * reply of nothing.
 */
export function LeaveOtherChat({ conversationId, sessionShown }: { conversationId: string | null; sessionShown: boolean }) {
  const { currentConversation, clearCurrentConversation, isStreaming } = useChat()
  const other = sessionShown && !!currentConversation && currentConversation.id !== conversationId
  useLayoutEffect(() => { if (other) clearCurrentConversation() }, [other, clearCurrentConversation])
  const own = sessionShown && !isStreaming && !!currentConversation && currentConversation.id === conversationId ? conversationId : null
  useEffect(() => {
    if (!own) return
    const leave = () => { if (takeHanded(own)) clearCurrentConversation() }
    leave()
    return onFirstMessage(leave)
  }, [own, clearCurrentConversation])
  return null
}

/** Tells the status panel which model the chat has selected, which is the
 *  package's default until the viewer picks one. */
function ModelShare() {
  const { selectedProvider, models } = useChat()
  const m = models.find(x => x.id === selectedProvider)
  const model = m ? modelLabel(m) : selectedProvider ? modelLabel({ id: selectedProvider }) : null
  const provider = m?.provider ?? null
  useEffect(() => { share({ model, modelProvider: provider }) }, [model, provider])
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

// The phone layout's breakpoint in styles.css; the conversations list turns
// into the package's drawer at the same width the bottom bar appears.
const PHONE_QUERY = '(max-width: 760px)'

/** How many pw code chats stay drawn, hidden, once left. */
const KEPT_SESSIONS = 3

interface KeptSession { conversationId: string; machine: string; sessionId: string; link: ChatLink }

/**
 * The pw code chats to draw: the open one, when it shows its session, and
 * the ones opened just before it, which stay drawn but hidden (Activity),
 * so a return to one is immediate. A chat set to the Studio assistant
 * leaves the list, and the list empties when the viewer's caches do
 * (viewerCache.ts).
 */
function useKeptSessions(activeId: string | null, pw: ReturnType<typeof useChatLink>): KeptSession[] {
  const [kept, setKept] = useState<KeptSession[]>([])
  const current = activeId && pw.shown && pw.link?.session
    ? { conversationId: activeId, machine: pw.machine, sessionId: pw.link.session, link: pw.link }
    : null
  useEffect(() => {
    if (!activeId) return
    setKept(k => {
      const rest = k.filter(x => x.conversationId !== activeId)
      // Only once its link is read is a chat known to be another kind.
      if (!current) return rest.length === k.length ? k : rest
      const same = k[0]?.conversationId === current.conversationId && k[0].machine === current.machine && k[0].sessionId === current.sessionId && k[0].link === current.link
      return same ? k : [current, ...rest].slice(0, KEPT_SESSIONS)
    })
  }, [activeId, current?.machine, current?.sessionId, current?.link]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const clear = () => setKept([])
    window.addEventListener(VIEWER_CACHES_CLEARED, clear)
    return () => window.removeEventListener(VIEWER_CACHES_CLEARED, clear)
  }, [])
  if (!current) return kept.filter(k => k.conversationId !== activeId)
  return [current, ...kept.filter(k => k.conversationId !== current.conversationId)].slice(0, KEPT_SESSIONS)
}

function usePhoneLayout(): boolean {
  const [phone, setPhone] = useState(() => matchMedia(PHONE_QUERY).matches)
  useEffect(() => {
    const mq = matchMedia(PHONE_QUERY)
    const on = () => setPhone(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  return phone
}

/** Puts text at the end of what the composer holds, the cursor after it. */
function insertInBox(text: string): void {
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

/** active: the chat is the view on screen. Find in the transcript listens
 *  for ⌘F / Ctrl+F on the whole page, so a hidden chat leaves the key to
 *  the view that is showing. */
export function ChatView({ active = true }: { active?: boolean } = {}) {
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
  const [showManage, setShowManage] = useState(false)
  // In a narrow window (a phone, or a desktop window under the phone
  // breakpoint) the conversations list is the package's drawer, opened by
  // the package's own button above the thread, and by the bottom-bar Chat
  // item; the package closes it on a pick, a tap outside or Escape.
  const phone = usePhoneLayout()
  const [railOpen, setRailOpen] = useState(false)
  useEffect(() => {
    const t = () => setRailOpen(o => !o)
    window.addEventListener('ade:toggle-chat-rail', t)
    return () => window.removeEventListener('ade:toggle-chat-rail', t)
  }, [])
  useEffect(() => { setRailOpen(false) }, [activeId])
  // The chat in front of the person: its notices are dealt with.
  const front = active && !showAttachments && !showManage ? activeId : null
  useEffect(() => {
    setFrontChat(front)
    return () => setFrontChat(null)
  }, [front])

  const adapter = useMemo(() => createStudioAdapter(), [])
  const [credNote, setCredNote] = useState<string | null>(null)
  // The note on screen, readable from handlers defined before render.
  const credNoteRef = useRef<string | null>(null)
  credNoteRef.current = credNote
  const effectiveTheme = useEffectiveTheme()
  const [voiceOpen, setVoiceOpen] = useState(false)
  const vocab = useLabelVocabulary()
  const [scope, setScope] = useState<Set<string>>(new Set())
  const [chatFilter, setChatFilterState] = useState<'all' | 'mine'>(getChatListFilter())
  const [chatEpoch, setChatEpoch] = useState(0)
  const [multiUser, setMultiUser] = useState(false)
  // Off means the server will not return anyone else's conversations,
  // so the show-all toggle has nothing to show and is hidden.
  const [sharedHistory, setSharedHistory] = useState(true)
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
      forgetModels()
      fetchModels({ refresh: true }).then(applyModels).catch(() => { /* the remount below refetches regardless */ })
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
      if (text) insertInBox(text)
    }
    window.addEventListener('ade-chat-insert', onInsert)
    return () => window.removeEventListener('ade-chat-insert', onInsert)
  }, [])
  const [scopeFilter, setScopeFilter] = useState('')

  // What the listing says, applied wherever it came from: the banner above
  // the thread, and a remount of the picker when the set of marked models
  // changes, since the picker is the package's and only re-reads on mount.
  // A remount mid-reply would drop the visible stream, so it waits for the
  // next idle moment.
  const marksRef = useRef<string>('')
  // A dismissed availability notice stays dismissed for that exact set of
  // marked models. It returns only when the set changes: a model marked or
  // cleared, or a lock turning into an outage. Kept per browser.
  const DISMISS_KEY = 'ade-marks-dismissed'
  const marksNote = useRef<{ text: string; marks: string } | null>(null)
  const dismissedMarks = () => { try { return localStorage.getItem(DISMISS_KEY) ?? '' } catch { return '' } }
  const dismissNote = () => {
    if (marksNote.current && marksNote.current.text === credNoteRef.current) {
      try { localStorage.setItem(DISMISS_KEY, marksNote.current.marks) } catch { /* storage unavailable */ }
    }
    setCredNote(null)
  }
  const applyModels = (d: ModelsResponse) => {
    if (d.error && !(d.models ?? []).length) { setCredNote(String(d.error)); return }
    const impaired = (d.impaired ?? []) as { id: string; locked: boolean; reason?: string }[]
    const marks = impaired.map(m => `${m.id}:${m.locked ? 'L' : 'U'}`).sort().join(',')
    if (impaired.length) {
      // Say what the provider said. A lock gets the unlock step; anything
      // else is quoted, since guessing a cause misleads (this banner once
      // blamed an 8-hour key lock for a provider that was rejecting the
      // model names themselves).
      const locked = impaired.filter(m => m.locked)
      const reasons = [...new Set(impaired.map(m => m.reason).filter(Boolean))] as string[]
      const n = impaired.length
      const head = `${n} model${n === 1 ? ' is' : 's are'} marked [${locked.length ? 'locked' : 'unavailable'}] because the provider refused a test request`
      const why = locked.length
        ? ': it reports the key is locked. Unlock it, then Re-check under Settings, Model access.'
        : reasons.length ? `: "${reasons[0]}". Re-check under Settings, Model access, once the provider is answering again.` : '. Re-check under Settings, Model access.'
      const text = head + why
      marksNote.current = { text, marks }
      if (dismissedMarks() !== marks) setCredNote(text)
    } else {
      // Nothing marked any more: a later recurrence should be shown again.
      try { localStorage.removeItem(DISMISS_KEY) } catch { /* storage unavailable */ }
      if (marksRef.current && marksNote.current?.text === credNoteRef.current) setCredNote(null)
      marksNote.current = null
    }
    if (marksRef.current && marks !== marksRef.current) setChatEpoch(e => e + 1)
    marksRef.current = marks
  }
  // Keep the status fresh without a reload: ask again when the tab comes
  // back into view and every five minutes while it stays there. The
  // server answers from its cache and refreshes behind the answer, so
  // these are cheap.
  useEffect(() => {
    const tick = () => { if (document.visibilityState === 'visible') fetchModels().then(applyModels).catch(() => {}) }
    const onVisible = () => { if (document.visibilityState === 'visible') { forgetModels(); tick() } }
    document.addEventListener('visibilitychange', onVisible)
    const id = window.setInterval(() => { forgetModels(); tick() }, 5 * 60_000)
    return () => { document.removeEventListener('visibilitychange', onVisible); window.clearInterval(id) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    migrateRememberedModel()
    // The model picker is the package's and renders labels from the id
    // alone, so a locked provider cannot be marked inside it (upstream
    // issue filed). The warning therefore lives here, above the thread,
    // where it cannot be missed or truncated.
    fetchModels().then(applyModels).catch(() => {})
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
    const onCredError = (e: Event) => setCredNote(String((e as CustomEvent).detail ?? 'Model credential needed.'))
    window.addEventListener('ade-credential-error', onCredError)
    // Back and forward between conversations. Only the chat's own hashes
    // move it: opening a file (#open=), or another view, leaves the open
    // conversation alone. Taking every other hash as "no conversation"
    // closed the thread whenever a chat attachment or link was opened,
    // which cancelled the reply in progress and dropped the queued
    // messages behind it.
    const onHash = () => {
      const h = location.hash
      const m = h.match(/^#chat=([0-9a-f-]{8,})$/i)
      if (m) setActiveIdState(m[1])
      else if (h === '' || h === '#') setActiveIdState(null)
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
  const openNewChat = useCallback(() => {
    setActiveId(null); setShowAttachments(false); setShowManage(false)
    // Land the cursor in the composer so typing starts immediately.
    const focus = (tries: number) => {
      const box = document.querySelector<HTMLTextAreaElement>('.chat-canvas textarea')
      if (box) box.focus()
      else if (tries > 0) setTimeout(() => focus(tries - 1), 100)
    }
    setTimeout(() => focus(5), 50)
  }, [setActiveId])
  const startNewChat = useCallback(() => {
    // A new chat asked for here is a plain one.
    dropLibraryStart()
    setNewChatEngine('')
    openNewChat()
  }, [openNewChat])
  // New pw code chat here, in the Library (pwcode/chatHere.ts).
  useEffect(() => {
    const on = (e: Event) => {
      const text = (e as CustomEvent<{ text: string }>).detail?.text
      openNewChat()
      // Once the new chat's own box is drawn in place of the open chat's.
      if (text) setTimeout(() => insertInBox(text), 50)
    }
    window.addEventListener(CHAT_HERE_EVENT, on)
    return () => window.removeEventListener(CHAT_HERE_EVENT, on)
  }, [openNewChat])
  const LinkComponent = useCallback(function StudioChatLink({ target, className, title, onClick, children }: ChatLinkProps) {
    return (
      <a
        href="#"
        className={className}
        title={title}
        // A chat the pointer rests on is read ahead, so it opens from what
        // is known (chatRecord.ts).
        onPointerEnter={target.kind === 'conversation' ? () => prefetchChat(target.id) : undefined}
        onFocus={target.kind === 'conversation' ? () => prefetchChat(target.id) : undefined}
        onClick={e => {
          e.preventDefault()
          onClick?.(e)
          if (target.kind === 'conversation') { setActiveId(target.id); setShowAttachments(false); setShowManage(false) }
          else if (target.kind === 'newChat') startNewChat()
          else if (target.kind === 'attachments') { setShowAttachments(true); setShowManage(false) }
          else if (target.kind === 'external' && target.href.startsWith('#')) location.hash = target.href
          else if (target.kind === 'external') window.open(target.href, '_blank', 'noopener')
        }}
      >{breakable(children)}</a>
    )
  }, [startNewChat])
  const cfg = useAppConfig()
  // A chat with a pw code session shows the session itself (pwcode/).
  const pw = useChatLink(activeId)
  const held = useModelHeld()
  const keptSessions = useKeptSessions(activeId, pw)
  // The libraries the chat searches are part of its scope, ticked in the
  // same menu as the labels: every library until the user narrows it. The
  // button names the choice when there is more than one library.
  const libraries = useMemo(() => cfg.libraries ?? [], [cfg.libraries])
  const hasLibraries = libraries.length > 1
  const libraryIds = useMemo(() => libraries.map(l => l.id), [libraries])
  const [chatScope, setChatScope] = useChatScope(cfg.libraries)
  const libLabel = scopeLabel(libraries, chatScope)

  const canvasRef = useRef<HTMLDivElement>(null)
  // The Persona and Scope buttons float over the top right of the thread,
  // where the package puts the end of its find bar (the count, the steps
  // and Close). The bar stops short of them, or, where that leaves it too
  // narrow, sits below them (styles.css, .chat-canvas main search). On a
  // phone the package's own header row already puts the bar below them.
  useEffect(() => {
    const canvas = canvasRef.current
    const controls = canvas?.querySelector<HTMLElement>(':scope > .chat-controls')
    if (!canvas || !controls || typeof ResizeObserver === 'undefined') return
    const phone = matchMedia(PHONE_QUERY)
    // Where the thread is too narrow for the buttons' labels (a narrow
    // window, or the Status panel open), the labels drop to icons, the
    // engine's last, so the row never runs over the conversations list.
    const measure = () => {
      const main = canvas.querySelector('main')?.getBoundingClientRect()
      fitControls(controls, main && !phone.matches ? main.width - 24 : null)
      const c = controls.getBoundingClientRect()
      const beside = main && c.width > 0 && !phone.matches && main.width - c.width - 40 >= 280
      const below = main && c.width > 0 && !phone.matches && !beside
      canvas.style.setProperty('--chat-find-pad', beside ? `${Math.ceil(c.width) + 24}px` : '0px')
      canvas.style.setProperty('--chat-find-top', below ? `${Math.ceil(c.bottom - main.top) + 6}px` : '0px')
    }
    measure()
    const ro = new ResizeObserver(measure)
    ro.observe(controls)
    ro.observe(canvas)
    // A label that changes while hidden (a persona chosen, the engine's
    // model) does not resize the row, so the fit is checked again then.
    const mo = typeof MutationObserver === 'undefined' ? null : new MutationObserver(measure)
    mo?.observe(controls, { subtree: true, childList: true, characterData: true })
    return () => { ro.disconnect(); mo?.disconnect() }
  }, [cfg.loaded, chatEpoch])

  // A notice over the top of the thread (pw code's model, the Studio
  // assistant answering, a missing key) sits over the thread, not the
  // conversations list, and pushes the start of the conversation below it
  // rather than covering it: the band at the top of the thread
  // (styles.css) grows to the notice's foot.
  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas || typeof ResizeObserver === 'undefined' || typeof MutationObserver === 'undefined') return
    return watchNotices(canvas)
  }, [cfg.loaded, chatEpoch])

  // The empty state freezes its greeting and starter prompts at mount, so
  // wait for the deployment config before mounting the chat tree.
  if (!cfg.loaded) return <div className="chat-wrap"><div className="chat-canvas card" /></div>

  setViewerUsername(cfg.user?.username ?? '')

  const changeChatFilter = (next: 'all' | 'mine') => {
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
      drawerOpen={railOpen}
      onDrawerOpenChange={setRailOpen}
      currentUser={cfg.user}
      navigation={{
        toConversation: id => { setActiveId(id); setShowAttachments(false); setShowManage(false) },
        toNewChat: startNewChat,
      }}
      activeConversationId={activeId}
      config={{
        suggestedPrompts: cfg.suggestedPrompts,
        LinkComponent,
        // The model picker's Manage link and the no-model empty state's button.
        extraLinks: { manageProviders: '#view=settings:access', addProvider: accountsHref(held ? undefined : 'endpoint') },
        // Same-origin /?embed= image sources render as live viewers (DAGs,
        // 3D models) instead of broken images; the server emits them in
        // chat replies. Formerly a dist patch, now the supported hook.
        markdownComponents: {
          // Inline code and text, with the library files they name as links.
          ...citedMarkdown,
          // A link to a place in the Studio opens in this tab, and says so.
          a: ChatMarkdownLink,
          img: ({ node: _node, ...props }) =>
            typeof props.src === 'string' && props.src.startsWith('/?embed=') ? (
              <EmbedFrame src={props.src} title={String(props.alt ?? 'embedded viewer')} />
            ) : isLibrarySource(props.src) ? (
              // A file of the library, which may be gone by the time the
              // reply is read again.
              <LibraryImage {...props} />
            ) : (
              <img {...props} />
            ),
        },
        // A call that changes something on the platform waits for the
        // user's answer; the Studio draws the package's approval block with
        // the alternative the server allows beside it.
        // The tools that run GUFI show the commands they ran in their row,
        // and fold with the other finished calls.
        // A pw code session's running Bash step offers moving it to the
        // background (Ctrl+B); the hint draws nothing in other chats.
        slots: { composerUsage: <><NewChatModelSlot conversationId={activeId} hasSession={!!pw.link?.session} /><EffortPicker conversationId={activeId} /></>, partRenderers: { approval: ToolApproval }, toolRenderers: GUFI_TOOL_RENDERERS, toolFooters: PWCODE_TOOL_FOOTERS, toolPresentation: gufiToolPresentation },
        strings: {
          thinking: {
            thoughtFor: (duration: string) => `Worked for ${duration}`,
          },
          agentTranscript: {
            // A pw code chat answers a plan under the transcript
            // (pwcode/session/PlanAnswer.tsx); the Studio has no terminal.
            planPending: 'Plan waiting for your answer, below',
            approvalPending: 'Waiting for an answer',
            denyWithFeedback: 'No, and tell the assistant why',
            feedbackPlaceholder: 'Tell the assistant what to do instead…',
          },
          emptyState: {
            noProvidersTitle: 'A model is needed',
            noProvidersBody: held ? 'pw code has no model set. Choose one in the message box, or add one under Settings, Accounts.' : 'Sign in with ACTIVATE, or add an OpenAI-compatible endpoint such as a model server on this computer, under Settings, Accounts. Browsing, search, and adding material work without one.',
            addProvider: 'Open Accounts',
          },
          sidebar: {
            manageProviders: 'Model access',
          },
          input: {
            placeholderNoProviders: held ? 'Choose or add a model to start chatting' : 'Add a model under Settings, Accounts to start chatting',
          },
        },
      }}
    >
      <div className="chat-wrap">
        <div ref={canvasRef} className={`chat-canvas card${railOpen ? ' rail-open' : ''}`}>
          {credNote && (
            <div className="cred-banner">
              <Callout
                variant="bar"
                tone="warning"
                live
                action={/Model access/.test(credNote)
                  ? <Button variant="primary" size="sm" onClick={() => { location.hash = '#view=settings:access' }}>Open Model access</Button>
                  : <Button variant="primary" size="sm" onClick={() => { location.hash = '#view=settings:accounts' }}>Open Accounts</Button>}
                onDismiss={dismissNote}
              >
                {credNote}
              </Callout>
            </div>
          )}
          {!showAttachments && !showManage && !pw.shown && <PWCodeApprovals conversationId={activeId} />}
          {!showAttachments && !showManage && <PWCodeNotice conversationId={activeId} />}
          {!showAttachments && !showManage && <PWCodeModelNotice conversationId={activeId} />}
          {!showAttachments && !showManage && <NewChatTargetSlot conversationId={activeId} hasSession={!!pw.link?.session} persona={persona} canvas={canvasRef} />}
          <FilterReloader />
          <TransferNoteWatcher />
          <EngineNoteReader />
          {!pw.shown && <PWCodeWatcher />}
          <LeaveOtherChat conversationId={activeId} sessionShown={pw.shown} />
          <PwCodeOwnModel conversationId={activeId} fresh={pw.known && !pw.link?.session} />
          <RetryWithModel />
          <ModelShare />
          <StudioChatLayout
            sidebarMode={phone ? 'drawer' : 'inline'}
            sidebarTop={({ collapsed }) => (
              <>
                <ManageChatsRailItem
                  active={showManage}
                  collapsed={collapsed}
                  // Like the package's own rows, a pick closes the phone drawer.
                  onSelect={() => { setShowManage(true); setShowAttachments(false); setRailOpen(false) }}
                />
                {/* Above the list it filters, so nothing covers the
                    sidebar's collapse button at its foot; gone on the rail. */}
                {multiUser && sharedHistory && !showAttachments && !collapsed && (
                  <div className="chat-filter" title="Show every conversation on this deployment, or only yours">
                    <SegmentedControl
                      label="Conversations shown"
                      value={chatFilter}
                      onValueChange={changeChatFilter}
                      options={[{ value: 'mine', label: 'My chats' }, { value: 'all', label: 'All chats' }]}
                    />
                  </div>
                )}
              </>
            )}
          >
            {showManage ? (
              <ChatsManager
                activeId={activeId}
                onOpen={id => { setActiveId(id); setShowManage(false) }}
                onActiveDeleted={() => setActiveId(null)}
                onClose={() => setShowManage(false)}
              />
            ) : showAttachments ? <AttachmentManager /> : activeId ? (<>
              {/* The pw code chats opened last stay drawn, hidden, so going
                  back to one shows it as it was, and its feed picks up
                  after its last event; hidden, nothing of one runs. */}
              {keptSessions.map(k => (
                <Activity key={`${k.conversationId}/${k.machine}/${k.sessionId}`} mode={k.conversationId === activeId ? 'visible' : 'hidden'}>
                  <PWCodeSession conversationId={k.conversationId} machine={k.machine} sessionId={k.sessionId} username={cfg.user?.username ?? ''}
                    notes={<PWCodeToolsNote conversationId={k.conversationId} link={k.link} />} />
                </Activity>
              ))}
              {!(pw.shown && pw.link?.session) && <StudioCited conversationId={activeId} scope={chatScope}><ChatThread conversationId={activeId} find={active} /></StudioCited>}
            </>) : (
              // Read live, so a theme toggle swaps a light and a dark icon.
              <EmptyChatBrand cfg={cfg} dark={effectiveTheme === 'dark'}>
                <ChatEmptyState />
              </EmptyChatBrand>
            )}
          </StudioChatLayout>
          {activeId && !showAttachments && !showManage && <ConversationScrubber />}
          <SteerBridge />
          {/* A pw code session's composer has its own command menu, which
              this one would answer Enter for. */}
          {!showAttachments && !showManage && !pw.shown && <SlashPalette canvas={canvasRef} conversationId={activeId} persona={persona} machine={pw.machine} session={pw.link?.session} />}
          {!showAttachments && !showManage && <MentionPalette canvas={canvasRef} />}
          <MentionLinks canvas={canvasRef} libraryIds={libraryIds} />
          {!showAttachments && !showManage && <NextUp canvas={canvasRef} />}
          {voiceOpen && cfg.features?.voice?.url && <VoiceOverlay url={cfg.features.voice.url} onClose={() => setVoiceOpen(false)} />}
          <div className="chat-controls" hidden={showManage}>
            {cfg.features?.voice?.enabled && (
              <button className="scope-btn voice-btn" title="Talk with the assistant (feature preview)" onClick={() => setVoiceOpen(true)}>
                <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4"><rect x="5.5" y="1.5" width="5" height="8" rx="2.5"/><path d="M3 7.5a5 5 0 0 0 10 0M8 12.5v2M5.5 14.5h5"/></svg>
                <span className="persona-label">Voice</span>
              </button>
            )}
            <Popover
              open={personaOpen}
              onOpenChange={setPersonaOpen}
              title="Persona for this conversation"
              align="end"
              // A long list opens on its filter, which takes focus itself;
              // otherwise focus moves to the panel, which scrolls nothing.
              initialFocus={personaList.length > 8 ? 'none' : 'popup'}
              className="scope-menu persona-menu"
              trigger={
                <button
                  className={`scope-btn ${persona ? 'active' : ''}`}
                  title="The standing persona for this conversation, from the shared library"
                >
                  {persona
                    ? <PersonaIcon icon={personaList.find(p => p.name === persona)?.icon} name={persona} size={16} />
                    : <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4"><circle cx="8" cy="5.5" r="2.8"/><path d="M2.6 14c.6-3 2.8-4.5 5.4-4.5s4.8 1.5 5.4 4.5"/></svg>}
                  <span className="persona-label">{persona ? personaList.find(p => p.name === persona)?.name ?? persona : 'Persona'}</span>
                </button>
              }
            >
              <button className={`persona-row ${!persona ? 'on' : ''}`} onClick={() => { setPersonaState(null); setPersonaOpen(false) }}>
                <span className="persona-name">Default</span>
                <span className="muted">how this workspace normally answers</span>
              </button>
              {personaList.length > 8 && (
                <SearchInput
                  size="sm"
                  label={`Filter ${personaList.length} personas`}
                  value={personaFilter}
                  onValueChange={setPersonaFilter}
                  autoFocus
                />
              )}
              {personaList.length === 0 && (
                <EmptyState variant="bare" title="No personas yet. Add one in the Agents tab and it appears here." />
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
            </Popover>
            {(hasLibraries || vocab.length > 0) && (
            <Popover
              aria-label="Chat scope"
              align="end"
              initialFocus="popup"
              className="scope-menu chat-scope-menu"
              trigger={
                <button
                  className={`scope-btn ${scope.size || (hasLibraries && chatScope !== 'all') ? 'active' : ''}`}
                  title="What the assistant searches in this chat: which libraries, and which labels"
                >
                  <svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="1.4"><path d="M1.5 3h13l-5 5.5V13l-3 1.5V8.5z"/></svg>
                  <span className="persona-label">Scope{hasLibraries && <span className="scope-sub">{libLabel}</span>}</span>
                  {scope.size > 0 && <span className="scope-count">{scope.size}</span>}
                </button>
              }
            >
              {hasLibraries && (
                <>
                  <div className="scope-menu-head">Libraries</div>
                  <LibraryTicks className="scope-libs" libraries={libraries} value={chatScope} onChange={setChatScope} />
                </>
              )}
              {vocab.length > 0 && (
                <>
                  <div className="scope-menu-head">Labels</div>
                  {vocab.length > 8 && (
                    <SearchInput size="sm" label="Filter labels" value={scopeFilter} onValueChange={setScopeFilter} />
                  )}
                  <div className="scope-list">
                    {visibleVocab.map(v => (
                      <label key={v.tag} className="scope-item">
                        <Checkbox
                          checked={scope.has(v.tag)}
                          onCheckedChange={() => toggleScope(v.tag)}
                        />
                        <span className="scope-tag">{v.tag}</span>
                        <span className="scope-n">{v.count}</span>
                      </label>
                    ))}
                    {visibleVocab.length === 0 && <EmptyState variant="bare" title="No labels match." />}
                  </div>
                </>
              )}
              <div className="scope-menu-foot">
                {vocab.length > 0
                  ? <Button
                      variant="ghost"
                      size="sm"
                      disabled={!scope.size}
                      onClick={() => { setScope(new Set()); setLabelScope([]) }}
                    >Clear labels</Button>
                  : <span />}
                <PopoverClose render={<Button variant="secondary" size="sm" />}>Done</PopoverClose>
              </div>
            </Popover>
            )}
            {!showAttachments && <PWCodeEngine conversationId={activeId} />}
            <EngineMarks />
            {activeId && !showAttachments && <SaveChatButton conversationId={activeId} />}
            <PanelToggle className="chat-panel-toggle" />
          </div>
          {/* Managing chats hides the controls above; the panel toggle stays. */}
          {showManage && <div className="chat-controls"><PanelToggle className="chat-panel-toggle" /></div>}
        </div>
        <SideViewHost />
      </div>
    </ChatProvider>
  )
}

/** The library files a Studio assistant chat's replies name, as links: in
 *  the libraries it searches first. */
function StudioCited({ conversationId, scope, children }: { conversationId: string; scope: Scope; children: React.ReactNode }) {
  const { currentConversation, isStreaming } = useChat()
  const open = currentConversation?.id === conversationId
  return (
    <CitedPaths key={conversationId} where={open ? { libraries: scope === 'all' ? [] : scope } : null} busy={isStreaming} messages={open ? currentConversation?.messages : undefined}>
      {/* Until this chat is read, the thread holds the one open before it,
          which is not drawn here (styles.css). */}
      <div className="chat-own" data-opening={currentConversation && !open ? '' : undefined}>{children}</div>
    </CitedPaths>
  )
}

/**
 * Steering: a message sent while a reply is running waits in the chat's
 * queue until the reply ends. This hands it to the running reply as well,
 * which takes it in before its next step (POST /api/chat/steer); once the
 * server says it did (a steer event), it leaves the queue. A message the
 * reply does not reach, or one with attachments, goes out from the queue
 * after the reply as before.
 */
function SteerBridge() {
  const { queuedMessages, isStreaming, currentConversation, removeQueuedMessage } = useChat()
  const posted = useRef(new Set<string>())
  useEffect(() => {
    const on = (e: Event) => { const id = String((e as CustomEvent).detail ?? ''); if (id) removeQueuedMessage(id) }
    window.addEventListener('ade-steer-applied', on)
    return () => window.removeEventListener('ade-steer-applied', on)
  }, [removeQueuedMessage])
  const conversationId = currentConversation?.id
  useEffect(() => {
    if (!isStreaming || !conversationId) return
    for (const m of queuedMessages ?? []) {
      const text = typeof m.content === 'string' ? m.content.trim() : ''
      if (posted.current.has(m.id) || !text || (m.attachments?.length ?? 0) > 0) continue
      posted.current.add(m.id)
      void fetch('/api/chat/steer', {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ conversationId, id: m.id, text }),
      }).catch(() => { /* it stays queued and goes out after the reply */ })
    }
  }, [queuedMessages, isStreaming, conversationId])
  return null
}
