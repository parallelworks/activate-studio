package server

import "github.com/parallelworks/activate-studio/server-go/internal/platformtrack"

func init() { registerTrack(50, "platform", platformtrack.Register) }
