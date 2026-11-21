package server

import "github.com/parallelworks/activate-studio/server-go/internal/agentsmd"

func init() { registerTrack(49, "agent instructions", agentsmd.Register) }
