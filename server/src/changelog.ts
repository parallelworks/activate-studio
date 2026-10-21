import fs from 'node:fs'
import path from 'node:path'
import { PROJECT_ROOT } from './config.js'

/**
 * Release notes and the change log, as entries a person reads. Both come
 * from scripts/release-notes.mjs: a release's notes are one section per
 * pull request, `### <title> (#N)` with its description below, and
 * CHANGELOG.md holds the same for every version under `## vX.Y (date)`,
 * newest first. Entries titled "Change log through vX" only regenerate the
 * change log and are left out.
 */

export interface ChangeEntry {
  /** The pull request's title, a one-line summary of the change. */
  title: string
  pr: number | null
  /** The pull request's description, markdown. */
  body: string
}

export interface ChangeVersion {
  version: string
  /** YYYY-MM-DD, when known. */
  date: string | null
  entries: ChangeEntry[]
}

const ENTRY = /^###\s+(.*?)\s*\(#(\d+)\)\s*$/
// The script writes this when it cannot find the pull request.
const BARE_ENTRY = /^###\s+#(\d+)\s*$/
const VERSION = /^##\s+(v\d+\.\d+(?:\.\d+)?\S*)(?:\s+\(([^)]*)\))?\s*$/
const OPEN_FENCE = /^\s{0,3}(`{3,}|~{3,})/
const CLOSE_FENCE = /^\s{0,3}(`{3,}|~{3,})\s*$/
const HOUSEKEEPING = /^change log through\b/i
const TAG_ONLY = /^_No pull requests; tag only\._$/

/** Each line, and whether it is inside a fenced code block, where a line
 *  that looks like a heading is not one. */
function* lines(md: string): Generator<[string, boolean]> {
  let fence: string | null = null
  for (const line of md.replace(/\r\n?/g, '\n').split('\n')) {
    if (fence) {
      const close = CLOSE_FENCE.exec(line)
      if (close && close[1][0] === fence[0] && close[1].length >= fence.length) fence = null
      yield [line, true]
      continue
    }
    const open = OPEN_FENCE.exec(line)
    if (open) { fence = open[1]; yield [line, true]; continue }
    yield [line, false]
  }
}

/** One release's notes as entries. Notes with no entry headings, such as a
 *  release written by hand, are one entry titled "Release notes". */
export function parseNotes(md: string): ChangeEntry[] {
  const found: { title: string; pr: number | null; body: string[] }[] = []
  const before: string[] = []
  for (const [line, code] of lines(String(md ?? ''))) {
    const m = code ? null : ENTRY.exec(line)
    const bare = code || m ? null : BARE_ENTRY.exec(line)
    if (m) found.push({ title: m[1].trim(), pr: Number(m[2]), body: [] })
    else if (bare) found.push({ title: `Pull request #${bare[1]}`, pr: Number(bare[1]), body: [] })
    else if (found.length) found[found.length - 1].body.push(line)
    else before.push(line)
  }
  const lead = before.join('\n').trim()
  const entries: ChangeEntry[] = lead && !TAG_ONLY.test(lead) ? [{ title: 'Release notes', pr: null, body: lead }] : []
  for (const e of found) {
    if (HOUSEKEEPING.test(e.title)) continue
    entries.push({ title: e.title || `Pull request #${e.pr}`, pr: e.pr, body: e.body.join('\n').trim() })
  }
  return entries
}

/** CHANGELOG.md as versions in the file's order (newest first), each with
 *  its entries; the title and introduction above the first version are not
 *  part of any. */
export function parseChangelog(md: string): ChangeVersion[] {
  const versions: { version: string; date: string | null; text: string[] }[] = []
  for (const [line, code] of lines(String(md ?? ''))) {
    const v = code ? null : VERSION.exec(line)
    if (v) versions.push({ version: v[1], date: v[2]?.trim() || null, text: [] })
    else if (versions.length) versions[versions.length - 1].text.push(line)
  }
  return versions.map(v => ({ version: v.version, date: v.date, entries: parseNotes(v.text.join('\n')) }))
}

/** The change log this build carries: CHANGELOG.md at the top of the app,
 *  or STUDIO_CHANGELOG_FILE. */
export function changelogFile(): string {
  return process.env.STUDIO_CHANGELOG_FILE || path.join(PROJECT_ROOT, 'CHANGELOG.md')
}

let cache: { file: string; mtimeMs: number; size: number; versions: ChangeVersion[] } | null = null

/** The bundled change log, parsed once per change of the file. `included`
 *  is false when the build has none. */
export function readChangelog(): { included: boolean; versions: ChangeVersion[] } {
  const file = changelogFile()
  let st: fs.Stats
  try { st = fs.statSync(file) } catch { return { included: false, versions: [] } }
  if (!st.isFile()) return { included: false, versions: [] }
  if (cache && cache.file === file && cache.mtimeMs === st.mtimeMs && cache.size === st.size) {
    return { included: true, versions: cache.versions }
  }
  let md: string
  try { md = fs.readFileSync(file, 'utf8') } catch { return { included: false, versions: [] } }
  const versions = parseChangelog(md)
  cache = { file, mtimeMs: st.mtimeMs, size: st.size, versions }
  return { included: true, versions }
}
