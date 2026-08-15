import { ReactElement } from 'react'
import { useAppConfig } from '../config'

/**
 * Classification banner across the top of the window, in the style the
 * platform draws around an embedded session.
 *
 * Inside the platform frame the banner is already on screen above this app,
 * so drawing another one repeats it; the app draws its own only when it is
 * open on its own, which is the case the platform's banner does not cover.
 * A deployment that wants it in both places sets bannerWhenEmbedded.
 */

/** True when this document is not the top-level one, i.e. the app is running
 *  inside the platform's session frame. Comparing the window references is
 *  allowed across origins; reading through them is not. */
export function isEmbedded(): boolean {
  try { return window.self !== window.top } catch { return true }
}

/** Black or white text, whichever the background can carry. Deployments pick
 *  their own color, and a light one with white text is unreadable. */
export function bannerInk(hex: string): string {
  const m = /^#([0-9a-f]{6})$/i.exec((hex ?? '').trim())
  if (!m) return '#ffffff'
  const n = parseInt(m[1], 16)
  const [r, g, b] = [(n >> 16) & 255, (n >> 8) & 255, n & 255].map(v => {
    const c = v / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  const l = 0.2126 * r + 0.7152 * g + 0.0722 * b
  // Whichever ink contrasts more, by the WCAG ratio. A fixed lightness
  // threshold puts white on orange, which is the marking that needs black.
  return (l + 0.05) / 0.05 > 1.05 / (l + 0.05) ? '#101010' : '#ffffff'
}

export function ClassificationBanner(): ReactElement | null {
  const cfg = useAppConfig()
  const text = cfg.bannerText?.trim()
  if (!text) return null
  if (isEmbedded() && !cfg.bannerWhenEmbedded) return null
  const bg = cfg.bannerColor || '#24612e'
  return (
    <div className="cls-banner" style={{ background: bg, color: bannerInk(bg) }} role="note">
      {text}
    </div>
  )
}
