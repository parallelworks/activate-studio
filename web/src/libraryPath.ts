import { currentLibrary, splitLibraryPath, type PublicLibrary } from './api'
import { useAppConfig } from './config'

/**
 * With All libraries showing, a path starts with its library's id
 * (`u-space-studio/uploads/a.md`), which is how requests address it but
 * not what a person calls it. Where a path is shown, the id reads as the
 * library's name: `zz-test-mylib/uploads/a.md`.
 */
export interface LibraryPathParts { id: string; label: string; rel: string }

/** The library a path in All mode names, with its name; null outside All
 *  mode, or when the path does not start with a known library's id. */
export function libraryPathParts(path: string, libraries: readonly Pick<PublicLibrary, 'id' | 'label'>[] | undefined): LibraryPathParts | null {
  if (currentLibrary() !== 'all') return null
  const t = splitLibraryPath(path)
  const lib = t && libraries?.find(l => l.id === t.id)
  return t && lib ? { id: t.id, label: lib.label, rel: t.rel } : null
}

/** The path as a person reads it: the library's name in place of its id. */
export function shownPath(path: string, libraries: readonly Pick<PublicLibrary, 'id' | 'label'>[] | undefined): string {
  const p = libraryPathParts(path, libraries)
  return p ? (p.rel ? `${p.label}/${p.rel}` : p.label) : path
}

/** shownPath with the libraries this Studio lists. */
export function useShownPath(): (path: string) => string {
  const libraries = useAppConfig().libraries
  return path => shownPath(path, libraries)
}
