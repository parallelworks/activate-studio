import {
  type ClaudeSignInState as SignInState,
  canOpenSignInLink,
  useClaudeSignIn as useSignIn,
  usePwCodeTranslations,
} from '@parallelworks/code'
import { Button, LoadingRegion, Skeleton } from '@parallelworks/ui'
import { NoticeCard } from '@parallelworks/ui/ai'
import { CheckIcon } from '@parallelworks/ui/icons'
import { useEffect, useMemo, useRef, useState } from 'react'
import { pwCode } from '../api'
import { describeError as defaultDescribeError } from '../PWCodeSession'
import './AccountSignIn.css'

const loginCalls = {
  cancelClaudeLogin: pwCode.cancelClaudeLogin,
  followClaudeLogin: pwCode.followClaudeLogin,
  startClaudeLogin: pwCode.startClaudeLogin,
  writeClaudeLogin: pwCode.writeClaudeLogin,
}

// No terminal emulator here: the prompt runs at a fixed size and its output
// is drawn as text from the hook's state.
const terminal = { size: () => ({ columns: 100, rows: 30 }) }

export interface AccountSignInProps {
  machine: string
  machineName: string
  sessionId: string | undefined
  account: string
  onComplete: () => void
  onUse: (() => void) | undefined
  onBack: () => void
  describeError?: ((error: unknown) => string) | undefined
}

function openLink(uri: string) {
  if (canOpenSignInLink(uri)) window.open(uri, '_blank', 'noopener,noreferrer')
}

/** Signs an account in on the machine: the CLI's prompt, its sign-in page to
 *  open, and a field for the code that page gives. */
export function AccountSignIn({
  machine,
  machineName,
  sessionId,
  account,
  onComplete,
  onUse,
  onBack,
  describeError = defaultDescribeError,
}: AccountSignInProps) {
  const t = usePwCodeTranslations('agents')
  const signIn = useSignIn(loginCalls, {
    machine,
    machineName,
    sessionId,
    account,
    onSignedIn: onComplete,
    onUse,
    terminal,
  })
  const { login, error, signInPage, signedIn } = signIn
  const text = useMemo(() => terminalText(signIn.output), [signIn.output])
  const screen = useRef<HTMLPreElement>(null)
  useEffect(() => {
    const el = screen.current
    if (el) el.scrollTop = el.scrollHeight
  }, [text])
  return (
    <div className="pwcode-signin" data-testid="pwcode-signin">
      {signIn.instructions !== null && <p className="pwcode-signin-muted">{signIn.instructions}</p>}
      {signIn.starting && (
        <LoadingRegion label={t('claude.startingSignIn')}>
          <Skeleton height={14} />
        </LoadingRegion>
      )}
      {error ? <NoticeCard tone="warning">{describeError(error)}</NoticeCard> : null}
      {signIn.inputTooLong && <NoticeCard tone="warning">{t('claude.inputTooLong')}</NoticeCard>}
      {signIn.truncatedNote !== null && <p className="pwcode-signin-small">{signIn.truncatedNote}</p>}
      {signInPage !== null && canOpenSignInLink(signInPage) && (
        <div>
          <Button variant="primary" onClick={() => openLink(signInPage)}>{t('claude.openSignInPage')}</Button>
        </div>
      )}
      {!signedIn && (
        <>
          <pre ref={screen} className="pwcode-signin-output" aria-label={t('claude.signInTerminal')} tabIndex={0} data-testid="pwcode-signin-output">
            {text}
          </pre>
          <CodeField key={signIn.attempt} signIn={signIn} />
        </>
      )}
      {login && (
        <p role="status" className={signedIn ? 'pwcode-signin-status is-done' : 'pwcode-signin-status'}>
          {signedIn && <CheckIcon className="pwcode-signin-check" aria-hidden />}
          {signIn.status}
        </p>
      )}
      <SignInActions signIn={signIn} onBack={onBack} />
    </div>
  )
}

/** Where the code from the sign-in page is pasted; Enter in the prompt is `\r`. */
function CodeField({ signIn }: { signIn: SignInState }) {
  const [code, setCode] = useState('')
  const field = useRef<HTMLInputElement>(null)
  const { acceptsInput, input } = signIn
  useEffect(() => {
    if (acceptsInput) field.current?.focus()
  }, [acceptsInput])
  return (
    <form
      className="pwcode-signin-code"
      onSubmit={event => {
        event.preventDefault()
        const value = code.trim()
        if (!value) return
        if (input(value + '\r')) setCode('')
      }}
    >
      <input
        ref={field}
        className="pwcode-signin-input"
        aria-label="Code from the sign-in page"
        placeholder="Paste the code from the sign-in page"
        value={code}
        onChange={event => setCode(event.target.value)}
        disabled={!acceptsInput}
        autoComplete="off"
        spellCheck={false}
        data-testid="pwcode-signin-code"
      />
      <Button type="submit" variant="secondary" disabled={!acceptsInput || !code.trim()}>Send</Button>
    </form>
  )
}

function SignInActions({ signIn, onBack }: { signIn: SignInState; onBack: () => void }) {
  const t = usePwCodeTranslations('agents')
  const { signedIn, use } = signIn
  // The code field that held focus goes once the sign-in is reported.
  const next = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (signedIn) next.current?.focus()
  }, [signedIn])
  return (
    <div className="pwcode-signin-actions">
      {signIn.canReconnect && <Button variant="secondary" onClick={signIn.reconnect}>{t('claude.reconnectSignIn')}</Button>}
      {signIn.canRetry ? <Button variant="primary" onClick={signIn.retry}>{t('claude.retrySignIn')}</Button> : null}
      <Button ref={use ? undefined : next} variant={signedIn && !use ? 'primary' : 'secondary'} onClick={onBack}>
        {signIn.closeLabel}
      </Button>
      {use && <Button ref={next} variant="primary" onClick={use}>{t('claude.useThisAccount')}</Button>}
    </div>
  )
}

const ESC = '\u001b'

/**
 * The prompt's screen as text: a small line model of what a terminal would
 * show. Escape sequences are dropped, but the cursor moves and erases the
 * CLI redraws with are applied, so a redrawn prompt doesn't repeat.
 */
export function terminalText(output: string): string {
  const lines: string[][] = [[]]
  let row = 0
  let col = 0
  const line = () => (lines[row] ??= [])
  const moveTo = (r: number) => {
    row = Math.max(0, r)
    while (lines.length <= row) lines.push([])
  }
  const chars = Array.from(output)
  for (let i = 0; i < chars.length; i++) {
    const c = chars[i]!
    if (c === ESC) {
      const kind = chars[i + 1]
      if (kind === '[') {
        // CSI: parameters, intermediates, then one final byte.
        let j = i + 2
        let params = ''
        while (j < chars.length && /[0-?]/.test(chars[j]!)) params += chars[j++]
        while (j < chars.length && /[ -/]/.test(chars[j]!)) j++
        const final = chars[j]
        i = j
        const n = Number.parseInt(params, 10)
        const count = Number.isNaN(n) || n < 1 ? 1 : n
        switch (final) {
          case 'A': moveTo(row - count); break
          case 'B': case 'E': moveTo(row + count); if (final === 'E') col = 0; break
          case 'F': moveTo(row - count); col = 0; break
          case 'C': col += count; break
          case 'D': col = Math.max(0, col - count); break
          case 'G': col = count - 1; break
          case 'H': case 'f': {
            const [r, k] = params.split(';').map(p => Number.parseInt(p, 10))
            moveTo((r || 1) - 1)
            col = (k || 1) - 1
            break
          }
          case 'K': {
            const l = line()
            if (params === '2') l.length = 0
            else if (params === '1') for (let x = 0; x <= col && x < l.length; x++) l[x] = ' '
            else l.length = Math.min(l.length, col)
            break
          }
          case 'J':
            if (params === '2' || params === '3') { lines.length = 0; lines.push([]); row = 0; col = 0 }
            else { line().length = Math.min(line().length, col); lines.length = row + 1 }
            break
        }
        continue
      }
      if (kind === ']' || kind === 'P' || kind === '_' || kind === '^' || kind === 'X') {
        // OSC and other strings, such as a hyperlink's address, end at BEL or ESC \.
        let j = i + 2
        while (j < chars.length && chars[j] !== '\u0007' && !(chars[j] === ESC && chars[j + 1] === '\\')) j++
        i = chars[j] === ESC ? j + 1 : j
        continue
      }
      // A two-character escape, or one with a charset designator.
      i += kind && '()*+'.includes(kind) ? 2 : 1
      continue
    }
    if (c === '\n') { moveTo(row + 1); col = 0; continue }
    if (c === '\r') { col = 0; continue }
    if (c === '\b') { col = Math.max(0, col - 1); continue }
    if (c === '\t') { col = (Math.floor(col / 8) + 1) * 8; continue }
    if (c < ' ' || c === '\u007f') continue
    const l = line()
    while (l.length < col) l.push(' ')
    l[col++] = c
  }
  return lines.map(l => l.join('').trimEnd()).join('\n').replace(/\n+$/, '')
}
