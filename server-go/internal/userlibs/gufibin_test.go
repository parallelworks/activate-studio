package userlibs

import (
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// A GUFI folder the host reports with ".." in it (a PATH entry written that
// way) is kept without the ".." steps, since a library's GUFI bin directory
// may not hold them and Add would refuse what Test connection filled in.
func TestGufiStepKeepsTheFolderWithoutDotDot(t *testing.T) {
	m := &Module{}
	run := func(argv []string, _ time.Duration) remote.Result {
		if len(argv) > 2 && argv[0] == "sh" && argv[2] == searchTools {
			return remote.Result{Stdout: "/opt/gufi/bin/../bin\n"}
		}
		return remote.Result{Stdout: "gufi_query 0.6\n"}
	}
	var steps []step
	g := m.gufiStep(probeHost{Name: "box"}, run, &steps, nil, "/home/u", false, false)
	if g.Bin != "/opt/gufi/bin" {
		t.Fatalf("bin %q", g.Bin)
	}
	if _, err := remotePath(g.Bin, "GUFI bin directory"); err != nil {
		t.Fatalf("the folder found is refused: %v", err)
	}
}
