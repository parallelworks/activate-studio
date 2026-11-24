import { useEffect, useState } from 'react'
import { Button } from '@parallelworks/ui'
import { ApprovalBlock, type ApprovalPart, type PartRendererProps } from '@parallelworks/ui/ai'
import { ChatTransferPlan, type HeldTransfer } from './ChatTransferPlan'

/** A tool the user may run in place of the held call, with the same
 *  arguments. The server lists these on the approval it sends. */
export interface ApprovalAlternative { tool: string; label: string; description?: string }

/** The location a held save_transfer_location call would save. */
export interface HeldLocation { label: string; host: string; root: string; rsyncPath?: string; port?: number; sshOptions?: string }

/** A place a held write may go: the person picks one, and the answer
 *  carries the call with that library and path (args), which the server
 *  checks against the places it offered. */
export interface WhereOption { label: string; library: string; path: string; shown: string; args: string }

type HeldApproval = ApprovalPart & { instead?: ApprovalAlternative[]; transfer?: HeldTransfer; savedLocation?: HeldLocation; where?: WhereOption[] }

/** Asks where a new file goes when the chat's context does not say: a
 *  button for each place, with the path it would get, and one to not save. */
function WhereChoice({ held, onPick }: { held: HeldApproval; onPick: (o: WhereOption | null) => void }) {
  return (
    <section className="chat-xfer" aria-label={held.header ?? 'Where should this be saved?'} data-testid="chat-where">
      <p className="tool-approval-where-head"><strong>{held.header ?? 'Where should this be saved?'}</strong></p>
      {held.reason && <p className="chat-xfer-muted">{held.reason}</p>}
      <div className="tool-approval-where-options">
        {(held.where ?? []).map(o => (
          <div key={o.args} className="tool-approval-alt">
            <Button variant="secondary" size="sm" onClick={() => onPick(o)}>{o.label}</Button>
            <span className="tool-approval-alt-note chat-xfer-mono">{o.shown}</span>
          </div>
        ))}
        <div className="tool-approval-alt">
          <Button variant="ghost" size="sm" onClick={() => onPick(null)}>Don't save it</Button>
        </div>
      </div>
    </section>
  )
}

/** The location the assistant asks to save, drawn above the approval
 *  buttons as the transfer plan is: its label, host and folder. */
function SavedLocationCard({ l }: { l: HeldLocation }) {
  return (
    <section className="chat-xfer" aria-label="Location to save" data-testid="chat-saved-location">
      <dl className="chat-xfer-facts">
        <dt>Label</dt><dd>{l.label}</dd>
        <dt>Host</dt><dd className="chat-xfer-mono">{l.host}</dd>
        <dt>Folder</dt><dd className="chat-xfer-mono">{l.root}</dd>
        {l.port ? <><dt>Port</dt><dd className="chat-xfer-mono">{l.port}</dd></> : null}
        {l.sshOptions ? <><dt>ssh options</dt><dd className="chat-xfer-mono">{l.sshOptions}</dd></> : null}
        {l.rsyncPath ? <><dt>rsync</dt><dd className="chat-xfer-mono">{l.rsyncPath}</dd></> : null}
      </dl>
      <p className="chat-xfer-muted chat-xfer-foot">
        Saved locations are listed in <a href="#view=settings:transfers">Settings, Transfers</a>.
      </p>
    </section>
  )
}

/** Whether the ISO time has passed, turning true when it does. */
function useExpired(at: string | undefined): boolean {
  const [expired, setExpired] = useState(() => !!at && Date.parse(at) <= Date.now())
  useEffect(() => {
    if (!at || expired) return
    const left = Date.parse(at) - Date.now()
    if (left <= 0) { setExpired(true); return }
    const timer = setTimeout(() => setExpired(true), Math.min(left, 86_400_000))
    return () => clearTimeout(timer)
  }, [at, expired])
  return expired
}

/**
 * A call the server holds for the user's approval: the chat package's own
 * approval block (allow, deny, deny with a note), and under it any tool the
 * server allows in its place, such as destroying a cluster the assistant
 * asked to stop. The package's block has no control for that alternative,
 * though its answer carries one (`instead`). A call someone else asked for
 * names them and offers no buttons; the server refuses anyone else's answer
 * regardless. A transfer the assistant asked for shows its plan above the
 * block while it waits; once answered, the package draws the answer on the
 * call's line, and the call's result names the transfer. A location the
 * assistant asks to save shows its label, host and folder the same way.
 */
export function ToolApproval({ part, onApprovalAnswer }: PartRendererProps) {
  const held = part as HeldApproval
  const expired = useExpired(part.kind === 'approval' ? held.expiresAt : undefined)
  if (part.kind !== 'approval') return null
  const answer = held.waitingOn ? undefined : onApprovalAnswer
  const alternatives = answer && !held.resolved && !expired && held.approvalKind === 'confirm' ? held.instead ?? [] : []
  const transfer = held.transfer?.plan ? held.transfer : undefined
  const saved = held.savedLocation?.host ? held.savedLocation : undefined
  if (held.where?.length && answer && !held.resolved && !expired) {
    return (
      <div className="tool-approval">
        <WhereChoice held={held} onPick={o => answer(held.id, o ? { kind: 'confirm', allowed: true, args: o.args } : { kind: 'confirm', allowed: false })} />
      </div>
    )
  }
  return (
    <div className="tool-approval">
      {transfer && !held.resolved && !expired && <ChatTransferPlan transfer={transfer} />}
      {saved && !held.resolved && !expired && <SavedLocationCard l={saved} />}
      <ApprovalBlock part={held} {...(answer ? { onApprovalAnswer: answer } : {})} />
      {alternatives.map(alt => (
        <div key={alt.tool} className="tool-approval-alt">
          <Button
            variant="secondary"
            size="sm"
            onClick={() => answer?.(held.id, { kind: 'confirm', allowed: true, instead: alt.tool })}
          >
            {alt.label}
          </Button>
          {alt.description && <span className="tool-approval-alt-note">{alt.description}</span>}
        </div>
      ))}
    </div>
  )
}
