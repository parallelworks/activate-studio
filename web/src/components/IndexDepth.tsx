import { useEffect, useRef, useState, type ReactNode } from 'react'
import { Field, Select, Slider, type SelectItem } from '@parallelworks/ui'
import { api, type IndexDepth } from '../api'

/**
 * How deep a library is indexed, as one scale of five stops, each adding to
 * the one before: Names; Text, the words inside files; Text + OCR, text in
 * images and scanned pages; Meaning on documents; and Meaning on all files.
 * Each stop takes longer to index than the one before, search by meaning
 * most of all, and many folders (code, data, simulation output) need no
 * more than Text, so each library chooses. The main folder and a library
 * built on this computer start at Meaning on documents, which covers
 * documents only; one built on another system starts at Text + OCR.
 */

export interface DepthStop { value: IndexDepth; label: string; help: string }

export const DEPTHS: DepthStop[] = [
  { value: 'names', label: 'Names',
    help: 'Names, sizes, dates and owners of every file and folder. Search finds names; nothing is read from inside the files. The fastest.' },
  { value: 'text', label: 'Text',
    help: 'Adds the words inside files: text, code, PDFs and Office documents. Search finds names and words.' },
  { value: 'ocr', label: 'Text + OCR',
    help: 'Adds text in images and scanned PDF pages, read by tesseract, and image descriptions where AI features make them.' },
  { value: 'meaning-documents', label: 'Meaning on documents',
    help: 'Adds search by meaning: a plain question also finds passages that say it in other words. Covers the first few passages of PDF, Office, text, Markdown and image files.' },
  { value: 'meaning-full', label: 'Meaning on all files',
    help: 'Search by meaning covers up to twelve passages of every file with text, code and data included. Indexing takes about three times as long as Meaning on documents.' },
]

/** The stop a library built on this computer starts at. */
export const DEFAULT_LIBRARY_DEPTH: IndexDepth = 'meaning-documents'
/** The stop a library built on another system starts at: search does not
 *  read vectors made there yet (REMOTE_MEANING). */
export const DEFAULT_REMOTE_LIBRARY_DEPTH: IndexDepth = 'ocr'
/** What the server reads for a library added before a depth was stored at
 *  creation, for a list that has no depth for it. */
export const UNSTORED_LIBRARY_DEPTH: IndexDepth = 'ocr'

/** Under a Meaning stop in the form that builds a library here: what it
 *  costs, the order the phases run in, and the faster choice for a very
 *  large folder. Nothing counts the files first, so this is a note, not a
 *  change of the default. */
export const MEANING_LAST = 'Search by meaning is the slowest part of indexing, so it runs last, in the background: names are searchable first, then words. For a folder of more than about 500,000 files, Text + OCR is faster; meaning can be added later in Settings, Libraries.'
export const meaningLastNote = (depth: IndexDepth) => (depth.startsWith('meaning') ? MEANING_LAST : null)
/** Under the depth in the form that builds a library on another system:
 *  why it starts at Text + OCR, and how to have meaning made anyway. */
export const REMOTE_MEANING = 'Starts at Text + OCR: the Studio does not search by meaning in indexes on other systems yet. To make the vectors there anyway, choose a Meaning stop; the build makes them only where the Studio\'s embedding model is in ~/.activate-studio/models on that system.'

const stopOf = (depth: IndexDepth) => Math.max(0, DEPTHS.findIndex(d => d.value === depth))
export const depthLabel = (depth: IndexDepth) => DEPTHS[stopOf(depth)].label

/** Whether a depth reads images and scans, where tesseract is installed. */
const readsImages = (depth: IndexDepth) => stopOf(depth) >= stopOf('ocr')

/**
 * The scale, notched at the stops between its ends and each stop named
 * under it, with what the chosen one adds.
 * Dragging, or the arrow keys, settle for a moment before the choice is
 * passed on, so going across several stops saves once; a click on a stop's
 * name saves at once. `ocr` false says tesseract is not installed here.
 */
export function IndexDepthControl({ value, onChange, disabled, label = 'Index depth', ocr = null, note }: {
  value: IndexDepth
  onChange: (depth: IndexDepth) => void
  disabled?: boolean
  label?: string
  ocr?: boolean | null
  /** A line under the help of the stop shown, such as why a form starts where it does. */
  note?: (depth: IndexDepth) => ReactNode
}) {
  const [shown, setShown] = useState(value)
  const latest = useRef(value)
  const timer = useRef<number | undefined>(undefined)
  useEffect(() => { latest.current = value; setShown(value) }, [value])
  useEffect(() => () => window.clearTimeout(timer.current), [])
  const pick = (depth: IndexDepth, settle: boolean) => {
    setShown(depth)
    window.clearTimeout(timer.current)
    const commit = () => { if (depth !== latest.current) onChange(depth) }
    if (settle) timer.current = window.setTimeout(commit, 350)
    else commit()
  }
  const stop = DEPTHS[stopOf(shown)]
  const noOcr = ocr === false && readsImages(shown)
  const extra = note?.(shown)
  return (
    <Field group label={label} className="depth-control"
      description={<>{stop.help}{noOcr && ' tesseract is not installed here, so images and scans are found by name until it is.'}{extra && <span className="depth-note">{extra}</span>}</>}>
      <div className="depth-scale">
        <Slider className="depth-slider" min={0} max={DEPTHS.length - 1} step={1} value={stopOf(shown)} disabled={disabled}
          aria-label={`${label}: ${stop.label}`}
          onValueChange={n => { const d = DEPTHS[Math.round(n)]; if (d) pick(d.value, true) }} />
        <div className="depth-notches" aria-hidden="true">
          {DEPTHS.map((d, k) => (d.value === shown || k === 0 || k === DEPTHS.length - 1 ? null
            : <span key={d.value} className="depth-notch" style={{ left: `${(k * 100) / (DEPTHS.length - 1)}%` }} />))}
        </div>
        <div className="depth-stops">
          {DEPTHS.map(d => (
            <button key={d.value} type="button" className="depth-stop" aria-pressed={d.value === shown} disabled={disabled}
              onClick={() => pick(d.value, false)}>
              {d.label}
            </button>
          ))}
        </div>
      </div>
    </Field>
  )
}

/** The same choice in a table cell. */
export function IndexDepthSelect({ value, onChange, disabled, library }: {
  value: IndexDepth
  onChange: (depth: IndexDepth) => void
  disabled?: boolean
  library: string
}) {
  const items: SelectItem<string>[] = DEPTHS.map(d => ({ value: d.value, label: d.label }))
  return (
    <Select className="depth-select" size="sm" items={items} value={value} disabled={disabled}
      aria-label={`Index depth of ${library}`} onValueChange={v => { if (v) onChange(v as IndexDepth) }} />
  )
}

/** Whether tesseract is installed on this computer, once asked; null until
 *  known, or when `ask` is false. */
export function useOcrHere(ask = true): boolean | null {
  const [ocr, setOcr] = useState<boolean | null>(null)
  useEffect(() => {
    if (!ask) return
    let alive = true
    api.extractors().then(r => { if (alive) setOcr(r.extractors.find(x => x.name === 'tesseract')?.available ?? null) }).catch(() => {})
    return () => { alive = false }
  }, [ask])
  return ocr
}

/** The main folder's depth, read and saved here: setup's folder step. */
export function MainFolderDepth({ disabled }: { disabled?: boolean }) {
  const [depth, setDepth] = useState<IndexDepth | null>(null)
  const [ocr, setOcr] = useState<boolean | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let alive = true
    api.indexSettings().then(i => {
      if (!alive) return
      setDepth(i.settings.depth ?? 'meaning-documents')
      setOcr(i.readers?.extractors.find(x => x.name === 'tesseract')?.available ?? null)
    }).catch(() => { if (alive) setDepth('meaning-documents') })
    return () => { alive = false }
  }, [])
  if (!depth) return null
  const pick = (next: IndexDepth) => {
    const was = depth
    setDepth(next)
    setError('')
    api.saveIndexSettings({ depth: next }).catch(e => { setDepth(was); setError(String((e as Error).message ?? e)) })
  }
  return (
    <div>
      <IndexDepthControl value={depth} onChange={pick} disabled={disabled} ocr={ocr} />
      {error && <p className="muted" role="alert" style={{ margin: '4px 0 0' }}>{error}</p>}
    </div>
  )
}
