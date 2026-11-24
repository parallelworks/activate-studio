import type { ReactNode } from 'react'
import { Card, DisclosurePanel } from '@parallelworks/ui'
import { ChevronRightIcon } from '@parallelworks/ui/icons'
import type { AccountRowId } from '../accounts'

/**
 * One account on Settings, Accounts: a row with the account's name, one
 * line on what it is for and its state, which opens to the account's
 * settings. Every account is one of these, so none stands out over another.
 * The row's heading holds the button that opens it (aria-expanded), as the
 * Status panel's cards do; the button is named for the account, and its
 * line and state describe it. The panel stays mounted while closed, so a
 * form half filled in keeps what was typed.
 */
export function AccountRow({ id, title, description, state, open, onOpenChange, children }: {
  id: AccountRowId
  title: string
  description: ReactNode
  /** The account's state, at the row's right. */
  state: ReactNode
  open: boolean
  onOpenChange: (open: boolean) => void
  children: ReactNode
}) {
  const name = `account-${id}-name`
  const panel = `account-${id}-panel`
  return (
    <Card size="fill" padding="none" interactive={false} className="ix-card account-row"
      render={<section id={`account-${id}`} data-account={id} aria-labelledby={name} />}>
      <h2 className="account-row-heading">
        <button type="button" className="account-row-head" aria-expanded={open} aria-controls={panel}
          aria-labelledby={name} aria-describedby={`account-${id}-help account-${id}-state`}
          onClick={() => onOpenChange(!open)}>
          <span className="account-row-title">
            <span id={name} className="ix-card-name account-row-name">{title}</span>
            <span id={`account-${id}-help`} className="ix-card-help account-row-help">{description}</span>
          </span>
          <span id={`account-${id}-state`} className="account-row-state">{state}</span>
          <ChevronRightIcon aria-hidden="true" size={14} className={`account-row-chev${open ? ' open' : ''}`} />
        </button>
      </h2>
      <DisclosurePanel open={open} id={panel} keepMounted>
        <div className="ix-card-body account-row-body">{children}</div>
      </DisclosurePanel>
    </Card>
  )
}
