package server

import "github.com/parallelworks/activate-studio/server-go/internal/index"

func init() { registerTrack(20, "index", index.Register) }
