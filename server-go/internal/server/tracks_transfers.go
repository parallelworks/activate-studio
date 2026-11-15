package server

import "github.com/parallelworks/activate-studio/server-go/internal/transfers"

func init() { registerTrack(48, "transfers", transfers.Register) }
