import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { SWRConfig } from 'swr'
import { IntlProvider } from 'use-intl'
import { loadPwCodeMessages, type PwCodeMessages,  } from '@parallelworks/code'
import { Button, IconButton, type MenuEntry, Popover, toast } from '@parallelworks/ui'
import { ChatProvider } from '@parallelworks/ui/ai'
import { createOpenAIChatAdapter } from '@parallelworks/ui/ai/openai'
import { AddIcon, ExternalLinkIcon, HelpIcon, SidebarOpenIcon } from '@parallelworks/ui/icons'
import { ChatMarkdownLink } from '../chatLinks'
import { EFFORT_WORDS } from '../labelScope'
import { citedMarkdown } from '../citedPaths'
import { NeedsActivate } from '../components/NeedsActivate'
import { useAppConfig } from '../config'
import { useWatch } from '../live'
import { PageHeadActions } from '../pageHead'
import { publishListing, sessionSeen, setFrontChat } from '../notices'
import { knowSession, type PageSession, pwCodePage, startedSession } from './api'
import { CodeSidebar } from './CodeSidebar'
import { CodeSession, StartingSession } from './CodeSession'
import { CodeStart, type Starting } from './CodeStart'
import { LinkedSession, type Delegated } from './LinkedSession'
import { type MachineRef, paneFor, type SessionRef, sessionHash, sessionInHash, useOpenInStudio, sameMachine } from './openInStudio'
import { useMachineTools } from './machineTools'
import { HowSessionsAppear } from './parts'

/**
 * The Code page: the person's pw code sessions as PW Code on the web shows
 * them, built from the same packages (@parallelworks/code for what is
 * listed, started and changed, @parallelworks/ui/ai for how it is drawn).
 * The list of sessions by machine sits beside the start of a new one, or
 * the session that is open, drawn as a pw code chat draws it and live
 * (CodeSession). Opening or starting one stays on the page; continuing it in
 * a Studio chat is an action of its own.
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

// A session's replies are drawn as a chat's: the library files they name
// open here, in this tab.
const chatConfig = { markdownComponents: { ...citedMarkdown, a: ChatMarkdownLink }, strings: { effort: EFFORT_WORDS } }

/** The model picker reads the platform's models from a chat's context. */
function CodeChat() {
  const cfg = useAppConfig()
  const adapter = useMemo(() => createOpenAIChatAdapter({ baseUrl: '/api/code-page/openai', storageKey: 'studio.code.models' }), [])
  const navigation = useMemo(() => ({ toConversation: () => {}, toNewChat: () => {} }), [])
  return (
    <ChatProvider adapter={adapter} currentUser={cfg.user ?? { username: '' }} navigation={navigation} config={chatConfig}>
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

/** Where the page's sessions are, null until the Studio says; one that does
 *  not answer leaves the page to say what its list could not do. */
function useWhere(): Where | null {
  const [value, setValue] = useState<Where | null>(null)
  useEffect(() => {
    let gone = false
    const set = (v: Where | null) => { if (!gone) setValue(v ?? { local: false, codeUrl: '' }) }
    void fetch('/api/code-page/where').then(r => (r.ok ? r.json() as Promise<Where> : null)).then(set, () => set(null))
    return () => { gone = true }
  }, [])
  return value
}

function CodeScreen() {
  const cfg = useAppConfig()
  const { data } = pwCodePage.useSessions()
  // The menu's marks and the Notices page show what this list shows.
  useEffect(() => { publishListing(data) }, [data])
  const machines = useMemo(() => data?.machines ?? [], [data])
  const studio = useOpenInStudio(machines)
  const where = useWhere()
  const [delegatedList, setDelegated] = useState<{ sessions?: Delegated[] } | null>(null)
  useWatch<{ sessions?: Delegated[] }>('/api/delegated', 30_000, true, setDelegated)
  const delegatedById = useMemo(() => new Map((delegatedList?.sessions ?? []).map(d => [d.session, d])), [delegatedList])
  const delegated = useMemo(() => new Set(delegatedById.keys()), [delegatedById])
  const [onlyDelegated, setOnlyDelegated] = useState(false)
  // The session that is open: the page's address names it, so a link to it
  // (Fleet, Agents, the status panel, a chat's note), going back and a
  // reload open the same one.
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
    if (sessionInHash(location.hash)) location.hash = '#view=code'
  }, [])
  const inChat = useMemo(() => new Set((studio.here?.chats ?? []).map(c => c.session)), [studio.here])
  const opened = useMemo((): { machine?: string; sessionId?: string } => {
    const machine = linked?.machineId || link?.machine
    return link ? { ...(machine ? { machine } : {}), sessionId: link.session } : {}
  }, [link, linked])
  const [collapsed, setCollapsed] = useState(false)
  const narrow = useNarrow()
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [startKey, setStartKey] = useState(0)
  // A session on the Studio's own machine starts with its tools. With no
  // platform the only machine is this computer, whose pw code the Studio may
  // only list: then nothing is said of the tools.
  const here = studio.here
  const local = where?.local === true
  const isOwn = useCallback((m: MachineRef) => (local ? here?.available || undefined : here ? sameMachine(m, here) : undefined), [here, local])
  const tools = useMachineTools(isOwn)
  // What is being started, and whether the person went to another session
  // meanwhile: it then opens only when they pick it.
  const [starting, setStarting] = useState<Starting | null>(null)
  const [startedAt, setStartedAt] = useState<Record<string, Starting>>({})
  const left = useRef(false)

  const show = useCallback((machine: string, session: string) => {
    setLink({ machine, session })
    if (location.hash !== sessionHash(machine, session)) location.hash = sessionHash(machine, session)
  }, [])
  const open = useCallback((s: Pick<PageSession, 'machineId' | 'id'> & { archivedAt?: string | null }) => {
    setDrawerOpen(false)
    left.current = true
    show(s.machineId, s.id)
  }, [show])
  const continueIn = useCallback((s: SessionRef) => {
    setDrawerOpen(false)
    // One that cannot be continued here shows why, with what still works.
    void studio.open(s).then(ok => { if (!ok) show(s.machineId, s.id) })
  }, [studio, show])
  // A start that is under way keeps its composer, which says if it fails.
  const newSession = useCallback(() => { setDrawerOpen(false); closeLink(); if (!starting) setStartKey(k => k + 1) }, [closeLink, starting])
  const onStarting = useCallback((s: Starting | null) => {
    if (!s && left.current) toast.error('The session did not start. Its message and the reason are under New session.')
    left.current = false
    setStarting(s)
  }, [])
  const onStarted = useCallback((machine: string, id: string) => {
    setStarting(s => {
      if (s) setStartedAt(all => ({ ...all, [id]: s }))
      return null
    })
    studio.reload()
    if (!left.current) show(machine, id)
    else toast.success('The session started. It is in the list.')
  }, [show, studio.reload])
  const shownAt = link ? paneFor({ machineId: opened.machine ?? '', id: link.session }, machines, studio.here) : null
  // Shown as soon as it is known to be the person's: listed, in a chat of
  // theirs, or just started here.
  const known = !!link && (!!linked || inChat.has(link.session) || !!startedSession(link.session))
  const inPane = !!link && !!shownAt?.machine && known
  const shownOn = machines.find(m => m.id === opened.machine)
  // What the pane shows is seen: the end of work delegated to the session,
  // and the replies and questions of the chat that holds it.
  const seenSession = inPane ? link.session : ''
  const seenChat = inPane ? shownAt.conversationId : undefined
  useEffect(() => { if (seenSession) sessionSeen(seenSession) }, [seenSession])
  useEffect(() => {
    if (!seenChat) return
    setFrontChat(seenChat)
    return () => setFrontChat(null)
  }, [seenChat])

  const sessionUrl = (s: PageSession) => (where?.codeUrl ? `${where.codeUrl}/${encodeURIComponent(s.machineId)}/${encodeURIComponent(s.id)}` : '')
  const extraMenu = (s: PageSession): MenuEntry[] => {
    const why = studio.why(s)
    const url = sessionUrl(s)
    return [
      { label: 'Open', onClick: () => open(s) },
      { label: inChat.has(s.id) ? 'Open in its Studio chat' : 'Continue in a Studio chat', onClick: () => continueIn(s), ...(why ? { disabledReason: why } : {}) },
      ...(url ? [{ label: 'Open in ACTIVATE', icon: <ExternalLinkIcon />, onClick: () => { window.open(url, '_blank', 'noopener,noreferrer') } }] : []),
    ]
  }

  // Neither the start nor its controls show before the Studio says whose they are.
  if (!where) return <div className="code-page code-page-loading" aria-busy="true" />
  if (where.signIn) return <SignInNeeded />
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
      <main className="code-main" data-pane={inPane || (starting && !link) ? 'session' : 'start'}>
        <PageHeadActions>
          {narrow && <Button variant="secondary" size="sm" icon={<SidebarOpenIcon aria-hidden />} data-testid="code-sessions-drawer" onClick={() => setDrawerOpen(true)}>Sessions</Button>}
          <IconButton variant="outline" size="sm" icon={<AddIcon aria-hidden />} label="New session" data-testid="agents-new-session" onClick={newSession} />
          {where.codeUrl && !inPane && !(starting && !link) && (
            <IconButton variant="outline" size="sm" icon={<ExternalLinkIcon aria-hidden />} label="Open in ACTIVATE" href={where.codeUrl} target="_blank" rel="noopener noreferrer" />
          )}
          <Popover
            align="end"
            aria-label="How sessions appear"
            className="code-how-popover"
            trigger={<IconButton variant="outline" size="sm" icon={<HelpIcon aria-hidden />} label="How sessions appear" data-testid="agents-how-button" />}
          >
            <HowSessionsAppear compact />
          </Popover>
        </PageHeadActions>
        {starting && !link && <StartingSession starting={starting} />}
        {inPane && link && shownAt && (
          <CodeSession
            session={linked}
            machine={linked ? machines.find(m => m.id === linked.machineId) : undefined}
            pane={shownAt}
            sessionId={link.session}
            first={startedSession(link.session)?.first}
            where={startedAt[link.session]}
            username={cfg.user?.username ?? ''}
            delegated={delegatedById.get(link.session)}
            inChat={!!shownAt.conversationId}
            elsewhere={shownOn ? isOwn(shownOn) === false : false}
            why={linked ? studio.why(linked) : shownAt.conversationId ? '' : 'This session is not listed yet.'}
            url={linked ? sessionUrl(linked) : ''}
            onContinue={() => continueIn(linked ?? { machineId: opened.machine ?? '', id: link.session })}
            onClose={newSession}
            onMoved={id => { knowSession(id, { shownOn: shownAt.machine, conversationId: shownAt.conversationId }); show(opened.machine ?? '', id) }}
          />
        )}
        {/* Kept while a session is open or being started: a message half
            written stays, and a start that fails comes back to it. */}
        <div className="code-start" hidden={inPane || (!!starting && !link)}>
          <CodeStart key={startKey} onOpen={open} onStarting={onStarting} onStarted={onStarted} tools={tools} top={link && !inPane ? answer => (
            <LinkedSession
              session={linked}
              machine={linked ? machines.find(m => m.id === linked.machineId) : undefined}
              missing={!!data && !linked && !!studio.here}
              delegated={linked ? delegatedById.get(linked.id) : undefined}
              why={linked ? studio.why(linked) : ''}
              inChat={!!linked && inChat.has(linked.id)}
              url={linked ? sessionUrl(linked) : ''}
              onAnswer={answer}
              onOpen={() => linked && continueIn(linked)}
              onClose={closeLink}
            />
          ) : undefined} />
        </div>
      </main>
      {studio.dialog}
      {tools.dialog}
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
