package server

import "github.com/parallelworks/activate-studio/server-go/internal/delegation"

func init() { registerTrack(55, "delegation", delegation.Register) }
