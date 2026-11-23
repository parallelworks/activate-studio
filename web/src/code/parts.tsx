import { CopyButton, RunStatusIcon, StatusDot, usePersistedState } from '@parallelworks/ui'
import { DesktopIcon, PauseIcon, PinFilledIcon, ClusterIcon } from '@parallelworks/ui/icons'
import {
  howSessionsAppear,
  isComputer,
  type PwCodeMachine,
  type PwCodePreference,
  type PwCodeSession,
  sessionMark,
  startEffortPreference,
  startMachinePreference,
  startModePreference,
  startAccountPreference,
  useStartAccountPreference,
  useStartEffortPreference,
  useStartMachinePreference,
  useStartModePreference,
} from '@parallelworks/code'
import { useTranslations } from 'use-intl'

/**
 * The small parts of PW Code on the web's Code page that are its app's own
 * rather than the packages', written again for the Studio (each is on the
 * list of requests to the packages' owners): the mark a session's state
 * shows, a machine's icon, how sessions get here, and where the start's
 * choices are kept. The platform keeps those choices in the person's
 * account; the Studio keeps them in this browser.
 */

export type Translate = ReturnType<typeof useTranslations<'agents'>>

/** A pw code preference, kept in this browser. */
export function useSavedPreference<T>({ key, fallback }: PwCodePreference<T>) {
  return usePersistedState<T>(`studio.code.${key}`, fallback)
}

export function useStartMode() {
  return useStartModePreference(useSavedPreference(startModePreference))
}
export function useStartEffort() {
  return useStartEffortPreference(useSavedPreference(startEffortPreference))
}
export function useStartMachine() {
  return useStartMachinePreference(useSavedPreference(startMachinePreference))
}
export function useStartAccount(machine: string | undefined) {
  return useStartAccountPreference(useSavedPreference(startAccountPreference), machine)
}

/** A session at work turns, in the mark a running workflow run has. */
export function SessionMark({ session, machine, size, label }: {
  session: PwCodeSession
  machine: PwCodeMachine | undefined
  size: 'row' | 'rail'
  /** Names the state where nothing beside the mark spells it out. */
  label?: string | undefined
}) {
  const mark = sessionMark(session, machine)
  return (
    <span {...(label ? { role: 'img', 'aria-label': label, title: label } : {})} className={`code-mark code-mark-${size}`}>
      {mark.working ? (
        <RunStatusIcon data-testid="agent-session-working" status="running" label="" />
      ) : mark.usageLimit ? (
        <PauseIcon data-testid="agent-session-usage-limit" strokeWidth={2.5} className="code-mark-limit" />
      ) : (
        <StatusDot variant={mark.tone} size={size === 'row' ? 6 : 10} pulse={mark.pulse} />
      )}
    </span>
  )
}

/** A pinned session's mark. */
export function PinnedMark({ t }: { t: Translate }) {
  return (
    <span data-testid="agent-session-pinned" role="img" aria-label={t('sidebar.pinned')} title={t('sidebar.pinned')} className="code-pinned">
      <PinFilledIcon />
    </span>
  )
}

/** A machine's icon: the one the platform gives a resource, else a
 *  computer's or a server's. */
export function MachineIcon({ machine }: { machine: PwCodeMachine & { imageUrl?: string } }) {
  if (!isComputer(machine) && machine.imageUrl) {
    return <img src={machine.imageUrl} alt="" className="code-machine-icon" onError={e => { e.currentTarget.style.display = 'none' }} />
  }
  return isComputer(machine)
    ? <DesktopIcon aria-hidden className="code-machine-icon" />
    : <ClusterIcon aria-hidden className="code-machine-icon" />
}

function CopyCommand({ command }: { command: string }) {
  return (
    <span className="code-how-command">
      <code>{command}</code>
      <CopyButton quiet text={command} label={command} />
    </span>
  )
}

/** Nothing is listed until its owner opens it: the empty page says how, and
 *  the help button says it again once sessions are listed. */
export function HowSessionsAppear({ compact = false }: { compact?: boolean }) {
  const how = howSessionsAppear(useTranslations('agents'))
  return (
    <section data-testid="agents-how" className={`code-how${compact ? ' compact' : ''}`}>
      <h2>{how.title}</h2>
      <ol>
        {how.steps.map(step => (
          <li key={step.command}>
            {step.before}
            <CopyCommand command={step.command} />
            {step.after}
          </li>
        ))}
      </ol>
    </section>
  )
}
