/**
 * Voice conversations, as a feature preview. The Unmute deployment is its
 * own platform session on its own domain, so an iframe of it is a
 * cross-site frame: the session proxy asks for its own sign-in inside the
 * frame, and mobile browsers block the cookie that would carry the
 * existing session across. Opening it at top level is where a platform
 * session works, so the button does that, and this panel explains what
 * is about to happen and what the voice can reach.
 *
 * Serving it from the Studio's own origin instead is the better answer
 * and needs a proxy plus a base path in Unmute's frontend build; until
 * that lands, one tab is honest and works everywhere.
 */
export function VoiceOverlay({ url, onClose }: { url: string; onClose: () => void }) {
  return (
    <div className="voice-panel card" role="dialog" aria-label="Voice conversation">
      <h3>Talk with the assistant</h3>
      <p className="muted view-sub">
        Voice opens in its own tab: it listens, decides when you have finished a thought, answers aloud, and can be
        interrupted. The assistant behind it is this one, with the same knowledge base, tools, and workflows.
      </p>
      <p className="muted view-sub">Feature preview. The conversation stays in that tab and is not recorded here yet.</p>
      <div className="query-actions">
        <a className="btn-primary" href={url} target="_blank" rel="noopener noreferrer" onClick={onClose}>Open voice</a>
        <button className="btn-secondary" onClick={onClose}>Not now</button>
      </div>
    </div>
  )
}
