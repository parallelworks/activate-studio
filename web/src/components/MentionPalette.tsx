import { useEffect, useRef, useState } from 'react'
import { useAppConfig } from '../config'
import { getChatLibrary } from '../labelScope'

/**
 * Library files for the composer: type @ and the chat's library appears,
 * its top folder first; typing narrows it by file name (the index's name
 * search, or the folder's own listing where the library has no index yet),
 * a folder opens in place, and choosing a file writes @[path] into the
 * message, or @[library:path] for a library other than the main one. The
 * server reads each named file into the turn (chat/mentions.go), and the
 * sent message draws the token as a link to the file (MentionLinks).
 *
 * Like SlashPalette, this attaches to the chat package's textarea from
 * outside: a capture-phase listener sees arrows and Enter before the
 * package does, and a choice goes in through the native value setter and
 * an input event, which is how a controlled textarea learns of it.
 */
export interface MentionItem { path: string; name: string; dir: boolean; lib: string; libLabel: string }

/** The token a chosen file becomes in the message. */
export function mentionToken(path: string, lib: string, primaryId: string): string {
  return lib && lib !== primaryId ? `@[${lib}:${path}]` : `@[${path}]`
}

/** The @ query the caret is in, if any: the text after an @ that starts a
 *  word, up to the caret, with no spaces or brackets in it. */
export function mentionQuery(value: string, caret: number): { start: number; query: string } | null {
  const m = /(^|\s)@([^\s@[\]]{0,200})$/.exec(value.slice(0, caret))
  return m ? { start: caret - m[2].length - 1, query: m[2] } : null
}

interface TreeEntry { name: string; path: string; type: string }
interface Hit { path: string; name?: string; library?: { id: string; label: string } | null }

export function MentionPalette({ canvas }: { canvas: React.RefObject<HTMLDivElement | null> }) {
  const cfg = useAppConfig()
  const libs = cfg.libraries ?? []
  const primary = libs.find(l => l.primary) ?? libs[0]
  const primaryId = primary?.id ?? 'kb'
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [items, setItems] = useState<MentionItem[]>([])
  const [loading, setLoading] = useState(false)
  const [index, setIndex] = useState(0)
  const [pos, setPos] = useState<{ left: number; bottom: number; width: number } | null>(null)
  const taRef = useRef<HTMLTextAreaElement | null>(null)
  const startRef = useRef(0)
  const stateRef = useRef({ open: false, index: 0, items: [] as MentionItem[] })
  stateRef.current = { open, index: Math.min(index, Math.max(0, items.length - 1)), items }

  // The chat's library: one id, else the main one (All searches every
  // library by name, and browses the main one's folders).
  const chatLib = getChatLibrary()
  const libId = chatLib && chatLib !== 'all' ? chatLib : primaryId
  const labelOf = (id: string) => libs.find(l => l.id === id)?.label ?? id

  useEffect(() => {
    if (!open) return
    let gone = false
    const ctl = new AbortController()
    const folderOf = (q: string) => (q.includes('/') ? q.slice(0, q.lastIndexOf('/')) : '')
    const listFolder = async (dir: string, filter: string): Promise<MentionItem[]> => {
      const r = await fetch(`/api/kb/tree?path=${encodeURIComponent(dir)}&library=${encodeURIComponent(libId)}`, { signal: ctl.signal })
      if (!r.ok) return []
      const d = (await r.json()) as { entries?: TreeEntry[] }
      const f = filter.toLowerCase()
      return (d.entries ?? [])
        .filter(e => !f || e.name.toLowerCase().includes(f))
        .sort((a, b) => Number(b.type === 'dir') - Number(a.type === 'dir') || a.name.localeCompare(b.name))
        .slice(0, 40)
        .map(e => ({ path: e.path, name: e.name, dir: e.type === 'dir', lib: libId, libLabel: labelOf(libId) }))
    }
    const run = async () => {
      setLoading(true)
      try {
        let next: MentionItem[]
        if (query === '' || query.endsWith('/')) {
          next = await listFolder(query.replace(/\/+$/, ''), '')
        } else {
          const base = query.slice(query.lastIndexOf('/') + 1)
          const scope = chatLib === 'all' ? 'all' : libId
          const r = await fetch(`/api/search?name=${encodeURIComponent(base)}&limit=40&library=${encodeURIComponent(scope)}`, { signal: ctl.signal })
          const d = r.ok ? ((await r.json()) as { hits?: Hit[] }) : {}
          const want = query.toLowerCase()
          next = (d.hits ?? [])
            .filter(h => !query.includes('/') || h.path.toLowerCase().includes(want))
            .slice(0, 40)
            .map(h => {
              const lib = h.library?.id ?? libId
              return { path: h.path, name: h.name ?? h.path.split('/').pop() ?? h.path, dir: false, lib, libLabel: h.library?.label ?? labelOf(lib) }
            })
          // No index, or nothing by that name in it: the folder's listing.
          if (!next.length) next = await listFolder(folderOf(query), base)
        }
        if (!gone) { setItems(next); setIndex(0) }
      } catch { if (!gone) setItems([]) } finally { if (!gone) setLoading(false) }
    }
    const t = setTimeout(run, query ? 150 : 0)
    return () => { gone = true; clearTimeout(t); ctl.abort() }
    // labelOf reads libs, which the config fixes for the page's life.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, query, libId, chatLib])

  const write = (ta: HTMLTextAreaElement, text: string, keepOpen: boolean) => {
    const caret = ta.selectionStart ?? ta.value.length
    const value = ta.value.slice(0, startRef.current) + text + ta.value.slice(caret)
    const setter = Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set
    setter?.call(ta, value)
    ta.dispatchEvent(new Event('input', { bubbles: true }))
    ta.focus()
    const at = startRef.current + text.length
    ta.setSelectionRange(at, at)
    if (!keepOpen) setOpen(false)
  }
  const accept = (item: MentionItem) => {
    const ta = taRef.current
    if (!ta) return
    if (item.dir) {
      // A folder opens in place: the query becomes its path.
      write(ta, `@${item.path}/`, true)
      setQuery(`${item.path}/`)
      return
    }
    write(ta, `${mentionToken(item.path, item.lib, primaryId)} `, false)
  }
  const acceptRef = useRef(accept)
  acceptRef.current = accept

  useEffect(() => {
    const root = canvas.current
    if (!root) return
    let detach: (() => void) | null = null
    const place = (ta: HTMLTextAreaElement) => {
      const c = root.getBoundingClientRect()
      const t = ta.getBoundingClientRect()
      setPos({ left: t.left - c.left, bottom: c.bottom - t.top + 8, width: Math.min(620, Math.max(320, t.width)) })
    }
    const attach = () => {
      const ta = root.querySelector('textarea') as HTMLTextAreaElement | null
      if (!ta || ta === taRef.current) return
      detach?.()
      taRef.current = ta
      const check = () => {
        const q = mentionQuery(ta.value, ta.selectionStart ?? ta.value.length)
        if (q) { startRef.current = q.start; setQuery(q.query); setOpen(true); place(ta) } else setOpen(false)
      }
      const onKey = (e: KeyboardEvent) => {
        const st = stateRef.current
        if (!st.open) return
        if (e.key === 'ArrowDown') { e.preventDefault(); e.stopPropagation(); setIndex(i => Math.min(i + 1, st.items.length - 1)); return }
        if (e.key === 'ArrowUp') { e.preventDefault(); e.stopPropagation(); setIndex(i => Math.max(i - 1, 0)); return }
        if ((e.key === 'Enter' && !e.shiftKey) || e.key === 'Tab') {
          const item = st.items[st.index]
          // Enter with nothing listed still closes the list, never sends
          // a half-typed @ by surprise.
          e.preventDefault(); e.stopPropagation()
          if (item) acceptRef.current(item); else setOpen(false)
          return
        }
        if (e.key === 'Escape') { e.preventDefault(); e.stopPropagation(); setOpen(false) }
      }
      const onBlur = () => setTimeout(() => setOpen(false), 150)
      ta.addEventListener('input', check)
      ta.addEventListener('click', check)
      ta.addEventListener('keydown', onKey, true)
      ta.addEventListener('blur', onBlur)
      detach = () => {
        ta.removeEventListener('input', check); ta.removeEventListener('click', check)
        ta.removeEventListener('keydown', onKey, true); ta.removeEventListener('blur', onBlur); taRef.current = null
      }
    }
    attach()
    // The composer remounts when the conversation changes; follow it.
    const obs = new MutationObserver(attach)
    obs.observe(root, { childList: true, subtree: true })
    return () => { obs.disconnect(); detach?.() }
  }, [canvas])

  if (!open || !pos) return null
  const cur = Math.min(index, Math.max(0, items.length - 1))
  const multi = libs.length > 1
  return (
    <div className="slash-palette mention-palette" style={{ left: pos.left, bottom: pos.bottom, width: pos.width }} role="listbox" aria-label="Library files" data-testid="mention-palette">
      <div className="mention-head muted">{query && !query.endsWith('/') ? `Files named “${query}”` : `${query ? query : 'Top folder'} in ${labelOf(libId)}`}</div>
      {items.map((it, i) => (
        <div
          key={`${it.lib}:${it.path}`}
          role="option"
          aria-selected={i === cur}
          className={`slash-item mention-item${i === cur ? ' current' : ''}`}
          onMouseDown={e => { e.preventDefault(); accept(it) }}
          onMouseEnter={() => setIndex(i)}
        >
          <span className="mention-name">{it.dir ? `${it.name}/` : it.name}</span>
          <span className="slash-desc">{it.path.includes('/') ? it.path.slice(0, it.path.lastIndexOf('/') + 1) : ''}{multi && it.lib !== primaryId ? ` · ${it.libLabel}` : ''}</span>
        </div>
      ))}
      {!items.length && <div className="mention-empty muted">{loading ? 'Looking…' : 'No files match. Type part of a name, or / to open a folder.'}</div>}
      <div className="slash-foot muted">Enter or Tab picks · a folder opens · Esc closes</div>
    </div>
  )
}
