import { useState } from 'react'
import { CopyButton, RunStatusIcon, StatusDot, usePersistedState } from '@parallelworks/ui'
import { DesktopIcon, PauseIcon, PinFilledIcon, ClusterIcon } from '@parallelworks/ui/icons'
import {
  howSessionsAppear,
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
import { type MachineLooks, machineImage } from './machineIcon'

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

/** Images that did not load, so a row drawn again does not try them again. */
const broken = new Set<string>()

/** A machine's icon, as PW Code on the web picks it (machineIcon.ts): both
 *  themes' images are drawn and the theme shows one. A computer's or a
 *  server's glyph where the platform has none, or it did not load. */
export function MachineIcon({ machine }: { machine: Pick<PwCodeMachine, 'kind'> & MachineLooks }) {
  const [, setFailed] = useState(0)
  const image = machineImage(machine)
  if (!image || broken.has(image.light)) {
    return machine.kind === 'local'
      ? <DesktopIcon aria-hidden className="code-machine-icon" />
      : <ClusterIcon aria-hidden className="code-machine-icon" />
  }
  const fail = () => { broken.add(image.light); setFailed(n => n + 1) }
  if (!image.dark || broken.has(image.dark)) return <img src={image.light} alt="" className="code-machine-icon" data-testid="machine-icon" onError={fail} />
  const dark = image.dark
  return <>
    <img src={image.light} alt="" className="code-machine-icon on-light" data-testid="machine-icon" onError={fail} />
    <img src={dark} alt="" className="code-machine-icon on-dark" onError={() => { broken.add(dark); setFailed(n => n + 1) }} />
  </>
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
