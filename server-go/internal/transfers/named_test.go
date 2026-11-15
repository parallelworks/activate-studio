package transfers

import "testing"

func TestNamedPutsTheHostOnce(t *testing.T) {
	r := &RemoteFS{Name: "localhost"}
	for _, in := range []string{"Permission denied (publickey)", "localhost: Permission denied (publickey)"} {
		if got := r.named(502, in).Error(); got != "localhost: Permission denied (publickey)" {
			t.Errorf("named(%q) = %q", in, got)
		}
	}
}
