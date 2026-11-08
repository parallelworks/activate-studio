package platform

import "net/http"

// AccessChanged tells Node the CLI's location or the chosen context moved,
// so its own pw commands and gateway follow (frontBridge.ts); clearKey also
// removes the single owner's stored key there. Without Node it does nothing.
func (p *Platform) AccessChanged(clearKey bool) error {
	b := p.Node()
	if b == nil {
		return nil
	}
	return b.Background(http.MethodPost, BridgePrefix+"access-changed", map[string]bool{"clearKey": clearKey}, nil)
}

// InvalidateProviderProbes is chat/gateway.ts's invalidateProviderProbes,
// held by Node until the chat track ports it.
func (p *Platform) InvalidateProviderProbes() {
	if b := p.Node(); b != nil {
		if err := b.Background(http.MethodPost, BridgePrefix+"provider-probes/invalidate", map[string]any{}, nil); err != nil {
			p.log.Warn("provider probes: Node did not answer", "err", err.Error())
		}
	}
}
