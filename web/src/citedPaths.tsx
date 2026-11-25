import {
  Children, createContext, isValidElement, useContext, useEffect, useMemo, useRef, useSyncExternalStore,
  type ComponentProps, type ReactNode,
} from 'react'
import type { ChatMessage } from '@parallelworks/ui/ai'
import { breakable } from './components/breakable'
import { InLink } from './chatLinks'
import { useAppConfig } from './config'
import { buildOpenHash } from './nav'

/**
 * The library files a reply names, drawn as links that open them. A reply
 * is asked to link the files it cites; a path it leaves as inline code or
 * plain text is looked up here. The paths in view are asked for together
 * (POST /api/libraries/cited), which answers, as the person, those that are
 * files of their libraries; the rest stay as written. What was asked is
 * kept while the chat is open, so nothing is asked again but, once, the
 * paths that were no file yet when the reply naming them ends.
 */

export interface CitedFile { path: string; lib?: string }
interface Also { path: string; lib?: string }

/** Where a chat's paths are: its session's machine and folder, or the
 *  libraries the Studio searches. */
export interface CitedWhere {
  machine?: { id: string; name?: string; hostname?: string }
  cwd?: string
  libraries?: string[]
}

/* ---- what may be a path ---- */

const EXT = /\.[A-Za-z][A-Za-z0-9]{0,9}$/
const NOT_IN_PATH = /[\n\r\t<>|"*?{}\;`$]|:\/\//
const LINES = /(?::\d+(?:[-:]\d+)?|#L\d+(?:-L?\d+)?)$/

const lastPart = (p: string) => p.slice(p.lastIndexOf('/') + 1)

/** The path inline code may be, without the line it ends in; null for code
 *  that is not a path. */
export function codePath(text: string): string | null {
  const p = text.trim().replace(LINES, '')
  if (p.length < 3 || p.length > 500 || NOT_IN_PATH.test(p) || /^[-#@]/.test(p)) return null
  const name = lastPart(p)
  if (!name || name === '.' || name === '..') return null
  if (EXT.test(name)) return p
  return p.includes('/') && !/\s/.test(p) ? p : null
}

// A run of path parts that ends in a name with an extension, or one from
// the root or the home folder. Not a part of a web address or of a longer
// word.
const CH = String.raw`[\p{L}\p{N}_@+.~%&=-]`
const TOKEN = new RegExp(String.raw`(?<![\p{L}\p{N}_/@.:~\\-])(?:(?:~?/|\.{1,2}/)?(?:${CH}+/)*${CH}*[\p{L}\p{N}_)-]\.[A-Za-z][A-Za-z0-9]{0,9}|~?/(?:${CH}+/)+${CH}*[\p{L}\p{N}_])(?![\p{L}\p{N}_/@]|\.[\p{L}\p{N}])`, 'gu')
const WORD = /^[^\s,;:()"“”‘’'`<>|[\]*]*[^\s.,;:!?()"“”‘’'`<>|[\]*]$/u
const MAX_WORDS = 5

export interface TextPath { start: number; end: number; path: string }

/** The paths text may hold, each with the ways back over the words before
 *  it that make a name with spaces in it, the longest first. */
export function textPaths(text: string): TextPath[][] {
  const out: TextPath[][] = []
  if (!text.includes('.') && !text.includes('/')) return out
  let after = 0
  for (const m of text.matchAll(TOKEN)) {
    const start = m.index, end = start + m[0].length
    const bare = !m[0].includes('/')
    if (m[0].length > 500 || (bare && (m[0].includes('@') || !/\.[A-Za-z][A-Za-z0-9]+$/.test(m[0]) || m[0].length < 5))) continue
    const ways: TextPath[] = [{ start, end, path: m[0] }]
    if (EXT.test(m[0]) && !/^[~/.]/.test(m[0])) {
      let at = start
      for (let n = 0; n < MAX_WORDS && at - 1 > after && text[at - 1] === ' '; n++) {
        let from = Math.max(text.lastIndexOf(' ', at - 2) + 1, after)
        const open = /^[("'“‘[]+/.exec(text.slice(from, at - 1))?.[0].length ?? 0
        from += open
        if (!WORD.test(text.slice(from, at - 1))) break
        at = from
        ways.push({ start: at, end, path: text.slice(at, end) })
        if (open) break
        if (at === 0) break
      }
    }
    out.push(ways.reverse())
    after = end
  }
  return out
}

/* ---- the files a chat's steps name in full ---- */

const OPEN_LINK = /#open=file:([^)\s&"\]]+)(?:&q=[^)\s&"\]]*)?(?:&lib=([a-z0-9][a-z0-9_-]*))?/g
const IN_LIBRARY = /\[library ([a-z0-9][a-z0-9_-]*)\] ([^\n]+)/g
const STUDIO_TOOL = /studio|_kb\b|_kb_|corpus|read_original|show_in_viewer|list_libraries/i
const FILE_ARG = /^(?:file_?path|path|notebook_path|filename)$/
const MAX_STEPS = 4000

function unescaped(s: string): string {
  try { return decodeURIComponent(s) } catch { return s }
}

/** The files a chat's tool calls read, wrote or were given, and those its
 *  replies link, by file name. */
export function stepFiles(messages: readonly ChatMessage[]): Map<string, Also[]> {
  const out = new Map<string, Also[]>()
  let n = 0
  const add = (path: string, lib?: string) => {
    path = path.trim()
    const name = lastPart(path)
    if (!name || path.length > 500 || n >= MAX_STEPS) return
    const had = out.get(name) ?? []
    if (had.some(a => a.path === path && a.lib === lib)) return
    out.set(name, [...had, { path, ...(lib ? { lib } : {}) }])
    n++
  }
  const links = (s: string) => {
    if (!s.includes('#open=file:')) return
    for (const m of s.matchAll(OPEN_LINK)) add(unescaped(m[1]), m[2] ?? 'kb')
  }
  for (const m of messages) {
    if (m.role !== 'assistant') continue
    if (typeof m.content === 'string') links(m.content)
    for (const raw of m.parts ?? []) {
      const p = raw as { kind?: string; text?: string; content?: string; name?: string; args?: unknown; result?: unknown }
      links(p.text ?? p.content ?? '')
      if (p.kind !== 'tool_call') continue
      let args: Record<string, unknown> = {}
      try { args = (typeof p.args === 'string' ? JSON.parse(p.args) : p.args ?? {}) as Record<string, unknown> } catch { /* still arriving */ }
      for (const [k, v] of Object.entries(args ?? {})) {
        if (FILE_ARG.test(k) && typeof v === 'string') add(v, typeof args.library === 'string' && args.library !== 'all' ? args.library : undefined)
      }
      const result = typeof p.result === 'string' ? p.result.slice(0, 100_000) : ''
      links(result)
      if (!result || !STUDIO_TOOL.test(p.name ?? '')) continue
      for (const r of result.matchAll(IN_LIBRARY)) add(r[2], r[1])
      for (const line of result.split('\n', 2000)) {
        const l = line.trim().replace(/^(?:[-*]|\d+[.)])\s+/, '')
        if (l.length < 400 && !l.startsWith('[library ') && codePath(l) === l && EXT.test(l)) add(l)
      }
    }
  }
  return out
}

/* ---- asking ---- */

/** How long the paths drawn together are gathered, and while a reply
 *  streams, whose paths come one at a time. */
const GATHER_MS = 200
const STREAM_MS = 1500
/** Paths in one request. */
const BATCH = 300

export class CitedStore {
  private files = new Map<string, CitedFile | null>()
  private drawn = new Map<string, number>()
  private asking = new Set<string>()
  private listeners = new Set<() => void>()
  private timer: ReturnType<typeof setTimeout> | undefined
  private where: CitedWhere | null = null
  private busy = false
  private messages: () => readonly ChatMessage[] = () => []

  /** Where the paths are, null until it is known, and whether a reply is
   *  on its way. */
  set(where: CitedWhere | null, busy: boolean, messages: () => readonly ChatMessage[]) {
    const moved = JSON.stringify(where) !== JSON.stringify(this.where)
    const ended = this.busy && !busy
    this.where = where
    this.busy = busy
    this.messages = messages
    if (moved) this.files.clear()
    // A path named before its file was written.
    else if (ended) for (const [p, f] of this.files) if (!f) this.files.delete(p)
    if (moved || ended) this.gather(GATHER_MS)
    if (moved) this.tell()
  }

  get(path: string): CitedFile | null { return this.files.get(path) ?? null }

  subscribe = (fn: () => void) => { this.listeners.add(fn); return () => { this.listeners.delete(fn) } }

  /** The paths a part of a reply shows, until it is gone. */
  draw(paths: readonly string[]): () => void {
    for (const p of paths) this.drawn.set(p, (this.drawn.get(p) ?? 0) + 1)
    if (paths.some(p => !this.files.has(p) && !this.asking.has(p))) this.gather(this.busy ? STREAM_MS : GATHER_MS)
    return () => {
      for (const p of paths) {
        const n = (this.drawn.get(p) ?? 1) - 1
        if (n > 0) this.drawn.set(p, n)
        else this.drawn.delete(p)
      }
    }
  }

  close() { clearTimeout(this.timer); this.timer = undefined }

  private tell() { for (const fn of this.listeners) fn() }

  private gather(ms: number) {
    if (this.timer !== undefined || !this.where) return
    this.timer = setTimeout(() => { this.timer = undefined; void this.ask() }, ms)
  }

  private async ask() {
    const paths = [...this.drawn.keys()].filter(p => !this.files.has(p) && !this.asking.has(p)).slice(0, BATCH)
    const where = this.where
    if (!paths.length || !where) return
    for (const p of paths) this.asking.add(p)
    const steps = stepFiles(this.messages())
    const body = {
      ...where,
      paths: paths.map(path => {
        const also = (steps.get(lastPart(path)) ?? []).filter(a => a.path === path || a.path.endsWith(`/${path}`)).slice(0, 12)
        return also.length ? { path, also } : { path }
      }),
    }
    try {
      const r = await fetch('/api/libraries/cited', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) })
      if (!r.ok) throw new Error(String(r.status))
      const found = ((await r.json()) as { files?: Record<string, CitedFile> }).files ?? {}
      if (where === this.where || JSON.stringify(where) === JSON.stringify(this.where)) {
        for (const p of paths) this.files.set(p, found[p] ?? null)
        this.tell()
      }
    } catch {
      // Left as written, until another part of the chat is drawn.
    } finally {
      for (const p of paths) this.asking.delete(p)
    }
    if ([...this.drawn.keys()].some(p => !this.files.has(p)) && paths.every(p => this.files.has(p))) this.gather(GATHER_MS)
  }
}

const Store = createContext<CitedStore | null>(null)

/** The paths of the replies drawn inside are looked up there: nothing is
 *  until where is known. */
export function CitedPaths({ where, busy, messages, children }: {
  where: CitedWhere | null
  busy: boolean
  messages: readonly ChatMessage[] | undefined
  children: ReactNode
}) {
  const store = useMemo(() => new CitedStore(), [])
  const held = useRef(messages)
  held.current = messages
  const key = JSON.stringify(where)
  useEffect(() => { store.set(where, busy, () => held.current ?? []) }, [store, key, busy]) // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => () => store.close(), [store])
  return <Store.Provider value={store}>{children}</Store.Provider>
}

const none = () => () => {}

/** The files among paths, as they are known. */
function useCited(paths: readonly string[]): (path: string) => CitedFile | null {
  const store = useContext(Store)
  const inLink = useContext(InLink)
  const on = store && !inLink && paths.length ? store : null
  const key = paths.join('\n')
  useEffect(() => on?.draw(paths), [on, key]) // eslint-disable-line react-hooks/exhaustive-deps
  useSyncExternalStore(on ? on.subscribe : none, () => (on ? paths.map(p => { const f = on.get(p); return f ? `${f.lib ?? ''}:${f.path}` : '' }).join('\n') : ''))
  return p => on?.get(p) ?? null
}

/* ---- drawing ---- */

function CitedLink({ file, children }: { file: CitedFile; children: ReactNode }) {
  const label = useAppConfig().libraries?.find(l => l.id === (file.lib ?? 'kb'))?.label ?? 'the Library'
  return (
    <a className="link wrap-break-word cited-path" title={`Open ${file.path} in ${label}`}
      href={buildOpenHash({ kind: 'file', target: file.path, ...(file.lib ? { lib: file.lib } : {}) })}>{children}</a>
  )
}

function textOf(children: ReactNode): string {
  let s = ''
  Children.forEach(children, c => {
    if (typeof c === 'string' || typeof c === 'number') s += c
    else if (isValidElement<{ children?: ReactNode }>(c)) s += textOf(c.props.children)
  })
  return s
}

/** Inline code (a path, most often) wraps after its separators, never mid
 *  word; the classes are Streamdown's own. One that is a library file's
 *  path opens it. */
function InlineCode({ node: _node, className, children, ...props }: ComponentProps<'code'> & { node?: unknown }) {
  const path = codePath(textOf(children))
  const file = useCited(path ? [path] : [])
  const found = path ? file(path) : null
  const code = (
    <code className={['rounded bg-muted px-1.5 py-0.5 font-mono text-sm', className].filter(Boolean).join(' ')} data-streamdown="inline-code" {...props}>{breakable(children)}</code>
  )
  return found ? <CitedLink file={found}>{code}</CitedLink> : code
}

/** Plain text, with the library files it names as links. */
function CitedText({ text }: { text: string }) {
  const found = useMemo(() => textPaths(text), [text])
  const all = useMemo(() => [...new Set(found.flatMap(ways => ways.map(w => w.path)))], [found])
  const file = useCited(all)
  const out: ReactNode[] = []
  let at = 0
  for (const ways of found) {
    const way = ways.find(w => file(w.path))
    const f = way && file(way.path)
    if (!way || !f) continue
    if (way.start > at) out.push(text.slice(at, way.start))
    out.push(<CitedLink key={way.start} file={f}>{way.path}</CitedLink>)
    at = way.end
  }
  if (!out.length) return <>{text}</>
  if (at < text.length) out.push(text.slice(at))
  return <>{out}</>
}

const citedIn = (children: ReactNode): ReactNode =>
  Children.map(children, c => (typeof c === 'string' && c.trim() ? <CitedText text={c} /> : c))

type Props = { node?: unknown; className?: string; children?: ReactNode }
type Tag = 'h1' | 'h2' | 'h3' | 'h4' | 'h5' | 'h6' | 'td' | 'th' | 'em' | 'del'

/** A tag as Streamdown draws it, its text with links. */
function tag(Name: Tag, classes: string, data?: string) {
  return function CitedTag({ node: _node, className, children, ...props }: Props) {
    const cls = [classes, className].filter(Boolean).join(' ')
    return <Name className={cls || undefined} {...(data ? { 'data-streamdown': data } : {})} {...props}>{citedIn(children)}</Name>
  }
}

const only = (children: ReactNode): ReactNode => {
  const parts = (Array.isArray(children) ? children : [children]).filter(c => c != null && c !== '')
  return parts.length === 1 ? parts[0] as ReactNode : null
}
const nodeName = (c: ReactNode): string | undefined =>
  (isValidElement<{ node?: { tagName?: string } }>(c) ? c.props.node?.tagName : undefined)

function Paragraph({ node: _node, children, ...props }: Props) {
  const one = only(children)
  // As Streamdown: an image or a code block is not set in a paragraph.
  if (nodeName(one) === 'img' || (nodeName(one) === 'code' && isValidElement<object>(one) && 'data-block' in one.props)) return <>{children}</>
  return <p {...props}>{citedIn(children)}</p>
}

function ListItem({ node: _node, className, children, ...props }: Props) {
  const one = only(children)
  const inner = isValidElement<{ children?: ReactNode }>(one) && nodeName(one) === 'p' ? one.props.children : children
  return <li className={['py-1 [&>p]:inline', className].filter(Boolean).join(' ')} data-streamdown="list-item" {...props}>{citedIn(inner)}</li>
}

function Strong({ node: _node, className, children, ...props }: Props) {
  return <span className={['font-semibold', className].filter(Boolean).join(' ')} data-streamdown="strong" {...props}>{citedIn(children)}</span>
}

/** The chat's markdown tags that hold text, for ChatProvider's
 *  markdownComponents. Code blocks and links are not among them. */
export const citedMarkdown = {
  inlineCode: InlineCode,
  p: Paragraph,
  li: ListItem,
  strong: Strong,
  em: tag('em', ''),
  del: tag('del', ''),
  td: tag('td', 'px-4 py-2 text-sm', 'table-cell'),
  th: tag('th', 'whitespace-nowrap px-4 py-2 text-left font-semibold text-sm', 'table-header-cell'),
  h1: tag('h1', 'mt-6 mb-2 font-semibold text-3xl', 'heading-1'),
  h2: tag('h2', 'mt-6 mb-2 font-semibold text-2xl', 'heading-2'),
  h3: tag('h3', 'mt-6 mb-2 font-semibold text-xl', 'heading-3'),
  h4: tag('h4', 'mt-6 mb-2 font-semibold text-lg', 'heading-4'),
  h5: tag('h5', 'mt-6 mb-2 font-semibold text-base', 'heading-5'),
  h6: tag('h6', 'mt-6 mb-2 font-semibold text-sm', 'heading-6'),
} as object
