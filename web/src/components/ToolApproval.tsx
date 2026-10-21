import { useEffect, useState } from 'react'
import { Button } from '@parallelworks/ui'
import { ApprovalBlock, type ApprovalPart, type PartRendererProps } from '@parallelworks/ui/ai'

/** A tool the user may run in place of the held call, with the same
 *  arguments. The server lists these on the approval it sends. */
export interface ApprovalAlternative { tool: string; label: string; description?: string }

type HeldApproval = ApprovalPart & { instead?: ApprovalAlternative[] }

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
 * regardless.
 */
export function ToolApproval({ part, onApprovalAnswer }: PartRendererProps) {
  const held = part as HeldApproval
  const expired = useExpired(part.kind === 'approval' ? held.expiresAt : undefined)
  if (part.kind !== 'approval') return null
  const answer = held.waitingOn ? undefined : onApprovalAnswer
  const alternatives = answer && !held.resolved && !expired && held.approvalKind === 'confirm' ? held.instead ?? [] : []
  return (
    <div className="tool-approval">
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
