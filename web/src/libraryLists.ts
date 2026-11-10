import { postJson } from './components/LibraryFormParts'

/**
 * Library lists: a file an administrator exports from one Studio and each
 * person imports into their own (docs/LIBRARIES.md, "Library lists"). The
 * server writes the file and checks one before an import
 * (/api/libraries/export, /api/libraries/import/check); the import adds each
 * entry here through the same routes as the Add a library forms, so the
 * same checks and connection tests run.
 */

export type EntryKind = 'index' | 'folder' | 'ssh' | 'pw'

export interface ListReadThrough { destination: string; port?: number; sshOptions?: string[]; root?: string }

/** One library in a list, as the server normalized it. */
export interface ListEntry {
  kind: EntryKind | string
  label?: string
  mode?: 'connect' | 'build'
  destination?: string
  port?: number
  sshOptions?: string[]
  cluster?: string
  indexRoot?: string
  sourceRoot?: string
  indexDir?: string
  gufiBin?: string
  depth?: string
  readThrough?: ListReadThrough
}

export interface LibraryList {
  format: 'activate-studio-libraries'
  version: number
  exportedAt: string
  studioVersion?: string
  libraries: ListEntry[]
}

export type EntryState = 'new' | 'present' | 'invalid' | 'unavailable'

/** One entry as the check reports it. */
export interface CheckedEntry {
  index: number
  state: EntryState
  /** Why it cannot be added; for present, the library it already is. */
  why?: string
  where: string
  entry: ListEntry | null
  /** An ssh entry's user name ('' for none in the file) and host. */
  user?: string
  host?: string
}

export interface ListCheck {
  version: number
  exportedAt: string
  studioVersion: string
  entries: CheckedEntry[]
  /** The login name this Studio runs as, offered for ssh entries with none. */
  localUser: string
}

export interface ExportAnswer {
  file: LibraryList
  name: string
  skipped: { id: string; label: string; why: string }[]
}

export const exportLibraries = (o: { ids?: string[]; primary?: boolean; keepUser?: boolean }) =>
  postJson<ExportAnswer>('/api/libraries/export', o)

/** The check before an import, of a file's text. */
export const checkList = (text: string) => postJson<ListCheck>('/api/libraries/import/check', { text })

/** The kind of an entry in plain words. */
export function kindLabel(e: ListEntry | null): string {
  if (!e) return 'Unreadable entry'
  switch (e.kind) {
    case 'index': return 'GUFI index on this computer'
    case 'folder': return 'Folder on this computer, indexed here'
    case 'ssh': return e.mode === 'build' ? 'Folder on another system, indexed there over ssh' : 'GUFI index over ssh'
    case 'pw': return e.mode === 'build' ? 'Folder on a cluster, indexed there over pw ssh' : 'GUFI index on a cluster over pw ssh'
    default: return `Unknown kind (${e.kind || 'none'})`
  }
}

export interface ImportResult { state: 'added' | 'failed' | 'skipped'; text: string; id?: string; label?: string }

interface Step { name: string; ok: boolean; detail: string }
interface BuildCheck { ok: boolean; steps: Step[]; create?: string[]; sourceRoot?: string | null }

const firstFailed = (steps: Step[]) => steps.find(s => !s.ok)?.detail ?? ''

const errText = (e: unknown) => (e instanceof Error ? e.message : String(e))

async function putJson<T>(url: string, body: unknown): Promise<T> {
  const res = await fetch(url, { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
  const data = (await res.json().catch(() => ({}))) as { error?: string }
  if (!res.ok) throw new Error(data.error ?? `${url}: ${res.status}`)
  return data as T
}

/** Drops empty fields, so a request carries what a form would send. */
function given<T extends Record<string, unknown>>(o: T): Partial<T> {
  return Object.fromEntries(Object.entries(o).filter(([, v]) => v !== undefined && v !== '' && !(Array.isArray(v) && !v.length))) as Partial<T>
}

/** user@host, or host alone when no user name is given. */
export const destinationWith = (user: string, host: string) => (user.trim() ? `${user.trim()}@${host}` : host)

/**
 * Adds one entry through the routes the Add a library forms use: the same
 * validation, connection test or build check, and add. A folder that does
 * not exist where it should is a failure; an index folder the build needs
 * is created, as the forms do after asking. `user` replaces an ssh entry's
 * user name ('' for none).
 */
export async function importEntry(e: ListEntry, user?: string): Promise<ImportResult> {
  const label = e.label || undefined
  try {
    let added: { id: string; label: string }
    let where = ''
    if (e.kind === 'index') {
      const r = await postJson<{ library: { id: string; label: string } }>('/api/libraries', given({ label, indexRoot: e.indexRoot, sourceRoot: e.sourceRoot }))
      added = r.library
    } else if (e.kind === 'folder') {
      const body = given({ label, sourceRoot: e.sourceRoot, indexRoot: e.indexDir, depth: e.depth })
      const check = await postJson<BuildCheck>('/api/libraries/build/check', body)
      if (!check.ok) return { state: 'failed', text: `The check did not pass. ${firstFailed(check.steps)}`.trim() }
      if (check.sourceRoot && check.create?.includes(check.sourceRoot)) return { state: 'failed', text: `The folder ${check.sourceRoot} is not on this computer.` }
      const r = await postJson<{ library: { id: string; label: string } }>('/api/libraries/build', { ...body, create: !!check.create?.length })
      added = r.library
      where = ' Building its index.'
    } else if (e.kind === 'ssh' || e.kind === 'pw') {
      const host = e.kind === 'ssh'
        ? given({ destination: user === undefined ? e.destination : destinationWith(user, e.destination!.slice(e.destination!.lastIndexOf('@') + 1)), port: e.port, sshOptions: e.sshOptions })
        : { cluster: e.cluster }
      const base = `/api/user-libraries/${e.kind}`
      if (e.mode === 'build') {
        const body = { ...host, ...given({ label, sourceRoot: e.sourceRoot, indexRoot: e.indexDir, gufiBin: e.gufiBin, depth: e.depth }) }
        const check = await postJson<BuildCheck>(`${base}/build/test`, body)
        if (!check.ok) return { state: 'failed', text: `The check did not pass. ${firstFailed(check.steps)}`.trim() }
        if (check.sourceRoot && check.create?.includes(check.sourceRoot)) return { state: 'failed', text: `The folder ${check.sourceRoot} does not exist there.` }
        added = await postJson<{ id: string; label: string }>(`${base}/build`, { ...body, ...(check.create?.length ? { create: true } : {}) })
        where = ' Building its index there.'
      } else {
        added = await postJson<{ id: string; label: string }>(base, { ...host, ...given({ label, indexRoot: e.indexRoot, sourceRoot: e.sourceRoot, gufiBin: e.gufiBin }) })
      }
    } else {
      return { state: 'skipped', text: `The kind ${e.kind} is not one this Studio knows.` }
    }
    let note = ''
    if (e.readThrough && e.kind !== 'folder' && e.mode !== 'build') {
      try {
        await putJson(`/api/libraries/${encodeURIComponent(added.id)}/read-through`, e.readThrough)
        note = ` Originals are read through ${e.readThrough.destination}.`
      } catch (err) {
        note = ` Its originals could not be set to be read through ${e.readThrough.destination}: ${errText(err)}`
      }
    }
    return { state: 'added', text: `Added.${where}${note}`, id: added.id, label: added.label }
  } catch (err) {
    return { state: 'failed', text: errText(err) }
  }
}
