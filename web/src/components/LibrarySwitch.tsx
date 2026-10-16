import { useEffect, useState } from 'react'
import { Select, cx, fieldBox } from '@parallelworks/ui'
import { ChevronDownIcon } from '@parallelworks/ui/icons'
import { currentLibrary, onLibraryChange, setLibrary, type PublicLibrary } from '../api'

/**
 * The library picker in the Library rail header. Only rendered when more
 * than one library is mounted; a read-only library says so beside its
 * name, since that is the one fact a user needs before trying to drop a
 * file into it.
 */
export function LibrarySwitch({ libraries }: { libraries: PublicLibrary[] }) {
  const [id, setId] = useState(currentLibrary())
  useEffect(() => onLibraryChange(setId), [])
  const known = id === ALL_LIBRARIES || libraries.some(l => l.id === id) ? id : (libraries[0]?.id ?? 'kb')
  if (libraries.length < 2) return null
  const items = [
    ...libraries.map(l => ({ value: l.id, label: `${l.label}${l.site ? ` (on ${l.site})` : l.writable ? '' : ' (read only)'}` })),
    { value: ALL_LIBRARIES, label: 'All libraries' },
  ]
  // A trigger of its own, drawn as the default one is, so the picker can
  // carry its hint: the default trigger takes no tooltip.
  return (
    <Select
      items={items}
      value={known}
      onValueChange={v => { if (v != null) setLibrary(v) }}
      aria-label="Library"
      size="sm"
      triggerTooltip="Which library to browse and search"
      trigger={
        <button type="button" className={cx(fieldBox({ size: 'sm' }), 'flex cursor-pointer items-center justify-between gap-2 pr-1.5 text-left', 'library-switch')}>
          <span className="truncate">{items.find(i => i.value === known)?.label ?? known}</span>
          <ChevronDownIcon aria-hidden="true" className="shrink-0 text-(--theme-muted-text-color)" />
        </button>
      }
    />
  )
}

/** The switcher's entry for every library at once: one tree with a folder
 *  per library, and search, queries, and statistics over all of them. */
export const ALL_LIBRARIES = 'all'

/** Whether the library currently selected may be written to. */
export function libraryWritable(libraries: PublicLibrary[] | undefined, id: string): boolean {
  if (id === ALL_LIBRARIES) return false
  if (!libraries?.length) return true
  return libraries.find(l => l.id === id)?.writable ?? false
}
