import crypto from 'node:crypto'
import os from 'node:os'
import path from 'node:path'
import { authEnabled } from '../auth.js'
import { KbError } from '../kb.js'
import { getLibrary, type Library } from '../libraries.js'
import { describe } from '../libraryDetails.js'
import './copy.js'
import { assertFits, assertNoneExist, checkDestination, defaultFolder, expandHome, freeBytes, namedFolder, shownFolder, size, topNames } from './destination.js'
import { hiddenPath, listSource, lookAt, relCheck, routeId, routeLabel, sourceRoutes, type ItemKind } from './source.js'
import { moverFor, moverIds, type Mover, type SourceRoute } from './types.js'

/**
 * The assistant's one way to copy files, up to the point a person says yes.
 *
 * A call's arguments are untrusted. They are held to a fixed shape, each path
 * to a place inside a library the viewer can see and the destination to an
 * allowed folder, and from them the Studio makes a plan out of what it
 * already knows: the index's count and size, where the files can be read,
 * which mover will do it, and where they land. The plan is what the approval
 * shows and what the held call records (chat/approvals.ts); jobs.ts runs it,
 * and only from an approved hold. There is no argument for moving or
 * deleting anything, and no way to name a destination on another host.
 */

export const TRANSFER_TOOL = 'transfer_files'
export const isTransferTool = (name: string) => name === TRANSFER_TOOL

/** Paths one call may name; a folder counts as one. */
const MAX_PATHS = 100
/** How long a plan waits for the index, or for a count of a local folder. */
const FACTS_MS = 20_000

const PARAMETERS = {
  type: 'object',
  properties: {
    paths: { type: 'array', items: { type: 'string' }, description: 'Files or folders to copy, relative to the library\'s root, as search_kb or list_kb_dir gave them' },
    library: { type: 'string', description: 'Which library: an id from list_libraries; default the one selected in this conversation' },
    destination: { type: 'string', description: 'A folder on this computer to copy into, as the user named it (~/ is their home). Leave it out for their Downloads folder. Only folders the user allows work; the result lists them when one is refused.' },
  },
  required: ['paths'],
}

export const TRANSFER_TOOL_SPEC = {
  type: 'function' as const,
  function: {
    name: TRANSFER_TOOL,
    description: 'Copy files or folders from a library to a folder on this computer, by default the user\'s Downloads folder. Use it when the user asks to download, save or copy files here. Find the files first (search_kb, list_kb_dir). It copies and never moves or deletes: the originals stay where they are, and nothing at the destination is replaced. The user sees the plan (file count, size, source, destination) and approves each call before it runs, and the copy then runs as a job in the status panel. Hidden files and links inside a folder are not copied.',
    parameters: PARAMETERS,
  },
}

/** Identifies what a call to the tool does, so an approval given under one
 *  definition does not run a changed one: its parameters and the movers. */
export function transferToolHash(): string {
  return crypto.createHash('sha256').update([TRANSFER_TOOL, JSON.stringify(PARAMETERS), moverIds().join(',')].join('\0')).digest('hex')
}

export interface TransferArgs { library?: string; paths: string[]; destination?: string }

/** A call's arguments in their fixed shape. Only declared parameters are
 *  kept, so nothing else a model sends is ever seen. Throws with the reason. */
export function transferArgs(raw: unknown): TransferArgs {
  let input: unknown = raw
  if (typeof raw === 'string') {
    try { input = raw.trim() ? JSON.parse(raw) : {} } catch { throw new KbError(400, 'The arguments were not valid JSON.') }
  }
  if (!input || typeof input !== 'object' || Array.isArray(input)) throw new KbError(400, 'The arguments were not valid JSON.')
  const a = input as Record<string, unknown>
  const list = Array.isArray(a.paths) ? a.paths : a.paths === undefined || a.paths === null || a.paths === '' ? [] : [a.paths]
  if (!list.length) throw new KbError(400, 'Name at least one file or folder in paths.')
  if (list.length > MAX_PATHS) throw new KbError(400, `One call takes at most ${MAX_PATHS} paths; copy their folder instead.`)
  const paths = [...new Set(list.map(relCheck))]
  const hidden = paths.find(hiddenPath)
  if (hidden) throw new KbError(403, `${hidden} is a hidden path, which the Library does not show, so it is not copied.`)
  const within = paths.find(p => paths.some(q => p.startsWith(`${q}/`)))
  if (within) throw new KbError(400, `${within} is inside another of the paths; name only the folder.`)
  const library = typeof a.library === 'string' && a.library.trim() ? a.library.trim() : undefined
  if (a.destination !== undefined && a.destination !== null && typeof a.destination !== 'string') throw new KbError(400, 'The destination must be a folder path.')
  const dest = typeof a.destination === 'string' ? a.destination.trim() : ''
  if (dest.split('/').includes('..')) throw new KbError(400, 'The destination must be a full path with no "..".')
  return { ...(library ? { library } : {}), paths, ...(dest ? { destination: path.normalize(expandHome(dest)) } : {}) }
}

/** The same call always has the same text, and a different call never does. */
export function canonicalTransfer(a: TransferArgs): string {
  return JSON.stringify({ destination: a.destination, library: a.library, paths: a.paths })
}

/** What a held call records and an approval shows. No key, and nothing the
 *  client can change: it stays on the server with the held call. */
export interface TransferPlan {
  library: string
  libraryLabel: string
  items: string[]
  /** routeId of where the files are read. */
  route: string
  /** routeLabel: the same place in words. */
  source: string
  mover: string
  moverLabel: string
  /** The folder's real path, and how it reads in a plan. */
  dest: string
  destShown: string
  /** Null when neither the index nor a quick count could say. */
  files: number | null
  bytes: number | null
  /** Where those numbers came from. */
  counted: 'index' | 'source' | null
}

const withTimeout = <T>(p: Promise<T>, ms: number): Promise<T | null> => {
  let timer: ReturnType<typeof setTimeout>
  return Promise.race([p.catch(() => null), new Promise<null>(r => { timer = setTimeout(() => r(null), ms) })]).finally(() => clearTimeout(timer))
}

/** What the index knows of the items, or for a folder on this computer the
 *  folder itself when the index does not have them. The index gives no files
 *  for a folder it has not reached yet, and that is not a count. */
async function facts(lib: Library, route: SourceRoute, items: string[], viewerId: string | null): Promise<Pick<TransferPlan, 'files' | 'bytes' | 'counted'>> {
  const indexed = await withTimeout(Promise.all(items.map(i => describe(lib, i, viewerId, true))), FACTS_MS)
  if (indexed?.every(d => d.files > 0)) return { files: indexed.reduce((n, d) => n + d.files, 0), bytes: indexed.reduce((n, d) => n + d.size, 0), counted: 'index' }
  if (route.kind === 'local') {
    const l = await withTimeout(listSource(route, items), FACTS_MS)
    if (l) return { files: l.files.length, bytes: l.bytes, counted: 'source' }
  }
  return { files: null, bytes: null, counted: null }
}

/** The first place that has every item, with the reasons the others did not.
 *  A path that leads outside the library ends the search: it would on any
 *  host. */
async function reach(lib: Library, items: string[], viewerId: string | null): Promise<{ route: SourceRoute; mover: Mover; kinds: Map<string, ItemKind> }> {
  const { routes, tried } = sourceRoutes(lib, viewerId)
  let status = 404
  for (const route of routes) {
    const picked = moverFor(route)
    if (!('mover' in picked)) { tried.push(`${route.name}: ${picked.why}`); continue }
    try {
      return { route, mover: picked.mover, kinds: await lookAt(route, items) }
    } catch (e) {
      const s = e instanceof KbError ? e.status : 502
      if (s === 400) throw e
      if (s !== 404) status = 502
      const said = String((e as Error).message).replace(`${route.name}: `, '')
      tried.push(`${route.name}: ${said}`)
    }
  }
  throw new KbError(status, `These files could not be reached, so nothing was planned. Tried ${tried.join('; ').replace(/\.$/, '') || 'nowhere: this library has no location for its files'}.`)
}

/** Makes the plan for a call, or says why there can be none. */
export async function planTransfer(raw: unknown, ctx: { viewerId: string | null; library: string | null }): Promise<
  { args: TransferArgs; canonical: string; plan: TransferPlan } | { problem: string }
> {
  try {
    const given = transferArgs(raw)
    const want = given.library ?? ctx.library
    if (String(want ?? '').toLowerCase() === 'all') throw new KbError(400, 'Name one library (an id from list_libraries) to copy from.')
    const lib = getLibrary(want, undefined, ctx.viewerId)
    const destination = given.destination ? namedFolder(given.destination) : defaultFolder()
    if (!destination) throw new KbError(403, 'This Studio has no folder to copy into. The deployment can name them in STUDIO_TRANSFER_FOLDERS.')
    const dest = await checkDestination(destination)
    const names = topNames(given.paths)
    await assertNoneExist(dest.folder, names)
    const { route, mover } = await reach(lib, given.paths, ctx.viewerId)
    const known = await facts(lib, route, given.paths, ctx.viewerId)
    if (known.bytes !== null) assertFits(known.bytes, await freeBytes(dest.existing), dest.folder)
    const args: TransferArgs = { library: lib.id, paths: given.paths, destination }
    return {
      args, canonical: canonicalTransfer(args),
      plan: {
        library: lib.id, libraryLabel: lib.label, items: given.paths, route: routeId(route), source: routeLabel(route),
        mover: mover.id, moverLabel: mover.label, dest: dest.folder, destShown: shownFolder(dest.folder), ...known,
      },
    }
  } catch (e) {
    if (e instanceof KbError) return { problem: `${e.message}${/Nothing|nothing/.test(e.message) ? '' : ' Nothing was copied.'}` }
    return { problem: `The copy could not be planned: ${String((e as Error).message ?? e).slice(0, 200)}` }
  }
}

/** Text for a person from a name that came from a file system: control
 *  characters, and the ones that reorder or hide text, are written as
 *  escapes, so a file name cannot change what the approval appears to say. */
export function shownText(s: string): string {
  return s.replace(/[\0-\x1f\x7f-\x9f\u200b-\u200f\u2028-\u202e\u2060-\u2069\ufeff]/g, c => {
    if (c === '\n') return '\\n'
    if (c === '\t') return '\\t'
    return `\\u${c.charCodeAt(0).toString(16).padStart(4, '0')}`
  })
}

export const plural = (n: number, one: string) => `${n.toLocaleString('en-US')} ${one}${n === 1 ? '' : 's'}`

/** What is being copied, in a few words. */
export function planSummary(p: Pick<TransferPlan, 'items' | 'files' | 'bytes'>): string {
  if (p.files === null || p.bytes === null) return plural(p.items.length, 'item')
  return `${plural(p.files, 'file')} (${size(p.bytes)})`
}

/** The approval part the chat draws for a held transfer, in the shape
 *  platformTools.approvalPart gives, with the plan's rows. */
export function transferApprovalPart(plan: TransferPlan, toolCallId: string, expiresAt: string): Record<string, unknown> {
  const shown = plan.items.slice(0, 10).map(shownText)
  const more = plan.items.length - shown.length
  return {
    kind: 'approval',
    id: toolCallId,
    approvalKind: 'confirm',
    toolName: TRANSFER_TOOL,
    subject: 'call',
    header: `Copy ${planSummary(plan)} to ${shownText(plan.destShown)}?`,
    command: `${TRANSFER_TOOL}(library: ${JSON.stringify(plan.library)}, paths: ${shownText(JSON.stringify(plan.items.slice(0, 10)))}${more ? ` and ${more} more` : ''}, destination: ${shownText(JSON.stringify(plan.dest))})`,
    reason: 'Copies only: the originals stay where they are, and nothing at the destination is replaced. It runs as a job in the status panel, and the Studio checks the copy before it appears there.',
    canAllowDirectory: false,
    expiresAt,
    // Not part of the package's ApprovalPart: the plan, which the Studio
    // draws above the card.
    plan: [
      { label: 'Files', value: plan.files === null ? 'Counted when the job starts' : plural(plan.files, 'file') },
      { label: 'Size', value: plan.bytes === null ? 'Counted when the job starts' : size(plan.bytes) },
      { label: 'From', value: `${plan.libraryLabel}: ${shownText(plan.source)}` },
      { label: 'Items', value: shown.join(', ') + (more ? ` and ${more} more` : '') },
      { label: 'To', value: `${shownText(plan.dest)} on ${authEnabled() ? 'the Studio\'s server' : 'this computer'} (${os.hostname()})` },
      { label: 'Mover', value: plan.moverLabel },
      ...(plan.counted === 'index' ? [{ label: 'Note', value: 'The numbers are the index\'s, which can lag behind the files. The job counts them again at the source.' }] : []),
    ],
  }
}

/**
 * Whether a viewer can be offered copies, and if not, why. Held to the same
 * rules as the tools that change the platform for who may approve (a person
 * present, in their own conversation) with none of its need for a platform
 * or a platform key, so a standalone Studio has them. changesAvailable is
 * not involved: those tools keep their own test.
 */
export function transfersAvailable(o: {
  viewer: { id: string } | null
  owner?: string | null
  username?: string | null
}): { ok: true } | { ok: false; why: string } {
  if (!o.viewer) return { ok: false, why: 'nobody in this chat can approve a copy' }
  if (authEnabled() && o.owner && o.owner !== o.username) return { ok: false, why: 'this conversation belongs to someone else' }
  if (!defaultFolder()) return { ok: false, why: 'this Studio signs people in and the deployment has named no folder for copies (STUDIO_TRANSFER_FOLDERS)' }
  return { ok: true }
}
