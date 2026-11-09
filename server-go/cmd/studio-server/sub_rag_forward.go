package main

import (
	"io"

	"github.com/parallelworks/activate-studio/server-go/internal/ragendpoint"
)

// rag-forward is the RAG endpoint's forwarder, which `pw endpoints run`
// starts on the port it assigns (internal/ragendpoint).
func init() {
	registerSubcommand(ragendpoint.Subcommand, subcommand{
		Summary: "forward the platform endpoint's port to this Studio (the RAG endpoint)",
		Run:     func(_ []string, _ io.Reader, _, stderr io.Writer) int { return ragendpoint.Forward(stderr) },
	})
}
