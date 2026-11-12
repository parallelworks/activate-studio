import { useEffect } from 'react'
import { buildOpenHash } from '../nav'

/**
 * The @[path] tokens in sent messages read as links and open their file.
 * The chat package draws a user's message as plain text, and React owns
 * those text nodes, so nothing here changes the DOM: the tokens are styled
 * with the CSS Custom Highlight API (::highlight(studio-mention) in
 * styles.css), and a click on one is found from the caret position under
 * the pointer. A browser without highlights shows the tokens as plain text
 * and clicks still open them.
 */
const TOKEN = /@\[([^\]\n]{1,500})\]/g
const LIB = /^([a-z0-9][a-z0-9_-]{0,39}):(.+)$/

export function parseMention(body: string, libraryIds: string[]): { path: string; lib?: string } {
  const m = LIB.exec(body.trim())
  if (m && libraryIds.includes(m[1])) return { path: m[2], lib: m[1] }
  return { path: body.trim() }
}

function bubbleTexts(root: HTMLElement): Text[] {
  const out: Text[] = []
  for (const b of root.querySelectorAll('.chat-bubble-user')) {
    const w = document.createTreeWalker(b, NodeFilter.SHOW_TEXT)
    for (let n = w.nextNode(); n; n = w.nextNode()) if ((n as Text).data.includes('@[')) out.push(n as Text)
  }
  return out
}

function tokenAt(node: Text, offset: number): string | null {
  TOKEN.lastIndex = 0
  for (let m = TOKEN.exec(node.data); m; m = TOKEN.exec(node.data)) {
    if (offset >= m.index && offset <= m.index + m[0].length) return m[1]
  }
  return null
}

function caretAt(x: number, y: number): { node: Node; offset: number } | null {
  const d = document as Document & {
    caretPositionFromPoint?: (x: number, y: number) => { offsetNode: Node; offset: number } | null
    caretRangeFromPoint?: (x: number, y: number) => Range | null
  }
  const p = d.caretPositionFromPoint?.(x, y)
  if (p) return { node: p.offsetNode, offset: p.offset }
  const r = d.caretRangeFromPoint?.(x, y)
  return r ? { node: r.startContainer, offset: r.startOffset } : null
}

export function MentionLinks({ canvas, libraryIds }: { canvas: React.RefObject<HTMLDivElement | null>; libraryIds: string[] }) {
  useEffect(() => {
    const root = canvas.current
    if (!root) return
    const css = (globalThis as { CSS?: { highlights?: Map<string, unknown> } }).CSS
    const HighlightCtor = (globalThis as { Highlight?: new (...r: Range[]) => unknown }).Highlight
    let queued = 0
    const paint = () => {
      queued = 0
      if (!css?.highlights || !HighlightCtor) return
      const ranges: Range[] = []
      for (const t of bubbleTexts(root)) {
        TOKEN.lastIndex = 0
        for (let m = TOKEN.exec(t.data); m; m = TOKEN.exec(t.data)) {
          const r = document.createRange()
          r.setStart(t, m.index)
          r.setEnd(t, m.index + m[0].length)
          ranges.push(r)
        }
      }
      css.highlights.set('studio-mention', new HighlightCtor(...ranges))
    }
    const schedule = () => { if (!queued) queued = requestAnimationFrame(paint) }
    schedule()
    const obs = new MutationObserver(schedule)
    obs.observe(root, { childList: true, subtree: true, characterData: true })
    const onClick = (e: MouseEvent) => {
      const bubble = (e.target as Element | null)?.closest?.('.chat-bubble-user')
      if (!bubble || window.getSelection()?.toString()) return
      const at = caretAt(e.clientX, e.clientY)
      if (!at || at.node.nodeType !== Node.TEXT_NODE || !bubble.contains(at.node)) return
      const body = tokenAt(at.node as Text, at.offset)
      if (!body) return
      const { path, lib } = parseMention(body, libraryIds)
      e.preventDefault()
      location.hash = buildOpenHash({ kind: 'file', target: path, ...(lib ? { lib } : {}) })
    }
    root.addEventListener('click', onClick)
    return () => {
      obs.disconnect()
      if (queued) cancelAnimationFrame(queued)
      root.removeEventListener('click', onClick)
      css?.highlights?.delete('studio-mention')
    }
  }, [canvas, libraryIds])
  return null
}
