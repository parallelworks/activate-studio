import { useCallback, useEffect, useState } from 'react'
import { useChat } from '@parallelworks/ai-chat'

/**
 * A rail of ticks along the bottom of the thread, one per message, so a
 * long conversation can be navigated by pointing at it rather than by
 * scrolling. Hovering a tick previews that message; clicking jumps to it.
 *
 * Prototype, and coupled to the chat package's markup on purpose. The
 * package exposes no per-message anchor and no handle on its scroll
 * container, so the rendered messages are found positionally. Two
 * consequences shape this file. Every lookup fails soft, so a markup
 * change makes the rail disappear rather than break the thread. And the
 * DOM is the source of truth for what exists, because the conversation in
 * state is not necessarily what is on screen: branching renders a subset,
 * and a pseudo message renders nothing at all. The message array is used
 * only for role and text, and only when its count agrees with the DOM.
 *
 * The durable version belongs in the package, where a message id on the
 * element would remove all of this guessing.
 */

interface Msg { id: string; role: string; content?: string | null; pseudo?: unknown }
interface Tick { role: 'user' | 'assistant' | null; text: string }

const MIN_TICK = 5
const MAX_TICK = 22
const PREVIEW_CHARS = 220
const MIN_MESSAGES = 6

function scrollHost(): HTMLElement | null {
  const canvas = document.querySelector('.chat-canvas')
  if (!canvas) return null
  const candidates = canvas.querySelectorAll<HTMLElement>('.overflow-y-auto')
  for (const el of candidates) if (el.querySelector('.max-w-4xl')) return el
  return null
}

/** The shape this needs from an element, kept narrow so the rule below
 *  can be exercised without a DOM. */
export interface BlockLike {
  textContent: string | null
  className: string
  querySelector(selector: string): unknown
}

/**
 * Which of the list's children are messages. The list also holds the
 * streaming and queued blocks and a trailing spacer. The spacer carries no
 * text, so it drops out on its own; a queued message has to be excluded by
 * its marker class, which is the one class name worth naming here because
 * it is the only thing distinguishing a queued message from a sent one.
 * Exported for the sake of being testable without a browser.
 */
export function selectMessageBlocks<T extends BlockLike>(children: T[]): T[] {
  return children.filter(c =>
    !!c.textContent?.trim()
    && !c.querySelector('[class*="group/queued"]')
    && !c.className.includes('group/queued'))
}

/** The message blocks currently on screen, in order. */
function messageNodes(): HTMLElement[] {
  const list = scrollHost()?.querySelector<HTMLElement>('.max-w-4xl')
  if (!list) return []
  const children = Array.from(list.children).filter((c): c is HTMLElement => c instanceof HTMLElement)
  return selectMessageBlocks(children)
}

export function ConversationScrubber() {
  const { currentConversation, isStreaming } = useChat()
  const [ticks, setTicks] = useState<Tick[]>([])
  const [hover, setHover] = useState<number | null>(null)
  const [current, setCurrent] = useState(0)

  const messages = (currentConversation?.messages ?? []) as Msg[]
  const conversationId = currentConversation?.id ?? null

  // Rebuild from the DOM whenever the thread could have changed. Reading
  // after paint matters: the nodes do not exist until the thread renders.
  useEffect(() => {
    let cancelled = false
    const build = () => {
      if (cancelled) return
      const nodes = messageNodes()
      const real = messages.filter(m => !m.pseudo)
      // Trust the data for role and text only when it describes the same
      // number of things the DOM is showing; otherwise fall back to what
      // is on screen, which is always right about what can be scrolled to.
      const aligned = real.length === nodes.length
      setTicks(nodes.map((n, i) => aligned
        ? { role: real[i].role === 'user' ? 'user' : 'assistant', text: (real[i].content ?? '').trim() }
        : { role: null, text: (n.textContent ?? '').trim() }))
    }
    const raf = requestAnimationFrame(build)
    return () => { cancelled = true; cancelAnimationFrame(raf) }
  }, [conversationId, messages.length, isStreaming])

  const jump = useCallback((i: number) => {
    messageNodes()[i]?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }, [])

  // Where you are now, so the rail reads as a position rather than a list.
  // Computed on scroll rather than with an observer per message, which at
  // several hundred messages would be a lot of observers for one number.
  useEffect(() => {
    const host = scrollHost()
    if (!host || ticks.length === 0) return
    let frame = 0
    const onScroll = () => {
      if (frame) return
      frame = requestAnimationFrame(() => {
        frame = 0
        const nodes = messageNodes()
        const top = host.getBoundingClientRect().top
        let best = 0
        for (let i = 0; i < nodes.length; i++) {
          if (nodes[i].getBoundingClientRect().top - top <= 8) best = i
          else break
        }
        setCurrent(best)
      })
    }
    host.addEventListener('scroll', onScroll, { passive: true })
    onScroll()
    return () => {
      host.removeEventListener('scroll', onScroll)
      if (frame) cancelAnimationFrame(frame)
    }
  }, [ticks.length])

  // Below a handful of messages the rail is clutter: scrolling already
  // finds everything.
  if (ticks.length < MIN_MESSAGES) return null

  const longest = Math.max(...ticks.map(t => t.text.length), 1)
  const preview = hover != null ? ticks[hover] : null

  return (
    <div className="chat-scrubber" aria-label="Conversation timeline">
      {preview && (
        <div
          className="chat-scrubber-card"
          style={{ left: `${((hover! + 0.5) / ticks.length) * 100}%` }}
        >
          {preview.role && (
            <div className="chat-scrubber-role">{preview.role === 'user' ? 'You' : 'Assistant'}</div>
          )}
          <div className="chat-scrubber-text">{preview.text.slice(0, PREVIEW_CHARS) || '(no text)'}</div>
        </div>
      )}
      <div className="chat-scrubber-rail">
        {ticks.map((t, i) => (
          <button
            key={i}
            type="button"
            className={`chat-scrubber-tick${t.role === 'user' ? ' user' : ''}${i === current ? ' current' : ''}`}
            style={{ height: `${MIN_TICK + Math.round((MAX_TICK - MIN_TICK) * Math.sqrt(t.text.length / longest))}px` }}
            title={`Message ${i + 1} of ${ticks.length}`}
            onMouseEnter={() => setHover(i)}
            onMouseLeave={() => setHover(h => (h === i ? null : h))}
            onFocus={() => setHover(i)}
            onBlur={() => setHover(null)}
            onClick={() => jump(i)}
          />
        ))}
      </div>
    </div>
  )
}
