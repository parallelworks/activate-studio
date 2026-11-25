import { useState } from 'react'
import { Button, Loader, StatusLabel, formatBytes } from '@parallelworks/ui'
import { FolderIcon } from '@parallelworks/ui/icons'
import type { FolderCount } from '../api'
import { plural } from '../plural'
import { StepList, type CheckStep } from './LibraryFormParts'

/** Something the person should know, in a few words, with the rest on expand. */
export interface CheckWarning { short: string; detail?: string }

/** What a folder holds, in one line. */
export function countLine(c: FolderCount): string {
  if (!c.files && !c.partial) return 'No files yet'
  const at = c.partial ? 'At least ' : ''
  const kinds = c.kinds.slice(0, 3).map(k => `${k.files.toLocaleString()} ${k.name.toUpperCase()}`).join(', ')
  return [`${at}${plural(c.files, 'file')}`, formatBytes(c.bytes), ...(kinds ? [kinds] : [])].join(' · ')
}

/** A check's warnings, from its steps: the few words the server gives each,
 *  else its first sentence. */
export const stepWarnings = (steps: CheckStep[]): CheckWarning[] =>
  steps.filter(s => s.ok && s.warn).map(s => ({ short: s.short ?? s.detail.replace(/\.(\s[\s\S]*)?$/, ''), detail: s.detail }))

/**
 * What the check that ran by itself found, on one card: the folder, what it
 * holds, whether it is new, and warnings in a few words, each opening to
 * its detail. What stops it says what to do, in one line.
 */
export function FolderCheckCard({ path, place, facts, counting = false, isNew = false, busy, warnings, blocked, steps, onAgain }: {
  path: string
  /** The computer it is on. */
  place: string
  /** What it holds, in one line, once known. */
  facts?: string
  counting?: boolean
  /** It does not exist yet, and is made once confirmed. */
  isNew?: boolean
  busy: boolean
  warnings: CheckWarning[]
  /** Why it cannot be added, and what to do. */
  blocked?: string
  steps: CheckStep[]
  onAgain: () => void
}) {
  const [details, setDetails] = useState(false)
  const line = busy ? 'Checking…' : isNew ? 'New, made when you index it' : facts ?? (counting ? 'Counting its files…' : '')
  return (
    <section className={`lib-check${blocked ? ' blocked' : ''}`} aria-label="Folder check" aria-busy={busy} data-testid="folder-check">
      <div className="lib-check-head">
        <FolderIcon aria-hidden className="lib-check-icon" />
        <div className="lib-check-what">
          <span className="lib-check-path code-mono" title={path}>{path}</span>
          <span className="lib-check-line muted" role="status" data-testid="folder-check-line">
            {(busy || (counting && !isNew && !facts && !blocked)) && <Loader inline decorative size={12} />}{blocked && !busy ? place : line}
          </span>
        </div>
        {!busy && steps.length > 0 && (
          <Button variant="ghost" size="xs" aria-expanded={details} onClick={() => setDetails(d => !d)}>{details ? 'Hide details' : 'Details'}</Button>
        )}
      </div>
      {!busy && blocked && (
        <div className="lib-check-blocked">
          <StatusLabel variant="error">{blocked}</StatusLabel>
          <Button variant="ghost" size="xs" onClick={onAgain}>Check again</Button>
        </div>
      )}
      {!busy && warnings.length > 0 && (
        <ul className="lib-check-warnings" aria-label="Warnings">
          {warnings.map(w => (
            <li key={w.short}>
              {w.detail
                ? <details><summary><span className="lib-check-dot" aria-hidden />{w.short}</summary><p>{w.detail}</p></details>
                : <span className="lib-check-plain"><span className="lib-check-dot" aria-hidden />{w.short}</span>}
            </li>
          ))}
        </ul>
      )}
      {!busy && details && <div className="lib-check-steps"><StepList steps={steps} label="Every step of the check" /></div>}
    </section>
  )
}
