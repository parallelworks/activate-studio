package search

import "github.com/parallelworks/activate-studio/server-go/internal/bridge"

// GufiVtPrefix is Node's way in for its gufi_vt wrapper (gufiVt.ts). The
// wrapper calls Node on Node's own port, never through the front, so the
// front refuses the path to clients: 404 Node-free, where nothing serves
// it, and 404 with Node behind as well.
const GufiVtPrefix = "/internal/gufi-vt"

func init() { bridge.RegisterPrivatePrefix(GufiVtPrefix) }
