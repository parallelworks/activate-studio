import { useEffect, useState } from 'react'
import { StatusDot } from '@parallelworks/ui'
import {
  placesReady, placesText, readyParts, transferApi, type PlacesReady, type TransferLocation, type TransferSettings,
} from '../transferApi'

/** The places transfers can use now, read once: the locations and the
 *  settings' detected facts. Null until both have answered, or on an error. */
export function useTransfersReady(enabled = true): PlacesReady | null {
  const [ready, setReady] = useState<PlacesReady | null>(null)
  useEffect(() => {
    if (!enabled) return
    let live = true
    Promise.all([
      transferApi.locations().catch(() => [] as TransferLocation[]),
      transferApi.settings().catch(() => null as TransferSettings | null),
    ]).then(([locs, st]) => { if (live) setReady(placesReady(locs, st)) })
    return () => { live = false }
  }, [enabled])
  return ready
}

/** "Ready to use": what was found, so a person sees transfers work before
 *  setting anything up. */
export function ReadySummary({ ready, compact = false }: { ready: PlacesReady; compact?: boolean }) {
  return (
    <div className={`xfer-ready${compact ? ' compact' : ''}`} role="status">
      <div className="xfer-ready-head">
        <StatusDot variant={ready.total ? 'success' : 'warning'} />
        <b>{ready.total ? 'Ready to use' : 'Nothing found yet'}</b>
        <span className="muted">{placesText(ready)}</span>
      </div>
      <p className="xfer-ready-parts muted">{readyParts(ready).join(' · ')}</p>
    </div>
  )
}
