package transfers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// api is the routes of a rig over HTTP; the viewer is the X-Test-User
// header (none: sign-in off).
type api struct {
	t   *testing.T
	srv *httptest.Server
}

func newAPI(t *testing.T, r *rig) *api {
	mux := http.NewServeMux()
	r.s.Routes(mux, nil, func(req *http.Request) (Viewer, error) {
		if u := req.Header.Get("X-Test-User"); u != "" {
			return Viewer{ID: u, AuthOn: true, Key: u + "-key"}, nil
		}
		return Viewer{Key: "deployment-key"}, nil
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &api{t, srv}
}

func (a *api) do(method, path, user string, body any) (int, map[string]any) {
	a.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, a.srv.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("X-Test-User", user)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	b, _ := io.ReadAll(res.Body)
	if err := json.Unmarshal(b, &out); err != nil {
		a.t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, b)
	}
	return res.StatusCode, out
}

// transfer plans and starts one, and polls the list until it ends.
func (a *api) transfer(user string, body map[string]any) map[string]any {
	a.t.Helper()
	st, out := a.do("POST", "/api/transfers/plan", user, body)
	if st != 200 {
		a.t.Fatalf("plan: %d %v", st, out)
	}
	plan := out["plan"].(map[string]any)
	st, out = a.do("POST", "/api/transfers", user, map[string]any{"planId": plan["id"]})
	if st != 200 {
		a.t.Fatalf("start: %d %v", st, out)
	}
	id := out["transfer"].(map[string]any)["id"].(string)
	for i := 0; i < 500; i++ {
		_, out = a.do("GET", "/api/transfers/"+id, user, nil)
		tr := out["transfer"].(map[string]any)
		if finished(tr["state"].(string)) {
			tr["plan"] = plan
			return tr
		}
		time.Sleep(20 * time.Millisecond)
	}
	a.t.Fatalf("%s did not end", id)
	return nil
}

func TestEndToEndOverTheRoutes(t *testing.T) {
	r := newRig(t)
	a := newAPI(t, r)
	write(t, filepath.Join(r.hostA, "results", "run1", "out.csv"), "a,b\n1,2\n")
	write(t, filepath.Join(r.hostA, "results", "run1", "big.bin"), strings.Repeat("0123456789", 50000))
	write(t, filepath.Join(r.hostA, "results", "it's $(id).txt"), "odd name")
	write(t, filepath.Join(r.kb, "papers", "p1.md"), "# paper one")
	write(t, filepath.Join(r.kb, "papers", "p2.md"), "# paper two")

	st, out := a.do("GET", "/api/transfers/locations", "", nil)
	if st != 200 || len(out["locations"].([]any)) < 6 {
		t.Fatalf("locations %d %v", st, out)
	}
	st, out = a.do("GET", "/api/transfers/browse?location=saved:a&path=results", "", nil)
	if st != 200 || len(out["entries"].([]any)) != 2 || out["parent"] != "" {
		t.Fatalf("browse %d %v", st, out)
	}
	st, out = a.do("GET", "/api/transfers/browse?location=saved:a&path=../etc", "", nil)
	if st != 400 || out["error"] == nil {
		t.Fatalf("browse outside: %d %v", st, out)
	}
	st, _ = a.do("GET", "/api/transfers/browse?location=saved:a&path=missing", "", nil)
	if st != 404 {
		t.Fatalf("browse missing: %d", st)
	}

	// Host A to this computer: rsync pulls.
	tr := a.transfer("", map[string]any{"action": "copy", "source": map[string]any{"location": "saved:a", "paths": []string{"results"}},
		"destination": map[string]any{"location": "computer:Downloads", "path": ""}})
	if tr["state"] != StateSucceeded || tr["mover"] != MoverRsync {
		t.Fatalf("pull %v", tr)
	}
	dl := filepath.Join(r.home, "Downloads")
	if len(read(t, filepath.Join(dl, "results", "run1", "big.bin"))) != 500000 || read(t, filepath.Join(dl, "results", "it's $(id).txt")) != "odd name" {
		t.Fatal("pull landed wrong")
	}
	noStaging(t, dl)

	// Host A to host B: two hosts, studio-copy through the Studio.
	tr = a.transfer("", map[string]any{"action": "copy", "source": map[string]any{"location": "saved:a", "paths": []string{"results/run1"}},
		"destination": map[string]any{"location": "saved:b", "path": "."}})
	if tr["state"] != StateSucceeded || tr["mover"] != MoverCopy {
		t.Fatalf("host to host %v", tr)
	}
	if read(t, filepath.Join(r.hostB, "run1", "out.csv")) != "a,b\n1,2\n" {
		t.Fatal("host to host landed wrong")
	}
	files := tr["files"].(map[string]any)
	if files["done"].(float64) != 2 || files["total"].(float64) != 2 {
		t.Fatalf("progress %v", files)
	}

	// A move out of the main library to host B: rsync pushes, the Studio
	// checks sha256, then removes the originals and tells the index.
	tr = a.transfer("", map[string]any{"action": "move", "source": map[string]any{"location": "library:kb", "paths": []string{"papers"}},
		"destination": map[string]any{"location": "saved:b", "path": ""}})
	if tr["state"] != StateSucceeded || tr["mover"] != MoverRsync {
		t.Fatalf("move %v", tr)
	}
	if read(t, filepath.Join(r.hostB, "papers", "p2.md")) != "# paper two" {
		t.Fatal("move landed wrong")
	}
	gone(t, filepath.Join(r.kb, "papers"))
	if strings.Join(r.ix.removed, ",") != "kb:papers/p1.md,kb:papers/p2.md" {
		t.Fatalf("index removed %v", r.ix.removed)
	}

	// A sync back with deleteExtra into the library: the landing re-indexes.
	write(t, filepath.Join(r.kb, "papers", "stale.md"), "stale")
	tr = a.transfer("", map[string]any{"action": "sync", "source": map[string]any{"location": "saved:b", "paths": []string{"papers"}},
		"destination": map[string]any{"location": "library:kb", "path": "papers"}, "options": map[string]any{"deleteExtra": true, "mover": "studio-copy"}})
	plan := tr["plan"].(map[string]any)
	if tr["state"] != StateSucceeded || tr["deleted"].(float64) != 1 || plan["deletesCount"].(float64) != 1 {
		t.Fatalf("sync %v", tr)
	}
	gone(t, filepath.Join(r.kb, "papers", "stale.md"))
	if read(t, filepath.Join(r.kb, "papers", "p1.md")) != "# paper one" || len(r.ix.landed) == 0 {
		t.Fatalf("sync landed wrong; index %v", r.ix.landed)
	}

	// The list: newest first, active empty now, since filters the done.
	_, out = a.do("GET", "/api/transfers?state=active", "", nil)
	if len(out["transfers"].([]any)) != 0 {
		t.Fatalf("active %v", out)
	}
	_, out = a.do("GET", "/api/transfers?state=done&limit=2", "", nil)
	list := out["transfers"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["action"] != "sync" || list[0].(map[string]any)["events"] != nil {
		t.Fatalf("done %v", list)
	}
	_, out = a.do("GET", "/api/transfers?state=all&since="+time.Now().Add(time.Hour).UTC().Format(time.RFC3339), "", nil)
	if len(out["transfers"].([]any)) != 0 {
		t.Fatalf("since %v", out)
	}
	st, out = a.do("GET", "/api/transfers/movers?source=library:kb&destination=saved:b", "", nil)
	if st != 200 || len(out["movers"].([]any)) != 4 {
		t.Fatalf("movers %d %v", st, out)
	}
	// Another user sees none of it.
	_, out = a.do("GET", "/api/transfers?state=all", "bob", nil)
	if len(out["transfers"].([]any)) != 0 {
		t.Fatalf("bob sees %v", out)
	}
	id := list[0].(map[string]any)["id"].(string)
	if st, _ := a.do("GET", "/api/transfers/"+id, "bob", nil); st != 404 {
		t.Fatalf("bob read it: %d", st)
	}
	if st, _ := a.do("DELETE", "/api/transfers/"+id, "", nil); st != 200 {
		t.Fatalf("delete %d", st)
	}
}

// slow is studio-copy that waits for a while before each file, so a test
// can cancel it part way.
type slow struct{ copyMover }

func (s *slow) Name() string { return "slow" }
func (s *slow) Start(ctx context.Context, j *MoveJob) (string, error) {
	return s.start(func(ctx context.Context, pj *procJob) error {
		for _, f := range j.Files {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
			if err := copyOne(ctx, j, f, pj); err != nil {
				return err
			}
			pj.files.Add(1)
		}
		return nil
	}), nil
}

func TestCancelLeavesNothingBehind(t *testing.T) {
	r := newRig(t)
	a := newAPI(t, r)
	for i := 0; i < 20; i++ {
		write(t, filepath.Join(r.kb, "many", "f"+itoa(i)+".txt"), "data")
	}
	r.s.movers = append(r.s.movers, &slow{})
	st, out := a.do("POST", "/api/transfers/plan", "", map[string]any{"action": "copy", "source": map[string]any{"location": "library:kb", "paths": []string{"many"}},
		"destination": map[string]any{"location": "saved:a", "path": ""}, "options": map[string]any{"mover": "slow"}})
	if st != 200 {
		t.Fatalf("plan %d %v", st, out)
	}
	_, out = a.do("POST", "/api/transfers", "", map[string]any{"planId": out["plan"].(map[string]any)["id"]})
	id := out["transfer"].(map[string]any)["id"].(string)
	for i := 0; i < 100; i++ {
		_, out = a.do("GET", "/api/transfers?state=active", "", nil)
		if l := out["transfers"].([]any); len(l) == 1 && l[0].(map[string]any)["files"].(map[string]any)["done"].(float64) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if st, out := a.do("POST", "/api/transfers/"+id+"/cancel", "bob", nil); st != 404 {
		t.Fatalf("bob cancelled: %d %v", st, out)
	}
	_, out = a.do("POST", "/api/transfers/"+id+"/cancel", "", nil)
	if out["cancelled"] != true {
		t.Fatalf("cancel %v", out)
	}
	tr := r.wait(single, id)
	if tr.State != StateCancelled {
		t.Fatalf("%+v", tr)
	}
	gone(t, filepath.Join(r.hostA, "many"))
	noStaging(t, r.hostA)
	if _, out := a.do("POST", "/api/transfers/"+id+"/cancel", "", nil); out["cancelled"] != false || out["why"] == nil {
		t.Fatalf("cancel again %v", out)
	}
	es, _ := os.ReadDir(r.hostA)
	if len(es) != 0 {
		t.Fatalf("left in host A: %v", es)
	}
}

func TestListAndSummaryRoutes(t *testing.T) {
	r := newRig(t)
	history(t, filepath.Join(r.dir, "ix2"), 120, time.Now())
	r.s.store = openStore(filepath.Join(r.dir, "ix2"), quiet)
	a := newAPI(t, r)
	seen := map[string]bool{}
	path := "/api/transfers?state=done&limit=50"
	pages := 0
	for {
		st, out := a.do("GET", path, "", nil)
		if st != 200 {
			t.Fatalf("%d %v", st, out)
		}
		for _, x := range out["transfers"].([]any) {
			seen[x.(map[string]any)["id"].(string)] = true
		}
		pages++
		if out["next"] == nil {
			break
		}
		path = "/api/transfers?state=done&limit=50&before=" + out["next"].(string)
	}
	if pages != 3 || len(seen) != 120-18 {
		t.Fatalf("%d pages, %d transfers", pages, len(seen))
	}
	_, out := a.do("GET", "/api/transfers?q=RUN%2011&outcome=succeeded&startedBy=user", "", nil)
	for _, x := range out["transfers"].([]any) {
		m := x.(map[string]any)
		if !strings.Contains(m["label"].(string), "run 11") || m["state"] != "succeeded" || m["startedBy"] != "user" {
			t.Fatalf("filter let through %v", m)
		}
	}
	for _, bad := range []string{"limit=0", "limit=x", "outcome=maybe", "startedBy=robot", "state=some", "since=yesterday", "before=zzz"} {
		if st, out := a.do("GET", "/api/transfers?"+bad, "", nil); st != 400 || out["error"] == nil {
			t.Fatalf("%s: %d %v", bad, st, out)
		}
	}
	st, out := a.do("GET", "/api/transfers/summary", "", nil)
	if st != 200 || out["active"].(float64) != 0 || out["awaitingApproval"].(float64) != 0 || out["failedLast24h"].(float64) != 20 {
		t.Fatalf("summary %d %v", st, out)
	}
	if st, out := a.do("GET", "/api/transfers/summary", "bob", nil); st != 200 || out["failedLast24h"].(float64) != 4 {
		t.Fatalf("bob summary %v", out)
	}
}
