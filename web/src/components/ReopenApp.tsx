import { useState } from 'react'
import { Button } from '@parallelworks/ui'
import { inAppWindow, reopenApp } from '../nativeShell'

const BY_HAND = 'Quit and reopen ACTIVATE Studio to finish.'

/**
 * For an update that changed the app's own window, which runs the old copy
 * until the app opens again: Reopen now asks the window to quit and open the
 * new copy. A browser, or a window from before the app could do that, gets
 * the words instead; the footer's line, which says them itself, gets nothing.
 */
export function ReopenApp({ compact = false }: { compact?: boolean }) {
  const [byHand, setByHand] = useState(() => !inAppWindow())
  const [busy, setBusy] = useState(false)
  if (byHand) return compact ? null : <span className="muted">{BY_HAND}</span>
  return (
    <Button variant={compact ? 'secondary' : 'primary'} size={compact ? 'xs' : 'sm'} loading={busy} onClick={async () => {
      setBusy(true)
      if (!(await reopenApp())) { setByHand(true); setBusy(false) }
    }}>Reopen now</Button>
  )
}
