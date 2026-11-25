import { useState } from 'react'
import { Button } from '@parallelworks/ui'
import { removeTools } from './studioTools'

/** What Add allows in pw code on a machine, said where the person chooses it. */
export function AllowsNote({ allows, lead = 'It' }: { allows?: string[]; lead?: string }) {
  if (!allows?.length) return null
  return (
    <p className="muted" data-testid="studio-tools-allows">
      {lead} also sets pw code there to run these tools without asking, as they only read: {allows.join(', ')}.
      The rest still ask, and all of them still ask in a turn sent from a chat here or from the web. Remove takes this back.
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
      onRemoved(r.removed || r.rules
        ? `This Studio's tools are out of pw code on ${where}, and so is what it allowed there. A session already open keeps the tools it started with.`
        : `pw code on ${where} had none of this Studio's tools.`)
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
