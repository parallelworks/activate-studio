package gufi

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The runner's cost over running gufi_query directly, on the fixture
// index or on STUDIO_GUFI_BENCH_INDEX:
//
//	go test -run '^$' -bench . -benchtime 20x ./internal/gufi
func benchIndex(b *testing.B) (bin, root string) {
	bin = os.Getenv("STUDIO_GUFI_BIN")
	if bin == "" {
		bin = "/opt/gufi/bin"
	}
	if _, err := os.Stat(filepath.Join(bin, ToolQuery)); err != nil {
		b.Skip("no gufi_query")
	}
	if root = os.Getenv("STUDIO_GUFI_BENCH_INDEX"); root != "" {
		return bin, root
	}
	return bin, buildFixture(b, bin, b.TempDir())
}

func benchArgs(root string) []string {
	plan, _ := (&Planner{IndexRoot: root}).Canned("largest", CannedParams{N: 25})
	return append(plan.Args(), root)
}

func BenchmarkDirectExec(b *testing.B) {
	bin, root := benchIndex(b)
	args := benchArgs(root)
	env := EnvFor(bin, os.Environ(), nil, EnvOptions{})
	b.ResetTimer()
	for b.Loop() {
		cmd := exec.Command(filepath.Join(bin, ToolQuery), args...)
		cmd.Env = env
		if _, err := cmd.Output(); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRunner(b *testing.B) {
	bin, root := benchIndex(b)
	args := benchArgs(root)
	r := NewRunner(NewLocal(bin), nil, RunnerOptions{})
	b.ResetTimer()
	for b.Loop() {
		if _, err := r.Run(context.Background(), Command{Tool: ToolQuery, Args: args}); err != nil {
			b.Fatal(err)
		}
	}
}

type noop struct{}

func (noop) Exec(context.Context, Command) (Result, error) { return Result{}, nil }

// BenchmarkAdmission is the runner alone: admission, release and the log
// line, with an executor that does nothing.
func BenchmarkAdmission(b *testing.B) {
	r := NewRunner(noop{}, nil, RunnerOptions{})
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r.Run(context.Background(), Command{Tool: ToolQuery})
		}
	})
}
