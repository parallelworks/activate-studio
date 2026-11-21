import { useEffect, useState } from 'react'
import { Field, Input, Select } from '@parallelworks/ui'
import { api, type PwSystems } from '../api'

/** The list's choice for a system typed by name. */
const ANOTHER = '\u0000another'

export interface SystemExtra { value: string; label: string; description?: string }

/**
 * One of the person's systems on the platform, reached over pw ssh: picked
 * from their systems, running ones first, or typed by name when the list
 * cannot be had or the system is not in it. The system named `preferred`
 * (the Studio's own cluster, for My library) is chosen first where it is
 * listed, otherwise the first running one. `extra` adds choices of the
 * caller's own after the systems, such as a host over plain ssh; choosing
 * one hands its value to `onExtra`.
 */
export function PwSystemField({ value, onChange, preferred, help, extra = [], extraValue, onExtra, onSystems }: {
  value: string
  onChange: (cluster: string) => void
  preferred?: string
  help?: (text: string) => string | undefined
  extra?: SystemExtra[]
  /** The extra choice in use, if one is. */
  extraValue?: string | null
  onExtra?: (value: string) => void
  onSystems?: (s: PwSystems) => void
}) {
  const [systems, setSystems] = useState<PwSystems | null>(null)
  const [typing, setTyping] = useState(false)
  useEffect(() => {
    let live = true
    api.pwSystems().then(r => {
      if (!live) return
      setSystems(r); onSystems?.(r)
      const pick = r.systems.find(x => preferred && x.name === preferred) ?? r.systems.find(x => x.running)
      if (!value && pick) onChange(pick.name)
    }).catch(e => {
      if (!live) return
      const r = { usable: true, why: null, systems: [], error: String((e as Error).message ?? e) }
      setSystems(r); onSystems?.(r)
      if (!value && preferred) onChange(preferred)
    })
    return () => { live = false }
    // Read once; the choice is the caller's from then on.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const say = help ?? ((t: string) => t)
  const listed = !!systems?.systems.length
  const typed = systems !== null && !extraValue && (typing || (!listed && !extra.length) || (!listed && value !== ''))
  const chosen = extraValue ?? (typing ? ANOTHER : value || null)
  return (
    <>
      {(systems === null || listed || extra.length > 0) && (
        <Field label="System" description={say('Your systems on the platform, running ones first. One that is off has to be started on ACTIVATE before it can be reached.')}>
          <Select value={chosen} placeholder={systems === null ? 'Loading…' : 'Choose a system'} disabled={systems === null}
            items={[
              ...(systems?.systems ?? []).map(x => ({ value: x.name, label: x.label, description: x.label !== x.name ? x.name : undefined, hint: x.name === preferred ? "the Studio's cluster" : x.running ? 'running' : x.status })),
              ...extra,
              { value: ANOTHER, label: 'Another system…', description: 'Type its name' },
            ]}
            onValueChange={v => {
              if (!v) return
              if (extra.some(x => x.value === v)) { setTyping(false); onExtra?.(v); return }
              setTyping(v === ANOTHER)
              onChange(v === ANOTHER ? '' : v)
            }} />
        </Field>
      )}
      {typed && (
        <Input label={listed ? 'System name' : 'System'} placeholder="my-cluster" mono autoComplete="off" spellCheck={false} value={value}
          description={say('The name of a running system, as pw ssh takes it.')} onChange={e => onChange(e.target.value.trim())} />
      )}
    </>
  )
}
