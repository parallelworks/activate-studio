package server

import "github.com/parallelworks/activate-studio/server-go/internal/notices"

// After the tracks whose state it reads.
func init() { registerTrack(80, "notices", notices.Register) }
