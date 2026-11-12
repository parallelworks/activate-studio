/**
 * Copy buttons work where the async clipboard is refused. Every copy
 * button (the package's useCopy, the chat's code blocks, the Studio's own)
 * calls navigator.clipboard.writeText, which a browser refuses in a frame
 * not granted clipboard-write (the Studio opened inside the ACTIVATE page
 * is one) and which does not exist on plain http. When it is refused, the
 * text is copied the older way, from a hidden textarea with
 * document.execCommand('copy'), which a click in a frame may still do.
 */
export function copyWithTextarea(text: string): boolean {
  const ta = document.createElement('textarea')
  ta.value = text
  ta.setAttribute('readonly', '')
  ta.style.cssText = 'position:fixed;top:0;left:0;width:1px;height:1px;opacity:0;pointer-events:none'
  const active = document.activeElement as HTMLElement | null
  document.body.appendChild(ta)
  ta.select()
  ta.setSelectionRange(0, text.length)
  let ok = false
  try { ok = document.execCommand('copy') } catch { ok = false }
  ta.remove()
  active?.focus?.({ preventScroll: true })
  return ok
}

export function installClipboardFallback(): void {
  const nav = navigator as Navigator & { clipboard?: Clipboard }
  const original = nav.clipboard?.writeText?.bind(nav.clipboard)
  const writeText = async (text: string): Promise<void> => {
    if (original) {
      try { await original(text); return } catch (e) {
        if (copyWithTextarea(text)) return
        throw e
      }
    }
    if (!copyWithTextarea(text)) throw new DOMException('Copy was refused', 'NotAllowedError')
  }
  try {
    if (nav.clipboard) Object.defineProperty(nav.clipboard, 'writeText', { value: writeText, configurable: true })
    else Object.defineProperty(nav, 'clipboard', { value: { writeText }, configurable: true })
  } catch { /* a locked-down navigator keeps its own behavior */ }
}
