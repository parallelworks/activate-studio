package search

import "github.com/parallelworks/activate-studio/server-go/internal/bridge"

// GufiVtPrefix is the way in for gufi_vt's remote command. Node's wrapper
// calls Node on Node's own port, never through the front; the Go server's
// subcommand (VtRemoteMain) calls the Go server with the bridge secret.
// The front refuses the path to anything without that secret: 404.
const GufiVtPrefix = "/internal/gufi-vt"

func init() { bridge.RegisterPrivatePrefix(GufiVtPrefix) }
