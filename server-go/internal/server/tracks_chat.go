package server

import "github.com/parallelworks/activate-studio/server-go/internal/chat"

func init() { registerTrack(70, "chat", chat.Register) }
