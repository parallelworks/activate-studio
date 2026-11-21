package libraries

import "testing"

func TestLocalLibraryNamesTheClusterTheStudioRunsBeside(t *testing.T) {
	t.Setenv("STUDIO_HOME_CLUSTER", "")
	t.Setenv("PW_USER", "someone")
	t.Setenv("PW_POOL_NAME", "cluster-a")
	if h := Public(Library{ID: "kb", Label: "Main"}).Host; h == nil || *h != "cluster-a" {
		t.Fatalf("a folder here on a workflow deployment: host %v, want cluster-a", h)
	}
	if h := Public(Library{ID: "r", Label: "Remote", Site: "pw://someone/cluster-b"}).Host; h != nil {
		t.Fatalf("a remote library is named by its site, not the host: %v", *h)
	}
	t.Setenv("STUDIO_HOME_CLUSTER", "pw://someone/cluster-c")
	if h := Public(Library{ID: "kb", Label: "Main"}).Host; h == nil || *h != "cluster-c" {
		t.Fatalf("STUDIO_HOME_CLUSTER comes first: host %v", h)
	}
	t.Setenv("STUDIO_HOME_CLUSTER", "")
	t.Setenv("PW_POOL_NAME", "")
	if h := Public(Library{ID: "kb", Label: "Main"}).Host; h != nil {
		t.Fatalf("a Studio that does not know its cluster names none: %v", *h)
	}
}
