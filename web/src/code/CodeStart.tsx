import { type ReactNode, useMemo, useState } from 'react'
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
import { describeError } from '../pwcode/PWCodeSession'
import { type PageSession, pwCodePage } from './api'
import { AgentModelControls, useLastAgentModel } from './AgentModelControls'
import { DirectoryPicker } from './DirectoryPicker'
import { HowSessionsAppear, MachineIcon, SessionMark, type Translate, useStartAccount, useStartEffort, useStartMachine, useStartMode } from './parts'

/**
 * A new session, as PW Code on the web starts one (its AgentsStart): the
 * composer with the machine, the folder, the permission mode (held to the
 * machine's remote ceiling), the model and the effort; under it what waits
 * on the person, answered in place, and the sessions to pick back up. A
 * session started here opens in a Studio chat.
 */

type Machine = PwCodeStartMachine & PwCodeMachine

export function CodeStart({ onOpen, onStarted, top }: {
  onOpen: (s: Pick<PageSession, 'machineId' | 'id'> & { archivedAt?: string | null }) => void
  onStarted: (machine: string, id: string) => void
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
        ? <Composer location={location} finding={screen.loading} permission={startMode} onStarted={onStarted} t={t} />
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
                      meta={<>{multiMachine && machine && <span className="code-mono">{machine.name}</span>}<span>{sessionClock(s, compact)}</span></>}
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
                  {showMachine && machine && <span className="code-mono muted">{machine.name}</span>}
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

function machineMenuEntries(location: PwCodeStartLocation<Machine>, t: Translate): MenuEntry[] {
  const menu = machineMenu(location, t)
  const byId = machinesById([...location.startable, ...location.down])
  const icon = (id: string) => {
    const machine = byId.get(id)
    return machine && <MachineIcon machine={machine} />
  }
  const up = menu.startable.map(m => ({ label: m.label, description: m.description, icon: icon(m.id), checked: m.checked, onClick: () => menu.pick(m.id) }))
  const unavailable = menu.down.map(m => ({ label: m.label, description: m.description, icon: icon(m.id), disabled: true, onClick: () => menu.pick(m.id) }))
  if (menu.downLabel === undefined) return [...up, ...unavailable]
  return [...up, menuDivider, { label: menu.downLabel, items: unavailable }]
}

function MachineChip({ finding, machine, items, t }: { finding: boolean; machine: Machine | undefined; items: MenuEntry[]; t: Translate }) {
  const chip = machineChip(machine, t, finding)
  if (finding) {
    return <span data-testid="agents-finding-machines" className="code-chip muted"><Loader inline decorative size={12} />{chip.text}</span>
  }
  return (
    <ActionMenu
      label={chip.label}
      items={items}
      align="start"
      trigger={
        <button type="button" aria-label={chip.label} data-testid="agents-machine-chip" className="code-chip">
          {machine && <MachineIcon machine={machine} />}
          <span className="code-chip-label">{chip.text}</span>
          <ChevronDownIcon className="code-chip-caret" aria-hidden />
        </button>
      }
    />
  )
}

function Composer({ location, finding, permission, onStarted, t }: {
  location: PwCodeStartLocation<Machine>
  finding: boolean
  permission: PwCodeStartPermission
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
  const { disabled, canStart } = startControls(machine, starter.starting)
  const claudeReady = !!machine?.claudeCodeAllowed && !!machine?.claudeInstalled
  return (
    <ChatInput
      allowEmpty
      attachments={false}
      tools={false}
      requireModel={false}
      onSend={prompt => starter.start(prompt)}
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
          <MachineChip finding={finding} machine={machine} items={machineMenuEntries(location, t)} t={t} />
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
  )
}
