package transfers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/mcpclient"
)

// fakeConduit is a CONDUIT MCP endpoint whose namespace is /mnt/a and
// /mnt/b, folders of the rig's two hosts. start_transfer copies at once
// (cp -R) and get_transfer_status says FINALIZED; lie makes it leave the
// largest file under a temporary name, as pftool did in testing, and still
// say FINALIZED.
type fakeConduit struct {
	mu      sync.Mutex
	actions []string
	sources [][]string
	lie     bool
	ns      map[string]string
}

func (f *fakeConduit) real(p string) string {
	for k, v := range f.ns {
		if p == k || strings.HasPrefix(p, k+"/") {
			return v + strings.TrimPrefix(p, k)
		}
	}
	return "/nonexistent" + p
}

func (f *fakeConduit) serve(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     any             `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &req)
		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s1")
			result = map[string]any{"protocolVersion": "2025-06-18"}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "start_transfer"}, map[string]any{"name": "get_transfer_status"}}}
		case "tools/call":
			var p struct {
				Name string `json:"name"`
				Args struct {
					Action      string   `json:"action"`
					Source      []string `json:"source"`
					Destination string   `json:"destination"`
					TransferID  string   `json:"transfer_id"`
				} `json:"arguments"`
			}
			json.Unmarshal(req.Params, &p)
			switch p.Name {
			case "start_transfer":
				f.mu.Lock()
				f.actions = append(f.actions, p.Args.Action)
				f.sources = append(f.sources, p.Args.Source)
				f.mu.Unlock()
				dst := f.real(strings.TrimSuffix(p.Args.Destination, "/"))
				for _, s := range p.Args.Source {
					exec.Command("cp", "-R", f.real(s), dst+"/").Run()
				}
				if f.lie {
					filepath.Walk(dst, func(path string, fi os.FileInfo, err error) error {
						if err == nil && fi.Mode().IsRegular() && fi.Size() > 100 {
							os.Rename(path, path+"+20261104_172323")
						}
						return nil
					})
				}
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "submitted transfer_id=t-1"}},
					"structuredContent": map[string]any{"submitted": true, "transfer_id": "t-1"}}
			case "get_transfer_status":
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "state=TRANSFER_FINALIZED active=false"}},
					"structuredContent": map[string]any{"state": "TRANSFER_FINALIZED", "active": false, "details": map[string]any{"filesTransferred": 0, "dataTransferred": "1.5 KB"}}}
			}
		}
		out, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func conduitRig(t *testing.T, lie bool) (*rig, *fakeConduit) {
	r := newRig(t)
	f := &fakeConduit{lie: lie, ns: map[string]string{"/mnt/a": r.hostA, "/mnt/b": r.hostB}}
	srv := f.serve(t)
	servers := []json.RawMessage{json.RawMessage(`{"name":"fake","url":"` + srv.URL + `"}`)}
	r.s.d.Conduit = MCPConduit{mcpclient.New(func() []json.RawMessage { return servers })}
	r.env["STUDIO_CONDUIT_POLL_MS"] = "20"
	fake := "fake"
	r.set.S.Movers.Conduit = ConduitSettings{Enabled: true, Server: &fake, Mounts: []ConduitMount{
		{Location: "saved:a", Path: "", Conduit: "/mnt/a"}, {Location: "saved:b", Path: "", Conduit: "/mnt/b"},
	}}
	return r, f
}

func TestConduitCopiesAndIsChecked(t *testing.T) {
	r, f := conduitRig(t, false)
	write(t, filepath.Join(r.hostA, "data", "x.txt"), "x")
	write(t, filepath.Join(r.hostA, "data", "sub", "big.bin"), strings.Repeat("b", 1500))
	q := req(ActionCopy, "saved:a", []string{"data"}, "saved:b", "")
	p := r.mustPlan(single, q)
	if p.Mover != MoverConduit {
		t.Fatalf("plan chose %s: %+v", p.Mover, p.Movers)
	}
	tr := r.run(single, q)
	if tr.State != StateSucceeded || read(t, filepath.Join(r.hostB, "data", "sub", "big.bin")) != strings.Repeat("b", 1500) {
		t.Fatalf("%+v", tr)
	}
	noStaging(t, r.hostB)
	if f.sources[0][0] != "/mnt/a/data" {
		t.Fatalf("sources %v", f.sources)
	}
	// A move sends COPY too, and the Studio removes the originals.
	os.MkdirAll(filepath.Join(r.hostB, "moved"), 0o755)
	tr = r.run(single, req(ActionMove, "saved:a", []string{"data"}, "saved:b", "moved"))
	if tr.State != StateSucceeded {
		t.Fatalf("move %+v", tr)
	}
	gone(t, filepath.Join(r.hostA, "data"))
	read(t, filepath.Join(r.hostB, "moved", "data", "x.txt"))
	for _, a := range f.actions {
		if a != "COPY" {
			t.Fatalf("CONDUIT was sent %s", a)
		}
	}
	// It cannot stop a transfer, and says so.
	if (&conduitMover{s: r.s}).Cancel("x") {
		t.Fatal("cancel said yes")
	}
}

func TestConduitThatLiesFails(t *testing.T) {
	r, _ := conduitRig(t, true)
	write(t, filepath.Join(r.hostA, "data", "big.bin"), strings.Repeat("b", 1500))
	tr := r.run(single, req(ActionMove, "saved:a", []string{"data"}, "saved:b", ""))
	if tr.State != StateFailed || !strings.Contains(tr.Error, "nothing was put in place") {
		t.Fatalf("%+v", tr)
	}
	gone(t, filepath.Join(r.hostB, "data"))
	noStaging(t, r.hostB)
	if read(t, filepath.Join(r.hostA, "data", "big.bin")) == "" {
		t.Fatal("the original went")
	}
}

func TestConduitTypedSource(t *testing.T) {
	r, _ := conduitRig(t, false)
	write(t, filepath.Join(r.hostA, "archive", "a.txt"), "aa")
	ls := r.s.Locations(context.Background(), single)
	var c Location
	for _, l := range ls {
		if l.ID == "conduit:fake" {
			c = l
		}
	}
	if !c.Available || c.Browsable {
		t.Fatalf("conduit location %+v", c)
	}
	q := req(ActionCopy, "conduit:fake", []string{"/mnt/a/archive"}, "saved:b", "")
	p := r.mustPlan(single, q)
	if p.Files != -1 || p.Bytes != -1 || p.Mover != MoverConduit {
		t.Fatalf("typed plan %+v", p)
	}
	tr := r.run(single, q)
	if tr.State != StateSucceeded || read(t, filepath.Join(r.hostB, "archive", "a.txt")) != "aa" || tr.Files.Total != 1 {
		t.Fatalf("%+v", tr)
	}
	for _, bad := range []Request{
		req(ActionMove, "conduit:fake", []string{"/mnt/a/archive"}, "saved:b", ""),
		req(ActionCopy, "conduit:fake", []string{"relative"}, "saved:b", ""),
		req(ActionCopy, "saved:a", []string{"archive"}, "conduit:fake", ""),
	} {
		if _, err := r.plan(single, bad); statusOf(err) != 400 {
			t.Fatalf("%+v: %v", bad, err)
		}
	}
	// With sign-in on it is not offered at all.
	for _, l := range r.s.Locations(context.Background(), Viewer{ID: "u", AuthOn: true}) {
		if l.Kind == KindConduit {
			t.Fatal("conduit offered with sign-in on")
		}
	}
}
