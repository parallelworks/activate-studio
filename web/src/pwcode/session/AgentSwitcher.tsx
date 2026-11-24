import { Button, IconButton, RunStatusIcon, StatusDot } from '@parallelworks/ui'
import { ActivityLine } from '@parallelworks/ui/ai'
import {
  agentMark,
  type PwCodeAgentState,
  type PwCodeAgentSwitcher,
  type PwCodeConversation,
  type PwCodeSessionActivity,
  type PwCodeStateTone,
  type PwCodeSwitcherAgent,
  type PwCodeSwitcherCount,
  type PwCodeSwitcherRow,
  switcherAgent,
  useDismissSubagent,
  usePwCodeAgentLine,
  usePwCodeAgentSwitcher,
} from '@parallelworks/code'
import {
  ArrowLeftIcon,
  ChevronDownIcon,
  ChevronRightIcon,
  ChevronUpIcon,
  CloseIcon,
  DisconnectIcon,
  StopIcon,
} from '@parallelworks/ui/icons'
import {
  type ReactNode,
  type RefObject,
  useCallback,
  useEffect,
  useEffectEvent,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from 'react'
import { useTranslations } from 'use-intl'
import './AgentSwitcher.css'

export { type PwCodeSwitcherAgent as SwitcherAgent, switcherAgent } from '@parallelworks/code'

function cx(...names: (string | false | null | undefined)[]): string {
  return names.filter(Boolean).join(' ')
}

function AgentMark({ state }: { state: PwCodeAgentState | 'idle' }) {
  const mark = agentMark(state)
  return (
    <span className="pwcode-agent-mark">
      {mark.working ? (
        <RunStatusIcon status="running" label="" />
      ) : (
        <StatusDot size={8} variant={mark.tone} pulse={mark.pulse} off={mark.off} />
      )}
    </span>
  )
}

const toneClass: Record<PwCodeStateTone, string> = {
  warning: 'pwcode-tone-warning',
  error: 'pwcode-tone-error',
  muted: 'pwcode-tone-muted',
}

function scrollParent(el: HTMLElement): HTMLElement | null {
  for (let parent = el.parentElement; parent; parent = parent.parentElement) {
    const { overflowY } = getComputedStyle(parent)
    if (overflowY === 'auto' || overflowY === 'scroll') return parent
  }
  return null
}

const AT_LATEST_PX = 16

/** The row sits in the composer's dock over the transcript's end: when it
 *  grows, a reader at the latest message stays there. */
function useKeepLatestInView(ref: RefObject<HTMLElement | null>) {
  useEffect(() => {
    const strip = ref.current
    if (!strip || typeof ResizeObserver === 'undefined') return
    let height = 0
    const observer = new ResizeObserver(() => {
      const grew = strip.offsetHeight - height
      height = strip.offsetHeight
      const scroller = scrollParent(strip)
      if (!scroller || grew <= 0) return
      const fromLatest = scroller.scrollHeight - scroller.scrollTop - scroller.clientHeight
      if (fromLatest - grew < AT_LATEST_PX) scroller.scrollTop = scroller.scrollHeight
    })
    observer.observe(strip)
    return () => observer.disconnect()
  }, [ref])
}

/** Draws the turn's activity line, without its token counts when `brief`. */
export type StatusActivity = ((brief: boolean) => ReactNode) | null

/** Where focus goes once a removed agent's row leaves the list: the row after
 *  it, or before it, or the main conversation's when `then` is null. */
export interface RemovalFocus {
  removed: string
  then: string | null
}

function focusLost(): boolean {
  const active = document.activeElement
  return !active || active === document.body || !active.isConnected
}

function rowButton(list: HTMLElement, id: string): HTMLElement | null {
  const row = Array.from(list.querySelectorAll<HTMLElement>('li[data-agent]')).find(li => li.getAttribute('data-agent') === id)
  return row?.querySelector('button') ?? null
}

/** Puts focus on a neighbouring row once the removed one leaves, instead of
 *  dropping it to the top of the page. */
function useFocusAfterRemoval(list: RefObject<HTMLUListElement | null>, focus: RefObject<RemovalFocus | null>, shown: string[]) {
  const listed = shown.join(' ')
  useEffect(() => {
    const pending = focus.current
    if (!pending || listed.split(' ').includes(pending.removed)) return
    focus.current = null
    if (list.current && focusLost()) {
      ;(rowButton(list.current, pending.then ?? '') ?? rowButton(list.current, ''))?.focus()
    }
  }, [list, focus, listed])
}

/** Whether the line drops token counts and tallies to stay on one line. It
 *  is measured in full form, and tries it again once it has the room. */
function useBriefLine(lineRef: RefObject<HTMLElement | null>, holds: string): boolean {
  const [brief, setBrief] = useState<{ holds: string; needs: number } | null>(null)
  const refit = useEffectEvent(() => {
    const line = lineRef.current
    if (!line) return
    if (brief?.holds !== holds) {
      if (line.scrollWidth > line.clientWidth + 1) setBrief({ holds, needs: line.scrollWidth })
    } else if (line.clientWidth >= brief.needs) {
      setBrief(null)
    }
  })
  useLayoutEffect(() => { refit() })
  // Elapsed times tick inside the line's ends without a render here.
  useLayoutEffect(() => {
    const line = lineRef.current
    if (!line || typeof ResizeObserver === 'undefined') return
    const observer = new ResizeObserver(() => refit())
    for (const el of [line, ...Array.from(line.children)]) observer.observe(el)
    return () => observer.disconnect()
  }, [lineRef])
  return brief?.holds === holds
}

function AgentsToggle({
  brief, summary, working, approvals, failed, open, listId, onOpenChange, onView,
}: Pick<PwCodeAgentSwitcher, 'summary' | 'working' | 'approvals' | 'failed'> & {
  brief: boolean
  open: boolean
  listId: string
  onOpenChange: (open: boolean) => void
  onView: (id: string) => void
}) {
  const t = useTranslations('agents')
  const tally = (count: PwCodeSwitcherCount) =>
    brief ? <><span aria-hidden>{count.count}</span><span className="sr-only">{count.label}</span></> : count.label
  return (
    <div data-testid="agent-switcher" className="pwcode-agents-toggle">
      {approvals && (
        <Button type="button" variant="ghost" size="xs" icon={<StatusDot variant="warning" size={7} pulse />} onClick={() => onView(approvals.first)}>
          {tally(approvals)}
        </Button>
      )}
      {failed && (
        <Button type="button" variant="ghost" size="xs" icon={<StatusDot variant="error" size={7} />} onClick={() => onView(failed.first)}>
          {tally(failed)}
        </Button>
      )}
      <button type="button" aria-expanded={open} aria-controls={listId} onClick={() => onOpenChange(!open)} className="pwcode-agents-open">
        <span className="pwcode-agents-title">{t('switcher.title')}</span>
        {working && <AgentMark state="running" />}
        <span className={cx('pwcode-agents-summary', brief && 'sr-only')}>{summary}</span>
        {open ? <ChevronDownIcon aria-hidden className="pwcode-icon" /> : <ChevronUpIcon aria-hidden className="pwcode-icon" />}
      </button>
    </div>
  )
}

export interface AgentSwitcherProps {
  activity: StatusActivity
  goal?: ReactNode
  tasks?: ReactNode
  agents: PwCodeSwitcherAgent[]
  mainState: 'running' | 'approval' | 'idle'
  /** The subagent in view; null for the main conversation. */
  viewing: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
  earlierOpen: boolean
  onEarlierOpenChange: (open: boolean) => void
  onView: (id: string | null) => void
  onStop: (id: string) => void
  onStopAll: () => void
  /** Takes an agent that is not running off the list, resolving to whether it went. */
  onDismiss: (id: string) => Promise<boolean>
  /** Kept above the row, since removing the agent in view remounts the list in the main conversation. */
  focusAfterRemoval: RefObject<RemovalFocus | null>
}

/** The composer's status row: what the turn is doing and, at its end, the
 *  session's goal and agents. Opened, the agents list under it: the main
 *  conversation and each subagent, earlier turns' ones folded. Under all of
 *  it, the conversation's task list. */
export function AgentSwitcher({
  activity, goal, tasks, agents, mainState, viewing, open, onOpenChange, earlierOpen, onEarlierOpenChange,
  onView, onStop, onStopAll, onDismiss, focusAfterRemoval,
}: AgentSwitcherProps) {
  const t = useTranslations('agents')
  const listId = useId()
  const statusRef = useRef<HTMLDivElement>(null)
  useKeepLatestInView(statusRef)
  const { main, agents: rows, earlier, summary, working, approvals, failed, running } =
    usePwCodeAgentSwitcher({ agents, mainState, viewing, open, earlierOpen })
  const listRef = useRef<HTMLUListElement>(null)
  // In the order the list draws them: this turn's, then the earlier ones shown.
  const shown = rows.filter(r => !r.folded).map(r => r.agent.id)
  useFocusAfterRemoval(listRef, focusAfterRemoval, shown)
  const dismiss = (id: string, button: HTMLElement) => {
    if (button === document.activeElement) {
      const at = shown.indexOf(id)
      focusAfterRemoval.current = { removed: id, then: shown[at + 1] ?? shown[at - 1] ?? null }
    }
    void onDismiss(id).then(taken => {
      if (!taken && focusAfterRemoval.current?.removed === id) focusAfterRemoval.current = null
    })
  }
  const lineRef = useRef<HTMLDivElement>(null)
  const brief = useBriefLine(lineRef, [
    activity ? 'activity' : '', goal ? 'goal' : '', working ? 'working' : '', summary, approvals?.count ?? 0, failed?.count ?? 0,
  ].join('|'))

  const row = ({ agent, ...line }: PwCodeSwitcherRow) => (
    <li key={agent.id} data-testid="agent-switcher-row" data-agent={agent.id} className={cx('pwcode-agent-row', line.viewing && 'is-viewing')}>
      <button type="button" aria-current={line.viewing ? 'true' : undefined} onClick={() => onView(agent.id)} className="pwcode-agent-pick">
        <AgentMark state={line.state} />
        <span className="pwcode-agent-type">{line.type}</span>
        <span className="pwcode-agent-desc">{line.description}</span>
        <span className={cx('pwcode-agent-state', line.flagged ? toneClass[line.stateTone] : 'sr-only')}>{line.stateWord}</span>
        <span className="pwcode-agent-time">
          {line.elapsed}
          {line.tokens && <span className="pwcode-agent-tokens">{line.elapsed !== null && ' · '}{line.tokens}</span>}
        </span>
      </button>
      <span className="pwcode-agent-action">
        {line.stoppable ? (
          <IconButton variant="ghost" size="xs" icon={<StopIcon aria-hidden className="pwcode-icon" />} label={line.stopLabel} onClick={() => onStop(agent.id)} />
        ) : (
          <IconButton
            variant="ghost"
            size="xs"
            icon={<CloseIcon aria-hidden className="pwcode-icon" />}
            label={line.dismissLabel}
            onClick={event => dismiss(agent.id, event.currentTarget)}
            className={cx('pwcode-agent-dismiss', line.viewing && 'is-shown')}
          />
        )}
      </span>
    </li>
  )

  const listed = agents.length > 0
  return (
    <div ref={statusRef} data-testid="composer-status" className="pwcode-status">
      {/* Hidden, not left out, when only the tasks show: its resize observer attaches once. */}
      <div ref={lineRef} className="pwcode-status-line" hidden={!activity && !goal && !listed}>
        <div className={brief ? 'pwcode-status-activity is-brief' : 'pwcode-status-activity'}>{activity?.(brief)}</div>
        <div className="pwcode-status-end">
          {goal}
          {listed && (
            <AgentsToggle brief={brief} summary={summary} working={working} approvals={approvals} failed={failed}
              open={open} listId={listId} onOpenChange={onOpenChange} onView={onView} />
          )}
        </div>
      </div>
      {listed && open && (
        <section id={listId} aria-label={t('switcher.title')} className="pwcode-agents-list">
          <ul ref={listRef}>
            <li data-testid="agent-switcher-row" data-agent="" className={cx('pwcode-agent-row', main.viewing && 'is-viewing')}>
              <button type="button" aria-current={main.viewing ? 'true' : undefined} onClick={() => onView(null)} className="pwcode-agent-pick">
                <AgentMark state={main.state} />
                <span className="pwcode-agent-desc pwcode-agent-main">{t('switcher.main')}</span>
                <span className={cx('pwcode-agent-state', main.flagged ? toneClass[main.stateTone] : 'sr-only')}>{main.stateWord}</span>
              </button>
              <span className="pwcode-agent-action" />
            </li>
            {rows.filter(r => r.agent.current).map(row)}
            {earlier && (
              <li>
                <button type="button" aria-expanded={earlierOpen} onClick={() => onEarlierOpenChange(!earlierOpen)} className="pwcode-agents-earlier">
                  {earlierOpen ? <ChevronDownIcon aria-hidden className="pwcode-icon" /> : <ChevronRightIcon aria-hidden className="pwcode-icon" />}
                  {earlier.label}
                </button>
              </li>
            )}
            {rows.filter(r => !r.agent.current && !r.folded).map(row)}
          </ul>
          {running > 0 && (
            <div className="pwcode-agents-foot">
              <Button type="button" variant="ghost" size="xs" icon={<StopIcon aria-hidden className="pwcode-icon" />} onClick={onStopAll}>
                {t('switcher.stopAll')}
              </Button>
            </div>
          )}
        </section>
      )}
      {tasks}
    </div>
  )
}

/** Says which subagent the transcript under it is, over that transcript. */
export function SubagentBar({ agent, stopping, onStop, onBack }: {
  /** Unset until the subagent's own details load. */
  agent: PwCodeSwitcherAgent | undefined
  stopping: boolean
  onStop: () => void
  onBack: () => void
}) {
  const t = useTranslations('agents')
  const line = usePwCodeAgentLine()
  const shown = agent && line(agent)
  return (
    <div data-testid="subagent-bar" className="pwcode-subagent-bar">
      {shown && (
        <span className="pwcode-subagent-name">
          <AgentMark state={shown.state} />
          <span className="pwcode-agent-type">{shown.type}</span>
          <span className="pwcode-agent-desc">{shown.description}</span>
        </span>
      )}
      <span className="pwcode-subagent-actions">
        {shown && <span className={cx('pwcode-agent-state', toneClass[shown.stateTone])}>{shown.stateWord}</span>}
        {shown?.stoppable && (
          <Button type="button" variant="ghost" size="xs" loading={stopping} onClick={onStop}>{t('switcher.stopThis')}</Button>
        )}
        <Button type="button" variant="outline" size="xs" icon={<ArrowLeftIcon aria-hidden className="pwcode-icon" />} onClick={onBack}>
          {t('switcher.back')}
        </Button>
      </span>
    </div>
  )
}

/** The line over the composer while the session works or its feed is down. */
export function SessionActivity({ activity, brief = false }: {
  activity: PwCodeSessionActivity
  /** Leave the token counts out, for a line with no room for them. */
  brief?: boolean
}) {
  switch (activity.kind) {
    case 'reconnecting':
      return (
        <div data-testid="agent-reconnecting" className="pwcode-status-reconnecting">
          <DisconnectIcon aria-hidden className="pwcode-icon" />
          <span className="pwcode-ellipsis">{activity.text}</span>
        </div>
      )
    case 'usageLimit':
      return (
        <div className="pwcode-status-reconnecting">
          <span className="pwcode-ellipsis">
            pw code waits for its usage limit to reset{activity.resumeAt ? `, at ${new Date(activity.resumeAt).toLocaleTimeString()}` : ''}.
          </span>
        </div>
      )
    case 'working':
      return (
        <ActivityLine testId="agent-activity" startedAt={activity.startedAt}
          inputTokens={brief ? undefined : activity.inputTokens} outputTokens={brief ? undefined : activity.outputTokens} word={activity.word} />
      )
  }
}

/** The switcher's open state, kept above both the main and the subagent view
 *  so the list stays as it was when the view changes between them. */
export interface SwitcherState {
  open: boolean
  setOpen: (open: boolean) => void
  earlierOpen: boolean
  setEarlierOpen: (open: boolean) => void
  focusAfterRemoval: RefObject<RemovalFocus | null>
}

export function useSwitcherState(): SwitcherState {
  const [open, setOpen] = useState(false)
  const [earlierOpen, setEarlierOpen] = useState(false)
  const focusAfterRemoval = useRef<RemovalFocus | null>(null)
  return { open, setOpen, earlierOpen, setEarlierOpen, focusAfterRemoval }
}

/** The session's subagents as the switcher lists them, with the feed's latest token counts. */
export function useSessionAgents(conversation: Pick<PwCodeConversation, 'subagents' | 'liveState'>): PwCodeSwitcherAgent[] {
  const { subagents, liveState } = conversation
  const usage = liveState?.subagentUsage
  return useMemo(() => subagents.map(s => switcherAgent(s, usage?.[s.id])), [subagents, usage])
}

export interface SessionStatusRowProps {
  conversation: PwCodeConversation
  /** The session screen's activity (`usePwCodeSessionScreen(...).activity`),
   *  drawn as the line's start when `activity` is not given. */
  screen?: { activity: PwCodeSessionActivity | null } | undefined
  /** The line's start in place of the screen's, such as a subagent's own line. */
  activity?: StatusActivity | undefined
  /** The goal chip, drawn at the line's end before the agents. */
  goal?: ReactNode
  /** The task list, drawn under the line and the agents list. */
  tasks?: ReactNode
  /** The subagent in view; undefined for the main conversation. */
  agent: string | undefined
  onSelectAgent: (id: string | undefined) => void
  /** From `useSwitcherState()` in the host; else the row keeps its own, which
   *  resets when the view changes between main and a subagent. */
  switcher?: SwitcherState | undefined
}

/** The composer's status row wired to a session, as PW Code web's session
 *  page builds it: pass as the composer's `activity`. Draws nothing while
 *  there is no activity, goal, task or agent. */
export function SessionStatusRow({ conversation, screen, activity, goal, tasks, agent, onSelectAgent, switcher }: SessionStatusRowProps) {
  const own = useSwitcherState()
  const state = switcher ?? own
  const agents = useSessionAgents(conversation)
  const showMain = useCallback(() => onSelectAgent(undefined), [onSelectAgent])
  const dismiss = useDismissSubagent(conversation, { viewing: agent ?? null, showMain })
  const screenActivity = screen?.activity
  const line: StatusActivity = activity !== undefined
    ? activity
    : screenActivity ? brief => <SessionActivity activity={screenActivity} brief={brief} /> : null
  if (!line && !goal && !tasks && agents.length === 0) return null
  return (
    <AgentSwitcher
      activity={line}
      goal={goal}
      tasks={tasks}
      agents={agents}
      mainState={conversation.mainState}
      viewing={agent ?? null}
      open={state.open}
      onOpenChange={state.setOpen}
      earlierOpen={state.earlierOpen}
      onEarlierOpenChange={state.setEarlierOpen}
      onView={id => onSelectAgent(id ?? undefined)}
      onStop={id => void conversation.stopSubagent(id)}
      onStopAll={() => void conversation.stopAllSubagents()}
      onDismiss={dismiss}
      focusAfterRemoval={state.focusAfterRemoval}
    />
  )
}
