import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode, type SyntheticEvent } from 'react'
import { isArchived, isMachineDown, machinesById, type PwCodeMachine, type PwCodeSession, sessionRowState } from '@parallelworks/code'
import { type BadgeVariant, isPlainClick, RelativeTime, RunStatusIcon, SearchInput, StatusDot, statusTextClass, Tooltip, type MenuEntry, type useContextMenu } from '@parallelworks/ui'
import { ImageIcon, NewChatIcon, SearchIcon, UsersIcon } from '@parallelworks/ui/icons'
import {
  ConversationLayout, ConversationSidebar, FOCUS_SIDEBAR_SEARCH_EVENT, groupConversationsByDate, KeyboardShortcutsProvider, ManageProvidersLink,
  SidebarEmpty, SidebarRailItem, SidebarRow, SidebarRowBody, type SidebarMode, useChat, useChatConfig, useConversationActions,
} from '@parallelworks/ui/ai'
import { prefetchChat } from '../chatRecord'
import { SessionMark, type Translate } from '../code/parts'
import { type ChatNotice, useNotices } from '../notices'
import { usePwCodeWords } from '../pwcode/PWCodeSession'

/**
 * The chat page's list of chats: the package's own (ChatSidebar), with each
 * chat's state marked as the Code page's list marks its sessions, and the
 * chats that are working or wait on the person first, under Active. Folded,
 * it is a rail with the marks of those, of the live ones and of the ones with
 * something new. The states are the notices', which the menu's marks and the
 * Code page's count read too.
 */

type Conversation = ReturnType<typeof useChat>['conversations'][number]

/** What a chat's row says of it. */
export interface ChatRowState {
  /** Listed under Active, and what comes first there. */
  active: boolean
  waiting: boolean
  /** Its session is live, as the Code page's rail has it. */
  live?: boolean
  unread?: 'reply' | 'note'
  /** The words beside its mark, shown where they say more than the mark. */
  text: string
  detail: boolean
  tone: BadgeVariant
  mark: (size: 'row' | 'rail') => ReactNode
}

const stateName = (s: ChatRowState) => s.waiting ? 'waiting' : s.active ? 'working' : s.unread ?? 'idle'

const size = (s: 'row' | 'rail') => (s === 'row' ? 6 : 10)

function dot(variant: BadgeVariant, label: string, pulse = false) {
  return (s: 'row' | 'rail') => (
    <span className={`code-mark code-mark-${s}`} {...(s === 'row' ? { role: 'img', 'aria-label': label, title: label } : {})}>
      <StatusDot variant={variant} size={size(s)} pulse={pulse} />
    </span>
  )
}

/**
 * The state of a chat's row: of a pw code chat, its session's, in the mark
 * and words the Code page gives it; of a Studio assistant chat, whether a
 * reply is being written, or a call waits for the person. Either way a
 * reply they have not seen is said once nothing else is going on.
 */
export function chatRowState(n: ChatNotice | undefined, session: PwCodeSession | undefined, machine: PwCodeMachine | undefined, listed: boolean, t: Translate | null): ChatRowState | null {
  if (!n) return null
  const unread = n.unread
  const said = unread === 'note' ? 'New note' : 'New reply'
  const open = session && !isArchived(session) ? session : undefined
  if (open && t && !isMachineDown(machine)) {
    // What the Studio knows first: a reply being written here, or a request
    // the listing is from before.
    const status = n.waiting ? 'awaiting_approval' : n.working ? 'running' : open.status
    const now = status === open.status ? open : { ...open, status } as PwCodeSession
    const row = sessionRowState(now, machine, t, at => new Date(at).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' }))
    const active = !!n.waiting || status === 'running' || status === 'awaiting_approval'
    const text = n.waiting ? 'Waiting on you' : unread && !active ? said : row.stateText
    return {
      active,
      waiting: !!n.waiting,
      live: row.live,
      unread: active ? undefined : unread,
      text,
      detail: !!n.waiting || (!!unread && !active) || row.showSecondLine,
      tone: n.waiting ? 'warning' : unread && !active ? 'info' : row.tone,
      mark: unread && !active ? dot('info', said) : s => <SessionMark session={now} machine={machine} size={s} label={s === 'row' ? text : undefined} />,
    }
  }
  if (n.waiting) return { active: true, waiting: true, text: 'Waiting on you', detail: true, tone: 'warning', mark: dot('warning', 'Waiting on you', true) }
  if (n.working) {
    return {
      active: true, waiting: false, text: 'Working', detail: false, tone: 'default',
      mark: s => <span className={`code-mark code-mark-${s}`}><RunStatusIcon data-testid="chat-working" status="running" label={s === 'row' ? 'Working' : ''} /></span>,
    }
  }
  if (unread) return { active: false, waiting: false, unread, text: said, detail: true, tone: 'info', mark: dot('info', said) }
  // A pw code chat whose session is over, or not to be reached now.
  if (n.session && (session ? t : listed)) {
    const text = session && isArchived(session) ? 'Session ended' : session ? 'Its machine is not connected' : 'Its session is not listed now'
    return { active: false, waiting: false, text, detail: false, tone: 'default', mark: dot('default', text) }
  }
  return null
}

/** A row's menu for who may only read its chat: no rename, no delete. */
export function readerItems(c: { isOwner?: boolean }, items: MenuEntry[]): MenuEntry[] {
  return c.isOwner === false ? items.filter(i => 'label' in i && i.label === 'Download') : items
}

function ChatRow({ conv, state, marks, current, menuItems, openMenu, onOpen }: {
  conv: Conversation
  state: ChatRowState | null
  /** The list has marks, so a chat without one keeps their place. */
  marks: boolean
  current: boolean
  menuItems: (row: HTMLLIElement) => MenuEntry[]
  openMenu: ReturnType<typeof useContextMenu>['openMenu']
  onOpen: () => void
}) {
  const { LinkComponent, strings } = useChatConfig()
  return (
    <SidebarRow selected={current} menuItems={menuItems} openMenu={openMenu}>
      <LinkComponent target={{ kind: 'conversation', id: conv.id }} className="flex min-w-0 flex-1 py-1 text-left"
        onClick={e => { if (isPlainClick(e)) onOpen() }}>
        <SidebarRowBody
          leading={state
            ? <span className="code-row-leading" data-testid="chat-row-mark" data-state={stateName(state)}>{state.mark('row')}</span>
            // Someone else's chat, shared: it has no state here.
            : conv.isOwner === false ? <span className="code-row-leading"><UsersIcon className="h-3 w-3 theme-muted-text" title={strings.sidebar.sharedWithYou} /></span>
              : marks && <span className="code-row-leading"><span className="code-mark code-mark-row" /></span>}
          title={state?.unread || state?.waiting ? <b className="chat-row-new">{conv.title || strings.sidebar.untitled}</b> : conv.title || strings.sidebar.untitled}
          time={<RelativeTime value={conv.updatedAt ?? conv.createdAt} format="compact" />}
          detail={state?.detail && <span className="code-row-detail"><span className={statusTextClass(state.tone)}>{state.text}</span></span>}
        />
      </LinkComponent>
    </SidebarRow>
  )
}

/** Moves between the rail's marks, as the list's rows do. */
function railKeys(e: KeyboardEvent<HTMLUListElement>) {
  if (e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) return
  const marks = [...e.currentTarget.querySelectorAll('button')]
  const at = marks.indexOf((e.target as Element).closest('button')!)
  const to = e.key === 'ArrowDown' ? at + 1 : e.key === 'ArrowUp' ? at - 1 : e.key === 'Home' ? 0 : e.key === 'End' ? marks.length - 1 : null
  if (at < 0 || to === null || !marks[to]) return
  e.preventDefault()
  marks[to].focus()
}

/** A chat the pointer or the focus rests on is read ahead, as a row's is. */
function railAhead(e: SyntheticEvent<HTMLUListElement>) {
  const id = (e.target as Element).closest('button')?.querySelector<HTMLElement>('[data-chat]')?.dataset.chat
  if (id) prefetchChat(id)
}

/**
 * The folded list: a mark for each chat that works, waits, is live or has
 * something new, and for the open one, which stays when it is read.
 */
function ChatRail({ chats, states, current, untitled, onOpen }: {
  chats: Conversation[]
  states: ReadonlyMap<string, ChatRowState>
  current: string | null
  untitled: string
  onOpen: (id: string) => void
}) {
  return (
    <li>
      <ul className="flex flex-col items-center gap-1" onKeyDown={railKeys} onPointerOver={railAhead} onFocus={railAhead}>
        {chats.map(c => {
          const state = states.get(c.id)
          const title = c.title || untitled
          return (
            <SidebarRailItem key={c.id} testId="chat-rail-mark" label={state ? `${title}, ${state.text}` : title} selected={c.id === current} onSelect={() => onOpen(c.id)}>
              <span className="flex" data-chat={c.id} data-state={state ? stateName(state) : 'open'}>
                {state ? state.mark('rail') : <span className="code-mark code-mark-rail"><StatusDot off size={10} /></span>}
              </span>
            </SidebarRailItem>
          )
        })}
      </ul>
    </li>
  )
}

function ChatSidebar({ top }: { top: (state: { collapsed: boolean }) => ReactNode }) {
  const { LinkComponent, strings } = useChatConfig()
  const words = strings.sidebar
  const {
    adapter, navigation, activeConversationId, conversations, sidebar, sidebarPresentation, drawerOpen, setSidebar, setDrawerOpen,
    toggleSidebar, isLoading, loadConversations, clearCurrentConversation,
  } = useChat()
  const { menuItems: allItems, openMenu, overlays } = useConversationActions()
  // Another person's chat is read and downloaded, and nothing else.
  const menuItems = (c: Conversation, row: HTMLLIElement) => readerItems(c, allItems(c, row))
  const drawer = sidebarPresentation === 'drawer'
  const collapsed = !drawer && sidebar !== 'expanded'
  const closeDrawer = () => setDrawerOpen(false)
  const [search, setSearch] = useState('')
  const searchBox = useRef<HTMLInputElement>(null)
  const reveal = useRef(() => {})
  reveal.current = () => {
    if (drawer) setDrawerOpen(true)
    else if (sidebar !== 'expanded') setSidebar('expanded')
  }
  useEffect(() => { void loadConversations() }, [loadConversations])
  useEffect(() => {
    const focusSearch = () => {
      reveal.current()
      requestAnimationFrame(() => searchBox.current?.focus())
    }
    document.addEventListener(FOCUS_SIDEBAR_SEARCH_EVENT, focusSearch)
    return () => document.removeEventListener(FOCUS_SIDEBAR_SEARCH_EVENT, focusSearch)
  }, [])

  const t = usePwCodeWords()
  const notices = useNotices()
  const states = useMemo(() => {
    const machines = machinesById(notices.machines)
    const sessions = new Map(notices.sessions.map(s => [s.id, s]))
    const out = new Map<string, ChatRowState>()
    for (const [id, n] of Object.entries(notices.chats)) {
      const session = n.row ?? (n.session ? sessions.get(n.session) : undefined)
      const s = chatRowState(n, session, session && machines.get(session.machineId), notices.listed, t)
      if (s) out.set(id, s)
    }
    return out
  }, [notices, t])

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase()
    return q ? conversations.filter(c => (c.title || words.untitled).toLowerCase().includes(q)) : conversations
  }, [conversations, search, words.untitled])
  // Folded, nothing is searched: what was typed does not hide a mark.
  const listed = collapsed ? conversations : filtered
  const groups = useMemo(() => {
    const at = (c: Conversation) => Date.parse(c.updatedAt ?? c.createdAt) || 0
    const active = listed.filter(c => states.get(c.id)?.active)
      .sort((a, b) => Number(states.get(b.id)!.waiting) - Number(states.get(a.id)!.waiting) || at(b) - at(a))
    return [
      ...(active.length ? [{ key: 'active', label: 'Active', ...(active.some(c => states.get(c.id)!.waiting) ? { tone: 'attention' as const } : {}), items: active }] : []),
      ...groupConversationsByDate(listed.filter(c => !states.get(c.id)?.active)).map(({ key, items }) => ({ key, label: words[key], items })),
    ]
  }, [listed, states, words])
  const onRail = useMemo(() => groups.flatMap(g => g.items).filter(c => {
    const s = states.get(c.id)
    return c.id === activeConversationId || (s && (s.active || s.live || s.unread))
  }), [groups, states, activeConversationId])

  const link = 'flex items-center h-9 px-2 gap-3 rounded-lg transition-colors hover:chat-tint'
  const label = `text-sm transition-opacity duration-200 ${collapsed ? 'opacity-0 w-0 overflow-hidden' : 'opacity-100'}`
  return (
    <>
      {(drawer || sidebar !== 'hidden') && (
        <ConversationSidebar<Conversation>
          collapsed={collapsed}
          drawer={drawer ? { open: drawerOpen, onClose: closeDrawer, label: words.drawerLabel } : undefined}
          onToggle={toggleSidebar}
          toggleLabels={{ open: words.openSidebar, close: words.closeSidebar }}
          resize={{ label: words.resizeLabel, hint: words.resizeHint }}
          groups={groups}
          groupTestId="chat-list-group"
          getKey={c => c.id}
          renderRow={c => (
            <ChatRow conv={c} state={states.get(c.id) ?? null} marks={states.size > 0} current={activeConversationId === c.id}
              menuItems={row => menuItems(c, row)} openMenu={openMenu} onOpen={closeDrawer} />
          )}
          loading={isLoading}
          placeholder={<SidebarEmpty title={conversations.length === 0 ? words.noConversations : strings.chrome.noMatches} />}
          cycle={{ current: listed.find(c => c.id === activeConversationId), onSelect: c => navigation.toConversation(c.id) }}
          rail={<ChatRail chats={onRail} states={states} current={activeConversationId} untitled={words.untitled} onOpen={id => navigation.toConversation(id)} />}
          top={<>
            {top({ collapsed })}
            <div className="flex flex-col gap-0.5 px-2 pt-2 pb-1">
              <LinkComponent target={{ kind: 'newChat' }} className={`${link} font-medium`} title={words.newChat}
                onClick={e => { if (isPlainClick(e)) { closeDrawer(); clearCurrentConversation() } }}>
                <NewChatIcon className="flex-shrink-0 w-4 h-4" />
                <span className={label}>{words.newChat}</span>
              </LinkComponent>
              {!!adapter.attachments && (
                <LinkComponent target={{ kind: 'attachments' }} className={link} title={words.attachments} onClick={closeDrawer}>
                  <ImageIcon className="flex-shrink-0 w-4 h-4" />
                  <span className={label}>{words.attachments}</span>
                </LinkComponent>
              )}
              {collapsed && conversations.length > 0 && (
                <Tooltip content={strings.chrome.searchLabel} side="right">
                  <button type="button" className={link} aria-label={strings.chrome.searchLabel} onClick={() => document.dispatchEvent(new Event(FOCUS_SIDEBAR_SEARCH_EVENT))}>
                    <SearchIcon className="flex-shrink-0 w-4 h-4" />
                  </button>
                </Tooltip>
              )}
            </div>
            {!collapsed && conversations.length > 0 && (
              <div className="px-2 pb-2">
                <SearchInput ref={searchBox} value={search} onValueChange={setSearch} label={strings.chrome.searchLabel} placeholder={strings.chrome.searchPlaceholder} />
              </div>
            )}
          </>}
          footer={<ManageProvidersLink />}
        />
      )}
      {overlays}
    </>
  )
}

/** The chat page's layout, with the Studio's list of chats. */
export function StudioChatLayout({ sidebarMode, sidebarTop, children }: { sidebarMode: SidebarMode; sidebarTop: (state: { collapsed: boolean }) => ReactNode; children: ReactNode }) {
  return (
    <KeyboardShortcutsProvider>
      <ConversationLayout sidebar={<ChatSidebar top={sidebarTop} />} sidebarMode={sidebarMode}>{children}</ConversationLayout>
    </KeyboardShortcutsProvider>
  )
}
