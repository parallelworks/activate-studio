import { useEffect, useState } from 'react'
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
  const known = libraries.some(l => l.id === id) ? id : (libraries[0]?.id ?? 'kb')
  if (libraries.length < 2) return null
  return (
    <select
      className="library-switch"
      value={known}
      title="Which library to browse and search"
      aria-label="Library"
      onChange={e => setLibrary(e.target.value)}
    >
      {libraries.map(l => (
        <option key={l.id} value={l.id}>{l.label}{l.writable ? '' : ' (read only)'}</option>
      ))}
    </select>
  )
}

/** Whether the library currently selected may be written to. */
export function libraryWritable(libraries: PublicLibrary[] | undefined, id: string): boolean {
  if (!libraries?.length) return true
  return libraries.find(l => l.id === id)?.writable ?? false
}
