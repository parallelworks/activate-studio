import { useUsageLimitWait } from '@parallelworks/code'
import { PauseIcon } from '@parallelworks/ui/icons'
import './UsageLimitWait.css'

export interface UsageLimitWaitProps {
  resumeAt: string | null | undefined
  /** Opens the session's accounts, where another one takes the turn over at once. */
  onOpenAccounts?: () => void
}

/** The running turn's line while it waits for a usage limit to reset. */
export function UsageLimitWait({ resumeAt, onOpenAccounts }: UsageLimitWaitProps) {
  const wait = useUsageLimitWait(resumeAt)
  const body = (
    <>
      <PauseIcon aria-hidden className="pwcode-limit-icon" />
      <span className="pwcode-limit-text">{wait.waiting}</span>
      {onOpenAccounts && <span className="pwcode-limit-switch">{wait.switchAccount}</span>}
    </>
  )
  if (!onOpenAccounts) return <div data-testid="pwcode-usage-limit-wait" className="pwcode-limit">{body}</div>
  return (
    <button type="button" data-testid="pwcode-usage-limit-wait" onClick={onOpenAccounts} className="pwcode-limit pwcode-limit-button">
      {body}
    </button>
  )
}
