/**
 * Citation links in replies, repaired as they stream.
 *
 * The prompt asks for each cited file as [path](#open=file:path), and the
 * web app opens such a link in the library viewer. Small models get this
 * wrong in a few recurring ways: they copy a placeholder from the prompt
 * into the path ([relative/path/notes/a.pdf]), leave out the link target,
 * percent-encode the link text, or leave out the library of a file that
 * is not in the main folder, so the link opens the wrong library. Each of
 * those is repaired here into the link the prompt asked for.
 *
 * The repair runs on the server, as the text streams, because the server
 * knows which files the tools returned (a bare [path] is linked only when
 * it names one of them) and which library each came from. A bracket is
 * held back only until it closes, at most a line, so the reader sees no
 * delay beyond that, and what is stored is what was shown.
 */

/** A file a tool returned, and its library; null for the main folder. */
export interface CitedFile { path: string; library: string | null }

/** What a repair knows: the files the tools returned in the conversation,
 *  by path, and the library the conversation searches, which a path that
 *  no tool returned is taken to be in. */
export interface CitationContext {
  known: Map<string, string | null>
  library: string | null
}

const PLACEHOLDER = /^\/?relative\/path\//i
const LIB_ID = /^[a-z0-9][a-z0-9_-]{0,39}$/
/** A bracket longer than this, or one a line ends inside, is not a citation. */
const MAX_HELD = 600

function decoded(s: string): string {
  try { return decodeURIComponent(s) } catch { return s }
}

function normalized(p: string): string {
  return p.trim().replace(/^\.\//, '').replace(/^\/+/, '')
}

/** The library a path is opened in: the one a tool returned it from, else
 *  the conversation's own when that is one library other than the main. */
function libraryOf(p: string, ctx: CitationContext): string | null {
  if (ctx.known.has(p)) return ctx.known.get(p) ?? null
  const lib = ctx.library
  return lib && lib !== 'kb' && lib !== 'all' ? lib : null
}

/** The viewer link for a file: percent-encoded except for its slashes, as
 *  the web app writes it, with parentheses encoded too so the markdown link
 *  stays whole, and &lib= for a library other than the main folder. */
export function openTarget(p: string, library: string | null, q = ''): string {
  const enc = encodeURIComponent(p).replace(/%2F/gi, '/').replace(/\(/g, '%28').replace(/\)/g, '%29')
  return `#open=file:${enc}${q}${library && library !== 'kb' && LIB_ID.test(library) ? `&lib=${library}` : ''}`
}

function link(text: string, p: string, library: string | null, q = ''): string {
  return `[${text.replace(/([[\]])/g, '\\$1')}](${openTarget(p, library, q)})`
}

/** The text a link shows: without the placeholder, and decoded, since
 *  %20 belongs in the target only. */
function shown(text: string): string {
  const t = text.replace(PLACEHOLDER, '')
  return /%[0-9a-f]{2}/i.test(t) ? decoded(t) : t
}

/** A bare [text], not a link: linked when it is a path with the prompt's
 *  placeholder in front, or a path a tool returned. */
function repairBare(inner: string, ctx: CitationContext): string | null {
  const p = normalized(decoded(inner))
  if (!p || p.includes('\n')) return null
  if (PLACEHOLDER.test(p)) {
    const rest = p.replace(PLACEHOLDER, '')
    return rest ? link(rest, rest, libraryOf(rest, ctx)) : null
  }
  return ctx.known.has(p) ? link(p, p, libraryOf(p, ctx)) : null
}

/** A [text](target) link: repaired when its target carries the placeholder,
 *  is a bare path a tool returned, or opens a file of another library
 *  without saying which. */
function repairLink(text: string, target: string, ctx: CitationContext): string | null {
  const t = target.trim()
  let raw: string
  let lib: string | null = null
  let q = ''
  if (t.startsWith('#open=file:')) {
    const m = /^(.*?)(&q=[^&]*)?(?:&lib=([a-z0-9_-]+))?$/.exec(t.slice('#open=file:'.length))
    if (!m) return null
    raw = m[1]
    q = m[2] ?? ''
    lib = m[3] ?? null
  } else if (/^[a-z][a-z0-9+.-]*:|^#|^\/\//i.test(t)) {
    return null // a URL, or a link within the app: not a file path
  } else {
    raw = t
  }
  const path0 = normalized(decoded(raw))
  const placeholder = PLACEHOLDER.test(path0)
  const p = placeholder ? path0.replace(PLACEHOLDER, '') : path0
  if (!p) return null
  const isOpen = t.startsWith('#open=file:')
  // Nothing to repair: a viewer link to a real path whose library is
  // said, or needs no saying, and whose text is plain.
  if (isOpen && !placeholder && !/\s/.test(raw) && (lib || !libraryOf(p, ctx))
    && !PLACEHOLDER.test(text) && !/%[0-9a-f]{2}/i.test(text)) return null
  // A target that is a bare path is a file link only when a tool returned it.
  if (!isOpen && !placeholder && !ctx.known.has(p)) return null
  return link(shown(text) || p, p, lib ?? libraryOf(p, ctx), q)
}

/**
 * Where the bracket at `at` ends, or null while it cannot be told yet
 * (more text may close it). `eof` says no more text is coming. A bracket
 * that is not a citation candidate ends at once, as one character.
 */
function bracketEnd(s: string, at: number, eof: boolean): { end: number; kind: 'text' | 'bare' | 'link' } | null {
  const close = s.indexOf(']', at + 1)
  const nl = s.indexOf('\n', at + 1)
  if (close < 0) {
    if ((nl >= 0) || s.length - at > MAX_HELD || eof) return { end: at + 1, kind: 'text' }
    return null
  }
  if ((nl >= 0 && nl < close) || close - at > MAX_HELD) return { end: at + 1, kind: 'text' }
  if (close + 1 >= s.length) return eof ? { end: close + 1, kind: 'bare' } : null
  if (s[close + 1] !== '(') return { end: close + 1, kind: 'bare' }
  const paren = s.indexOf(')', close + 2)
  const nl2 = s.indexOf('\n', close + 2)
  if (paren < 0) {
    if (nl2 >= 0 || s.length - at > MAX_HELD || eof) return { end: close + 1, kind: 'bare' }
    return null
  }
  if (nl2 >= 0 && nl2 < paren) return { end: close + 1, kind: 'bare' }
  return { end: paren + 1, kind: 'link' }
}

/** Repairs the citations in a whole text (stored replies, and tests). */
export function repairCitations(text: string, ctx: CitationContext): string {
  const s = new CitationStream(ctx)
  return s.push(text) + s.flush()
}

/**
 * The streaming form: push each piece of text as it arrives and send what
 * it returns; flush at the end of a model turn. A possible citation is
 * held until it closes, then sent repaired.
 */
export class CitationStream {
  private held = ''
  /** The character sent last, which comes before what is held. */
  private last = ''
  constructor(private ctx: CitationContext) {}

  push(t: string): string { return this.run(this.held + t, false) }

  /** Everything still held, as the end of the text. */
  flush(): string { return this.run(this.held, true) }

  /** Forget what is held: the turn it came from is being asked again. */
  drop(): void { this.held = '' }

  private run(s: string, eof: boolean): string {
    const out = this.scan(s, eof)
    if (out) this.last = out.at(-1)!
    return out
  }

  private scan(s: string, eof: boolean): string {
    let out = ''
    let i = 0
    this.held = ''
    for (;;) {
      const at = s.indexOf('[', i)
      if (at < 0) return out + s.slice(i)
      out += s.slice(i, at)
      const b = bracketEnd(s, at, eof)
      if (!b) { this.held = s.slice(at); return out }
      const piece = s.slice(at, b.end)
      const prev = at > 0 ? s[at - 1] : this.last
      const next = s[b.end] ?? ''
      let fixed: string | null = null
      // An image, an escaped bracket, code, the second half of a reference
      // link, and a reference or its definition are left as they are.
      const plain = prev !== '!' && prev !== '\\' && prev !== '`'
      if (b.kind === 'bare' && plain && prev !== ']' && next !== '[' && next !== ':') {
        fixed = repairBare(piece.slice(1, -1), this.ctx)
      } else if (b.kind === 'link' && plain) {
        const close = piece.indexOf('](')
        fixed = repairLink(piece.slice(1, close), piece.slice(close + 2, -1), this.ctx)
      }
      out += fixed ?? piece
      i = b.end
    }
  }
}
