//go:build platformtrack

package chat

import "github.com/parallelworks/activate-studio/server-go/internal/platform"

// The platform track's *platform.Platform has the methods chat.Platform
// names, so its merge is one assignment in New:
//
//	m.Plat = platformOf(a) // the track's shared *platform.Platform
//
// after which accessPlatform and the bridge's access op go. Built with
// -tags platformtrack against a tree that has internal/platform.
var _ Platform = (*platform.Platform)(nil)
