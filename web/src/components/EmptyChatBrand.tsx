import type { ReactNode } from 'react'

/** The Studio's own mark (web/icons/icon.svg), shown where a deployment has
 *  not uploaded an icon of its own. */
export const DEFAULT_MARK = '/favicon.svg'

/** The icon above the empty chat's greeting: the deployment's icon for the
 *  theme on screen (the dark one falls back to the light one), else the
 *  Studio's own mark. The same pick the sidebar makes for an uploaded icon. */
export function emptyChatIcon(cfg: { iconUrl: string | null; iconUrlDark: string | null }, dark: boolean): string {
  return (dark ? (cfg.iconUrlDark ?? cfg.iconUrl) : cfg.iconUrl) ?? DEFAULT_MARK
}

/**
 * Wraps the chat package's empty state. The icon rides as a pseudo-element
 * on the package's greeting heading (.empty-with-brand in styles.css), so it
 * sits directly above "what's on your mind" wherever the package centers it.
 */
export function EmptyChatBrand({ cfg, dark, children }: {
  cfg: { iconUrl: string | null; iconUrlDark: string | null }
  dark: boolean
  children: ReactNode
}) {
  const icon = emptyChatIcon(cfg, dark)
  return (
    <div
      className={`empty-with-brand${icon === DEFAULT_MARK ? ' default-mark' : ''}`}
      style={{ ['--empty-brand-icon' as string]: `url("${icon}")` }}
    >
      {children}
    </div>
  )
}
