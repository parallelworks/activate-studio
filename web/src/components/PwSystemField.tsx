import { useEffect, useState } from 'react'
import { Field, Input, Select } from '@parallelworks/ui'
import { api, type PwSystems } from '../api'

/** The list's choice for a system typed by name. */
const ANOTHER = '\u0000another'

export interface SystemExtra { value: string; label: string; description?: string }

/**
 * One of the person's systems on the platform, reached over pw ssh: picked
 * from their systems, running ones first, or typed by name when the list
 * cannot be had or the system is not in it. On a Studio a cluster hosts,
 * `home` names that cluster: it is listed first as the one this Studio runs
 * on, not as a remote computer, and nothing is chosen until the person
 * chooses, since there it is read as them over pw ssh where This computer
 * reads as the Studio's account. Elsewhere (the apps, where every cluster
 * is remote) the first running one is chosen to begin with. `extra` adds
 * choices of the caller's own after the systems, such as a host over plain
 * ssh; choosing one hands its value to `onExtra`.
 */
export function PwSystemField({ value, onChange, home, help, extra = [], extraValue, onExtra, onSystems }: {
  value: string
  onChange: (cluster: string) => void
  /** The cluster this Studio runs on, where a cluster hosts it. */
  home?: string
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
      const pick = home ? undefined : r.systems.find(x => x.running)
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
  const typed = systems !== null && !extraValue && (typing || (!listed && !extra.length) || (!listed && value !== ''))
  const chosen = extraValue ?? (typing ? ANOTHER : value || null)
  const listedSystems = [...(systems?.systems ?? [])].sort((a, b) => Number(b.name === home) - Number(a.name === home))
  const noun = home ? 'cluster' : 'remote computer'
  return (
    <>
      {(systems === null || listed || extra.length > 0) && (
        <Field label={home ? 'Cluster' : 'Remote computer'} description={say(home
          ? 'Your clusters on the platform, reached as you with your ACTIVATE sign-in: first the one this Studio runs on, then running ones. One that is off has to be started on ACTIVATE before it can be reached.'
          : 'Your clusters on the platform, running ones first. One that is off has to be started on ACTIVATE before it can be reached.')}>
          <Select value={chosen} placeholder={systems === null ? 'Loading…' : `Choose a ${noun}`} disabled={systems === null}
            items={[
              ...listedSystems.map(x => ({ value: x.name, label: x.name === home ? `${x.label}, where this Studio runs` : x.label, description: x.label !== x.name ? x.name : undefined, hint: x.running ? 'running' : x.status })),
              ...extra,
              { value: ANOTHER, label: `Another ${noun}…`, description: 'Type its name' },
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
        <Input label={listed ? 'Its name' : home ? 'Cluster' : 'Remote computer'} placeholder="my-cluster" mono autoComplete="off" spellCheck={false} value={value}
          description={say('The name of a running cluster, as pw ssh takes it.')} onChange={e => onChange(e.target.value.trim())} />
      )}
    </>
  )
}
