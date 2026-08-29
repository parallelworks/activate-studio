import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
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

const MIN_TICK = 6
const MAX_TICK = 26
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

/**
 * Why the rail is or is not showing, readable from the console as
 * __adeScrubber(). Every stage here is a place the positional lookup can
 * fail, and without this the answer to "I don't see it" is guesswork.
 */
function installDebug(ticks: number, min: number): void {
  ;(window as unknown as Record<string, unknown>).__adeScrubber = () => {
    const canvas = document.querySelector('.chat-canvas')
    const candidates = canvas ? canvas.querySelectorAll('.overflow-y-auto') : []
    const host = scrollHost()
    const list = host?.querySelector('.max-w-4xl')
    return {
      canvasFound: !!canvas,
      overflowCandidates: candidates.length,
      scrollHostFound: !!host,
      listFound: !!list,
      listChildren: list ? list.children.length : 0,
      messageBlocks: messageNodes().length,
      ticks,
      minimumToShow: min,
      showing: ticks >= min,
      railInDom: !!document.querySelector('.chat-scrubber'),
    }
  }
}

export function ConversationScrubber() {
  const { currentConversation, isStreaming } = useChat()
  const [ticks, setTicks] = useState<Tick[]>([])
  const [hover, setHover] = useState<number | null>(null)
  // The card is placed from the tick's own geometry. A percentage of the
  // rail's height does not work: the ticks are a centred, padded stack, so
  // the nth tick is not n/total of the way down the container and the card
  // pointed at the wrong message.
  const [cardTop, setCardTop] = useState(0)
  const cardRef = useRef<HTMLDivElement>(null)
  const [current, setCurrent] = useState(0)

  const messages = (currentConversation?.messages ?? []) as Msg[]
  const conversationId = currentConversation?.id ?? null

  // Kept in a ref so the observer below can read the current messages
  // without being torn down and rebuilt on every render.
  const messagesRef = useRef<Msg[]>(messages)
  messagesRef.current = messages

  // Rebuild whenever the thread's DOM changes. A single read after mount
  // was not enough: the effect can run before the thread has rendered, and
  // with the conversation already in state nothing else would change to
  // trigger a second look, so the rail stayed empty for the whole visit.
  useEffect(() => {
    const canvas = document.querySelector('.chat-canvas')
    if (!canvas) return
    let frame = 0
    const build = () => {
      frame = 0
      const nodes = messageNodes()
      const real = messagesRef.current.filter(m => !m.pseudo)
      // Trust the data for role and text only when it describes the same
      // number of things the DOM is showing; otherwise fall back to what
      // is on screen, which is always right about what can be scrolled to.
      const aligned = real.length === nodes.length
      const next = nodes.map((n, i) => aligned
        ? { role: (real[i].role === 'user' ? 'user' : 'assistant') as Tick['role'], text: (real[i].content ?? '').trim() }
        : { role: null, text: (n.textContent ?? '').trim() })
      // Replace only on a real change, since this runs on every mutation
      // the thread makes, streaming included.
      setTicks(prev => (prev.length === next.length
        && prev.every((t, i) => t.role === next[i].role && t.text === next[i].text)) ? prev : next)
    }
    const schedule = () => { if (!frame) frame = requestAnimationFrame(build) }
    const mo = new MutationObserver(schedule)
    mo.observe(canvas, { childList: true, subtree: true, characterData: true })
    schedule()
    return () => {
      mo.disconnect()
      if (frame) cancelAnimationFrame(frame)
    }
  }, [conversationId])

  const show = useCallback((i: number, el: HTMLElement) => {
    const wrap = selfRef.current
    if (!wrap) return
    const t = el.getBoundingClientRect()
    setHover(i)
    setCardTop(t.top - wrap.getBoundingClientRect().top + t.height / 2)
  }, [])

  const jump = useCallback((i: number) => {
    messageNodes()[i]?.scrollIntoView({ behavior: 'smooth', block: 'start' })
  }, [])

  // The rail belongs against the thread's left edge, and the thread starts
  // wherever the conversations rail currently ends: it is resizable and
  // collapsible, so the offset is measured rather than assumed.
  const selfRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const canvas = document.querySelector('.chat-canvas')
    if (!canvas) return
    const place = () => {
      const host = scrollHost()
      const el = selfRef.current
      if (!host || !el) return
      const left = host.getBoundingClientRect().left - canvas.getBoundingClientRect().left
      el.style.setProperty('--scrub-left', `${Math.max(0, Math.round(left))}px`)
    }
    place()
    const ro = new ResizeObserver(place)
    ro.observe(canvas)
    const host = scrollHost()
    if (host) ro.observe(host)
    window.addEventListener('resize', place)
    return () => { ro.disconnect(); window.removeEventListener('resize', place) }
  }, [ticks.length])

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

  installDebug(ticks.length, MIN_MESSAGES)

  // Nudge the card back inside once its real height is known, so a tick at
  // the very top or bottom does not put half the preview off the thread.
  useLayoutEffect(() => {
    const wrap = selfRef.current
    const card = cardRef.current
    if (hover == null || !wrap || !card) return
    const half = card.offsetHeight / 2
    const lo = half + 8
    const hi = Math.max(lo, wrap.clientHeight - half - 8)
    setCardTop(t => Math.min(Math.max(t, lo), hi))
  }, [hover])

  // Below a handful of messages the rail is clutter: scrolling already
  // finds everything.
  if (ticks.length < MIN_MESSAGES) return null

  const longest = Math.max(...ticks.map(t => t.text.length), 1)
  const preview = hover != null ? ticks[hover] : null

  return (
    <div className="chat-scrubber" ref={selfRef} aria-label="Conversation timeline">
      {preview && (
        <div
          className="chat-scrubber-card"
          ref={cardRef}
          style={{ top: `${cardTop}px` }}
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
            style={{ width: `${MIN_TICK + Math.round((MAX_TICK - MIN_TICK) * Math.sqrt(t.text.length / longest))}px` }}
            title={`Message ${i + 1} of ${ticks.length}`}
            onMouseEnter={e => show(i, e.currentTarget)}
            onMouseLeave={() => setHover(h => (h === i ? null : h))}
            onFocus={e => show(i, e.currentTarget)}
            onBlur={() => setHover(null)}
            onClick={() => jump(i)}
          />
        ))}
      </div>
    </div>
  )
}
