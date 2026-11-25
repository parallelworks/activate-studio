import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react'
import { createPortal } from 'react-dom'

/**
 * The whole name of a row whose name is cut off: in the Library's tree, the
 * folder menu of Add a library, the two sides of Transfers and the @ file
 * palette. It shows soon after the pointer rests on the row, and at once
 * when the keyboard reaches it; a name that fits has none.
 *
 * One tip serves every list, so a row costs two attributes: nameTip() on the
 * row, and data-tip-text on the element that holds the name. It is here
 * rather than the UI package's Tooltip because the package's opens for a
 * pointer or a focus on the text itself, and two of these lists move a
 * highlight while the focus stays in a text field.
 */
export const nameTip = (name: string, detail = '') => ({ 'data-tip-name': name, 'data-tip-detail': detail || undefined })

/** How long the pointer rests on a row first; once one has shown, the next
 *  row's shows at once. */
const REST = 200
const WARM = 400

/** The nearest element, up to the row, that cuts what it holds. */
function clipOf(text: HTMLElement, row: HTMLElement): HTMLElement {
  for (let el: HTMLElement | null = text; el && row.contains(el); el = el.parentElement) {
    const o = getComputedStyle(el).overflowX
    if (o && o !== 'visible') return el
  }
  return text
}

/** The name's own width, cut or not, and where it would end. */
export function textBox(text: Element): { width: number; right: number } | null {
  const range = document.createRange()
  range.selectNodeContents(text)
  return typeof range.getBoundingClientRect === 'function' ? range.getBoundingClientRect() : null
}

/** Whether the row's name is cut off, or ends under the button a hovered or
 *  selected row shows over its end. */
export function nameCut(row: HTMLElement): boolean {
  const text = row.matches('[data-tip-text]') ? row : row.querySelector<HTMLElement>('[data-tip-text]')
  if (!text) return false
  const clip = clipOf(text, row)
  if (clip.scrollWidth > clip.clientWidth) return true
  const name = textBox(text)
  if (!name || !name.width) return false
  // Part of a pixel over is cut too, which the whole numbers above miss.
  if (name.right > clip.getBoundingClientRect().right + 0.5) return true
  const over = row.querySelector<HTMLElement>('[data-tip-cover]')
  return !!over && row.matches(':hover, .selected') && getComputedStyle(over).position === 'absolute'
    && name.right > over.getBoundingClientRect().left
}

/** Whether an event in a row is about its name: its own buttons and boxes
 *  have their own labels. A focused box is the row's when it is how the
 *  keyboard reaches it. */
function aboutName(target: Element, row: HTMLElement, focus: boolean): boolean {
  const ctl = target.closest('button, a, input, [role="checkbox"]')
  if (!ctl || !row.contains(ctl) || ctl === row) return true
  if (ctl.matches('[data-tip-text]') || ctl.querySelector('[data-tip-text]')) return true
  return focus && ctl.matches('[role="checkbox"], input')
}

let show: ((row: HTMLElement) => void) | null = null
let hide: (() => void) | null = null
/** For a list whose highlight moves while the focus is in a text field. */
export const showNameTip = (row: HTMLElement | null) => { if (row) show?.(row) }
export const hideNameTip = () => hide?.()

/** Mounted once, beside the app. */
export function NameTips() {
  const id = useId()
  const [tip, setTip] = useState<{ row: HTMLElement; name: string; detail: string } | null>(null)
  const box = useRef<HTMLDivElement>(null)
  const [at, setAt] = useState<{ left: number; top: number } | null>(null)

  useEffect(() => {
    let timer = 0
    let frame = 0
    let shown: HTMLElement | null = null
    /** The row the pointer is on, and one it pressed, which stays quiet until the pointer leaves it. */
    let over: HTMLElement | null = null
    let pressed: HTMLElement | null = null
    let closed = -Infinity
    /** Reached by a key, whose row a list may still be scrolling into view. */
    let byKey = false
    /** Whether the last input was a key. Not :focus-visible, which the Linux
     *  window (WebKitGTK) leaves off when a key moves a focus a click made. */
    let keys = false
    const cancel = () => { clearTimeout(timer); cancelAnimationFrame(frame); frame = 0 }
    const close = () => {
      cancel()
      if (!shown) return
      shown = null
      closed = performance.now()
      setTip(null)
    }
    const open = (row: HTMLElement, key = false) => {
      cancel()
      byKey = key
      if (!row.isConnected || !nameCut(row)) { close(); return }
      shown = row
      setTip({ row, name: row.dataset.tipName ?? '', detail: row.dataset.tipDetail ?? '' })
    }
    // After the frame, once the list has moved its highlight or its focus.
    const openSoon = (row: () => HTMLElement | null) => {
      cancel()
      frame = requestAnimationFrame(() => { frame = 0; const r = row(); if (r) open(r, true); else close() })
    }
    const rowOf = (t: EventTarget | null) => (t instanceof Element ? t.closest<HTMLElement>('[data-tip-name]') : null)

    const onOver = (e: PointerEvent) => {
      if (e.pointerType === 'touch') return
      const row = rowOf(e.target)
      const named = row && aboutName(e.target as Element, row, false) ? row : null
      if (row !== pressed) pressed = null
      if (named === over) return
      over = named
      if (!named || named === pressed) { close(); return }
      if (named === shown) return
      const warm = shown !== null || performance.now() - closed < WARM
      close()
      if (warm) open(named)
      else timer = window.setTimeout(() => open(named), REST)
    }
    const onOut = (e: PointerEvent) => { if (!e.relatedTarget) { over = null; close() } }
    const onDown = (e: Event) => { keys = false; pressed = rowOf(e.target); close() }
    const onFocus = (e: FocusEvent) => {
      const target = e.target as Element
      const row = rowOf(target)
      if (!row || !aboutName(target, row, true) || !keys) return
      openSoon(() => (row.contains(document.activeElement) ? row : null))
    }
    const onBlur = (e: FocusEvent) => { const row = rowOf(e.target); if (row && row === shown && !row.contains(e.relatedTarget as Node | null)) close() }
    const onKey = (e: KeyboardEvent) => {
      if (['Shift', 'Control', 'Alt', 'Meta'].includes(e.key)) return
      keys = true
      const was = shown
      close()
      // A list that marks its current option for a text field: the option.
      const field = e.target instanceof Element ? e.target : null
      const moves = /^(Arrow(Up|Down)|Home|End)$/.test(e.key) || (e.ctrlKey && /^[np]$/.test(e.key))
      if (moves && (field?.hasAttribute('aria-activedescendant') || field?.getAttribute('role') === 'combobox')) {
        openSoon(() => {
          const option = field.getAttribute('aria-activedescendant')
          return option ? rowOf(document.getElementById(option)) : null
        })
      }
      // Closed with Escape, it stays closed until the pointer leaves the row.
      if (e.key === 'Escape' && was && was === over) pressed = was
    }
    // A scroll takes the row from under the pointer; one a key made brings its
    // row into view, and the tip follows it.
    const onScroll = () => {
      if (frame) return
      if (!byKey || !shown) { close(); return }
      setTip(t => (t ? { ...t } : t))
    }
    const look = window.setInterval(() => {
      if (shown && (!shown.isConnected || shown.checkVisibility?.() === false)) close()
    }, 300)

    show = row => openSoon(() => row)
    hide = close
    document.addEventListener('pointerover', onOver)
    document.addEventListener('pointerout', onOut)
    document.addEventListener('pointerdown', onDown, true)
    document.addEventListener('contextmenu', onDown, true)
    document.addEventListener('dragstart', onDown, true)
    document.addEventListener('focusin', onFocus)
    document.addEventListener('focusout', onBlur)
    document.addEventListener('keydown', onKey, true)
    window.addEventListener('scroll', onScroll, true)
    window.addEventListener('wheel', close, { capture: true, passive: true })
    window.addEventListener('resize', close)
    window.addEventListener('blur', close)
    return () => {
      close()
      clearInterval(look)
      show = null
      hide = null
      document.removeEventListener('pointerover', onOver)
      document.removeEventListener('pointerout', onOut)
      document.removeEventListener('pointerdown', onDown, true)
      document.removeEventListener('contextmenu', onDown, true)
      document.removeEventListener('dragstart', onDown, true)
      document.removeEventListener('focusin', onFocus)
      document.removeEventListener('focusout', onBlur)
      document.removeEventListener('keydown', onKey, true)
      window.removeEventListener('scroll', onScroll, true)
      window.removeEventListener('wheel', close, true)
      window.removeEventListener('resize', close)
      window.removeEventListener('blur', close)
    }
  }, [])

  // Under the row from where its name starts, or over it where there is no
  // room below, and within the window.
  useLayoutEffect(() => {
    if (!tip || !box.current) { setAt(null); return }
    const row = tip.row.getBoundingClientRect()
    const text = (tip.row.querySelector('[data-tip-text]') ?? tip.row).getBoundingClientRect()
    const { offsetWidth: w, offsetHeight: h } = box.current
    const below = row.bottom + 4
    setAt({
      left: Math.max(8, Math.min(text.left - 10, window.innerWidth - w - 8)),
      top: below + h + 8 <= window.innerHeight ? below : Math.max(8, row.top - h - 4),
    })
  }, [tip])

  if (!tip) return null
  return createPortal(
    <div ref={box} id={id} role="tooltip" className="name-tip" style={at ?? { left: 0, top: 0, visibility: 'hidden' }}>
      <span className="name-tip-name">{tip.name}</span>
      {tip.detail && <span className="name-tip-detail">{tip.detail}</span>}
    </div>,
    document.body,
  )
}
