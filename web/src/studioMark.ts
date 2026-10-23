import { markColors } from './accents'

/** The Studio's own mark (web/icons/icon.svg), as the app icon. */
export const DEFAULT_MARK = '/favicon.svg'

/**
 * The Studio's mark where no icon was uploaded, in the accent: the app icon
 * (web/icons/icon.svg) for the default accent, and the same drawing with its
 * tile and spark in the accent's colors for any other. A data URL, so it
 * works as an image source and in a CSS url(). The macOS app icon and the
 * browser tab's icon stay the app icon.
 */
const made = new Map<string, string>()
export function studioMark(accent: string | null | undefined): string {
  const c = markColors(accent)
  if (!c) return DEFAULT_MARK
  const key = `${c.top}${c.bottom}${c.sparkTop}${c.sparkBottom}`
  const hit = made.get(key)
  if (hit) return hit
  const svg = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512"><defs>`
    + `<linearGradient id="bg" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="${c.top}"/><stop offset="1" stop-color="${c.bottom}"/></linearGradient>`
    + `<radialGradient id="hl" cx="0.25" cy="0.12" r="0.9"><stop offset="0" stop-color="#fff" stop-opacity="0.18"/><stop offset="0.55" stop-color="#fff" stop-opacity="0"/></radialGradient>`
    + `<linearGradient id="sp" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="${c.sparkTop}"/><stop offset="1" stop-color="${c.sparkBottom}"/></linearGradient>`
    + `</defs><rect x="16" y="16" width="480" height="480" rx="106" fill="url(#bg)"/><rect x="16" y="16" width="480" height="480" rx="106" fill="url(#hl)"/>`
    + `<rect x="16.75" y="16.75" width="478.5" height="478.5" rx="105.25" fill="none" stroke="#fff" stroke-opacity="0.14" stroke-width="1.5"/>`
    + `<g fill="#ffffff"><rect x="140" y="164" width="150" height="40" rx="20"/><rect x="140" y="236" width="232" height="40" rx="20"/><rect x="140" y="308" width="190" height="40" rx="20" opacity="0.92"/></g>`
    + `<path d="M366 86 Q372 143 430 150 Q372 157 366 214 Q360 157 302 150 Q360 143 366 86 Z" fill="url(#sp)"/></svg>`
  const url = `data:image/svg+xml;charset=utf-8,${encodeURIComponent(svg)}`
  made.set(key, url)
  return url
}
