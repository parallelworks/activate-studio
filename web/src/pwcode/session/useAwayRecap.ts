import { type PwCodePresence, useAwayRecap as usePwCodeAwayRecap } from '@parallelworks/code'
import { readDraft } from '@parallelworks/ui/ai'
import { pwCode } from '../api'

export { recapAfterLeavingMs, recapAfterTurnMs } from '@parallelworks/code'

function away(): boolean {
  return document.visibilityState === 'hidden' || !document.hasFocus()
}

const presence: PwCodePresence = {
  away,
  watch: ({ leave, back }) => {
    const visibility = () => (away() ? leave() : back())
    window.addEventListener('blur', leave)
    window.addEventListener('focus', back)
    document.addEventListener('visibilitychange', visibility)
    return () => {
      window.removeEventListener('blur', leave)
      window.removeEventListener('focus', back)
      document.removeEventListener('visibilitychange', visibility)
    }
  },
}

/** Has the session recap (POST .../recap) while the reader is away from the
 *  page, as pw code does while its terminal is out of focus, so the recap is
 *  waiting when they come back. The machine declines while its recaps are off. */
export function useAwayRecap({
  machine,
  sessionId,
  draftKey,
  turnEndSeq,
  idle,
}: {
  machine: string
  sessionId: string
  /** The composer's draft key: nothing is asked for over a message being typed. */
  draftKey: string
  /** Changes each time the page sees a main turn end. */
  turnEndSeq: number | undefined
  /** Whether the session rests between turns and can be reached; read when the wait ends. */
  idle: boolean
}) {
  usePwCodeAwayRecap({
    api: pwCode,
    machine,
    sessionId,
    turnEndSeq,
    idle,
    drafting: () => !!readDraft(draftKey),
    presence,
  })
}
