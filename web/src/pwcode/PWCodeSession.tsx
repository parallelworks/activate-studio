import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import {
  ActivityLine,
  EffortPicker,
  ExpandAllProvider,
  NoticeBar,
  NoticeCard,
  OutputCard,
  PermissionPicker,
  SessionView,
} from '@parallelworks/ui/ai'
import {
  loadPwCodeMessages,
  modelLabel,
  pendingApprovals,
  promptHistory,
  type PwCodeMessages,
  type PwCodeSessionActivity,
  type PwCodeSessionScreen,
  sessionDraftKey,
  sessionSettingsOptions,
  usePwCodeComposer,
  usePwCodeConversation,
  usePwCodeSessionScreen,
  useSessionPicker,
  useStaleFor,
} from '@parallelworks/code'
import { IntlProvider } from 'use-intl'
import { pwCode } from './api'

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
  const [messages, setMessages] = useState<PwCodeMessages | null>(null)
  useEffect(() => {
    let gone = false
    void loadPwCodeMessages('en').then(m => { if (!gone) setMessages(m) }, () => {})
    return () => { gone = true }
  }, [])
  if (!messages) return <div className="pwcode-session pwcode-session-loading" aria-busy="true" />
  return (
    <IntlProvider locale="en" messages={messages} timeZone={Intl.DateTimeFormat().resolvedOptions().timeZone}>
      <SessionPane {...props} />
    </IntlProvider>
  )
}

export interface PWCodeSessionProps {
  machine: string
  sessionId: string
  username: string
  /** Studio notes drawn over the composer: the chat's tools. */
  notes?: ReactNode
  /** Studio parts drawn in the composer's settings row, left: what answers. */
  settingsExtra?: ReactNode
}

/** Words a refusal or failure the way the platform's problems carry them. */
export function describeError(error: unknown): string {
  if (!error) return ''
  if (typeof error === 'string') return error
  const e = error as { detail?: unknown; title?: unknown; message?: unknown; error?: unknown }
  for (const v of [e.detail, e.error, e.message, e.title]) if (typeof v === 'string' && v) return v
  return String(error)
}

const noop = () => {}

function SessionPane({ machine, sessionId, username, notes, settingsExtra }: PWCodeSessionProps) {
  const [expandAll, setExpandAll] = useState<boolean | undefined>(undefined)
  const [note, setNote] = useState('')
  const notify = useCallback((text: string) => setNote(text), [])
  const { picker, handleLocally, addsToTranscript } = useSessionPicker()
  const sessions = pwCode.useSessions()
  const listed = sessions.data?.sessions?.find(s => s.id === sessionId && s.machineId === machine)
  const read = pwCode.useSessionDetail(machine, sessionId, listed)
  const { data: detail, mutate, fresh } = read
  const machineRead = pwCode.useMachine(machine)

  const conversation = usePwCodeConversation({
    api: pwCode,
    machine,
    sessionId,
    sessions: { data: sessions.data, mutate: sessions.mutate },
    detail: read,
    describeError,
    notify,
    // The Studio shows the session its chat holds; /clear and a move are
    // made where the session runs, so nothing here follows them.
    onCleared: noop,
    onMoved: noop,
  })
  const { liveState, transcript, messages, queued, notice, dismissNotice, showError } = conversation

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
    handleLocally,
    notify,
    onCleared: noop,
    onArchived: noop,
  })
  const { composer: state } = page
  const history = useMemo(() => promptHistory(username, { id: sessionId, messages: transcript?.messages }), [username, sessionId, transcript?.messages])
  const settings = pwCode.useSessionSettings({
    ...sessionSettingsOptions(machine, conversation, page),
    onPatched: () => void mutate(),
    onError: showError,
  })
  const screen = usePwCodeSessionScreen({
    conversation,
    composer: page,
    read,
    machine: machineRead,
    staleFor,
    settings,
    images: [],
    describeError,
  })
  void picker

  const noticeCard = (notice || note) && (
    <NoticeCard tone={notice?.warn ? 'warning' : 'neutral'} onDismiss={() => { dismissNotice(); setNote('') }}>
      {notice?.text ?? note}
    </NoticeCard>
  )
  // The model choices: those the session's machine offers it, the names
  // the session reports, and the model it is on.
  const current = settings.model.value ?? detail?.model ?? ''
  const offered = useSessionModels(machine, sessionId)
  const modelNames = useMemo(() => {
    const names: Record<string, string> = {}
    for (const m of offered) names[m.id] = modelLabel(m.id, m.name)
    for (const [id, name] of Object.entries(liveState?.modelNames ?? {})) names[id] = name
    if (current && !names[current]) names[current] = modelLabel(current)
    return names
  }, [offered, liveState?.modelNames, current])
  const models = Object.keys(modelNames)

  // A question, plan or approval that arrives waits for the person: the
  // thread goes to it, as it would while following the turn, even when a
  // note over the composer moved the thread off its end meanwhile.
  const pane = useRef<HTMLDivElement>(null)
  const waiting = transcript ? pendingApprovals(transcript).length : 0
  const waited = useRef(waiting)
  useEffect(() => {
    const timers: number[] = []
    if (waiting > waited.current) {
      // The card renders and grows after the event: the thread's own
      // "Jump to latest" goes to its end once it has settled.
      for (const ms of [150, 500, 1200]) {
        timers.push(window.setTimeout(() => {
          const jump = [...(pane.current?.querySelectorAll<HTMLButtonElement>('button') ?? [])].find(b => b.textContent?.trim() === 'Jump to latest')
          if (jump) jump.click()
        }, ms))
      }
    }
    waited.current = waiting
    return () => { for (const t of timers) window.clearTimeout(t) }
  }, [waiting])

  return (
    <div className="pwcode-session" ref={pane}>
      <p className="sr-only" role="status">{screen.statusText}</p>
      <ExpandAllProvider value={expandAll}>
        <SessionView
          sessionKey={sessionDraftKey(sessionId, detail)}
          foldEveryRun
          pinPrompt
          messages={messages}
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
          loading={screen.loading}
          error={screen.failed ? (
            <div className="pwcode-session-failed">
              <p role="alert">{screen.failed.message}</p>
              <button type="button" className="code-approval-more" onClick={screen.retry}>Try again</button>
            </div>
          ) : undefined}
          banners={<SessionBanner banner={screen.banner} />}
          label="pw code session"
          flush
          find
          workingIndicator={false}
          footer={<>
            {page.outputs.map(o => (
              <OutputCard key={o.id} title={o.command} text={o.text} onDismiss={() => page.dismissOutput(o.id)} dismissLabel="Dismiss" />
            ))}
            {page.endNote && <NoticeCard><span className="pwcode-end-note">{page.endNote}</span></NoticeCard>}
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
            activity: screen.activity ? <SessionActivity activity={screen.activity} /> : undefined,
            notices: <>
              {screen.untrusted && <NoticeCard tone="warning">{screen.untrusted}</NoticeCard>}
              {noticeCard}
              {notes}
            </>,
            context: detail && <SessionContext machineName={page.machineName} workspace={detail.workspace} />,
            settingsLeft: <>
              <PermissionPicker
                mode={screen.permission.mode}
                ceiling={screen.permission.ceiling}
                accepted={screen.permission.accepted}
                onPick={settings.setMode}
                disabled={screen.permission.disabled}
              />
              {settingsExtra}
              {settings.saving && <span className="muted pwcode-saving">Saving…</span>}
            </>,
            settingsRight: <>
              {models.length > 0 && (
                <select className="pwcode-model" aria-label="The session's model" value={current}
                  disabled={settings.model.disabled} onChange={e => settings.model.onChange(e.target.value)}>
                  {models.map(id => <option key={id} value={id}>{modelNames[id]}</option>)}
                </select>
              )}
              <EffortPicker effort={settings.effort.value} onPick={settings.effort.onChange} disabled={settings.effort.disabled} />
              <button type="button" className="pwcode-expand" onClick={() => setExpandAll(open => !open)} aria-pressed={!!expandAll}
                title={expandAll ? 'Fold every step' : 'Open every step'}>{expandAll ? 'Fold steps' : 'Open steps'}</button>
            </>,
            hint: state.hint || undefined,
          }}
        />
      </ExpandAllProvider>
    </div>
  )
}

/** The models the session's machine offers it (GET .../models), or none. */
function useSessionModels(machine: string, sessionId: string): { id: string; name?: string }[] {
  const [models, setModels] = useState<{ id: string; name?: string }[]>([])
  useEffect(() => {
    let gone = false
    fetch(`/api/code/machines/${encodeURIComponent(machine)}/sessions/${encodeURIComponent(sessionId)}/models`)
      .then(r => (r.ok ? r.json() : []))
      .then((v: unknown) => { if (!gone && Array.isArray(v)) setModels(v.filter((m): m is { id: string; name?: string } => typeof m?.id === 'string')) })
      .catch(() => {})
    return () => { gone = true }
  }, [machine, sessionId])
  return models
}

/** The session's machine and folder, under the transcript. */
function SessionContext({ machineName, workspace }: { machineName: string; workspace: string }) {
  const folder = workspace.split('/').filter(Boolean).pop() ?? workspace
  return (
    <div className="pwcode-context" title={`${machineName}:${workspace}`}>
      <span className="pwcode-context-chip">{machineName}:{folder}</span>
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

/** The line over the composer while the session works or its feed is down. */
function SessionActivity({ activity }: { activity: PwCodeSessionActivity }) {
  switch (activity.kind) {
    case 'reconnecting':
      return <div className="pwcode-reconnecting muted">{activity.text}</div>
    case 'usageLimit':
      return <div className="pwcode-reconnecting muted">pw code waits for its usage limit to reset{activity.resumeAt ? `, at ${new Date(activity.resumeAt).toLocaleTimeString()}` : ''}.</div>
    case 'working':
      return <ActivityLine startedAt={activity.startedAt} inputTokens={activity.inputTokens} outputTokens={activity.outputTokens} word={activity.word} />
  }
}
