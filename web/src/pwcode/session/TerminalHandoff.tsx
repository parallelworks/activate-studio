import { handoffCommandParts, terminalHandoffPanel, usePwCodeTranslations } from '@parallelworks/code'
import { Button, CopyCodeBlock, IconButton, Popover, useClosePopover } from '@parallelworks/ui'
import { TerminalIcon } from '@parallelworks/ui/icons'
import { pwCode } from '../api'
import './TerminalHandoff.css'

export interface TerminalHandoffProps {
  machine: string
  sessionId: string
  /** The viewer, which a computer's command names. */
  username: string | undefined
  /** Runs the command in the Studio's own terminal; given only where that
   *  terminal reaches the session's machine. */
  onOpenTerminal?: (command: string) => void
}

/** Picks the open session up in pw code: the command copies for any terminal,
 *  and the Studio's terminal runs it in one press where it can. */
export function TerminalHandoff(props: TerminalHandoffProps) {
  const t = usePwCodeTranslations('agents')
  const label = t('terminal.title')
  return (
    <Popover
      trigger={<IconButton icon={<TerminalIcon />} label={label} variant="outline" size="sm" />}
      title={label}
      className="pwcode-handoff-popover"
    >
      <HandoffPanel {...props} />
    </Popover>
  )
}

function HandoffPanel({ machine, sessionId, username, onOpenTerminal }: TerminalHandoffProps) {
  const t = usePwCodeTranslations('agents')
  const { data } = pwCode.useSessions()
  const panel = terminalHandoffPanel(data, machine, sessionId, username, t)
  if (panel.status !== 'ready') return <p className="pwcode-handoff-muted">{panel.note}</p>
  const { command } = panel.handoff
  return (
    <div className="pwcode-handoff">
      <p className="pwcode-handoff-muted">{panel.body}</p>
      <div className="pwcode-handoff-command">
        <CopyCodeBlock text={command} label={panel.commandLabel} wrap>
          <CommandText command={command} />
        </CopyCodeBlock>
        {panel.anyTerminal && <p className="pwcode-handoff-note">{panel.anyTerminal}</p>}
      </div>
      {panel.archived && <p className="pwcode-handoff-note">{panel.archived}</p>}
      {onOpenTerminal && <OpenInTerminal command={command} onOpen={onOpenTerminal} />}
    </div>
  )
}

/** The session reference sits in an atomic box, so a narrow panel breaks the
 *  line before it rather than at a hyphen inside the session id. */
function CommandText({ command }: { command: string }) {
  const { head, session } = handoffCommandParts(command)
  return (
    <span className="pwcode-handoff-text">
      <span aria-hidden="true" className="pwcode-handoff-prompt">$</span>
      {head}
      <span className="pwcode-handoff-session">{session}</span>
    </span>
  )
}

function OpenInTerminal({ command, onOpen }: { command: string; onOpen: (command: string) => void }) {
  const t = usePwCodeTranslations('agents')
  const close = useClosePopover()
  return (
    <Button variant="primary" icon={<TerminalIcon />} className="pwcode-handoff-open" onClick={() => { onOpen(command); close() }}>
      {t('terminal.open')}
    </Button>
  )
}
