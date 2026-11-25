import type { BadgeVariant } from '@parallelworks/ui'

/** How a state lights its lamp. One meaning everywhere: work under way is
 *  `info` and pulses, work waiting its turn is `info` and still, work done is
 *  `success`, a failure is `error`, something that needs an answer or a look
 *  is `warning`, and idle or unknown is unlit. */
export interface StatusLook { variant?: BadgeVariant; off?: boolean; pulse?: boolean }

const LOOKS: Record<string, StatusLook> = {
  working: { variant: 'info', pulse: true },
  running: { variant: 'info', pulse: true },
  starting: { variant: 'info', pulse: true },
  started: { variant: 'info', pulse: true },
  provisioning: { variant: 'info', pulse: true },
  canceling: { variant: 'info', pulse: true },
  pending: { variant: 'info' },
  queued: { variant: 'info' },
  submitted: { variant: 'info' },
  completed: { variant: 'success' },
  'input-required': { variant: 'warning' },
  failed: { variant: 'error' },
  error: { variant: 'error' },
  refused: { variant: 'error' },
}

export const statusLook = (state: string): StatusLook => LOOKS[state] ?? { off: true }

/** A run's state as the package's run marks know it: one starting or in a
 *  queue would be drawn as not run. */
export const runLook = (state: string): string => RUN_LOOKS[state] ?? state
const RUN_LOOKS: Record<string, string> = {
  starting: 'started', provisioning: 'started', queued: 'pending', submitted: 'pending', cancelled: 'canceled', cancelling: 'canceling',
}

/** The run mark of a state that waits to start or is starting, or null. */
export const startingLook = (state: string): 'pending' | 'started' | null => {
  const look = runLook(state)
  return look === 'pending' || look === 'started' ? look : null
}
