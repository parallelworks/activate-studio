import type { ReactNode } from 'react'
import { ActionMenu, Loader, type MenuEntry } from '@parallelworks/ui'
import { ChevronDownIcon } from '@parallelworks/ui/icons'

/**
 * The chip that names a computer and opens the menu of the others: the Code
 * page's machine chip, a new chat's, and Add a library's.
 */
export function SystemChip({ label, text, title, icon, items, finding = false, search = false, testId, findingTestId }: {
  /** Names the chip and its menu. */
  label: string
  text: string
  /** Its tooltip, such as the computer's host name. */
  title?: string
  icon?: ReactNode
  items: MenuEntry[]
  /** Still being looked for: the text, with a spinner and no menu. */
  finding?: boolean
  /** A field atop the menu that narrows it. */
  search?: boolean
  testId?: string
  findingTestId?: string
}) {
  if (finding) {
    return <span data-testid={findingTestId} className="code-chip muted"><Loader inline decorative size={12} />{text}</span>
  }
  return (
    <ActionMenu
      label={label}
      items={items}
      align="start"
      {...(search ? { search: { placeholder: 'Find a computer', empty: 'None by that name' } } : {})}
      trigger={
        <button type="button" aria-label={label} title={title} data-testid={testId} className="code-chip">
          {icon}
          <span className="code-chip-label">{text}</span>
          <ChevronDownIcon className="code-chip-caret" aria-hidden />
        </button>
      }
    />
  )
}
