/**
 * Voice conversations, as a feature preview: the Unmute deployment's own
 * interface, in an overlay over the chat. Unmute is a separate service
 * (speech recognition, turn-taking, synthesis, and the model behind them,
 * which is this Studio's own assistant on the /v1 endpoint), so the
 * overlay is an iframe with microphone permission delegated to it. The
 * transcript stays with Unmute for now; recording it as a Studio
 * conversation is the next step once the voice turns flow through here.
 */
export function VoiceOverlay({ url, onClose }: { url: string; onClose: () => void }) {
  return (
    <div className="voice-overlay" role="dialog" aria-label="Voice conversation">
      <div className="voice-overlay-bar">
        <span className="voice-overlay-title">Voice · feature preview</span>
        <a className="link-button" href={url} target="_blank" rel="noopener noreferrer">open in a new tab</a>
        <button className="btn-secondary" onClick={onClose}>Close</button>
      </div>
      <iframe className="voice-overlay-frame" src={url} title="Voice conversation" allow="microphone; autoplay; camera" />
    </div>
  )
}
