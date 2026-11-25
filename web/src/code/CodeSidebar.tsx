import { type ReactElement, useMemo, useState } from 'react'
import {
  foldedGroupsPreference,
  machinesById,
  type PwCodeMachine,
  
  type SessionSection,
  searchedSessions,
  sessionClock,
  sessionKey,
  sessionLabel,
  sessionRailLabel,
  sessionRowState,
  showsPinMark,
  useCompactAgo,
  useResumeClock,
  useSessionList,
} from '@parallelworks/code'
import {
  ConversationSidebar,
  type ConversationSidebarGroup,
  SidebarEmpty,
  SidebarRailItem,
  SidebarRow,
  SidebarRowBody,
  type SidebarRowShortcut,
  useRowDialogs,
} from '@parallelworks/ui/ai'
import { Button, LoadingRegion, type MenuEntry, menuDivider, SearchInput, Skeleton, statusTextClass, toast, useContextMenu, usePersistedState } from '@parallelworks/ui'
import { useTranslations } from 'use-intl'
import { describeError } from '../pwcode/PWCodeSession'
import { type PageSession, pwCodePage } from './api'
import { MachineIcon, PinnedMark, SessionMark, type Translate, useCodeGroups } from './parts'
import { rowShortcut, useCodeSessionActions } from './sessionMenu'

/**
 * The Code page's list of sessions, as PW Code on the web's sidebar lists
 * them (its AgentsSidebar): the sections @parallelworks/code makes (what
 * needs you, pinned, the reader's own groups, then by when, and archived
 * last), each row with its state, machine and pull requests, the row's
 * menu and keys, and a rail of live sessions when the list is folded away.
 * A row opens its session in the page's pane, beside the list.
 */

type SidebarGroup = ConversationSidebarGroup<PageSession> & { label: string }

const SIDEBAR_KEY = 'studio.code.sidebar'

export function CodeSidebar({ open, onOpen, onNewSession, delegated, onlyDelegated, onOnlyDelegated, inChat, extraMenu, collapsed, onToggle, drawer }: {
  /** The session that is open, which the list marks. */
  open: { machine?: string; sessionId?: string }
  onOpen: (s: Pick<PageSession, 'machineId' | 'id'> & { archivedAt?: string | null }) => void
  onNewSession: () => void
  /** Sessions a Studio chat delegated work to, by id: their rows say so. */
  delegated: ReadonlySet<string>
  /** Lists only those. */
  onlyDelegated: boolean
  onOnlyDelegated: (only: boolean) => void
  /** Sessions a Studio chat continues, by id. */
  inChat: ReadonlySet<string>
  /** The Studio's own entries, first in a row's menu. */
  extraMenu: (s: PageSession) => MenuEntry[]
  collapsed: boolean
  onToggle: () => void
  /** On a phone the list opens over the page. */
  drawer?: { open: boolean; onClose: () => void; label: string } | undefined
}) {
  const t = useTranslations('agents')
  const { openMenu, contextMenu } = useContextMenu()
  const { data } = pwCodePage.useSessions()
  const machines = useMemo(() => data?.machines ?? [], [data])
  const machineById = useMemo(() => machinesById(machines), [machines])
  const { actions, toMenu, dialogs } = useCodeSessionActions({ machineById, open, onOpen, showStart: onNewSession })
  const custom = useCodeGroups()
  const folded = usePersistedState<string[]>(`studio.code.${foldedGroupsPreference.key}`, foldedGroupsPreference.fallback)
  const resumeClock = useResumeClock()
  const { compact } = useCompactAgo()
  const [search, setSearch] = useState('')
  const list = useSessionList<HTMLLIElement>({
    api: pwCodePage,
    machineById,
    groups: custom,
    folded: folded as unknown as readonly [unknown, (update: (previous: unknown) => string[]) => void],
    open,
    actions,
    newSession: onNewSession,
    promptGroupName: group => groupDialogs.requestRename(group),
    confirmGroupDelete: group => groupDialogs.requestDelete(group, null),
    onBlocked: reason => toast.error(reason),
  })
  const { groupNameWords: named, groupDeleteWords: deleted } = list
  const groupDialogs = useRowDialogs<{ id: string; name: string }>({
    strings: {
      deleteTitle: deleted.title,
      deleteBody: g => deleted.description(g as never),
      deleteAction: deleted.confirmLabel,
      renameTitle: named.title,
      renameLabel: named.label,
      renamePlaceholder: named.placeholder,
      renameAction: named.confirmLabel,
    },
    nameOf: g => g.name,
    onDelete: g => { list.deleteGroup(g as never); return true },
    onRename: (g, name) => list.renameGroup(g as never, name),
  })

  const toGroup = (section: SessionSection<PageSession>): SidebarGroup => ({
    key: section.key,
    label: section.label,
    ...(section.tone ? { tone: section.tone } : {}),
    items: section.sessions,
  })
  const query = search.trim()
  const delegatedHere = list.sessions.filter(s => delegated.has(s.id))
  const groups: SidebarGroup[] = onlyDelegated
    ? [{ key: 'delegated', label: 'Delegated by a chat', items: query ? searchedSessions(delegatedHere, query) : delegatedHere }].filter(g => g.items.length > 0)
    : query
      ? [{ key: 'search', label: t('sidebar.search'), items: searchedSessions(list.sessions, query) }].filter(g => g.items.length > 0)
      : list.sections.map(toGroup)
  const groupMenus = new Map(list.sections.filter(s => s.group).map(s => [s.key, toMenu(list.groupMenu(s.group!))]))

  return (
    <div className="code-sidebar" onContextMenu={e => {
      const heading = (e.target as Element).closest('h4')
      const key = heading?.querySelector('[data-group-key]')?.getAttribute('data-group-key')
      if (key && groupMenus.has(key)) openMenu(e, groupMenus.get(key)!)
      else if (!(e.target as Element).closest('a, button, input, [data-sidebar-row]')) openMenu(e, toMenu(list.listMenu))
    }}>
      <ConversationSidebar<PageSession>
        label={t('sidebar.label')}
        collapsed={collapsed}
        onToggle={onToggle}
        toggleLabels={{ open: t('sidebar.open'), close: t('sidebar.close') }}
        resize={{ storageKey: SIDEBAR_KEY, label: t('sidebar.resize'), hint: t('sidebar.resizeHint') }}
        drawer={drawer}
        top={!collapsed && (list.searchable || delegatedHere.length > 0 || onlyDelegated) && (
          <div className="code-sidebar-search">
            {list.searchable && <SearchInput value={search} onValueChange={setSearch} label={t('sidebar.search')} placeholder={t('sidebar.search')} />}
            {(delegatedHere.length > 0 || onlyDelegated) && (
              <Button variant={onlyDelegated ? 'primary' : 'secondary'} size="sm" aria-pressed={onlyDelegated} data-testid="code-only-delegated"
                title="Work a chat handed to pw code, in a library folder" onClick={() => onOnlyDelegated(!onlyDelegated)}>
                Delegated ({delegatedHere.length})
              </Button>
            )}
          </div>
        )}
        groups={groups.map(g => ({ ...g, label: <span data-group-key={g.key}>{g.label}</span> }))}
        groupsStorageKey={`studio.code.${foldedGroupsPreference.key}`}
        groupTestId="agent-session-group"
        getKey={sessionKey}
        placeholder={list.loading ? <SessionsLoading t={t} /> : (
          <div data-list-placeholder>
            <SidebarEmpty title={query || onlyDelegated ? 'No sessions match' : t('sidebar.noSessions')} error={list.loadError ? describeError(list.loadError) : undefined} />
          </div>
        )}
        cycle={{ current: list.openSession, onSelect: onOpen }}
        renderRow={s => (
          <SessionRow
            session={s}
            machine={machineById.get(s.machineId)}
            showMachine={list.multiMachine}
            selected={s === list.openSession}
            updated={sessionClock(s, compact)}
            delegated={delegated.has(s.id)}
            inChat={inChat.has(s.id)}
            menuItems={row => [...extraMenu(s), menuDivider, ...toMenu(list.menu(s, row))]}
            shortcuts={list.shortcuts(s).map(rowShortcut)}
            openMenu={openMenu}
            onOpen={() => onOpen(s)}
            resumeClock={resumeClock}
            t={t}
          />
        )}
        rail={list.rail.map(s => (
          <SidebarRailItem key={sessionKey(s)} testId="agent-rail-dot" label={sessionRailLabel(s, machineById.get(s.machineId), t, resumeClock)}
            selected={s === list.openSession} onSelect={() => onOpen(s)}>
            <SessionMark session={s} machine={machineById.get(s.machineId)} size="rail" />
          </SidebarRailItem>
        ))}
      >
        {contextMenu}
        {dialogs}
        {groupDialogs.dialogs}
      </ConversationSidebar>
    </div>
  )
}

const ROW_SKETCH = ['64%', '82%', '48%', '72%', '56%', '38%']

function SessionsLoading({ t }: { t: Translate }) {
  return (
    <div data-list-placeholder>
      <LoadingRegion label={t('sidebar.loading')} className="code-sidebar-loading">
        {ROW_SKETCH.map(width => (
          <span key={width} className="code-sidebar-sketch">
            <Skeleton width={width} height={12} />
            <Skeleton width={20} height={10} />
          </span>
        ))}
      </LoadingRegion>
    </div>
  )
}

function SessionRow({ session, machine, showMachine, selected, updated, delegated, inChat, menuItems, shortcuts, openMenu, onOpen, resumeClock, t }: {
  session: PageSession
  machine: PwCodeMachine | undefined
  showMachine: boolean
  selected: boolean
  updated: string
  delegated: boolean
  inChat: boolean
  menuItems: (row: HTMLLIElement) => MenuEntry[]
  shortcuts: SidebarRowShortcut[]
  openMenu: ReturnType<typeof useContextMenu>['openMenu']
  onOpen: () => void
  resumeClock: (at: string) => string
  t: Translate
}) {
  const { stateText, tone, showSecondLine, live } = sessionRowState(session, machine, t, resumeClock)
  const marked = showsPinMark(session)
  const prs = (session.pullRequests ?? []).filter(Boolean)
  const parts = [
    showMachine && machine && <span key="machine" className="code-row-machine" title={machine.name}><MachineIcon machine={machine} />{machine.name}</span>,
    showSecondLine && <span key="state" className={statusTextClass(tone)}>{stateText}</span>,
    inChat && <span key="chat" className="code-row-chat" title="A Studio chat continues this session">in a chat</span>,
    delegated && <span key="delegated" className="code-row-delegated" title="A Studio chat delegated work to this session">delegated</span>,
    prs.length > 0 && <span key="prs" className="code-row-prs">{prs.map((u: string) => u.replace(/^https?:\/\/(www\.)?github\.com\/[^/]+\//, '').replace('/pull/', '#')).join(', ')}</span>,
  ].filter((p): p is ReactElement => !!p)
  return (
    <SidebarRow selected={selected} menuItems={menuItems} shortcuts={shortcuts} openMenu={openMenu}>
      <button type="button" data-testid="agent-session-row" aria-current={selected ? 'page' : undefined} className="code-session-row" onClick={onOpen}>
        <SidebarRowBody
          leading={(live || marked) && (
            <span className="code-row-leading">
              {live && <span data-testid="agent-session-dot" className="code-row-dot"><SessionMark session={session} machine={machine} size="row" label={showSecondLine ? undefined : stateText} /></span>}
              {marked && <PinnedMark t={t} />}
            </span>
          )}
          title={sessionLabel(session, t)}
          time={updated}
          detail={parts.length > 0 && <span className="code-row-detail">{parts.flatMap((p, i) => (i ? [<span key={`dot${i}`} aria-hidden="true" className="code-row-sep">·</span>, p] : [p]))}</span>}
        />
      </button>
    </SidebarRow>
  )
}
