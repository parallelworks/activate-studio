import { useEffect, useState } from 'react'
import { Field, Input, Select } from '@parallelworks/ui'
import { api, type PwSystems } from '../api'

/** The list's choice for a system typed by name. */
const ANOTHER = '\u0000another'

/**
 * One of the person's systems on the platform, reached over pw ssh, in
 * setup: picked from their systems, running ones first, or typed by name
 * when the list cannot be had or the system is not in it. The first running
 * one is chosen to begin with.
 */
export function PwSystemField({ value, onChange, help, onSystems }: {
  value: string
  onChange: (cluster: string) => void
  help?: (text: string) => string | undefined
  onSystems?: (s: PwSystems) => void
}) {
  const [systems, setSystems] = useState<PwSystems | null>(null)
  const [typing, setTyping] = useState(false)
  useEffect(() => {
    let live = true
    api.pwSystems().then(r => {
      if (!live) return
      setSystems(r); onSystems?.(r)
      const pick = r.systems.find(x => x.running)
      if (!value && pick) onChange(pick.name)
    }).catch(e => {
      if (!live) return
      const r = { usable: true, why: null, systems: [], error: String((e as Error).message ?? e) }
      setSystems(r); onSystems?.(r)
    })
    return () => { live = false }
    // Read once; the choice is the caller's from then on.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])
  const say = help ?? ((t: string) => t)
  const listed = !!systems?.systems.length
  const typed = systems !== null && (typing || !listed)
  return (
    <>
      {(systems === null || listed) && (
        <Field label="Remote computer" description={say('Your clusters on the platform, running ones first. One that is off has to be started on ACTIVATE before it can be reached.')}>
          <Select value={typing ? ANOTHER : value || null} placeholder={systems === null ? 'Loading…' : 'Choose a remote computer'} disabled={systems === null}
            items={[
              ...(systems?.systems ?? []).map(x => ({ value: x.name, label: x.label, description: x.label !== x.name ? x.name : undefined, hint: x.running ? 'running' : x.status })),
              { value: ANOTHER, label: 'Another remote computer…', description: 'Type its name' },
            ]}
            onValueChange={v => {
              if (!v) return
              setTyping(v === ANOTHER)
              onChange(v === ANOTHER ? '' : v)
            }} />
        </Field>
      )}
      {typed && (
        <Input label={listed ? 'Its name' : 'Remote computer'} placeholder="my-cluster" mono autoComplete="off" spellCheck={false} value={value}
          description={say('The name of a running cluster, as pw ssh takes it.')} onChange={e => onChange(e.target.value.trim())} />
      )}
    </>
  )
}
