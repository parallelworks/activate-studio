import { toast } from '@parallelworks/ui'
import { copyWithTextarea } from './clipboard'

/**
 * Links to files and folders (docs/REFERENCE.md, Links to items):
 * <the Studio's address>/?item=<id>#item=<id>. The ID is an opaque one the
 * Studio keeps; no path or name is in the link, and the ID opens nothing
 * by itself. It is in the query, which a sign-in keeps, and in the
 * fragment, which a redirect that drops the query keeps.
 */
const ID = /^[A-Za-z0-9_-]{22}$/

/** The item an address names, or null. An ID no Studio makes reads as
 *  'invalid', which the server answers as it does any link that opens
 *  nothing. */
export function itemInAddress(at: { search: string; hash: string }): string | null {
  const inHash = /^#item=(.*)$/.exec(at.hash)?.[1]
  const params = new URLSearchParams(at.search)
  if (params.has('embed')) return null
  const id = inHash ?? params.get('item')
  if (id === null || id === undefined) return null
  return ID.test(id) ? id : 'invalid'
}

/** The same address with the item taken off, as it reads once the link has
 *  been followed. Everything else in it stays. */
export function withoutItem(at: { pathname: string; search: string; hash: string }): string {
  const query = at.search.replace(/^\?/, '').split('&').filter(p => p && p !== 'item' && !p.startsWith('item=')).join('&')
  return at.pathname + (query ? `?${query}` : '') + (at.hash.startsWith('#item=') ? '' : at.hash)
}

/** Takes the item off the address on screen, once its link was followed. */
export function dropItem(): void {
  const to = withoutItem(location)
  if (to !== location.pathname + location.search + location.hash) history.replaceState(null, '', to)
}

/** A host only this computer reaches. */
export function thisComputerOnly(base: string): boolean {
  let u: URL
  try { u = new URL(base) } catch { return true }
  if (u.protocol !== 'http:' && u.protocol !== 'https:') return true
  const h = u.hostname.replace(/^\[|\]$/g, '').toLowerCase()
  return h === 'localhost' || h.endsWith('.localhost') || h === '::1' || h === '0.0.0.0' || /^127\./.test(h)
}

/** The address links start with: the Studio's lasting one where it has one
 *  (linkBase), else the one this page is open at. */
export function linkStart(linkBase: string | undefined, at: { origin: string; pathname: string } = location): string {
  return (linkBase || at.origin + at.pathname).replace(/\/(index\.html)?$/, '')
}

export function linkAddress(start: string, id: string): string {
  return `${start}/?item=${id}#item=${id}`
}

/** What the action is called: a link to this computer's own Studio opens
 *  nowhere else, and says so. */
export function copyLinkLabel(linkBase: string | undefined): string {
  return thisComputerOnly(linkStart(linkBase)) ? 'Copy link (this computer only)' : 'Copy link'
}

/** Asks the Studio for the item's ID, made the first time it is asked for. */
export async function makeLink(item: { library: string; path: string }): Promise<string> {
  const res = await fetch('/api/links', {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ path: item.path, library: item.library }),
  })
  const body = await res.json().catch(() => null) as { id?: string; error?: string } | null
  if (!res.ok || !body?.id) throw new Error(body?.error ?? 'The link could not be made.')
  return body.id
}

/** Where a link could be neither copied nor refused: shown to copy by hand. */
let showAddress: ((address: string) => void) | null = null
export function onLinkToShow(fn: (address: string) => void): () => void {
  showAddress = fn
  return () => { if (showAddress === fn) showAddress = null }
}

/** Puts text that is still being made on the clipboard. WebKit takes a copy
 *  only from the click itself, so the text goes to it as a promise. */
async function copyWhenReady(text: Promise<string>): Promise<boolean> {
  try {
    if (typeof ClipboardItem === 'function' && navigator.clipboard?.write) {
      await navigator.clipboard.write([new ClipboardItem({ 'text/plain': text.then(t => new Blob([t], { type: 'text/plain' })) })])
      return true
    }
  } catch { /* refused, or it takes no promise: the plain way */ }
  const t = await text
  try { await navigator.clipboard.writeText(t); return true } catch { return copyWithTextarea(t) }
}

/** Copy link, from a click. */
export async function copyItemLink(item: { library: string; path: string }, linkBase: string | undefined): Promise<void> {
  const start = linkStart(linkBase)
  const address = makeLink(item).then(id => linkAddress(start, id))
  address.catch(() => { /* said below */ })
  let copied = false
  try { copied = await copyWhenReady(address) } catch { /* said below */ }
  try {
    const a = await address
    if (!copied) { showAddress?.(a); return }
    toast.success(thisComputerOnly(start) ? 'Link copied. It opens on this computer only.' : 'Link copied. It opens for people who can open the item.')
  } catch (e) {
    toast.error(e instanceof Error ? e.message : String(e))
  }
}

export type Opened =
  | { state: 'opened'; library: string; path: string; type: 'file' | 'dir'; replaced: boolean }
  | { state: 'none' | 'deleted' | 'unreachable' | 'failed'; text: string }
  | { state: 'signed-out'; text: string; signIn?: string }

/** Where a link leads for the person here. */
export async function openItem(id: string): Promise<Opened> {
  let res: Response
  try { res = await fetch(`/api/links/${encodeURIComponent(id)}`, { cache: 'no-store' }) } catch {
    return { state: 'failed', text: 'The Studio could not be reached. Try again in a moment.' }
  }
  const b = await res.json().catch(() => ({})) as { library?: string; path?: string; type?: string; replaced?: boolean; error?: string; reason?: string; signIn?: string }
  if (res.ok && b.library) {
    return { state: 'opened', library: b.library, path: b.path ?? '', type: b.type === 'dir' ? 'dir' : 'file', replaced: !!b.replaced }
  }
  if (res.status === 401) return { state: 'signed-out', text: 'Sign in to open this link.', signIn: b.signIn }
  const text = b.error ?? 'This link could not be opened. Try again in a moment.'
  const said = b.reason === 'none' || b.reason === 'deleted' || b.reason === 'unreachable' ? b.reason : 'failed'
  return { state: said, text }
}

/** Said over an item that is not the one its link was made for. */
export function replacedText(type: 'file' | 'dir'): string {
  return type === 'dir'
    ? 'This folder was replaced after the link was made. It may hold different files; check before relying on them.'
    : 'This file was replaced after the link was made. It may be a newer version of the same document or a different one; check it before relying on it.'
}
