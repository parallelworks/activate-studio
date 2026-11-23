import type { ReactNode } from 'react'
import { Button, Callout } from '@parallelworks/ui'
import { SIGN_IN_TO_USE, openAccounts } from '../accounts'

/**
 * The one prompt every ACTIVATE feature shows when the person has no
 * ACTIVATE credential for it: "Sign in with ACTIVATE to use this", with
 * Open Accounts. `children` says what the feature is, or what the server
 * said; `onOpen` replaces opening Accounts by its address (a dialog that
 * has to close first).
 */
export function NeedsActivate({ children, onOpen, className }: { children?: ReactNode; onOpen?: () => void; className?: string }) {
  return (
    <Callout tone="info" className={['needs-activate', className].filter(Boolean).join(' ')} title={SIGN_IN_TO_USE}
      action={<Button variant="secondary" size="sm" onClick={() => (onOpen ? onOpen() : openAccounts())}>Open Accounts</Button>}>
      {children}
    </Callout>
  )
}
