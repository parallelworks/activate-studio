import { type PublicLibrary } from './api'
import { ALL_LIBRARIES } from './components/LibrarySwitch'
import { useScopeValue, type Scope } from './scope'

/** Whether a search looks in a library by meaning: its level, for one the
 *  Studio builds, or the vectors it has, for one it only reads. */
export const meaningOn = (l: PublicLibrary) => (l.meaning === undefined || l.meaning === null ? l.caps.vectors : l.meaning !== 'off')

/** The libraries a search covers: the ones chosen, or all of them. */
export function librariesInScope(libraries: PublicLibrary[], scope: Scope | string): PublicLibrary[] {
  if (scope === ALL_LIBRARIES || libraries.length <= 1) return libraries
  const ids = Array.isArray(scope) ? scope : [scope]
  return libraries.filter(l => ids.includes(l.id))
}

/** What a search in the scope can match: meaning where any library in it
 *  has search by meaning, else the words in files where any has full text
 *  (the main folder does unless its depth is Names), else names only. */
export type SearchReach = 'meaning' | 'words' | 'names'
export function searchReach(libraries: PublicLibrary[], scope: Scope | string): SearchReach {
  const libs = librariesInScope(libraries, scope)
  if (!libs.length) return 'words'
  if (libs.some(meaningOn)) return 'meaning'
  if (libs.some(l => l.caps.fullText || (l.primary && l.depth !== 'names'))) return 'words'
  return 'names'
}

/** The search box's hint, for what the scope can match. */
export const SEARCH_PLACEHOLDER: Record<SearchReach, string> = {
  meaning: 'Ask in plain words, or "exact phrase", OR, -exclude, prefix*',
  words: 'Whole words; "exact phrase", OR, -exclude, prefix*',
  names: 'Part of a file or folder name',
}

/** The Search page's note in the header bar, likewise. */
export const SEARCH_NOTE: Record<SearchReach, string> = {
  meaning: 'Names, words and meaning, including Office and PDF content',
  words: 'Names and words, including Office and PDF content',
  names: 'File and folder names',
}

/** The libraries the Search page covers, kept current as they are chosen. */
export function useSearchScope(): Scope {
  return useScopeValue('search')
}
