import { useChat } from '@parallelworks/ai-chat'
import { useEffect, useRef, useState } from 'react'

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
 */
interface Suggestion { text: string; why: string }

export function NextUp({ canvas }: { canvas: React.RefObject<HTMLDivElement | null> }) {
  const { currentConversation, isStreaming } = useChat()
  const [items, setItems] = useState<Suggestion[]>([])
  const [empty, setEmpty] = useState(true)
  const [pos, setPos] = useState<{ left: number; bottom: number; width: number } | null>(null)
  const taRef = useRef<HTMLTextAreaElement | null>(null)
  const originalPlaceholder = useRef<string | null>(null)
  const seen = useRef<string | null>(null)
  const itemsRef = useRef<Suggestion[]>([])
  itemsRef.current = items

  const msgs = currentConversation?.messages ?? []
  const last = msgs[msgs.length - 1]
  const convId = currentConversation?.id ?? null
  const lastKey = last ? `${convId}:${last.id}:${last.role}` : null

  useEffect(() => {
    if (isStreaming || !convId || !last || last.role !== 'assistant') { if (!isStreaming && (!last || last.role !== 'assistant')) setItems([]); return }
    if (seen.current === lastKey) return
    seen.current = lastKey
    fetch(`/api/chat/suggestions?conversation=${encodeURIComponent(convId)}`).then(r => r.json())
      .then(d => setItems(Array.isArray(d.suggestions) ? d.suggestions.slice(0, 3) : []))
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
      detach = () => { ta.removeEventListener('input', onInput); ta.removeEventListener('keydown', onKey); taRef.current = null }
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
    const first = items[0]
    if (first && empty) ta.setAttribute('placeholder', `${first.text}  (Tab)`)
    else if (originalPlaceholder.current !== null) ta.setAttribute('placeholder', originalPlaceholder.current)
  }, [items, empty])

  if (!items.length || !empty || !pos || isStreaming) return null
  return (
    <div className="next-up" style={{ left: pos.left, bottom: pos.bottom, width: pos.width }} role="group" aria-label="Suggested next messages">
      {items.map(s => (
        <button key={s.text} className="next-up-chip" title={`Because ${s.why}`} onMouseDown={e => { e.preventDefault(); fill(s.text) }}>{s.text}</button>
      ))}
    </div>
  )
}
