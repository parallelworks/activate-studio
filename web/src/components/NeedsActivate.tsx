import type { ReactNode } from 'react'
import { Button, Callout } from '@parallelworks/ui'
import { SIGN_IN_STOPPED, SIGN_IN_TO_USE, openAccounts } from '../accounts'
import { useAppConfig } from '../config'

/**
 * The one prompt every ACTIVATE feature shows when the person has no
 * ACTIVATE credential for it: "Sign in with ACTIVATE to use this", with
 * Open Accounts, which opens its ACTIVATE row. `children` says what the feature is, or what the server
 * said; `onOpen` replaces opening Accounts by its address (a dialog that
 * has to close first). Where the person's kept sign-in stopped working, it
 * says so instead, with Sign in again, which opens the same row.
 */
export function NeedsActivate({ children, onOpen, className }: { children?: ReactNode; onOpen?: () => void; className?: string }) {
  const stopped = !!useAppConfig().signInStopped
  return (
    <Callout tone={stopped ? 'warning' : 'info'} className={['needs-activate', className].filter(Boolean).join(' ')} title={stopped ? SIGN_IN_STOPPED : SIGN_IN_TO_USE}
      action={<Button variant="secondary" size="sm" onClick={() => (onOpen ? onOpen() : openAccounts('activate'))}>{stopped ? 'Sign in again' : 'Open Accounts'}</Button>}>
      {stopped && <p className="identity-note">The platform no longer accepts it. Sign in again under Settings, Accounts.</p>}
      {children}
    </Callout>
  )
}
