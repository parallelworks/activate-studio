import { useId, useState } from 'react'
import { useTodoPanel } from '@parallelworks/code'
import type { TodoItem } from '@parallelworks/ui/ai'
import { ChevronDownIcon, ChevronRightIcon } from '@parallelworks/ui/icons'
import './SessionTasks.css'

const MARK: Record<TodoItem['status'], string> = {
  completed: '✔',
  in_progress: '■',
  pending: '□',
}

export interface SessionTasksProps {
  todos: TodoItem[]
  /** Kept by the view, so a fold outlasts the list; held here when left out. */
  open?: boolean
  onOpenChange?: (open: boolean) => void
}

/** The conversation's task list, pinned above the composer as the terminal
 *  pins it, with the running task in view. Folded, it names that task. */
export function SessionTasks({ todos, open: openProp, onOpenChange }: SessionTasksProps) {
  const listId = useId()
  const [openHere, setOpenHere] = useState(true)
  const open = openProp ?? openHere
  const setOpen = (next: boolean) => { setOpenHere(next); onOpenChange?.(next) }
  const { title, progress, running, rows, more } = useTodoPanel(todos)
  return (
    <section aria-label={title} data-testid="pwcode-session-tasks" className="pwcode-tasks">
      <button type="button" aria-expanded={open} aria-controls={listId} onClick={() => setOpen(!open)} className="pwcode-tasks-head">
        {open ? <ChevronDownIcon aria-hidden className="pwcode-tasks-chevron" /> : <ChevronRightIcon aria-hidden className="pwcode-tasks-chevron" />}
        <span className="pwcode-tasks-title">{title}</span>
        <span className="pwcode-tasks-progress">{progress}</span>
        {!open && running && <span className="pwcode-tasks-running">{running}</span>}
      </button>
      {open && (
        <>
          <ol id={listId} className={more?.all ? 'pwcode-tasks-list pwcode-tasks-all' : 'pwcode-tasks-list'}>
            {rows.map(({ todo, label, statusWord }, i) => (
              // A plan can name the same task twice.
              <li key={i} className={`pwcode-task pwcode-task-${todo.status}`}>
                <span aria-hidden className="pwcode-task-mark">{MARK[todo.status]}</span>
                <span className="sr-only">{statusWord}</span>
                <span className="pwcode-task-label">{label}</span>
              </li>
            ))}
          </ol>
          {more && (
            <button type="button" aria-controls={listId} onClick={more.toggle} className="pwcode-tasks-more">
              {more.label}
            </button>
          )}
        </>
      )}
    </section>
  )
}
