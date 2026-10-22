import { useEffect, useState } from 'react'
import { Field, SegmentedControl, Select, type SelectItem } from '@parallelworks/ui'
import { api, type MeaningLevel } from '../api'

/**
 * Search by meaning, as a level per library: Off, Documents, or Full.
 * Search always matches names and the words inside files; search by meaning
 * adds passages that say the same thing in other words. It is the slowest
 * part of indexing, and many folders (code, data, simulation output) gain
 * little from it, so each library chooses: the main folder starts at
 * Documents, the others at Off.
 */

export const MEANING_OPTIONS: { value: MeaningLevel; label: string }[] = [
  { value: 'off', label: 'Off' },
  { value: 'documents', label: 'Documents' },
  { value: 'full', label: 'Full' },
]

export const MEANING_HELP: Record<MeaningLevel, string> = {
  off: 'Search matches names and the words inside files. Nothing more is computed, so indexing is fastest.',
  documents: 'Search also finds passages that say the same thing in other words, in documents and images: PDF, Office, text, Markdown, and image descriptions, the first few passages of each.',
  full: 'Search by meaning covers up to twelve passages of every file with text, code and data included. Indexing takes about three times as long as Documents.',
}

/** The level as a short phrase, for summaries. */
export const meaningPhrase = (level: MeaningLevel) => level === 'off' ? 'off' : level === 'documents' ? 'documents' : 'all files'

/** Off, Documents, Full, with what the chosen one does. */
export function MeaningLevelControl({ value, onChange, disabled, label = 'Search by meaning' }: {
  value: MeaningLevel
  onChange: (level: MeaningLevel) => void
  disabled?: boolean
  label?: string
}) {
  return (
    <Field group label={label} description={MEANING_HELP[value]} className="meaning-level">
      <SegmentedControl options={MEANING_OPTIONS} value={value} disabled={disabled}
        onValueChange={v => { if (v) onChange(v as MeaningLevel) }} />
    </Field>
  )
}

/** The same choice in a table cell. */
export function MeaningLevelSelect({ value, onChange, disabled, library }: {
  value: MeaningLevel
  onChange: (level: MeaningLevel) => void
  disabled?: boolean
  library: string
}) {
  const items: SelectItem<string>[] = MEANING_OPTIONS.map(o => ({ value: o.value, label: o.label }))
  return (
    <Select className="meaning-select" size="sm" items={items} value={value} disabled={disabled}
      aria-label={`Search by meaning in ${library}`} onValueChange={v => { if (v) onChange(v as MeaningLevel) }} />
  )
}

/** The main folder's level, read and saved here: setup's folder step. */
export function MainFolderMeaning({ disabled }: { disabled?: boolean }) {
  const [level, setLevel] = useState<MeaningLevel | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let alive = true
    api.indexSettings().then(i => { if (alive) setLevel(i.settings.meaning) }).catch(() => { if (alive) setLevel('documents') })
    return () => { alive = false }
  }, [])
  if (!level) return null
  const pick = (next: MeaningLevel) => {
    const was = level
    setLevel(next)
    setError('')
    api.saveIndexSettings({ meaning: next }).catch(e => { setLevel(was); setError(String((e as Error).message ?? e)) })
  }
  return (
    <div>
      <MeaningLevelControl value={level} onChange={pick} disabled={disabled} />
      {error && <p className="muted" role="alert" style={{ margin: '4px 0 0' }}>{error}</p>}
    </div>
  )
}
