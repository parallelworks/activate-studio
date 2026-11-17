package server

import "github.com/parallelworks/activate-studio/server-go/internal/personal"

func init() { registerTrack(44, "personal sign-ins", personal.Register) }
