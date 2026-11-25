import { useEffect, useState } from 'react'
import { Button, CopyButton, Input, Loader, Modal } from '@parallelworks/ui'
import { onLinkToShow, type Opened } from '../itemLinks'
import { embedded, signInHref } from '../signIn'

export type LinkState = { id: string; state: 'opening' } | ({ id: string } & Exclude<Opened, { state: 'opened' }>)

const TITLE: Record<LinkState['state'], string> = {
  opening: 'Opening the link…',
  none: 'This link does not open',
  deleted: 'This item was deleted or moved',
  unreachable: 'The item could not be reached',
  failed: 'The link could not be opened',
  'signed-out': 'Sign in to open this link',
}

/** What following a link to an item came to, where it did not open. */
export function LinkNotice({ link, onClose, onRetry }: { link: LinkState | null; onClose: () => void; onRetry: (id: string) => void }) {
  // A link that opens at once shows nothing on the way.
  const [slow, setSlow] = useState<string | null>(null)
  const opening = link?.state === 'opening' ? link.id : null
  useEffect(() => {
    if (!opening) return
    const t = setTimeout(() => setSlow(opening), 500)
    return () => { clearTimeout(t); setSlow(null) }
  }, [opening])
  if (!link || (link.state === 'opening' && slow !== link.id)) return null
  const again = link.state === 'unreachable' || link.state === 'failed'
  return (
    <Modal
      open
      onClose={onClose}
      size="sm"
      title={TITLE[link.state]}
      footer={
        <>
          <Button variant={again || link.state === 'signed-out' ? 'ghost' : 'primary'} size="sm" onClick={onClose}>{link.state === 'opening' ? 'Cancel' : 'Close'}</Button>
          {again && <Button variant="primary" size="sm" onClick={() => onRetry(link.id)}>Try again</Button>}
          {link.state === 'signed-out' && link.signIn && (
            <Button variant="primary" size="sm" href={signInHref(link.signIn)} reloadDocument {...(embedded() ? { target: '_blank' as const, rel: 'noopener' } : {})}>Sign in</Button>
          )}
        </>
      }
    >
      {link.state === 'opening' ? <Loader label="Looking for the item" /> : <p className="link-notice-text">{link.text}</p>}
    </Modal>
  )
}

/** A link the browser would not put on the clipboard, to copy by hand. */
export function LinkToCopy() {
  const [address, setAddress] = useState<string | null>(null)
  useEffect(() => onLinkToShow(setAddress), [])
  if (!address) return null
  return (
    <Modal open onClose={() => setAddress(null)} size="sm" title="Copy this link"
      description="The browser did not let the Studio copy it."
      footer={<Button variant="primary" size="sm" onClick={() => setAddress(null)}>Done</Button>}>
      <div className="link-to-copy">
        <Input aria-label="Link" readOnly value={address} onFocus={e => e.target.select()} />
        <CopyButton text={address} label="link" />
      </div>
    </Modal>
  )
}
