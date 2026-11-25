import { type ReactNode, useEffect, useMemo, useState } from 'react'
import {
  machineChip,
  machineMenu,
  machinesById,
  type PwCodeMachine,
  type PwCodePicker,
  type PwCodeStartLocation,
  type PwCodeStartMachine,
  type PwCodeStartPermission,
  type PwCodeStartScreen,
  sessionClock,
  sessionKey,
  sessionLabel,
  shortlistMark,
  spansMachines,
  startControls,
  startShortlist,
  triageSessions,
  useCompactAgo,
  useStartLocation,
  useStartModel,
  useStartPermissionMode,
  useStartScreen,
  useStartSession,
  useTranscriptWords,
  waitingQuestions,
  waitingSince,
} from '@parallelworks/code'
import { ActionMenu, Loader, LoadingRegion, type MenuEntry, menuDivider, Section, Skeleton, StatusDot, toast } from '@parallelworks/ui'
import {
  type ApprovalAnswerValue,
  ApprovalBlock,
  ChatInput,
  ChatStage,
  NoticeCard,
  PermissionPicker,
  StageList,
  StageListRowContent,
  stageListRowClassName,
  useChat,
} from '@parallelworks/ui/ai'
import { ChevronDownIcon, ChevronRightIcon } from '@parallelworks/ui/icons'
import { useTranslations } from 'use-intl'
import { SystemChip } from '../components/SystemChip'
import { describeError } from '../pwcode/PWCodeSession'
import { type PageSession, pwCodePage, setToolsChoice } from './api'
import { AgentModelControls, useLastAgentModel } from './AgentModelControls'
import { DirectoryPicker } from './DirectoryPicker'
import { addsTools, asksTools, type MachineToolsControl, ToolsChoice, ToolsMark, ToolsMarkButton } from './machineTools'
import type { MachineLooks } from './machineIcon'
import { HowSessionsAppear, MachineIcon, SessionMark, type Translate, useStartAccount, useStartEffort, useStartMachine, useStartMode } from './parts'

/**
 * A new session, as PW Code on the web starts one (its AgentsStart): the
 * composer with the machine, the folder, the permission mode (held to the
 * machine's remote ceiling), the model and the effort; under it what waits
 * on the person, answered in place, and the sessions to pick back up. A
 * session started here opens in the page's pane.
 */

export type Machine = PwCodeStartMachine & PwCodeMachine & MachineLooks

/** A session being started: where, and its first message. */
export interface Starting {
  machine: string; folder: string; prompt: string
  /** Its icon. */
  on?: Machine
  /** The Studio's tools are added there first. */
  tools?: boolean
}

export function CodeStart({ onOpen, onStarting, onStarted, tools, top }: {
  onOpen: (s: Pick<PageSession, 'machineId' | 'id'> & { archivedAt?: string | null }) => void
  /** A start was sent (what), or ended without a session (null). */
  onStarting?: (s: Starting | null) => void
  onStarted: (machine: string, id: string) => void
  /** The Studio's tools on the machines. */
  tools: MachineToolsControl
  /** Above the composer: the session a link named. */
  top?: (answer: PwCodeStartScreen['answer']) => ReactNode
}) {
  const t = useTranslations('agents')
  const { data } = pwCodePage.useSessions()
  const { compact } = useCompactAgo()
  const machines = useMemo(() => (data?.machines ?? []) as Machine[], [data])
  const sessions = useMemo(() => data?.sessions ?? [], [data])
  const location = useStartLocation(machines, sessions, useStartMachine())
  const screen = useStartScreen(pwCodePage, { location, describeError })
  const startMode = useStartPermissionMode(useStartMode(), location.machine)
  const machineById = useMemo(() => machinesById(machines), [machines])
  const triage = useMemo(() => triageSessions(sessions, machineById), [sessions, machineById])
  const shortlist = startShortlist(triage)
  const multiMachine = spansMachines(sessions)

  if (screen.failed) {
    return (
      <div data-testid="agents-list-error" className="code-start-failed">
        <NoticeCard tone="danger">{screen.failed.message || 'The Studio could not list your pw code sessions.'}</NoticeCard>
      </div>
    )
  }
  return (
    <ChatStage heading={t('start.heading')} subheading={t('start.subheading')}>
      {top && <div className="code-start-top">{top(screen.answer)}</div>}
      {screen.showComposer
        ? <Composer location={location} finding={screen.loading} permission={startMode} tools={tools} onStarting={onStarting} onStarted={onStarted} t={t} />
        : <p data-testid="agents-start-none" className="code-start-none">{t('start.startHereNone')}</p>}
      <div className="code-start-below">
        {screen.loading && <ShortlistLoading t={t} />}
        {screen.explainEmpty && <HowSessionsAppear />}
        {triage.blocked.length > 0 && (
          <NeedsYou sessions={triage.blocked} machineById={machineById} showMachine={multiMachine} compact={compact} onAnswer={screen.answer} onOpen={onOpen} t={t} />
        )}
        {shortlist.length > 0 && (
          <StageList testId="agents-shortlist" flush>
            {shortlist.map(s => {
              const machine = machineById.get(s.machineId)
              const mark = shortlistMark(s, machine, t)
              return (
                <li key={sessionKey(s)}>
                  <button type="button" data-testid="agents-session-row" className={stageListRowClassName} onClick={() => onOpen(s)}>
                    <StageListRowContent
                      leading={mark && <SessionMark session={s} machine={machine} size="row" label={mark.label} />}
                      label={sessionLabel(s, t)}
                      meta={<>{multiMachine && machine && <MachineName machine={machine} />}<span>{sessionClock(s, compact)}</span></>}
                    />
                  </button>
                </li>
              )
            })}
          </StageList>
        )}
      </div>
    </ChatStage>
  )
}

/** Each session that waits on the person shows what it waits on, answered
 *  in place; its title opens the session. */
function NeedsYou({ sessions, machineById, showMachine, compact, onAnswer, onOpen, t }: {
  sessions: PageSession[]
  machineById: Map<string, PwCodeMachine>
  showMachine: boolean
  compact: (iso: string | undefined) => string
  onAnswer: PwCodeStartScreen['answer']
  onOpen: (s: Pick<PageSession, 'machineId' | 'id'> & { archivedAt?: string | null }) => void
  t: Translate
}) {
  const words = useTranscriptWords()
  const answer = async (s: PageSession, approvalId: string, value: ApprovalAnswerValue) => {
    const refused = await onAnswer(s, approvalId, value)
    if (refused) toast.error(refused)
  }
  return (
    <Section title={t('dash.needsYou')} collapsible={false}>
      <ul className="code-needs">
        {sessions.map(s => {
          const { questions, inSession } = waitingQuestions(s, words, t)
          const machine = machineById.get(s.machineId)
          return (
            <li key={sessionKey(s)}>
              <NoticeCard tone="warning" testId="agents-needs-row">
                <button type="button" className="code-needs-head" onClick={() => onOpen(s)}>
                  <StatusDot variant="warning" size={6} pulse />
                  <span className="code-needs-title">{sessionLabel(s, t)}</span>
                  {showMachine && machine && <MachineName machine={machine} muted />}
                  <span className="code-needs-when">{t('dash.waitingFor', { when: compact(waitingSince(s)) })}</span>
                  <ChevronRightIcon className="code-needs-chevron" />
                </button>
                {inSession === null
                  ? <div>{questions.map(part => <ApprovalBlock key={part.id} part={part} onApprovalAnswer={(id, value) => void answer(s, id, value)} />)}</div>
                  : <p className="code-needs-in muted">{inSession}</p>}
              </NoticeCard>
            </li>
          )
        })}
      </ul>
    </Section>
  )
}

const SHORTLIST_SKETCH = ['52%', '36%', '44%']

function ShortlistLoading({ t }: { t: Translate }) {
  return (
    <LoadingRegion label={t('sidebar.loading')}>
      <StageList testId="agents-shortlist-loading" flush>
        {SHORTLIST_SKETCH.map(width => (
          <li key={width}><span className="code-sketch-row"><Skeleton width={width} height={12} /><Skeleton width={24} height={10} /></span></li>
        ))}
      </StageList>
    </LoadingRegion>
  )
}

/** A machine's name after its icon. */
function MachineName({ machine, muted = false }: { machine: PwCodeMachine; muted?: boolean }) {
  return <span className={`code-machine-name code-mono${muted ? ' muted' : ''}`}><MachineIcon machine={machine} />{machine.name}</span>
}

/** The machine menu: each with its icon and the mark of the Studio's tools,
 *  then what can be done with them on the one chosen. */
export function machineMenuEntries(location: PwCodeStartLocation<Machine>, t: Translate, tools: MachineToolsControl): MenuEntry[] {
  const menu = machineMenu(location, t)
  const byId = machinesById([...location.startable, ...location.down])
  const icon = (id: string) => {
    const machine = byId.get(id)
    return machine && <MachineIcon machine={machine} />
  }
  const up = menu.startable.map(m => {
    const v = tools.view(byId.get(m.id))
    return {
      label: m.label,
      // The mark is only seen: the words are the row's.
      description: v ? `${m.description}, ${v.on ? 'with' : 'without'} this Studio's tools` : m.description,
      icon: icon(m.id), checked: m.checked, onClick: () => menu.pick(m.id),
      ...(v ? { badge: <ToolsMark on={v.on} /> } : {}),
    }
  })
  const own = tools.entries(location.machine)
  const unavailable = menu.down.map(m => ({ label: m.label, description: m.description, icon: icon(m.id), disabled: true, onClick: () => menu.pick(m.id) }))
  const rest = own.length ? [menuDivider, ...own] : []
  if (menu.downLabel === undefined) return [...up, ...unavailable, ...rest]
  return [...up, menuDivider, { label: menu.downLabel, items: unavailable }, ...rest]
}

export function MachineChip({ finding, machine, items, t }: { finding: boolean; machine: Machine | undefined; items: MenuEntry[]; t: Translate }) {
  const chip = machineChip(machine, t, finding)
  return (
    <SystemChip finding={finding} label={chip.label} text={chip.text} icon={machine && <MachineIcon machine={machine} />} items={items}
      testId="agents-machine-chip" findingTestId="agents-finding-machines" />
  )
}

function Composer({ location, finding, permission, tools, onStarting, onStarted, t }: {
  location: PwCodeStartLocation<Machine>
  finding: boolean
  permission: PwCodeStartPermission
  tools: MachineToolsControl
  onStarting?: (s: Starting | null) => void
  onStarted: (machine: string, id: string) => void
  t: Translate
}) {
  const { machine, directories, directory } = location
  const { selectedProvider, selectedAllocation, models, hasLoadedModels } = useChat()
  const [panel, setPanel] = useState<PwCodePicker>(null)
  const last = useLastAgentModel()
  const settings = useStartModel(pwCodePage, {
    machine,
    platform: { models: hasLoadedModels ? models : undefined, model: selectedProvider, allocation: selectedAllocation },
    last: last.last,
    account: useStartAccount(machine?.id),
    handedOff: false,
  })
  const { model, allocation, account } = settings
  const { saved: effort, remember: rememberEffort } = useStartEffort()
  const starter = useStartSession(pwCodePage, {
    machine,
    directory,
    mode: permission.mode,
    model,
    effort,
    allocation,
    account,
    onStarted,
  })
  const toolsThere = tools.view(machine)
  const asks = asksTools(toolsThere, machine)
  const [unticked, setUnticked] = useState<Record<string, boolean>>({})
  const ticked = !!machine && !unticked[machine.id]
  useEffect(() => {
    const id = machine?.id
    if (!id) return
    setToolsChoice(id, asks ? ticked : undefined)
    return () => setToolsChoice(id, undefined)
  }, [machine?.id, asks, ticked])
  const { disabled, canStart } = startControls(machine, starter.starting)
  const claudeReady = !!machine?.claudeCodeAllowed && !!machine?.claudeInstalled
  return <>
    <ChatInput
      allowEmpty
      attachments={false}
      tools={false}
      requireModel={false}
      onSend={async prompt => {
        // The page says where it is starting from the first moment; a start
        // that fails comes back here, with the message and the reason.
        onStarting?.({ machine: machine?.name ?? '', folder: directory, prompt, on: machine, tools: asks ? ticked : addsTools(toolsThere) })
        let took: Awaited<ReturnType<typeof starter.start>> | undefined
        try { took = await starter.start(prompt) } finally {
          if (!took) onStarting?.(null)
          // What the start kept or did of the machine's tools.
          void tools.reload()
        }
        return took
      }}
      disabled={!starter.ready}
      placeholder={t('start.composerPlaceholder')}
      inputLabel={t('start.composerPlaceholder')}
      sendLabel={t('start.startButton')}
      sendTestId="agents-start-button"
      notices={starter.error !== null && starter.error !== undefined && (
        <NoticeCard tone="danger" testId="agents-start-error">{describeError(starter.error)}</NoticeCard>
      )}
      context={
        <>
          <MachineChip finding={finding} machine={machine} items={machineMenuEntries(location, t, tools)} t={t} />
          {machine && <ToolsMarkButton machine={machine} control={tools} mode={permission.mode} />}
          {machine && <DirectoryPicker key={machine.id} machine={machine} directory={directory} recents={directories} onPick={location.pickDirectory} />}
        </>
      }
      settingsLeft={<PermissionPicker mode={permission.mode} ceiling={permission.ceiling} accepted={permission.accepted} onPick={permission.pick} />}
      settingsRight={
        <AgentModelControls
          key={machine?.id}
          api={pwCodePage}
          machine={machine?.id ?? ''}
          claudeReady={claudeReady}
          canStart={canStart}
          model={{ value: model, onChange: settings.pickModel, disabled: disabled.model }}
          effort={{ value: effort, onChange: rememberEffort, disabled: disabled.effort }}
          allocation={{ value: allocation, onChange: settings.pickAllocation, disabled: disabled.allocation }}
          account={account}
          panel={panel}
          onPanelChange={setPanel}
        />
      }
    />
    {asks && toolsThere?.tools && machine && (
      <ToolsChoice tools={toolsThere.tools} checked={ticked} disabled={starter.starting} onChange={on => setUnticked(u => ({ ...u, [machine.id]: !on }))} />
    )}
  </>
}
