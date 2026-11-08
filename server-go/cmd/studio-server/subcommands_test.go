package main

import (
	"io"
	"os"
	"testing"
)

func TestDispatchSubcommand(t *testing.T) {
	var got []string
	registerSubcommand("zz-test", subcommand{Run: func(args []string, _ io.Reader, _, _ io.Writer) int {
		got = args
		return 7
	}})
	defer delete(subcommands, "zz-test")
	old := os.Args
	defer func() { os.Args = old }()

	os.Args = []string{"studio-server", "zz-test", "a", "b"}
	code, ok := dispatchSubcommand()
	if !ok || code != 7 || len(got) != 2 || got[0] != "a" {
		t.Fatalf("dispatch: ok=%v code=%d args=%v", ok, code, got)
	}
	for _, args := range [][]string{{"studio-server"}, {"studio-server", "-routes"}, {"studio-server", "unknown"}} {
		os.Args = args
		if _, ok := dispatchSubcommand(); ok {
			t.Fatalf("dispatched %v", args)
		}
	}
}
