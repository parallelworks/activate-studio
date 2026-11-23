import {
  goalFacts,
  type PwCodeGoalLabel,
  type PwCodeGoalStatus,
  type PwCodeSessionGoal,
  type PwCodeTranslator,
  useGoalElapsed,
  usePwCodeTranslations,
  useSessionGoal,
} from '@parallelworks/code'
import { Button, Popover, StatusLabel, Textarea } from '@parallelworks/ui'
import { EditIcon, PauseIcon, StartIcon } from '@parallelworks/ui/icons'
import { type KeyboardEvent, type ReactNode, useId, useState } from 'react'
import { useFormatter } from 'use-intl'
import { pwCode } from '../api'
import { describeError as defaultDescribeError } from '../describeError'
import './GoalControl.css'

type Translate = PwCodeTranslator<'agents'>

export interface GoalControlProps {
  machine: string
  sessionId: string
  /** The chip's words and dot: the session screen's `goal`. */
  label: PwCodeGoalLabel
  /** The conversation's `goal`. */
  status: PwCodeGoalStatus
  /** The session takes no commands, such as while its machine is unreachable. */
  locked: boolean
  /** Words a refusal or failure; the session view's own by default. */
  describeError?: (error: unknown) => string
}

/** The session's goal at the end of the activity line, opening to what it is
 *  and how its run stands, where it can be edited, paused, resumed or cleared. */
export function GoalControl({ machine, sessionId, label, status, locked, describeError = defaultDescribeError }: GoalControlProps) {
  const t = usePwCodeTranslations('agents')
  const [open, setOpen] = useState(false)
  const goal = useSessionGoal(pwCode, machine, sessionId, { open, status, locked })
  const shown = useGoalElapsed(status, 'chip')
  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      side="top"
      align="end"
      title={t('detail.goalPanel.title')}
      className="pwcode-goal-panel"
      trigger={
        <button type="button" data-testid="agent-goal" className="pwcode-goal-chip">
          <StatusLabel className="pwcode-goal-label" variant={label.tone} off={label.off} pulse={label.pulse}>
            {label.text}
          </StatusLabel>
          {label.active && (
            <span data-testid="agent-goal-elapsed" className="pwcode-goal-elapsed">{shown}</span>
          )}
        </button>
      }
    >
      <GoalPanel goal={goal} status={status} t={t} describeError={describeError} />
    </Popover>
  )
}

function GoalPanel({ goal, status, t, describeError }: {
  goal: PwCodeSessionGoal
  status: PwCodeGoalStatus
  t: Translate
  describeError: (error: unknown) => string
}) {
  const failure = !!goal.error && (
    <p role="alert" className="pwcode-goal-error">{describeError(goal.error)}</p>
  )

  if (goal.view === 'loading') {
    return <p data-testid="agent-goal-loading" className="pwcode-goal-note">{goal.note}</p>
  }
  if (goal.view === 'none') {
    return (
      <div className="pwcode-goal-stack">
        {goal.note && <p className="pwcode-goal-note">{goal.note}</p>}
        {failure}
      </div>
    )
  }
  if (goal.view === 'editing') {
    return <GoalEditor goal={goal} failure={failure} t={t} />
  }
  const { lastCheck, toggle } = goal
  return (
    <div className="pwcode-goal-body">
      <div className="pwcode-goal-stack">
        <p data-testid="agent-goal-condition" className="pwcode-goal-condition">{goal.goal?.condition}</p>
        <GoalFacts goal={goal} status={status} t={t} />
      </div>
      {lastCheck && (
        <section className="pwcode-goal-check">
          <h3>{lastCheck.title}</h3>
          <p data-testid="agent-goal-reason">{lastCheck.reason}</p>
        </section>
      )}
      {goal.pausedNote && <p className="pwcode-goal-small">{goal.pausedNote}</p>}
      {failure}
      <div className="pwcode-goal-actions">
        <Button variant="outline" size="sm" icon={<EditIcon aria-hidden />} disabled={goal.busy} onClick={goal.startEdit}>
          {t('detail.goalPanel.edit')}
        </Button>
        <Button
          variant="outline"
          size="sm"
          icon={toggle.action === 'pause' ? <PauseIcon aria-hidden /> : <StartIcon aria-hidden />}
          loading={goal.pending === toggle.action}
          disabled={goal.busy}
          onClick={() => void goal[toggle.action]()}
        >
          {toggle.label}
        </Button>
        <Button
          variant="danger-outline"
          size="sm"
          className="pwcode-goal-clear"
          loading={goal.pending === 'clear'}
          disabled={goal.busy}
          onClick={() => void goal.clear()}
        >
          {t('detail.goalPanel.clear')}
        </Button>
      </div>
    </div>
  )
}

/** Where the run stands: its state, working time, the checks made and the tokens spent. */
function GoalFacts({ goal, status, t }: { goal: PwCodeSessionGoal; status: PwCodeGoalStatus; t: Translate }) {
  const format = useFormatter()
  const elapsed = useGoalElapsed(status, 'panel')
  const { state } = goal
  return (
    <p data-testid="agent-goal-facts" className="pwcode-goal-facts">
      <StatusLabel className="pwcode-goal-label" variant={state.tone} off={state.off} pulse={state.pulse}>
        {state.text}
      </StatusLabel>
      {goalFacts(elapsed, goal.goal ?? {}, t, format).map((fact, i) => (
        <span key={i} className="pwcode-goal-fact">
          <span aria-hidden="true">·</span>
          {fact}
        </span>
      ))}
    </p>
  )
}

function GoalEditor({ goal, failure, t }: { goal: PwCodeSessionGoal; failure: ReactNode; t: Translate }) {
  const hint = useId()
  const onKeyDown = (e: KeyboardEvent<HTMLTextAreaElement>) => {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) {
      e.preventDefault()
      void goal.save()
    }
  }
  return (
    <form
      className="pwcode-goal-body"
      onSubmit={e => {
        e.preventDefault()
        void goal.save()
      }}
    >
      <div className="pwcode-goal-stack">
        <Textarea
          data-testid="agent-goal-editor"
          aria-label={t('detail.goalPanel.conditionLabel')}
          aria-describedby={hint}
          value={goal.draft ?? ''}
          onChange={e => goal.setDraft(e.target.value)}
          onKeyDown={onKeyDown}
          autoFocus
          rows={3}
          autosize={{ maxRows: 10 }}
          disabled={goal.saving}
        />
        <p id={hint} className="pwcode-goal-small">{t('detail.goalPanel.editHint')}</p>
      </div>
      {failure}
      <div className="pwcode-goal-actions pwcode-goal-actions-end">
        <Button variant="ghost" size="sm" disabled={goal.saving} onClick={() => goal.setDraft(null)}>
          {t('detail.goalPanel.cancel')}
        </Button>
        <Button type="submit" variant="primary" size="sm" loading={goal.saving} disabled={!goal.canSave}>
          {t('detail.goalPanel.save')}
        </Button>
      </div>
    </form>
  )
}
