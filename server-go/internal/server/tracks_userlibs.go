package server

import "github.com/parallelworks/activate-studio/server-go/internal/userlibs"

func init() { registerTrack(45, "userlibs", userlibs.Register) }
