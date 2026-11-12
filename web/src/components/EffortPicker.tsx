import { useEffect, useState } from 'react'
import { EFFORTS, getEffort, setEffort } from '../labelScope'

/**
 * How hard the model reasons, beside the model picker in the composer.
 * Default sends nothing, so the model (and Settings' per-model template
 * options) decides; the others go to the model as its reasoning effort.
 * A native select keeps it keyboard and screen reader friendly and reads
 * as plain text like the model name beside it.
 */
export function EffortPicker({ conversationId }: { conversationId: string | null }) {
  const [value, setValue] = useState(() => getEffort(conversationId))
  useEffect(() => { setValue(getEffort(conversationId)) }, [conversationId])
  useEffect(() => {
    const on = () => setValue(getEffort(conversationId))
    window.addEventListener('ade:chat-effort', on)
    return () => window.removeEventListener('ade:chat-effort', on)
  }, [conversationId])
  const current = EFFORTS.find(e => e.value === value) ?? EFFORTS[0]
  return (
    <label className="composer-effort" title={`Reasoning effort: ${current.hint}`} data-testid="composer-effort">
      <span className="composer-effort-word">Effort</span>
      <select
        aria-label="Reasoning effort"
        value={value}
        onChange={e => { setValue(e.target.value); setEffort(e.target.value, conversationId) }}
      >
        {EFFORTS.map(e => <option key={e.value} value={e.value}>{e.label}</option>)}
      </select>
    </label>
  )
}
