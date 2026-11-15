import { useEffect, useState } from 'react'
import { Button, Callout, Modal } from '@parallelworks/ui'
import {
  ACTION_TEXT, TransferError, bytesText, confirmText, countWords, destLine, sourceLine, transferApi,
  type Plan, type PlanRequest, type Transfer,
} from '../transferApi'

/** The first names of a list the plan cut at 50, and how many more there are. */
function Names({ names, count }: { names: string[]; count: number }) {
  const shown = names.slice(0, 8)
  const more = count - shown.length
  return (
    <ul className="xfer-names">
      {shown.map(n => <li key={n} title={n}>{n}</li>)}
      {more > 0 && <li className="muted">and {more.toLocaleString()} more</li>}
    </ul>
  )
}

function useNow(ms: number): number {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => { const t = setInterval(() => setNow(Date.now()), ms); return () => clearInterval(t) }, [ms])
  return now
}

/**
 * The plan, said plainly, before anything moves: what goes where, how many
 * files, which mover and why, what is replaced and deleted, and when the
 * plan stops being good. Confirming starts it; files that changed since the
 * plan (a 409) offer a new plan in place of a start.
 */
export function TransferPlanDialog({
  plan, request, onClose, onStarted, onReplanned,
}: {
  plan: Plan
  request: PlanRequest
  onClose: () => void
  onStarted: (t: Transfer) => void
  onReplanned: (p: Plan) => void
}) {
  const [busy, setBusy] = useState<'start' | 'plan' | null>(null)
  const [error, setError] = useState<{ text: string; stale: boolean } | null>(null)
  const now = useNow(5000)
  const expires = Date.parse(plan.expiresAt)
  const expired = Number.isFinite(expires) && expires <= now
  const danger = plan.action === 'move' || plan.deletesCount > 0
  const mover = plan.movers.find(m => m.name === plan.mover)
  const others = plan.movers.filter(m => m.name !== plan.mover)

  const start = async () => {
    setBusy('start'); setError(null)
    try {
      onStarted(await transferApi.start(plan.id))
    } catch (e) {
      const err = e as TransferError
      setError(err.status === 409
        ? { text: 'The files changed since this plan was made, or the plan expired. Nothing was started.', stale: true }
        : { text: err.message, stale: false })
    } finally { setBusy(null) }
  }
  const replan = async () => {
    setBusy('plan'); setError(null)
    try { onReplanned(await transferApi.plan(request)) } catch (e) { setError({ text: (e as Error).message, stale: false }) } finally { setBusy(null) }
  }

  const what = plan.files < 0 ? 'Counted when the transfer starts' : countWords(plan.files, plan.dirs) || 'Nothing'
  const free = plan.freeBytes
  const short = free !== null && plan.bytes > 0 && plan.bytes > free

  return (
    <Modal
      open
      onClose={onClose}
      alert
      size="lg"
      title={`${ACTION_TEXT[plan.action]}: confirm the plan`}
      description={plan.label || undefined}
      footer={
        <>
          <Button variant="secondary" onClick={onClose}>Cancel</Button>
          {error?.stale || expired
            ? <Button variant="primary" onClick={() => void replan()} loading={busy === 'plan'}>Plan again</Button>
            : <Button variant={danger ? 'danger' : 'primary'} onClick={() => void start()} loading={busy === 'start'}>{confirmText(plan)}</Button>}
        </>
      }
    >
      <div className="xfer-plan">
        <dl className="xfer-plan-facts">
          <dt>Action</dt>
          <dd>{plan.action === 'sync'
            ? `Sync: send new and changed files${request.options?.checksum ? ', compared by checksum' : ', compared by size and modification time'}`
            : plan.action === 'move' ? 'Move: copy, check, then remove the originals' : 'Copy'}</dd>
          <dt>From</dt>
          <dd className="xfer-mono">{plan.source.paths.length > 1
            ? <>{plan.source.label}<Names names={plan.source.paths} count={plan.source.paths.length} /></>
            : sourceLine(plan.source)}</dd>
          <dt>To</dt><dd className="xfer-mono">{destLine(plan.destination)}</dd>
          <dt>Files</dt><dd>{what}</dd>
          <dt>Size</dt><dd>{bytesText(plan.bytes)}</dd>
          <dt>Free space</dt><dd>{free === null ? 'Not reported by the destination' : bytesText(free)}</dd>
          <dt>Mover</dt>
          <dd>
            <b>{plan.mover}</b>{plan.moverWhy ? `: ${plan.moverWhy}` : ''}
            {mover && !mover.ok && mover.why ? ` (${mover.why})` : ''}
            {others.length > 0 && (
              <ul className="xfer-movers">
                {others.map(m => <li key={m.name}>{m.name}: {m.ok ? 'can also reach both ends' : (m.why ?? 'cannot reach both ends')}</li>)}
              </ul>
            )}
          </dd>
          {plan.compare && <><dt>Compared by</dt><dd>{plan.compare}</dd></>}
          <dt>Plan good until</dt>
          <dd>{Number.isFinite(expires) ? new Date(expires).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' }) : plan.expiresAt}
            {expired && <span className="xfer-warn-text"> (expired)</span>}</dd>
        </dl>

        {plan.replacesCount > 0 && (
          <Callout tone="warning" size="sm" title={`Replaces ${countWords(plan.replacesCount)} at the destination`}>
            <Names names={plan.replaces} count={plan.replacesCount} />
          </Callout>
        )}
        {plan.deletesCount > 0 && (
          <Callout tone="error" size="sm" title={`Deletes ${countWords(plan.deletesCount)} at the destination that are not in the source`}>
            <Names names={plan.deletes} count={plan.deletesCount} />
          </Callout>
        )}
        {plan.action === 'move' && (
          <Callout tone="warning" size="sm" title="The originals are removed">
            Once every file is checked at the destination, Studio removes them from {plan.source.label}.
          </Callout>
        )}
        {(plan.skipped.hidden > 0 || plan.skipped.links > 0) && (
          <p className="muted xfer-plan-note">
            Skipped inside folders: {[plan.skipped.hidden ? `${plan.skipped.hidden.toLocaleString()} hidden` : '', plan.skipped.links ? `${plan.skipped.links.toLocaleString()} ${plan.skipped.links === 1 ? 'link' : 'links'}` : ''].filter(Boolean).join(', ')}.
          </p>
        )}
        {short && <Callout tone="error" size="sm">The destination reports {bytesText(free!)} free, less than the {bytesText(plan.bytes)} to send.</Callout>}
        {plan.warnings.map(w => <Callout key={w} tone="warning" size="sm">{w}</Callout>)}
        {(plan.indexNotes ?? []).map(n => <Callout key={n} tone="info" size="sm">{n}.</Callout>)}
        {expired && !error && <Callout tone="warning" size="sm" title="This plan expired">Plan again to see what would happen now.</Callout>}
        {error && <Callout tone="error" size="sm" live title={error.stale ? 'The files changed' : 'Not started'}>{error.text}</Callout>}
      </div>
    </Modal>
  )
}
