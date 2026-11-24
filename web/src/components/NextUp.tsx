import { useChat } from '@parallelworks/ui/ai'
import { useEffect, useRef, useState } from 'react'
import { currentLibrary, setLibrary } from '../api'
import { buildOpenHash } from '../nav'

/**
 * What to type next. When a turn ends, the server reads the tool calls it
 * just stored and offers up to three specific follow-ups: follow the run
 * that was launched, open the file the search cited, run the workflow
 * that was inspected. The first one becomes the composer's placeholder
 * while the box is empty, and Tab fills it in, as in a terminal that
 * completes a command; the others sit as chips above the composer. Typing
 * anything hides them, and a reply that asked a question offers nothing.
 * The composer belongs to the chat package, so this attaches to its
 * textarea from outside, the same way the slash palette does.
 *
 * A file the turn wrote is a chip of its own kind ("open"): a click opens
 * it in the Library viewer, in the library it was written to, and sends
 * nothing. One document written in several formats is one chip, which
 * opens the rendered one and names the rest in its title; past two, the
 * last chip opens the folder the files went to.
 */
interface Suggestion {
  text: string
  why: string
  kind?: 'open'
  path?: string
  lib?: string
  title?: string
  also?: string[]
  folder?: boolean
}

/** A file name cut in its middle to fit max characters, so its end and
 *  extension stay readable: "a-very-long-rep…quarter.pdf". */
export function middleCut(name: string, max = 32): string {
  const chars = [...name]
  if (chars.length <= max) return name
  const dot = name.lastIndexOf('.')
  const ext = dot > 0 && name.length - dot <= 8 ? [...name.slice(dot)].length : 0
  const tail = Math.min(chars.length - 1, ext + Math.max(4, Math.floor((max - ext) / 3)))
  const head = max - 1 - tail
  return chars.slice(0, head).join('') + '…' + chars.slice(chars.length - tail).join('')
}

/** Opens a written file, or the folder of several, in the Library viewer.
 *  A file the chat placed in the main library names no library, so the
 *  main one is chosen when another is open. */
export function openWritten(s: Suggestion): void {
  const lib = s.lib || 'kb'
  const cur = currentLibrary()
  if (cur !== 'all' && cur !== lib) setLibrary(lib)
  const hash = s.path ? buildOpenHash({ kind: 'file', target: s.path, ...(s.lib ? { lib: s.lib } : {}) }) : '#view=library'
  if (location.hash === hash) window.dispatchEvent(new HashChangeEvent('hashchange'))
  else location.hash = hash
}

export function NextUp({ canvas }: { canvas: React.RefObject<HTMLDivElement | null> }) {
  const { currentConversation, isStreaming } = useChat()
  const [items, setItems] = useState<Suggestion[]>([])
  const [empty, setEmpty] = useState(true)
  const [pos, setPos] = useState<{ left: number; bottom: number; width: number } | null>(null)
  const taRef = useRef<HTMLTextAreaElement | null>(null)
  const originalPlaceholder = useRef<string | null>(null)
  const seen = useRef<string | null>(null)
  const said = items.filter(s => s.kind !== 'open')
  const opens = items.filter(s => s.kind === 'open')
  const itemsRef = useRef<Suggestion[]>([])
  itemsRef.current = said
  const placeholder = said[0]?.text ?? ''

  const msgs = currentConversation?.messages ?? []
  const last = msgs[msgs.length - 1]
  const convId = currentConversation?.id ?? null
  const lastKey = last ? `${convId}:${last.id}:${last.role}` : null

  useEffect(() => {
    if (isStreaming || !convId || !last || last.role !== 'assistant') { if (!isStreaming && (!last || last.role !== 'assistant')) setItems([]); return }
    if (seen.current === lastKey) return
    seen.current = lastKey
    fetch(`/api/chat/suggestions?conversation=${encodeURIComponent(convId)}`).then(r => r.json())
      .then(d => {
        const all: Suggestion[] = Array.isArray(d.suggestions) ? d.suggestions : []
        setItems([...all.filter(s => s.kind !== 'open').slice(0, 3), ...all.filter(s => s.kind === 'open').slice(0, 2)])
      })
      .catch(() => setItems([]))
  }, [isStreaming, convId, lastKey, last])

  const fill = (text: string) => {
    const ta = taRef.current
    if (!ta) return
    Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set?.call(ta, text)
    ta.dispatchEvent(new Event('input', { bubbles: true }))
    ta.focus()
    ta.setSelectionRange(text.length, text.length)
    setItems([])
  }

  // Attach to the composer, follow it across remounts, and own its
  // placeholder only while a suggestion is showing and the box is empty.
  useEffect(() => {
    const root = canvas.current
    if (!root) return
    let detach: (() => void) | null = null
    const place = (ta: HTMLTextAreaElement) => {
      const c = root.getBoundingClientRect(); const t = ta.getBoundingClientRect()
      setPos({ left: t.left - c.left, bottom: c.bottom - t.top + 8, width: Math.min(720, Math.max(320, t.width)) })
    }
    const attach = () => {
      const ta = root.querySelector('textarea') as HTMLTextAreaElement | null
      if (!ta || ta === taRef.current) { if (ta) place(ta); return }
      detach?.()
      taRef.current = ta
      originalPlaceholder.current = ta.getAttribute('placeholder')
      setEmpty(!ta.value)
      place(ta)
      const onInput = () => { setEmpty(!ta.value); place(ta) }
      const onKey = (e: KeyboardEvent) => {
        if (e.key !== 'Tab' || e.shiftKey || ta.value) return
        const first = itemsRef.current[0]
        if (!first) return
        e.preventDefault()
        fill(first.text)
      }
      ta.addEventListener('input', onInput)
      ta.addEventListener('keydown', onKey)
      // The box grows when a long placeholder wraps, after it was placed.
      const sized = typeof ResizeObserver === 'undefined' ? null : new ResizeObserver(() => place(ta))
      sized?.observe(ta)
      detach = () => { ta.removeEventListener('input', onInput); ta.removeEventListener('keydown', onKey); sized?.disconnect(); taRef.current = null }
    }
    attach()
    const obs = new MutationObserver(attach)
    obs.observe(root, { childList: true, subtree: true })
    const onResize = () => { if (taRef.current) place(taRef.current) }
    window.addEventListener('resize', onResize)
    return () => { obs.disconnect(); detach?.(); window.removeEventListener('resize', onResize) }
  }, [canvas])

  useEffect(() => {
    const ta = taRef.current
    if (!ta) return
    if (placeholder && empty) ta.setAttribute('placeholder', `${placeholder}  (Tab)`)
    else if (originalPlaceholder.current !== null) ta.setAttribute('placeholder', originalPlaceholder.current)
  }, [placeholder, empty])

  if (!items.length || !empty || !pos || isStreaming) return null
  return (
    <div className="next-up" style={{ left: pos.left, bottom: pos.bottom, width: pos.width }} role="group" aria-label="Suggested next messages">
      {said.map(s => (
        <button key={s.text} className="next-up-chip" title={`Because ${s.why}`} onMouseDown={e => { e.preventDefault(); fill(s.text) }}>{s.text}</button>
      ))}
      {opens.map(s => (
        <button key={`open:${s.lib ?? ''}:${s.path}:${s.text}`} type="button" className="next-up-chip next-up-open" title={s.title} aria-label={s.title}
          onClick={() => openWritten(s)}>
          {s.folder ? FOLDER : FILE}
          <span>{s.folder ? s.text : middleCut(s.text)}</span>
          {s.also?.length ? <span className="next-up-also" aria-hidden="true">+{s.also.map(a => a.slice(a.lastIndexOf('.') + 1)).join(' ')}</span> : null}
        </button>
      ))}
    </div>
  )
}

const FILE = <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true"><path d="M4 1.8h5l3 3v9.4H4z" /><path d="M9 1.8v3h3" /></svg>
const FOLDER = <svg viewBox="0 0 16 16" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="1.4" aria-hidden="true"><path d="M1.8 4.2h4.4l1.4 1.6h6.6v7.4H1.8z" /></svg>
