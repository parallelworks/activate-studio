import { useState } from 'react'
import { Checkbox, Popover, Radio, RadioGroup, cx, fieldBox } from '@parallelworks/ui'
import { ChevronDownIcon } from '@parallelworks/ui/icons'
import type { PublicLibrary } from '../api'
import type { Scope } from '../scope'
import { libraryMachine, MachineLabel } from './MachineLabel'
import type { MachineRef } from '../machines'

/** A library's name as the pickers show it, with where it is or that it is
 *  read only. */
/** A library as the switcher names it: where it is, a remote system's or the
 *  cluster the Studio runs beside, else whether it is read only. */
type Named = Pick<PublicLibrary, 'label' | 'site' | 'host' | 'writable'> & Partial<Pick<PublicLibrary, 'access' | 'sharedFrom'>>

/** What the viewer may do in a library and where it is: Read/write, Read
 *  only or Shared, then "(on <system>)" for one on a cluster or host. */
export const libraryAccess = (l: Named) => {
  const a = l.access
  const kind = (a?.sharedFrom ?? l.sharedFrom) ? 'Shared' : (a?.write ?? l.writable) ? 'Read/write' : 'Read only'
  const on = a?.where === 'system' ? (a.system || l.site) : a?.where === 'bucket' ? null : (l.site || l.host)
  return `${kind}${on ? ` (on ${on})` : ''}`
}

/** What libraryAccess says of a library, with its machine as a label. */
export function LibraryAccessText({ library }: { library: Named }) {
  return <AccessText text={libraryAccess(library)} machine={libraryMachine(library)} />
}

/** An access with the machine it says it is on, as a label. */
export function AccessText({ text, machine }: { text: string; machine: MachineRef | null }) {
  const at = text.indexOf(' (on ')
  if (!machine || at < 0) return <>{text}</>
  return <>{text.slice(0, at)} (on <MachineLabel machine={machine} />)</>
}

/** A library as pickers name it: its label and its access. */
export const libraryName = (l: Named) => `${l.label} · ${libraryAccess(l)}`

/** What the picker's button says: all, the one, or how many. */
export function scopeLabel(libraries: PublicLibrary[], s: Scope): string {
  if (s === 'all') return 'All libraries'
  if (s.length === 1) return libraries.find(l => l.id === s[0])?.label ?? s[0]
  return `${s.length} libraries`
}

/**
 * Which libraries Search or Query look in: tick any of them, or All
 * libraries for every one. At least one stays ticked. Only rendered when
 * there is more than one library. With `single`, as the Stats page's maps
 * use it, it picks one library: no All, and ticking one leaves the other.
 */
export function LibraryScope({ libraries, value, onChange, hint, single = false }: {
  libraries: PublicLibrary[]
  value: Scope
  onChange: (s: Scope) => void
  /** The button's tooltip: which page it scopes. */
  hint: string
  single?: boolean
}) {
  if (libraries.length < 2) return null
  if (single) return <OneLibrary libraries={libraries} value={value === 'all' ? libraries[0].id : value[0]} onChange={id => onChange([id])} hint={hint} />
  const names = value === 'all' ? 'every library' : libraries.filter(l => value.includes(l.id)).map(l => l.label).join(', ')
  return (
    <Popover
      title="Libraries"
      align="start"
      className="library-scope-panel"
      trigger={
        <button type="button" title={`${hint}: ${names}`} aria-label={`Libraries: ${scopeLabel(libraries, value)}`}
          className={cx(fieldBox({ size: 'sm' }), 'flex cursor-pointer items-center justify-between gap-2 pr-1.5 text-left', 'library-switch library-scope')}>
          <span className="truncate">{scopeLabel(libraries, value)}</span>
          <ChevronDownIcon aria-hidden="true" className="shrink-0 text-(--theme-muted-text-color)" />
        </button>
      }
    >
      <LibraryTicks libraries={libraries} value={value} onChange={onChange} />
    </Popover>
  )
}

/**
 * The picker's list: All libraries, then a tick for each library. Ticking
 * every one is All; the last one ticked cannot be unticked. The chat's Scope
 * menu shows the same list.
 */
export function LibraryTicks({ libraries, value, onChange, className }: {
  libraries: PublicLibrary[]
  value: Scope
  onChange: (s: Scope) => void
  className?: string
}) {
  const ticked = (id: string) => value === 'all' || value.includes(id)
  const count = value === 'all' ? libraries.length : value.length
  const toggle = (id: string, on: boolean) => {
    const now = value === 'all' ? libraries.map(l => l.id) : value
    const next = on ? [...now, id] : now.filter(x => x !== id)
    if (!next.length) return
    onChange(next.length === libraries.length ? 'all' : libraries.map(l => l.id).filter(x => next.includes(x)))
  }
  return (
    <div className={cx('library-scope-list', className)} role="group" aria-label="Libraries to include">
      <label className="library-scope-row library-scope-all">
        <Checkbox
          aria-label="All libraries"
          checked={value === 'all'}
          indeterminate={value !== 'all' && count > 0}
          onCheckedChange={on => { if (on) onChange('all') }}
        />
        <span>All libraries</span>
      </label>
      {libraries.map(l => {
        const on = ticked(l.id)
        const last = on && count === 1
        return (
          <label key={l.id} className="library-scope-row" title={last ? 'At least one library stays ticked' : undefined}>
            <Checkbox aria-label={libraryName(l)} checked={on} disabled={last} onCheckedChange={v => toggle(l.id, !!v)} />
            <span className="truncate" title={libraryName(l)}>{l.label} · <LibraryAccessText library={l} /></span>
          </label>
        )
      })}
    </div>
  )
}

/** The picker's one-library form: the same button and list, one choice
 *  of them, which closes it. */
function OneLibrary({ libraries, value, onChange, hint }: {
  libraries: PublicLibrary[]
  value: string
  onChange: (id: string) => void
  hint: string
}) {
  const current = libraries.find(l => l.id === value) ?? libraries[0]
  // One pick is the whole choice, so it closes the list.
  const [open, setOpen] = useState(false)
  return (
    <Popover
      open={open}
      onOpenChange={setOpen}
      title="Library"
      align="start"
      className="library-scope-panel"
      trigger={
        <button type="button" title={`${hint}: ${current.label}`} aria-label={`Library: ${current.label}`}
          className={cx(fieldBox({ size: 'sm' }), 'flex cursor-pointer items-center justify-between gap-2 pr-1.5 text-left', 'library-switch library-scope')}>
          <span className="truncate">{current.label}</span>
          <ChevronDownIcon aria-hidden="true" className="shrink-0 text-(--theme-muted-text-color)" />
        </button>
      }
    >
      <RadioGroup className="library-scope-list" aria-label="Library to show" value={current.id} onValueChange={v => { setOpen(false); onChange(String(v)) }}>
        {libraries.map(l => (
          <label key={l.id} className="library-scope-row">
            <Radio value={l.id} aria-label={libraryName(l)} />
            <span className="truncate" title={libraryName(l)}>{l.label} · <LibraryAccessText library={l} /></span>
          </label>
        ))}
      </RadioGroup>
    </Popover>
  )
}
