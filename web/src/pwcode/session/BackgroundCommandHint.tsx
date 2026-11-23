import { offersBackground, usePwCodeTranslations } from '@parallelworks/code'
import { Tooltip } from '@parallelworks/ui'
import type { ToolFooterProps } from '@parallelworks/ui/ai'
import { createContext, use, useEffect, useRef } from 'react'
import './BackgroundCommandHint.css'

/** The offer to move a turn's running commands to the background, as
 *  usePwCodeComposer's `background` (or pwCode.useBackgroundCommands) holds it. */
export interface BackgroundCommands {
  offered: boolean
  moving: boolean
  move: () => Promise<void>
}

// The terminal's name for the key, the same in every language.
const backgroundKey = 'ctrl+b'

/** The session's offer to move its running commands to the background; unset
 *  outside a pw code session's main conversation, where the hint stays away. */
export const BackgroundCommandsContext = createContext<BackgroundCommands | null>(null)

/** Under a running Bash call, as the terminal offers ctrl+b under one. */
export function BackgroundCommandHint({ part }: ToolFooterProps) {
  const background = use(BackgroundCommandsContext)
  if (!background || !offersBackground(part, background)) return null
  return <Hint background={background} />
}

// Apart, so a chat with no session never asks for pw code's strings.
function Hint({ background }: { background: BackgroundCommands }) {
  const t = usePwCodeTranslations('agents')
  return (
    <Tooltip content={t('detail.backgroundTitle')} describesAnchor>
      <button
        type="button"
        data-testid="agent-background"
        className="pwcode-background-hint"
        aria-keyshortcuts="Control+B"
        disabled={background.moving}
        onClick={() => void background.move()}
      >
        {t('detail.backgroundHint', { key: backgroundKey })}
      </button>
    </Tooltip>
  )
}

/** ctrl+b moves the running commands while they are offered, as in the
 *  terminal; otherwise the key is left to the page. */
export function useBackgroundShortcut(background: BackgroundCommands, enabled: boolean) {
  const latest = useRef(background)
  latest.current = background
  const listening = enabled && background.offered && !background.moving
  useEffect(() => {
    if (!listening) return
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.ctrlKey && !e.metaKey && !e.altKey && !e.shiftKey && !e.isComposing && !e.repeat && e.key.toLowerCase() === 'b') {
        e.preventDefault()
        void latest.current.move()
      }
    }
    document.addEventListener('keydown', onKeyDown)
    return () => document.removeEventListener('keydown', onKeyDown)
  }, [listening])
}
