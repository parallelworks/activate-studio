import type { PwCodeUpdateNotice } from '@parallelworks/code'
import { Button } from '@parallelworks/ui'
import { NoticeCard } from '@parallelworks/ui/ai'
import { useFormatter } from 'use-intl'
import './PwUpdateNotice.css'

/** The newer pw for the session's machine: which it is, how far installing it
 *  got, and the button that runs /upgrade on the session to install it. */
export function PwUpdateNotice({ notice, starting, onUpdate, onDismiss }: {
  notice: PwCodeUpdateNotice
  /** /upgrade was asked for and has yet to answer. */
  starting: boolean
  onUpdate: () => void
  onDismiss: () => void
}) {
  const format = useFormatter()
  return (
    <NoticeCard tone={notice.tone} onDismiss={onDismiss} testId="pw-update-notice">
      <span className="pwcode-update">
        <span>{notice.text}</span>
        {notice.progress !== undefined && (
          <span className="pwcode-update-progress muted">{format.number(notice.progress / 100, { style: 'percent' })}</span>
        )}
        {notice.action && <Button variant="outline" size="xs" disabled={starting} aria-busy={starting} onClick={onUpdate}>{notice.action}</Button>}
      </span>
    </NoticeCard>
  )
}
