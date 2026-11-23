import { useState } from 'react'
import { Button } from '@parallelworks/ui'
import { inAppWindow, reopenApp } from '../nativeShell'

const BY_HAND = 'Quit and reopen ACTIVATE Studio to finish.'

/**
 * For an update that changed the app's own window, which runs the old copy
 * until the app opens again: Reopen now asks the window to quit and open the
 * new copy. A browser, or a window from before the app could do that, gets
 * the words instead.
 *
 * compact is the footer's line, which fits the narrowest sidebar on one
 * line: "Update ready" with Reopen now as a quiet link, like the footer's
 * other actions, or "Reopen to update" where the app cannot reopen itself.
 */
export function ReopenApp({ compact = false }: { compact?: boolean }) {
  const [byHand, setByHand] = useState(() => !inAppWindow())
  const [busy, setBusy] = useState(false)
  if (compact && byHand) return <span className="identity-text">Reopen to update</span>
  if (byHand) return <span className="muted">{BY_HAND}</span>
  const reopen = async () => {
    setBusy(true)
    if (!(await reopenApp())) { setByHand(true); setBusy(false) }
  }
  if (compact) return (
    <>
      <span className="identity-text">Update ready</span>
      <Button variant="ghost" size="xs" className="footer-sync" aria-label="Reopen now" loading={busy} onClick={reopen}>
        <span>Reopen<span className="footer-long"> now</span></span>
      </Button>
    </>
  )
  return <Button variant="primary" size="sm" loading={busy} onClick={reopen}>Reopen now</Button>
}
