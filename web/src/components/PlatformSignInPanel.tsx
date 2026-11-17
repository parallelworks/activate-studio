import { useEffect, useRef, useState } from 'react'
import { Button, Callout, CopyButton, ExternalLink, Loader } from '@parallelworks/ui'
import { api, type PlatformSignIn } from '../api'

/**
 * The viewer's own pw CLI sign-in, on a Studio several people use (GET,
 * POST /api/me/platform-sign-in): a device code they approve on the
 * platform, after which the Studio reaches their clusters as them, for a
 * space of their own and folders shared with them. It renews itself, and
 * Sign out ends it. Hidden where the route answers that this Studio keeps
 * no personal sign-ins.
 */
export function PlatformSignInPanel() {
  const [st, setSt] = useState<PlatformSignIn | null>(null)
  const [hidden, setHidden] = useState(false)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const timer = useRef<number | null>(null)

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

  if (hidden || (!st && !error)) return null
  const run = async (what: () => Promise<PlatformSignIn>) => {
    setBusy(true); setError('')
    try { setSt(await what()) } catch (e) { setError(String((e as Error).message ?? e)) } finally { setBusy(false) }
  }

  return (
    <div className="access-panel platform-sign-in">
      <p className="access-lead">Your pw CLI sign-in, for your clusters</p>
      <p className="muted view-sub">
        Lets the Studio reach your clusters as you, for a space of your own and for folders others share with you. It
        renews itself until you sign out, and nobody else here can use it.
      </p>
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
      {st?.state === 'failed' && <Callout tone="error" title="The sign-in did not finish">{st.message ?? ''}</Callout>}
      {(st?.state === 'none' || st?.state === 'failed') && (
        <div className="key-row">
          <Button variant="secondary" loading={busy} onClick={() => void run(api.startPlatformSignIn)}>Sign in with the pw CLI</Button>
        </div>
      )}
      {error && <Callout tone="error" live>{error}</Callout>}
    </div>
  )
}
