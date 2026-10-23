import type { ReactNode } from 'react'
import { DEFAULT_MARK, studioMark } from '../studioMark'

/** The Studio's own mark, shown where a deployment has not uploaded an icon
 *  of its own (studioMark.ts: in the accent, the app icon for the default). */
export { DEFAULT_MARK }

/** The icon above the empty chat's greeting: the deployment's icon for the
 *  theme on screen (the dark one falls back to the light one), else the
 *  Studio's own mark in the accent. The same pick the sidebar makes. */
export function emptyChatIcon(cfg: { iconUrl: string | null; iconUrlDark: string | null; accent?: string }, dark: boolean): string {
  return (dark ? (cfg.iconUrlDark ?? cfg.iconUrl) : cfg.iconUrl) ?? studioMark(cfg.accent)
}

/**
 * Wraps the chat package's empty state. The icon rides as a pseudo-element
 * on the package's greeting heading (.empty-with-brand in styles.css), so it
 * sits directly above "what's on your mind" wherever the package centers it.
 */
export function EmptyChatBrand({ cfg, dark, children }: {
  cfg: { iconUrl: string | null; iconUrlDark: string | null; accent?: string }
  dark: boolean
  children: ReactNode
}) {
  const icon = emptyChatIcon(cfg, dark)
  const uploaded = !!(dark ? (cfg.iconUrlDark ?? cfg.iconUrl) : cfg.iconUrl)
  return (
    <div
      className={`empty-with-brand${uploaded ? '' : ' default-mark'}`}
      style={{ ['--empty-brand-icon' as string]: `url("${icon}")` }}
    >
      {children}
    </div>
  )
}
