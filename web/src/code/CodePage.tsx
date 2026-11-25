import { useCallback, useEffect, useMemo, useState } from 'react'
import { SWRConfig } from 'swr'
import { IntlProvider } from 'use-intl'
import { loadPwCodeMessages, type PwCodeMessages,  } from '@parallelworks/code'
import { Button, type MenuEntry, Popover } from '@parallelworks/ui'
import { ChatProvider } from '@parallelworks/ui/ai'
import { createOpenAIChatAdapter } from '@parallelworks/ui/ai/openai'
import { AddIcon, ExternalLinkIcon, HelpIcon, SidebarOpenIcon } from '@parallelworks/ui/icons'
import { NeedsActivate } from '../components/NeedsActivate'
import { useAppConfig } from '../config'
import { type PageSession, pwCodePage } from './api'
import { CodeSidebar } from './CodeSidebar'
import { CodeStart } from './CodeStart'
import { LinkedSession, type Delegated } from './LinkedSession'
import { sessionInHash, useOpenInStudio, sameMachine } from './openInStudio'
import { MachineToolsOffer } from './MachineToolsOffer'
import { HowSessionsAppear } from './parts'

/**
 * The Code page: the person's pw code sessions as PW Code on the web shows
 * them, built from the same packages (@parallelworks/code for what is
 * listed, started and changed, @parallelworks/ui/ai for how it is drawn).
 * The list of sessions by machine sits beside the start of a new one;
 * opening a session continues it in a Studio chat, which shows it as PW
 * Code on the web's session page does.
 */
export function CodePage() {
  const [messages, setMessages] = useState<PwCodeMessages | null>(null)
  useEffect(() => {
    let gone = false
    void loadPwCodeMessages('en').then(m => { if (!gone) setMessages(m) }, () => {})
    return () => { gone = true }
  }, [])
  if (!messages) return <div className="code-page code-page-loading" aria-busy="true" />
  return (
    <IntlProvider locale="en" messages={messages} timeZone={Intl.DateTimeFormat().resolvedOptions().timeZone}>
      {/* Its own cache: the page's client and a chat's reach the same paths through different routes. */}
      <SWRConfig value={{ provider: () => new Map() }}>
        <CodeChat />
      </SWRConfig>
    </IntlProvider>
  )
}

/** The model picker reads the platform's models from a chat's context. */
function CodeChat() {
  const cfg = useAppConfig()
  const adapter = useMemo(() => createOpenAIChatAdapter({ baseUrl: '/api/code-page/openai', storageKey: 'studio.code.models' }), [])
  const navigation = useMemo(() => ({ toConversation: () => {}, toNewChat: () => {} }), [])
  return (
    <ChatProvider adapter={adapter} currentUser={cfg.user ?? { username: '' }} navigation={navigation}>
      <CodeScreen />
    </ChatProvider>
  )
}

/** A phone's width, where the list opens over the page. */
function useNarrow(): boolean {
  const query = '(max-width: 720px)'
  const [narrow, setNarrow] = useState(() => typeof window !== 'undefined' && window.matchMedia?.(query).matches === true)
  useEffect(() => {
    const m = window.matchMedia?.(query)
    if (!m) return
    const on = () => setNarrow(m.matches)
    m.addEventListener('change', on)
    return () => m.removeEventListener('change', on)
  }, [])
  return narrow
}

interface Where { local: boolean; codeUrl: string; signIn?: boolean }

function useJSON<T>(url: string, every = 0): T | null {
  const [value, setValue] = useState<T | null>(null)
  useEffect(() => {
    let gone = false
    const load = () => fetch(url).then(r => (r.ok ? r.json() : null)).then(v => { if (!gone && v) setValue(v as T) }, () => {})
    void load()
    const t = every ? setInterval(() => { if (document.visibilityState === 'visible') void load() }, every) : undefined
    return () => { gone = true; if (t) clearInterval(t) }
  }, [url, every])
  return value
}

function CodeScreen() {
  const { data } = pwCodePage.useSessions()
  const machines = useMemo(() => data?.machines ?? [], [data])
  const studio = useOpenInStudio(machines)
  const where = useJSON<Where>('/api/code-page/where')
  const delegatedList = useJSON<{ sessions?: Delegated[] }>('/api/delegated', 30_000)
  const delegatedById = useMemo(() => new Map((delegatedList?.sessions ?? []).map(d => [d.session, d])), [delegatedList])
  const delegated = useMemo(() => new Set(delegatedById.keys()), [delegatedById])
  const [onlyDelegated, setOnlyDelegated] = useState(false)
  // A link to the Code page that names a session: Fleet, Agents, the status panel, a chat's note.
  const [link, setLink] = useState(() => (typeof location === 'undefined' ? null : sessionInHash(location.hash)))
  useEffect(() => {
    const read = () => setLink(sessionInHash(location.hash))
    window.addEventListener('hashchange', read)
    return () => window.removeEventListener('hashchange', read)
  }, [])
  const sessions = useMemo(() => data?.sessions ?? [], [data])
  const linked = link ? sessions.find(s => s.id === link.session && (!link.machine || s.machineId === link.machine)) : undefined
  const closeLink = useCallback(() => {
    setLink(null)
    if (sessionInHash(location.hash)) history.replaceState(null, '', '#view=code')
  }, [])
  const inChat = useMemo(() => new Set((studio.here?.chats ?? []).map(c => c.session)), [studio.here])
  const [opened, setOpened] = useState<{ machine?: string; sessionId?: string }>(() => (link ? { machine: link.machine, sessionId: link.session } : {}))
  const [collapsed, setCollapsed] = useState(false)
  const narrow = useNarrow()
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [startKey, setStartKey] = useState(0)
  // The machine chosen to start a session on: the tools are offered there only.
  const [startMachine, setStartMachine] = useState<string | undefined>()
  const [startMode, setStartMode] = useState<string | undefined>()
  const startOn = machines.find(m => m.id === startMachine)
  const startsElsewhere = !!startOn && !!studio.here && !sameMachine(startOn, studio.here)

  const open = useCallback((s: Pick<PageSession, 'machineId' | 'id'> & { archivedAt?: string | null }) => {
    setOpened({ machine: s.machineId, sessionId: s.id })
    setDrawerOpen(false)
    // One that cannot be continued here shows why, with what still works.
    void studio.open(s).then(ok => { if (!ok) setLink({ machine: s.machineId, session: s.id }) })
  }, [studio])
  const newSession = useCallback(() => { setOpened({}); closeLink(); setStartKey(k => k + 1) }, [closeLink])
  const onStarted = useCallback((machine: string, id: string) => {
    setOpened({ machine, sessionId: id })
    void studio.open({ machineId: machine, id }, { started: true })
  }, [studio])

  const sessionUrl = (s: PageSession) => (where?.codeUrl ? `${where.codeUrl}/${encodeURIComponent(s.machineId)}/${encodeURIComponent(s.id)}` : '')
  const extraMenu = (s: PageSession): MenuEntry[] => {
    const why = studio.why(s)
    const url = sessionUrl(s)
    return [
      { label: inChat.has(s.id) ? 'Open its Studio chat' : 'Continue in Studio', onClick: () => open(s), ...(why ? { disabledReason: why } : {}) },
      ...(url ? [{ label: 'Open in ACTIVATE', icon: <ExternalLinkIcon />, onClick: () => { window.open(url, '_blank', 'noopener,noreferrer') } }] : []),
    ]
  }

  if (where?.signIn) return <SignInNeeded />
  return (
    <div className="code-page">
      <CodeSidebar
        open={opened}
        onOpen={open}
        onNewSession={newSession}
        delegated={delegated}
        onlyDelegated={onlyDelegated}
        onOnlyDelegated={setOnlyDelegated}
        inChat={inChat}
        extraMenu={extraMenu}
        collapsed={narrow ? false : collapsed}
        onToggle={() => (narrow ? setDrawerOpen(o => !o) : setCollapsed(c => !c))}
        drawer={narrow ? { open: drawerOpen, onClose: () => setDrawerOpen(false), label: 'Sessions' } : undefined}
      />
      <main className="code-main">
        <div className="code-main-actions">
          {narrow && <Button variant="secondary" size="sm" icon={<SidebarOpenIcon aria-hidden />} data-testid="code-sessions-drawer" onClick={() => setDrawerOpen(true)}>Sessions</Button>}
          <Button variant="secondary" size="sm" icon={<AddIcon aria-hidden />} data-testid="agents-new-session" onClick={newSession}>New session</Button>
          {where?.codeUrl && (
            <Button variant="ghost" size="sm" icon={<ExternalLinkIcon aria-hidden />} href={where.codeUrl} target="_blank" rel="noopener noreferrer">Open in ACTIVATE</Button>
          )}
          <Popover
            align="end"
            aria-label="How sessions appear"
            className="code-how-popover"
            trigger={<Button variant="ghost" size="sm" icon={<HelpIcon aria-hidden />} aria-label="How sessions appear" data-testid="agents-how-button" />}
          >
            <HowSessionsAppear compact />
          </Popover>
        </div>
        {/* A session on the Studio's own machine starts with its tools; on
            another, the start says when it would not. */}
        {startsElsewhere
          ? <MachineToolsOffer key={startMachine} machines={machines} selected={startMachine} always mode={startMode} />
          : <MachineToolsOffer machines={machines} selected={startMachine} />}
        <CodeStart key={startKey} onOpen={open} onStarted={onStarted} onMachine={setStartMachine} onMode={setStartMode} top={link ? answer => (
          <LinkedSession
            session={linked}
            machine={linked ? machines.find(m => m.id === linked.machineId) : undefined}
            missing={!!data && !linked}
            delegated={linked ? delegatedById.get(linked.id) : undefined}
            why={linked ? studio.why(linked) : ''}
            inChat={!!linked && inChat.has(linked.id)}
            url={linked ? sessionUrl(linked) : ''}
            onAnswer={answer}
            onOpen={() => linked && open(linked)}
            onClose={closeLink}
          />
        ) : undefined} />
      </main>
      {studio.dialog}
    </div>
  )
}

/** Where the person has no ACTIVATE sign-in of their own: the Code page
 *  lists, starts and changes sessions only as them, so it asks them to
 *  sign in under Settings, Accounts, as every ACTIVATE feature does. */
function SignInNeeded() {
  return (
    <div className="code-page code-sign-in" data-testid="code-sign-in">
      <NeedsActivate>The Code page lists, starts and changes your pw code sessions as you, with your own ACTIVATE sign-in.</NeedsActivate>
    </div>
  )
}
