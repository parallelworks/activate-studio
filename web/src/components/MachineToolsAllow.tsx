import { useState } from 'react'
import { Button } from '@parallelworks/ui'
import { removedSaid, removeTools } from './studioTools'

/** What adding them allows, in a sentence. */
export const allowsSaid = (allows: string[], lead = 'It') =>
  `${lead} also sets pw code there to run these tools without asking, as they only read: ${allows.join(', ')}. `
  + 'The rest still ask, and all of them still ask in a turn sent from a chat here or from the web. Remove takes this back.'

/** What Add allows in pw code on a machine, said where the person chooses it. */
export function AllowsNote({ allows, lead = 'It' }: { allows?: string[]; lead?: string }) {
  if (!allows?.length) return null
  return (
    <p className="muted" data-testid="studio-tools-allows">
      {allowsSaid(allows, lead)}
    </p>
  )
}

/** Remove: takes the Studio's entry, and what it allows, back out of pw code
 *  on the machine, once the person confirms. */
export function RemoveTools({ where, body, onRemoved }: {
  where: string
  body: { conversationId?: string; machineId?: string }
  /** With what was done, in a sentence. */
  onRemoved: (said: string) => void
}) {
  const [asking, setAsking] = useState(false)
  const [busy, setBusy] = useState(false)
  const [failed, setFailed] = useState('')
  if (!asking) return <button type="button" className="code-approval-more" onClick={() => setAsking(true)}>Remove</button>
  const remove = async () => {
    setBusy(true)
    setFailed('')
    try {
      const r = await removeTools(body)
      onRemoved(removedSaid(r, where))
    } catch (e) {
      setFailed((e as Error).message)
    } finally {
      setBusy(false)
    }
  }
  return (
    <span data-testid="studio-tools-remove">
      Remove this Studio's tools from pw code on {where}, and its rules that allow them?{' '}
      <Button variant="secondary" size="sm" loading={busy} disabled={busy} onClick={() => void remove()}>Remove</Button>{' '}
      <Button variant="ghost" size="sm" disabled={busy} onClick={() => setAsking(false)}>Keep</Button>
      {failed && <> {failed}</>}
    </span>
  )
}
