import type { SelectItem } from '@parallelworks/ui'

/** One condition row of the Query builder. */
export interface Filter { field: string; op: string; value: string }

/** The builder's form, as POST /api/query takes it (server/src/query.ts). */
export interface BuilderState {
  source: 'files' | 'directories'
  filters: Filter[]
  /** Every condition must hold, or any one. */
  match: 'all' | 'any'
  groupBy: 'none' | 'extension' | 'directory' | 'owner'
  sort: string
  dir: 'asc' | 'desc'
  limit: number | null
  subtree: string
}

export const FIELDS: SelectItem<string>[] = [
  { value: 'name', label: 'File name' },
  { value: 'path', label: 'Path' },
  { value: 'extension', label: 'Extension' },
  { value: 'owner', label: 'Owner' },
  { value: 'size', label: 'Size' },
  { value: 'modified', label: 'Modified' },
  { value: 'text', label: 'Text content' },
  { value: 'label', label: 'Label' },
]

/** The fields a directory has. */
export const DIR_FIELDS = new Set(['name', 'owner', 'size', 'modified'])

const TEXT_OPS: SelectItem<string>[] = [
  { value: 'contains', label: 'contains' },
  { value: 'not_contains', label: 'does not contain' },
  { value: 'eq', label: 'is' },
]
/** The conditions each field takes, worded for it. */
export const OPS: Record<string, SelectItem<string>[]> = {
  name: TEXT_OPS,
  path: TEXT_OPS,
  extension: [{ value: 'eq', label: 'is' }, { value: 'contains', label: 'contains' }, { value: 'not_contains', label: 'is not like' }],
  owner: TEXT_OPS,
  size: [{ value: 'gt', label: 'is larger than' }, { value: 'lt', label: 'is smaller than' }, { value: 'eq', label: 'is exactly' }],
  modified: [{ value: 'gt', label: 'is after' }, { value: 'lt', label: 'is before' }, { value: 'eq', label: 'is on' }],
  text: [{ value: 'match', label: 'has the words' }],
  label: [{ value: 'eq', label: 'is' }, { value: 'contains', label: 'contains' }],
}

export const PLACEHOLDER: Record<string, string> = {
  name: 'report', path: 'projects/2026', extension: 'pdf', owner: 'user name',
  size: '10 MB', modified: '2026-01-01 or 7 days', text: 'words to find', label: 'label',
}

/** A new row for a field, with its first condition. */
export const newFilter = (field = 'name'): Filter => ({ field, op: OPS[field][0].value, value: '' })

/** A field change keeps the value, and the condition when the new field
 *  takes it. */
export function withField(f: Filter, field: string): Filter {
  const op = OPS[field].some(o => o.value === f.op) ? f.op : OPS[field][0].value
  return { field, op, value: f.value }
}

export const BLANK: BuilderState = {
  source: 'files', filters: [newFilter()], match: 'all', groupBy: 'none', sort: 'size', dir: 'desc', limit: 50, subtree: '',
}

export interface Preset { id: string; label: string; state: Omit<BuilderState, 'subtree'> }

/** One click each; the form shows what they set, to change and run again. */
export const PRESETS: Preset[] = [
  { id: 'biggest', label: 'Biggest files', state: { source: 'files', filters: [], match: 'all', groupBy: 'none', sort: 'size', dir: 'desc', limit: 50 } },
  { id: 'oldest', label: 'Oldest files', state: { source: 'files', filters: [], match: 'all', groupBy: 'none', sort: 'modified', dir: 'asc', limit: 50 } },
  { id: 'recent', label: 'Changed in the last 7 days', state: { source: 'files', filters: [{ field: 'modified', op: 'gt', value: '7 days' }], match: 'all', groupBy: 'none', sort: 'modified', dir: 'desc', limit: 200 } },
  { id: 'dirs', label: 'Largest directories', state: { source: 'directories', filters: [], match: 'all', groupBy: 'none', sort: 'bytes', dir: 'desc', limit: 25 } },
  { id: 'extensions', label: 'Totals by extension', state: { source: 'files', filters: [], match: 'all', groupBy: 'extension', sort: 'bytes', dir: 'desc', limit: 25 } },
  { id: 'owners', label: 'Totals by owner', state: { source: 'files', filters: [], match: 'all', groupBy: 'owner', sort: 'bytes', dir: 'desc', limit: 25 } },
]

/**
 * A query saved as one of the former canned queries, as the builder form
 * that runs the same query: the page now offers those as presets.
 */
export function fromCanned(p: { canned?: string; n?: number; days?: number; subtree?: string }): BuilderState | null {
  const limit = Number(p.n) || 25
  const subtree = p.subtree ?? ''
  const files = { source: 'files' as const, filters: [] as Filter[], match: 'all' as const, groupBy: 'none' as const, limit, subtree }
  switch (p.canned) {
    case 'largest': return { ...files, sort: 'size', dir: 'desc' }
    case 'newest': return { ...files, sort: 'modified', dir: 'desc' }
    case 'oldest': return { ...files, sort: 'modified', dir: 'asc' }
    case 'recent': return { ...files, filters: [{ field: 'modified', op: 'gt', value: `${Number(p.days) || 7} days` }], sort: 'modified', dir: 'desc' }
    case 'by_extension': return { ...files, groupBy: 'extension', sort: 'bytes', dir: 'desc' }
    case 'big_directories': return { ...files, source: 'directories', sort: 'bytes', dir: 'desc' }
    default: return null
  }
}

/** A builder payload from elsewhere (a saved query, another page), with
 *  defaults for what it leaves out. */
export function toState(b: Partial<BuilderState> & { filters?: Filter[] }): BuilderState {
  return {
    source: b.source ?? 'files',
    filters: b.filters?.length ? b.filters : [newFilter()],
    match: b.match === 'any' ? 'any' : 'all',
    groupBy: b.groupBy ?? 'none',
    sort: b.sort ?? 'size',
    dir: b.dir ?? 'desc',
    limit: b.limit ?? 50,
    subtree: b.subtree ?? '',
  }
}

/** What the server is sent: rows with no value left out. */
export function payloadOf(s: BuilderState) {
  return { builder: { ...s, filters: s.filters.filter(f => f.value.trim() !== '') } }
}
