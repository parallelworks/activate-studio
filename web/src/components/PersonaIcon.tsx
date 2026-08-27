/**
 * A persona's mark. The icon field holds either an emoji or a
 * corpus-relative path to an image, and an image is served through the
 * ordinary corpus route because that is what it is. With neither, the
 * name's initials stand in, so every persona has a mark whether or not
 * anyone chose one and a library reads as a set rather than a list.
 */
export function PersonaIcon({ icon, name, size = 22 }: { icon?: string; name: string; size?: number }) {
  const style = { width: size, height: size, fontSize: Math.round(size * 0.52) }
  if (icon && /\.(png|jpe?g|webp|svg|gif)$/i.test(icon)) {
    return <img className="persona-icon" style={style} src={`/api/kb/raw?path=${encodeURIComponent(icon)}`} alt="" />
  }
  if (icon) return <span className="persona-icon glyph" style={style}>{icon}</span>
  const initials = name.replace(/[^a-z0-9]+/gi, ' ').trim().split(/\s+/).slice(0, 2).map(w => w[0] ?? '').join('').toUpperCase()
  return <span className="persona-icon initials" style={style}>{initials || '?'}</span>
}
