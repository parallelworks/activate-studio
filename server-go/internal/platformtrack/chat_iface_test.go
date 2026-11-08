package platformtrack

import (
	"github.com/parallelworks/activate-studio/server-go/internal/chat"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// The chat track codes against these methods of the platform package; a
// rename here fails to compile.
var _ chat.Platform = (*platform.Platform)(nil)
