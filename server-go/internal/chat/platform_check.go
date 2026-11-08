package chat

import "github.com/parallelworks/activate-studio/server-go/internal/platform"

// m.Plat is the platform track's shared *platform.Platform; a rename of one
// of the methods Platform names fails here.
var _ Platform = (*platform.Platform)(nil)
