package server

import "github.com/parallelworks/activate-studio/server-go/internal/libapi"

func init() { registerTrack(40, "libraries", libapi.Register) }
