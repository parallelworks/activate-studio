import { useEffect, useRef, useState } from 'react'
import { Button, Callout, CopyButton, ExternalLink, Loader } from '@parallelworks/ui'
import { api, type PlatformSignIn } from '../api'

const FAILED = 'The sign-in did not finish'

/**
 * Sign in with ACTIVATE: the viewer's own sign-in on a Studio several
 * people use (GET, POST /api/me/platform-sign-in). The pw CLI on the server
 * shows a device code, which the person approves on the platform; the
 * Studio then keeps the sign-in in a folder of theirs and renews its access
 * tokens, which it uses for their models, the platform, My library and
 * folders shared with them. Sign out ends it. Hidden where the route
 * answers that this Studio keeps no personal sign-ins. It shows the state
 * and the button only; the page around it says what the sign-in is for.
 * `onChanged` hears a sign-in or sign-out finish, so that page can read
 * again.
 */
export function PlatformSignInPanel({ onChanged }: { onChanged?: () => void }) {
  const [st, setSt] = useState<PlatformSignIn | null>(null)
  const [hidden, setHidden] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const timer = useRef<number | null>(null)
  const last = useRef<string | null>(null)

  const read = () => api.platformSignIn().then(setSt, (e: Error & { status?: number }) => {
    if (e.status === 401 || e.status === 403 || e.status === 409 || e.status === 404) setHidden(true)
    else setError(String(e.message ?? e))
  })
  useEffect(() => { void read() }, [])
  // While a sign-in waits for approval, look again every two seconds.
  useEffect(() => {
    if (st?.state !== 'starting' && st?.state !== 'waiting') return
    timer.current = window.setTimeout(() => void read(), 2000)
    return () => { if (timer.current) window.clearTimeout(timer.current) }
  }, [st])
  // Tell the page when the person becomes signed in or signed out.
  useEffect(() => {
    if (!st) return
    const was = last.current
    last.current = st.state
    if (was !== null && was !== st.state && (st.state === 'signed-in' || (was === 'signed-in' && st.state === 'none'))) onChanged?.()
  }, [st, onChanged])

  if (hidden || (!st && !error)) return null
  const run = async (what: () => Promise<PlatformSignIn>) => {
    setBusy(true); setError('')
    try { setSt(await what()) } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(false) }
  }
  const failure = st?.state === 'failed' && st.message && st.message.replace(/\.$/, '') !== FAILED ? st.message : ''

  return (
    <div className="platform-sign-in">
      {st?.state === 'signed-in' && (
        <Callout tone="success" title={`Signed in${st.account ? ` as ${st.account}` : ''}${st.host ? ` on ${st.host}` : ''}`}
          action={<Button variant="secondary" size="sm" disabled={busy} onClick={() => void run(api.signOutPlatform)}>Sign out</Button>}>
          {st.signedInAt && <p className="muted key-note">Since {st.signedInAt.slice(0, 16).replace('T', ' ')}.</p>}
        </Callout>
      )}
      {(st?.state === 'starting' || st?.state === 'waiting') && (
        <Callout className="rag-banner device-code" tone="info" live title="Approve the sign-in on the platform"
          action={<Button variant="secondary" size="sm" disabled={busy} onClick={() => void run(api.cancelPlatformSignIn)}>Cancel</Button>}>
          {st.url && st.code ? (
            <>
              <p className="identity-note">Open <ExternalLink href={st.url}>{st.url}</ExternalLink> and enter this code:</p>
              <div className="device-code-row">
                <code className="device-code-value">{st.code}</code>
                <CopyButton text={st.code} label="code" />
              </div>
            </>
          ) : null}
          <p className="key-note"><Loader inline size={14} label={st.url ? 'Waiting for approval…' : 'Starting…'} /></p>
        </Callout>
      )}
      {st?.state === 'failed' && (
        <Callout tone="error" title={FAILED}>
          {failure || 'Start again, and approve the code on the platform while signed in as yourself.'}
        </Callout>
      )}
      {(st?.state === 'none' || st?.state === 'failed') && (
        <div className="key-row">
          <Button variant="primary" loading={busy} onClick={() => void run(api.startPlatformSignIn)}>
            {st.state === 'failed' ? 'Try again' : 'Sign in with ACTIVATE'}
          </Button>
        </div>
      )}
      {error && <Callout tone="error" live>{error}</Callout>}
    </div>
  )
}
