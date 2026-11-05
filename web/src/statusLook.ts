import type { BadgeVariant } from '@parallelworks/ui'

/** How a state lights its lamp. One meaning everywhere: work under way is
 *  `info` and pulses, work done is `success`, a failure is `error`, something
 *  that needs an answer or a look is `warning`, and idle or unknown is unlit. */
export interface StatusLook { variant?: BadgeVariant; off?: boolean; pulse?: boolean }

const LOOKS: Record<string, StatusLook> = {
  working: { variant: 'info', pulse: true },
  running: { variant: 'info', pulse: true },
  starting: { variant: 'info', pulse: true },
  completed: { variant: 'success' },
  'input-required': { variant: 'warning' },
  failed: { variant: 'error' },
  error: { variant: 'error' },
  refused: { variant: 'error' },
}

export const statusLook = (state: string): StatusLook => LOOKS[state] ?? { off: true }
