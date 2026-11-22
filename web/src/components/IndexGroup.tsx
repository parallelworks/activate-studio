import { type ReactNode, useId } from 'react'
import { Card } from '@parallelworks/ui'

/**
 * A group of settings on a tab of Settings, Indexing: a card like a
 * library's on the Libraries tab, with a heading, one line of help, and its
 * settings ruled off from one another. Each child is one setting or one
 * block (a table, a status), shown as it is; the card only frames it.
 */
export function IndexGroup({ title, help, actions, children, label }: {
  title: string
  help?: ReactNode
  /** At the heading's right, such as the index's controls. */
  actions?: ReactNode
  /** Names the group for assistive technology where its title would not. */
  label?: string
  children: ReactNode
}) {
  const id = useId()
  return (
    <Card size="fill" padding="none" interactive={false} className="ix-card"
      render={<section aria-labelledby={label ? undefined : id} aria-label={label} />}>
      <div className="ix-card-head">
        <div className="ix-card-title">
          <h2 id={id} className="ix-card-name">{title}</h2>
          {help && <p className="ix-card-help">{help}</p>}
        </div>
        {actions && <div className="ix-card-actions">{actions}</div>}
      </div>
      <div className="ix-card-body">{children}</div>
    </Card>
  )
}
