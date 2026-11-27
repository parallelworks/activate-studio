import { AttachmentImageViewer } from '@parallelworks/ui/ai'
import { useEffect, useState } from 'react'

/** Where the Studio keeps a chat's attachments in the main library. */
export const ATTACHMENT_DIR = 'uploads/chat/'

/**
 * The library path a Studio chat attachment's id names, or null for any
 * other id. The Studio's id is the base64url of the path under uploads/chat/.
 * A pw code session's image has its own id, the address the session serves
 * it at or a data: URL, which is no path: decoded as one it was garbage, and
 * the Library opened it as a file that is not there.
 */
export function attachmentPath(id: string): string | null {
  if (!/^[A-Za-z0-9_-]+$/.test(id)) return null
  let rel: string
  try {
    const bin = atob(id.replace(/-/g, '+').replace(/_/g, '/'))
    rel = new TextDecoder('utf-8', { fatal: true }).decode(Uint8Array.from(bin, c => c.charCodeAt(0)))
  } catch {
    return null
  }
  if (!rel.startsWith(ATTACHMENT_DIR) || rel.length === ATTACHMENT_DIR.length) return null
  if (rel.split('/').some(part => part === '..' || part === '.' || part === '')) return null
  return rel
}

/** A pw code session's image as the Studio serves it, on this origin. */
function sessionImage(id: string): boolean {
  return /^\/api\/code\/machines\/[^/?#]+\/sessions\/[^/?#]+\/images\/[^/?#]+$/.test(id)
}

/** The address an attachment downloads from: the Studio's own, or the
 *  session's image. Anything else has none, and is shown from its preview. */
export function attachmentDownloadUrl(id: string): string {
  if (attachmentPath(id)) return `/api/chat/attachments/${id}/download`
  if (sessionImage(id)) return id
  return ''
}

interface OpenedImage { name: string; src: string; fullUrl?: string; back: HTMLElement | null }
const VIEW_IMAGE = 'ade-view-chat-image'

/** Opens an attachment from a chat message: one the Studio keeps in the
 *  Library, any other picture in the image viewer, at full size where the
 *  session serves it. */
export function openAttachment(att: { id: string; filename?: string; contentType?: string; previewUrl?: string }): void {
  const rel = attachmentPath(att.id)
  if (rel) {
    location.hash = `#open=file:${encodeURIComponent(rel).replace(/%2F/gi, '/')}`
    return
  }
  const full = sessionImage(att.id) ? att.id : undefined
  const src = att.previewUrl || full || (att.id.startsWith('data:image/') ? att.id : '')
  if (!src) return
  const back = document.activeElement instanceof HTMLElement ? document.activeElement : null
  window.dispatchEvent(new CustomEvent<OpenedImage>(VIEW_IMAGE, { detail: { name: att.filename || 'Image', src, fullUrl: full, back } }))
}

/** The viewer for a chat's pictures that are not in the Library. */
export function ChatImageViewer() {
  const [image, setImage] = useState<OpenedImage | null>(null)
  useEffect(() => {
    const show = (e: Event) => setImage((e as CustomEvent<OpenedImage>).detail)
    window.addEventListener(VIEW_IMAGE, show)
    return () => window.removeEventListener(VIEW_IMAGE, show)
  }, [])
  return (
    <AttachmentImageViewer
      images={image ? [{ name: image.name, src: image.src, fullUrl: image.fullUrl, href: image.fullUrl }] : []}
      index={image ? 0 : null}
      onIndexChange={() => {}}
      onClose={() => setImage(null)}
      returnFocusTo={() => image?.back ?? undefined}
    />
  )
}
