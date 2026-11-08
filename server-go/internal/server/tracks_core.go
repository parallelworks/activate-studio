package server

import "github.com/parallelworks/activate-studio/server-go/internal/core"

func init() { registerTrack(10, "core", core.Register) }
