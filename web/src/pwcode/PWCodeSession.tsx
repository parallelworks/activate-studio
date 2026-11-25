import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import {
  EffortPicker,
  ExpandAllProvider,
  NoticeBar,
  NoticeCard,
  OutputCard,
  PermissionPicker,
  SessionView,
  type ChatMessage,
} from '@parallelworks/ui/ai'
import {
  loadPwCodeMessages,
  pendingApprovals,
  promptHistory,
  type PwCodeMessages,
  type PwCodeSessionActivity,
  type PwCodeSessionScreen,
  sessionDraftKey,
  sessionSettingsOptions,
  useComposerPastes,
  useGoalNotice,
  usePwCodeComposer,
  usePwCodeConversation,
  usePwCodeSessionScreen,
  useSessionPicker,
  useStaleFor,
} from '@parallelworks/code'
import { createTranslator, IntlProvider } from 'use-intl'
import { refreshNotices, setReplying } from '../notices'
import type { Translate } from '../code/parts'
import { followCleared, pwCode } from './api'
import { describeError } from './describeError'
import { withEarlier, withoutCarried } from './carried'
import { AccountChip, AccountPanelModal } from './session/AccountPanel'
import { SessionActivity, SessionStatusRow, type StatusActivity, useSessionAgents, useSwitcherState } from './session/AgentSwitcher'
import { BackgroundCommandsContext, useBackgroundShortcut } from './session/BackgroundCommandHint'
import { ComposerImages } from './session/ComposerImages'
import { SessionContextRow } from './session/ContextRow'
import { GoalControl } from './session/GoalControl'
import { SessionTasks } from './session/SessionTasks'
import { SubagentView, type SubagentTasks } from './session/SubagentView'
import { TerminalHandoff } from './session/TerminalHandoff'
import { UsageLimitWait } from './session/UsageLimitWait'
import { useAwayRecap } from './session/useAwayRecap'
import { useComposerImages } from './session/useComposerImages'
import { usePlatformModels } from './session/usePlatformModels'
import { AgentModelControls } from '../code/AgentModelControls'
import { cachedChat, readChat } from '../chatRecord'

import { needsActivate } from '../accounts'
import { NeedsActivate } from '../components/NeedsActivate'
import { announceModel } from './liveModel'
import { announceName } from './chatNames'
import { PlanAnswer, openPlan } from './session/PlanAnswer'
import { focusComposer, focusWaiting } from './session/focusAnswer'

export { describeError }

/**
 * A pw code chat, shown as PW Code on the web shows a session: the
 * session's own transcript and live feed from @parallelworks/code, drawn
 * with @parallelworks/ui/ai's session view, and composed the way PW Code
 * web's session page composes them. So a turn is followed whoever started
 * it (this chat, PW Code on the web, a terminal, a goal step), from the
 * first event, through asks, plans and approvals, to its end; a dropped
 * feed reconnects after its last frame. The Studio's own parts sit around
 * it: what answers the chat, and its notes.
 */
export function PWCodeSession(props: PWCodeSessionProps) {
  return (
    <PwCodeWords loading={<div className="pwcode-session pwcode-session-loading" aria-busy="true" />}>
      <SessionPane {...props} />
    </PwCodeWords>
  )
}

/** pw code's words around its parts (@parallelworks/code reads them from
 *  use-intl), and `loading` until they are loaded. */
export function PwCodeWords({ children, loading = null }: { children: ReactNode; loading?: ReactNode }) {
  const [messages, setMessages] = useState<PwCodeMessages | null>(loadedMessages)
  useEffect(() => {
    if (messages) return
    let gone = false
    void loadMessages().then(m => { if (!gone) setMessages(m) }, () => {})
    return () => { gone = true }
  }, [messages])
  if (!messages) return <>{loading}</>
  return (
    <IntlProvider locale="en" messages={messages} timeZone={Intl.DateTimeFormat().resolvedOptions().timeZone}>
      {children}
    </IntlProvider>
  )
}

/** pw code's words for a part drawn before they are loaded: null until then. */
export function usePwCodeWords(): Translate | null {
  const [messages, setMessages] = useState<PwCodeMessages | null>(loadedMessages)
  useEffect(() => {
    if (messages) return
    let gone = false
    void loadMessages().then(m => { if (!gone) setMessages(m) }, () => {})
    return () => { gone = true }
  }, [messages])
  return useMemo(() => (messages ? createTranslator({ locale: 'en', messages, namespace: 'agents' }) as unknown as Translate : null), [messages])
}

// pw code's words, loaded once for the page: a chat switched to after the
// first draws its session in the same frame, not one load later.
let loadedMessages: PwCodeMessages | null = null
let loadingMessages: Promise<PwCodeMessages> | null = null
function loadMessages(): Promise<PwCodeMessages> {
  loadingMessages ??= loadPwCodeMessages('en').then(m => (loadedMessages = m), e => { loadingMessages = null; throw e })
  return loadingMessages
}

export interface PWCodeSessionProps {
  /** The Studio chat that holds the session; none for one the Code page
   *  shows that no chat does. */
  conversationId?: string
  machine: string
  sessionId: string
  username: string
  /** Studio notes drawn over the composer: the chat's tools. */
  notes?: ReactNode
  /** Studio parts drawn in the composer's settings row, left: what answers. */
  settingsExtra?: ReactNode
  /** The message the session was started with, shown until its transcript
   *  is read. */
  first?: string
  /** /clear made a new session, which the Code page's pane moves to. */
  onMoved?: (session: string) => void
}

const noop = () => {}

/** How long after a turn ends the Studio has stored its reply. */
const STORED_MS = 1500

/** Tells the notices when the session's turn starts or ends, and when it
 *  asks or is answered, rather than leaving them to their next read. */
function useNoticeState(conversationId: string | undefined, running: boolean, waiting: number) {
  const was = useRef({ running, waiting })
  useEffect(() => {
    if (conversationId) setReplying(conversationId, running && waiting === 0)
    const ended = was.current.running && !running
    if (ended || was.current.waiting !== waiting) refreshNotices({ after: ended ? STORED_MS : undefined })
    was.current = { running, waiting }
  }, [conversationId, running, waiting])
  useEffect(() => () => { if (conversationId) setReplying(conversationId, false) }, [conversationId])
}

// The platform's model list sets when a paste is saved as a file rather than
// sent as text, before the session reports its own limit; the Studio's chat
// does not hold that list, so until then a paste stays text.
const pasteApi = { uploadPaste: pwCode.uploadPaste, usePlatformModels: () => NO_MODELS }
const NO_MODELS = { models: [] }

/** A short screen, such as a phone on its side, starts with the task list
 *  folded: open, it would cover the transcript the composer leaves. */
function tasksStartOpen(): boolean {
  return !window.matchMedia?.('(max-height: 32rem)').matches
}

/** A chat record's messages to draw while the session is read: what was
 *  said and the steps the record kept of it. */
function storedMessages(list: unknown[] | undefined): ChatMessage[] | undefined {
  const out = (list ?? []).flatMap(m => {
    const x = m as Partial<ChatMessage>
    return typeof x.id === 'string' && (x.role === 'user' || x.role === 'assistant') && (x.content || x.parts?.length)
      ? [{ id: x.id, role: x.role, content: x.content ?? '', parts: x.parts, parentId: x.parentId, timestamp: x.timestamp, model: x.model }]
      : []
  })
  return out.length ? out : undefined
}

/** The chat's record's messages (chatRecord.ts): kept from the chat's
 *  open, which reads it, or read here when it was not. */
function useChatMessages(conversationId: string | undefined): ChatMessage[] | undefined {
  const [record, setRecord] = useState(() => cachedChat(conversationId))
  useEffect(() => {
    const kept = cachedChat(conversationId)
    setRecord(kept)
    if (kept || !conversationId) return
    let gone = false
    readChat(conversationId).then(c => { if (!gone) setRecord(c) }, () => {})
    return () => { gone = true }
  }, [conversationId])
  return useMemo(() => storedMessages(record?.messages), [record])
}

function SessionPane({ conversationId, machine, sessionId, username, notes, settingsExtra, first, onMoved }: PWCodeSessionProps) {
  const [expandAll, setExpandAll] = useState<boolean | undefined>(undefined)
  const [note, setNote] = useState('')
  const notify = useCallback((text: string) => setNote(text), [])
  const { picker, setPicker, handleLocally, addsToTranscript } = useSessionPicker()
  // /clear makes a new session: the chat goes on with it, as PW Code on the
  // web moves to it.
  const onCleared = useCallback((newId: string) => {
    if (!newId || newId === sessionId) return
    const follow = conversationId ? followCleared(conversationId, newId) : Promise.resolve()
    follow.then(() => onMoved?.(newId), (e: unknown) => setNote(describeError(e)))
  }, [conversationId, sessionId, onMoved])
  const sessions = pwCode.useSessions()
  const listed = sessions.data?.sessions?.find(s => s.id === sessionId && s.machineId === machine)
  const read = pwCode.useSessionDetail(machine, sessionId, listed)
  const { data: detail, mutate, fresh } = read
  const machineRead = pwCode.useMachine(machine)

  const goalNotice = useGoalNotice()
  const conversation = usePwCodeConversation({
    api: pwCode,
    machine,
    sessionId,
    sessions: { data: sessions.data, mutate: sessions.mutate },
    detail: read,
    describeError,
    notify,
    onCleared,
    // A session moved to another machine is continued from there again.
    onMoved: noop,
    goalNotice,
  })
  const { liveState, transcript, messages, queued, notice, dismissNotice, showError, addDirs, allowedDirs } = conversation

  const composerImages = useComposerImages({ machine, sessionId, messages: transcript?.messages, onError: notify })
  const { connected, disconnected } = pwCode.useSessionEvents(machine, sessionId, fresh ? conversation.cursor : undefined, conversation.onEvent)
  const staleFor = useStaleFor(detail, connected)
  const page = usePwCodeComposer({
    api: pwCode,
    machine,
    sessionId,
    conversation,
    machineInfo: machineRead.machine,
    feed: { connected, disconnected },
    staleFor,
    images: composerImages,
    handleLocally,
    notify,
    onCleared,
    onArchived: noop,
  })
  const { composer: state, background } = page

  // A subagent is shown in place of the main transcript, as PW Code on the
  // web shows one picked in its status row.
  const [agent, setAgent] = useState<string | undefined>(undefined)
  useEffect(() => { setAgent(undefined) }, [sessionId])
  useBackgroundShortcut(background, !agent)
  const switcher = useSwitcherState()
  const agents = useSessionAgents(conversation)
  const [tasksOpen, setTasksOpen] = useState(tasksStartOpen)

  // The person's earlier messages as they typed them, without the turns
  // the Studio carried over to the session (carried.ts).
  const history = useMemo(() => promptHistory(username, { id: sessionId, messages: withoutCarried(transcript?.messages) }), [username, sessionId, transcript?.messages])
  const draftKey = sessionDraftKey(sessionId, detail)
  useAwayRecap({ machine, sessionId, draftKey, turnEndSeq: transcript?.turnEndSeq, idle: page.idle })
  const pastes = useComposerPastes(pasteApi, { machine, sessionId, liveState, detail, describeError })
  const settings = pwCode.useSessionSettings({
    ...sessionSettingsOptions(machine, conversation, page),
    onPatched: () => void mutate(),
    onError: showError,
  })
  // The chat bar's engine button names the model this session runs on now.
  useEffect(() => { if (conversationId) announceModel(conversationId, settings.model.value) }, [conversationId, settings.model.value])
  // The chat's title follows the session's name: the Studio has taken it by
  // the time this view reads it, so the chat list is read again.
  const name = listed ? listed.name || listed.autoName || '' : detail?.name || detail?.autoName || ''
  useEffect(() => { if (conversationId && name) announceName(conversationId, name) }, [conversationId, name])
  const screen = usePwCodeSessionScreen({
    conversation,
    composer: page,
    read,
    machine: machineRead,
    staleFor,
    settings,
    images: composerImages.images,
    describeError,
  })

  // The chat list and the menu mark the chat as the session is now, and a
  // reply that ends in front of the person is seen.
  const running = page.turn.running
  useNoticeState(conversationId, running, transcript ? pendingApprovals(transcript).length : 0)

  // Until the session's transcript first arrives, the chat's own record of
  // it (read as this person, chatRecord.ts) is drawn, as before the session
  // view: a first open over a slow relay shows the conversation at once.
  const record = useChatMessages(conversationId)
  const stored = useMemo(() => {
    if (detail) return undefined
    return record?.length ? record : (first ? [{ id: 'pw-first', role: 'user' as const, content: first }] as ChatMessage[] : undefined)
  }, [detail, record, first])
  // The session's transcript holds what the Studio sent it, the turns it
  // missed before the person's message: the chat draws those turns from
  // its record, where they came, and the message as the person typed it.
  const shownMessages = useMemo(() => withoutCarried(stored ?? withEarlier(messages, record)), [stored, messages, record])

  const noticeText = notice?.text ?? note
  // A refusal for want of an ACTIVATE sign-in gets the one prompt every
  // feature shows, with Open Accounts.
  const noticeCard = (notice || note) && (needsActivate(typeof noticeText === 'string' ? noticeText : '')
    ? <NeedsActivate>{noticeText}</NeedsActivate>
    : (
      <NoticeCard tone={notice?.warn ? 'warning' : 'neutral'} onDismiss={() => { dismissNotice(); setNote('') }}>
        {noticeText}
      </NoticeCard>
    ))
  // The platform models pw code can run, for the model picker.
  const platformModels = usePlatformModels()

  // A question, plan or approval that arrives waits for the person: the
  // thread goes to it, as it would while following the turn, even when a
  // note over the composer moved the thread off its end meanwhile.
  const pane = useRef<HTMLDivElement>(null)
  const waiting = transcript ? pendingApprovals(transcript).length : 0
  const waited = useRef(waiting)
  // Whether the session's transcript has been seen yet: a card already
  // waiting when the chat opens arrives with it.
  const seen = useRef(false)
  useEffect(() => {
    const timers: number[] = []
    const opening = !seen.current
    let moved = false
    if (waiting > waited.current) {
      // The card renders and grows after the event: the thread's own
      // "Jump to latest" goes to its end once it has settled.
      for (const ms of [150, 500, 1200]) {
        timers.push(window.setTimeout(() => {
          const jump = [...(pane.current?.querySelectorAll<HTMLButtonElement>('button') ?? [])].find(b => b.textContent?.trim() === 'Jump to latest')
          if (jump) jump.click()
          // And the keyboard goes to its first answer (session/focusAnswer.ts).
          if (!moved) moved = focusWaiting(pane.current, opening)
        }, ms))
      }
    } else if (waiting === 0 && waited.current > 0) {
      for (const ms of [100, 400]) timers.push(window.setTimeout(() => { focusComposer(pane.current) }, ms))
    }
    waited.current = waiting
    return () => { for (const t of timers) window.clearTimeout(t) }
  }, [waiting])
  useEffect(() => { if (transcript) seen.current = true }, [transcript])

  const openAccounts = () => setPicker('claude')
  const activityLine = (a: PwCodeSessionActivity | null): StatusActivity =>
    a ? brief => (a.kind === 'usageLimit'
      ? <UsageLimitWait resumeAt={a.resumeAt} onOpenAccounts={openAccounts} />
      : <SessionActivity activity={a} brief={brief} />) : null
  const tasksNode = (todos: SubagentTasks | undefined) => todos?.length
    ? <SessionTasks todos={todos} open={tasksOpen} onOpenChange={setTasksOpen} />
    : null
  const status = (line: StatusActivity, { goal, tasks }: { goal?: ReactNode; tasks?: SubagentTasks }) =>
    line || goal || tasks?.length || agents.length > 0
      ? <SessionStatusRow conversation={conversation} activity={line} goal={goal} tasks={tasksNode(tasks)}
          agent={agent} onSelectAgent={setAgent} switcher={switcher} />
      : undefined
  const banner = <SessionBanner banner={screen.banner} />
  const contextRow = detail && (
    <SessionContextRow machine={machine} sessionId={sessionId} machineName={page.machineName}
      workspace={detail.workspace} added={addDirs} allowed={allowedDirs} pullRequests={detail.pullRequests} />
  )
  const viewing = agent ? agents.find(a => a.id === agent) : undefined
  // A plan waiting on an answer is answered here (session/PlanAnswer.tsx).
  const plan = useMemo(() => openPlan(transcript ? pendingApprovals(transcript) : null), [transcript])

  return (
    <div className="pwcode-session" ref={pane} onPasteCapture={composerImages.onPaste}>
      <p className="sr-only" role="status">{screen.statusText}</p>
      <ExpandAllProvider value={expandAll}>
        <BackgroundCommandsContext value={background}>
          {agent && (
            <SubagentView key={agent} machine={machine} sessionId={sessionId} subId={agent}
              listed={viewing} usage={liveState?.subagentUsage?.[agent]}
              subscribe={conversation.subscribe} loadPalette={page.loadPalette} sessionLock={page.sessionLock}
              sessionBanner={banner} notice={noticeCard} status={(line, { tasks }) => status(line, { tasks })} context={contextRow}
              modelNames={liveState?.modelNames} pastes={pastes} username={username}
              onSent={conversation.refreshSubagents} onBack={() => setAgent(undefined)} />
          )}
          <div className="pwcode-main" hidden={!!agent}>
            <SessionView
              sessionKey={draftKey}
              foldEveryRun
              messages={shownMessages}
              streamingMessage={transcript?.streaming ?? null}
              queuedMessages={queued}
              canRemoveQueued={conversation.canRemoveQueued}
              queueDelivery={conversation.queueDelivery}
              turn={{ running: page.turn.running, onStop: () => void page.turn.stop(), stopping: page.turn.stopping }}
              onSend={(text) => page.send(text)}
              addsToTranscript={addsToTranscript}
              onApprovalAnswer={conversation.answerApproval}
              showAuthor={screen.showAuthor}
              currentUsername={username}
              loading={screen.loading && !stored}
              error={screen.failed ? (
                <div className="pwcode-session-failed">
                  <p role="alert">{screen.failed.message}</p>
                  <button type="button" className="code-approval-more" onClick={screen.retry}>Try again</button>
                </div>
              ) : undefined}
              banners={banner}
              label="pw code session"
              flush
              find
              workingIndicator={false}
              footer={<>
                {page.outputs.map(o => (
                  <OutputCard key={o.id} title={o.command} text={o.text} onDismiss={() => page.dismissOutput(o.id)} dismissLabel="Dismiss" />
                ))}
                {page.endNote && <NoticeCard><span className="pwcode-end-note">{page.endNote}</span></NoticeCard>}
                {plan && <PlanAnswer key={plan.id} approvalId={plan.id}
                  approvals={conversationId ? `/api/chat/pwcode/${encodeURIComponent(conversationId)}/approvals` : `/api/chat/pwcode/page/${encodeURIComponent(machine)}/${encodeURIComponent(sessionId)}/approvals`} />}
              </>}
              composer={{
                attachments: false,
                tools: false,
                allowEmpty: screen.allowEmpty,
                requireModel: false,
                history,
                recall: page.recall,
                disabled: state.disabled,
                placeholder: state.placeholder,
                inputLabel: 'Message to this session',
                sendLabel: 'Send',
                pastes,
                slash: { commands: screen.commands, label: 'Commands', onOpen: page.openPalette, loading: page.paletteLoading },
                activity: status(activityLine(screen.activity), {
                  goal: screen.goal && conversation.goal ? (
                    <GoalControl key={`${machine}/${sessionId}`} machine={machine} sessionId={sessionId}
                      label={screen.goal} status={conversation.goal} locked={state.locked} />
                  ) : null,
                  tasks: transcript?.todos,
                }),
                notices: <>
                  {screen.untrusted && <NoticeCard tone="warning">{screen.untrusted}</NoticeCard>}
                  {noticeCard}
                  <ComposerImages images={composerImages.images} onRemove={composerImages.remove} />
                  {notes}
                </>,
                context: contextRow,
                settingsLeft: <>
                  <PermissionPicker
                    mode={screen.permission.mode}
                    ceiling={screen.permission.ceiling}
                    accepted={screen.permission.accepted}
                    onPick={settings.setMode}
                    disabled={screen.permission.disabled}
                  />
                  <AccountChip model={settings.model.value} account={settings.account.value} onOpen={openAccounts} />
                  {settingsExtra}
                  {settings.saving && <span className="muted pwcode-saving">Saving…</span>}
                </>,
                settingsRight: <>
                  <AgentModelControls
                    key={`${machine}/${sessionId}`}
                    api={pwCode}
                    platform={platformModels}
                    machine={machine}
                    sessionId={sessionId}
                    claudeReady={screen.claude.available}
                    canStart={screen.canStart}
                    model={settings.model}
                    modelNames={liveState?.modelNames}
                    effort={settings.effort}
                    allocation={settings.allocation}
                    account={settings.account.value ?? undefined}
                    panel={picker}
                    onPanelChange={setPicker}
                    onAccounts={openAccounts}
                  />
                  <TerminalHandoff machine={machine} sessionId={sessionId} username={username} />
                  <button type="button" className="pwcode-expand" onClick={() => setExpandAll(open => !open)} aria-pressed={!!expandAll}
                    title={expandAll ? 'Fold every step' : 'Open every step'}>{expandAll ? 'Fold steps' : 'Open steps'}</button>
                </>,
                hint: state.hint || undefined,
              }}
            />
          </div>
        </BackgroundCommandsContext>
      </ExpandAllProvider>
      {picker === 'claude' && (
        <AccountPanelModal key={`accounts:${machine}/${sessionId}`} machine={machine} machineName={page.machineName}
          sessionId={sessionId} machineFacts={screen.claude} waitingAtLimit={screen.waitingAtLimit}
          account={settings.account} onClose={() => setPicker(p => (p === 'claude' ? null : p))}
          onMovedOthers={notify} onMoveError={showError} />
      )}
    </div>
  )
}

function SessionBanner({ banner }: { banner: PwCodeSessionScreen['banner'] }) {
  if (!banner) return null
  return (
    <NoticeBar tone={banner.tone} title={banner.title} action={banner.action} onAction={banner.act}>
      {banner.body}
    </NoticeBar>
  )
}
