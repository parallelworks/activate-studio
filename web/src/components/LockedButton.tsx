import type { ReactNode } from 'react'
import { Button, Tooltip, toast } from '@parallelworks/ui'

/**
 * A button that may not be used here, with the reason: on hover and focus
 * (a tooltip, and the button's accessible description), and on a click or a
 * tap, as a notice, since a tooltip never opens on touch and some engines
 * show none on a disabled control. Used where the main library is read only
 * for the person.
 */
export function LockedButton({ reason, children, variant = 'secondary', className }: {
  reason: string
  children: ReactNode
  variant?: 'primary' | 'secondary' | 'outline' | 'ghost'
  className?: string
}) {
  return (
    <span className="locked-button" data-reason={reason} onClick={() => toast.info(reason, { id: 'locked-button' })}>
      <Tooltip content={reason}>
        <Button variant={variant} size="sm" disabled focusableWhenDisabled aria-description={reason} className={className}>{children}</Button>
      </Tooltip>
    </span>
  )
}
