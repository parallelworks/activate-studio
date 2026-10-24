import { ReactElement, useEffect, useId, useState } from 'react'
import { Button, Callout, IconButton, Input, Loader, Popover, PopoverClose, toast } from '@parallelworks/ui'
import { CloseIcon } from '@parallelworks/ui/icons'
import { api } from '../api'

type TagMenuProps = {
  paths: string[]
  /** Called after each successful apply with the server's path -> tags map. */
  onApplied: (updated: Record<string, string[]>) => void
} & (
  | {
      /** The button that opens it. Pass `open` and `onOpenChange` to open it
       *  from elsewhere too, such as a row's label button. */
      trigger: ReactElement
      open?: boolean
      onOpenChange?: (open: boolean) => void
      align?: 'start' | 'center' | 'end'
      onClose?: never
    }
  | {
      /** Mounted while open, with no trigger: it opens under the window's
       *  top-right corner, and this runs when it is dismissed. */
      onClose: () => void
      trigger?: never
      open?: never
      onOpenChange?: never
      align?: never
    }
)

/** The labels panel for a set of paths. */
export function TagMenu(props: TagMenuProps) {
  const headId = useId()
  const [corner] = useState(() => ({ clientX: window.innerWidth - 20, clientY: 58 }))
  const panel = <TagPanel paths={props.paths} headId={headId} onApplied={props.onApplied} />
  if (props.trigger) {
    return (
      <Popover
        trigger={props.trigger}
        open={props.open}
        onOpenChange={props.onOpenChange}
        align={props.align}
        initialFocus="popup"
        aria-labelledby={headId}
        className="tag-panel"
      >
        {panel}
      </Popover>
    )
  }
  const { onClose } = props
  // Closing waits for the press that dismissed it to finish, so a caller's
  // own toggle, pressed to close the menu, does not open it again.
  return (
    <Popover
      at={corner}
      open
      onOpenChange={next => { if (!next) setTimeout(onClose) }}
      align="end"
      initialFocus="popup"
      aria-labelledby={headId}
      className="tag-panel"
    >
      {panel}
    </Popover>
  )
}

/** The panel's body, mounted only while it is open, so the labels are read
 *  fresh each time it opens. */
function TagPanel({ paths, headId, onApplied }: {
  paths: string[]
  headId: string
  onApplied: (updated: Record<string, string[]>) => void
}) {
  const [vocab, setVocab] = useState<{ tag: string; count: number }[]>([])
  const [current, setCurrent] = useState<string[]>([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  // Kept by the Studio rather than on the files: a library whose files it
  // does not write, such as a read-only folder or an index on another machine.
  const [keptByStudio, setKeptByStudio] = useState(false)
  const inputId = useId()

  useEffect(() => {
    api.tagVocabulary().then(v => setVocab(v.tags)).catch(() => {})
    Promise.all(paths.slice(0, 30).map(p => api.tagsFor(p).catch(() => ({ own: [] as string[], inherited: [] as string[], stored: undefined }))))
      .then(all => {
        setCurrent([...new Set(all.flatMap(t => t.own))])
        setKeptByStudio(all.some(t => t.stored === 'studio'))
      })
  }, [paths])

  const apply = async (add: string[], remove: string[]) => {
    setBusy(true)
    setError('')
    try {
      const r = await api.applyTags(paths, add, remove)
      setCurrent(c => [...new Set([...c.filter(t => !remove.includes(t)), ...add])])
      onApplied(r.updated ?? {})
      toast.success(r.stored === 'studio' ? 'Labels saved.' : 'Applied and re-indexed.')
    } catch (e) {
      setError(String((e as Error).message ?? e))
    } finally {
      setBusy(false)
    }
  }

  const addFromInput = () => {
    const t = input.trim()
    if (t) { void apply([t], []); setInput('') }
  }

  return (
    <>
      <div className="tag-pop-head">
        <span className="field-label" id={headId}>Labels for {paths.length === 1 ? paths[0].split('/').pop() : `${paths.length} items`}</span>
        {busy && <Loader inline size={12} />}
        <PopoverClose render={<Button variant="secondary" size="sm" />}>Close</PopoverClose>
      </div>
      {keptByStudio && (
        <p className="muted tag-note">Saved in the Studio; the files are read only.</p>
      )}
      {paths.length > 1000 && (
        <p className="muted tag-note">Labels apply to the first 1,000 selected items per action.</p>
      )}
      {current.length > 0 && (
        <div className="tag-row">
          {current.map(t => (
            <span key={t} className="tag-pill on">
              {t}
              <IconButton className="tag-remove" size="xs" icon={<CloseIcon />} label="Remove label" onClick={() => void apply([], [t])} disabled={busy} />
            </span>
          ))}
        </div>
      )}
      {vocab.filter(v => !current.includes(v.tag)).length > 0 && (
        <>
          <div className="field-label">Existing labels</div>
          <div className="tag-row">
            {vocab.filter(v => !current.includes(v.tag)).slice(0, 14).map(v => (
              <button key={v.tag} className="tag-pill" onClick={() => void apply([v.tag], [])} disabled={busy}>
                {v.tag} <span className="tag-count">{v.count}</span>
              </button>
            ))}
          </div>
        </>
      )}
      <label className="field-label" htmlFor={inputId}>New label</label>
      <div className="url-row">
        <Input id={inputId} size="sm" value={input} onChange={e => setInput(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && addFromInput()} placeholder="e.g. validated, research, experiment" />
        <Button variant="primary" size="sm" onClick={addFromInput} disabled={busy || !input.trim()}>Add</Button>
      </div>
      {error && <Callout tone="error" size="sm" className="tag-note">{error}</Callout>}
    </>
  )
}
