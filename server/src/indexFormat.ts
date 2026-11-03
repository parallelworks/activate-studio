/**
 * Which GUFI wrote an index, by the columns of its top directory's summary
 * table. GUFI 0.8.0 changed the schema twice from what the Studio first
 * shipped with: `rollupscore` became `canrollup` and `isrolledup` (June
 * 2026, before the Studio's first release), and `entries`, `summary` and
 * `pentries_rollup` gained a `dev` column (October 2026).
 *
 * Measured with 0.8.0 tools (docs/REFERENCE.md, GUFI versions): every query
 * the Studio runs gives the same rows on an index from before `dev`, so such
 * an index needs no migration and no rebuild. An index from before the
 * rename reads the same way except where a folder was rolled up: GUFI cannot
 * tell the folder is rolled up, and lists that folder's entries more than once. That
 * is the one case the Studio says out loud.
 */

/** `current`: written by GUFI 0.8.0 or later (has `dev`). `earlier`: written by
 *  a GUFI from June to October 2026, which reads as it did. `older`: written
 *  before June 2026 (GUFI 0.7.0 and before), with `rollupscore`. */
export type IndexFormat = 'current' | 'earlier' | 'older'

/** What one library's index is known to be. `rolledUp` is set for `older`
 *  only: whether any folder of it was rolled up. */
export interface FormatRecord { format: IndexFormat; rolledUp?: boolean }

/** The columns of the top directory's summary table, as one comma-joined value. */
export const SUMMARY_COLUMNS_SQL = "SELECT group_concat(name) FROM pragma_table_info('summary');"

/** The format a summary table's columns show, or null when they are not those of a GUFI summary. */
export function formatOfColumns(columns: string | string[]): IndexFormat | null {
  const cols = new Set((Array.isArray(columns) ? columns : String(columns).split(',')).map(c => c.trim().toLowerCase()).filter(Boolean))
  if (!cols.has('totfiles') || !cols.has('isroot')) return null
  if (cols.has('rollupscore')) return 'older'
  if (cols.has('isrolledup') && cols.has('dev')) return 'current'
  if (cols.has('isrolledup')) return 'earlier'
  return null
}

/** What Settings says of an index's format. Only an index that reads wrongly
 *  has anything to say: one from before 0.8.0 reads as it did, and a note on
 *  every library built before the update would be noise. */
export function formatNote(r: FormatRecord | null | undefined): string | null {
  const gap = formatGap(r)
  return gap ? gap.charAt(0).toUpperCase() + gap.slice(1) + '.' : null
}

/** Why a search or query covers an index only in part, or null when it covers all of it. */
export function formatGap(r: FormatRecord | null | undefined): string | null {
  return r?.format === 'older' && r.rolledUp
    ? 'written by a GUFI before 0.8.0 and rolled up, so the files of the folders it rolled up are listed more than once and counts and sizes over them are too high; rebuild it with GUFI 0.8.0 or later'
    : null
}

/** A query's failure on an index of an earlier format, in plain words, or null when the message says nothing of format. */
export function formatFailure(message: string, r: FormatRecord | null | undefined): string | null {
  if (!r || r.format === 'current') return null
  if (/no such column:\s*dev\b/i.test(message)) return 'this index was written by a GUFI before 0.8.0 and has no dev column; rebuild it with GUFI 0.8.0 or later to add one'
  if (/no such column:\s*(canrollup|isrolledup)\b/i.test(message)) return 'this index was written by a GUFI before 0.8.0; rebuild it with GUFI 0.8.0 or later'
  return null
}
