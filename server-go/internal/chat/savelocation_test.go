package chat

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
)

// hereRunner runs a host's commands on this computer, so a saved location
// on a stand-in host can be tested.
type hereRunner struct{}

func (hereRunner) RunOnce(ctx context.Context, site, key string, argv []string, timeout time.Duration, input io.Reader) (remote.Result, error) {
	cmd := exec.CommandContext(ctx, "bash", "-c", remote.DirectCommand(argv))
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if input != nil {
		cmd.Stdin = input
	}
	code := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			return remote.Result{}, err
		}
		code = ee.ExitCode()
	}
	return remote.Result{Code: code, Stdout: out.String(), Raw: out.Bytes(), Stderr: errb.String()}, nil
}

func (hereRunner) Command(site, key string, argv []string) (transfers.Cmd, error) {
	return transfers.ExecCmd(exec.Command("bash", "-c", remote.DirectCommand(argv))), nil
}

// saveWorld is a transfer world whose service reaches stand-in hosts, with
// its settings in memory, an index-only library to suggest a location
// for, and a folder on a "host".
type saveWorld struct {
	*transferWorld
	set    *transfers.MemorySettings
	folder string
}

func newSaveWorld(t *testing.T) *saveWorld {
	w := newTransferWorld(t)
	dir := t.TempDir()
	folder := filepath.Join(dir, "archive")
	os.MkdirAll(folder, 0o755)
	site, _ := remote.SSHSite("user@files.example.com", 0, nil)
	set := &transfers.MemorySettings{S: transfers.DefaultSettings()}
	w.s = transfers.New(transfers.Deps{
		IndexBase: filepath.Join(dir, "ix-transfers"),
		Getenv:    func(string) string { return "" },
		Runner:    hereRunner{},
		Libraries: func(context.Context, transfers.Viewer) []transfers.LibraryLoc {
			return []transfers.LibraryLoc{
				{ID: "main", Label: "Knowledge base", Writable: true, FS: &transfers.LocalFS{Dir: w.kb}},
				{ID: "archive", Label: "Archive", Why: remote.NoShell(site), Site: site, Folder: folder},
			}
		},
		Clusters: func(context.Context, transfers.Viewer) ([]string, error) { return nil, nil },
		Settings: set,
		Home:     filepath.Join(dir, "home"),
	})
	return &saveWorld{transferWorld: w, set: set, folder: folder}
}

func TestSaveLocationSuggestedInLocations(t *testing.T) {
	w := newSaveWorld(t)
	conv := w.conv
	out, _ := w.m.executeInGo(w.r, "transfer_locations", `{}`, ToolCtx{ConversationID: &conv})
	want := "to save a location for its files (save_transfer_location, if the user has ssh access): label Archive files, host user@files.example.com, folder " + w.folder
	if out.Failed || !strings.Contains(out.Result, want) {
		t.Fatalf("locations %s", out.Result)
	}
}

func TestSaveLocationWaitsForApproval(t *testing.T) {
	w := newSaveWorld(t)
	args := `{"label":"Archive files","host":"user@files.example.com","folder":"` + w.folder + `"}`
	card, problem := w.hold("save_transfer_location", args, "srv-1", "call_1")
	if problem != "" {
		t.Fatal(problem)
	}
	loc, _ := card["savedLocation"].(map[string]any)
	if card["header"] != "Save the Transfers location Archive files?" || card["toolName"] != "save_transfer_location" || card["approvalKind"] != "confirm" ||
		card["canAllowDirectory"] != false || loc["label"] != "Archive files" || loc["host"] != "user@files.example.com" || loc["root"] != w.folder {
		t.Fatalf("card %v", card)
	}
	// Held: nothing saved, and the tool does not run outside a held call.
	if n := len(w.set.S.SavedLocations); n != 0 {
		t.Fatalf("saved before approval: %d", n)
	}
	conv := w.conv
	if out, _ := w.m.executeInGo(w.r, "save_transfer_location", args, ToolCtx{ConversationID: &conv}); !out.Failed || !strings.Contains(out.Result, "Nothing ran") {
		t.Fatalf("unheld %+v", out)
	}
	if n := len(w.set.S.SavedLocations); n != 0 {
		t.Fatalf("saved unheld: %d", n)
	}

	got := w.answer("srv-1", "call_1", true)
	if !got.OK || got.Failed || got.Summary != "saved saved:archive-files" ||
		!strings.Contains(got.Result, "Saved the Transfers location Archive files as saved:archive-files") || !strings.Contains(got.Result, "Studio can write there") {
		t.Fatalf("approved %+v", got)
	}
	if s := w.set.S.SavedLocations; len(s) != 1 || s[0].ID != "archive-files" || s[0].Host != "user@files.example.com" || s[0].Root != w.folder {
		t.Fatalf("saved %+v", s)
	}
	// Answered once only.
	if again := w.answer("srv-1", "call_1", true); again.OK {
		t.Fatalf("answered twice %+v", again)
	}
}

func TestSaveLocationDeclined(t *testing.T) {
	w := newSaveWorld(t)
	if _, problem := w.hold("save_transfer_location", `{"host":"user@files.example.com","folder":"`+w.folder+`"}`, "srv-1", "call_1"); problem != "" {
		t.Fatal(problem)
	}
	got := w.answer("srv-1", "call_1", false)
	if !got.OK || got.Summary != "declined" {
		t.Fatalf("declined %+v", got)
	}
	if n := len(w.set.S.SavedLocations); n != 0 {
		t.Fatalf("saved after a decline: %d", n)
	}
}

func TestSaveLocationTestFails(t *testing.T) {
	w := newSaveWorld(t)
	if _, problem := w.hold("save_transfer_location", `{"host":"user@files.example.com","folder":"/no/such/folder"}`, "srv-1", "call_1"); problem != "" {
		t.Fatal(problem)
	}
	got := w.answer("srv-1", "call_1", true)
	if !got.OK || !got.Failed || got.Summary != "test failed" || !strings.Contains(got.Result, "nothing was saved") {
		t.Fatalf("failed test %+v", got)
	}
	if n := len(w.set.S.SavedLocations); n != 0 {
		t.Fatalf("saved after a failed test: %d", n)
	}
}

func TestSaveLocationRefusals(t *testing.T) {
	w := newSaveWorld(t)
	for args, want := range map[string]string{
		`{"host":"user@files.example.com"}`:                        "host and folder are required",
		`{"host":"user@files.example.com","folder":"relative"}`:    "root:",
		`{"host":"-oProxyCommand=x","folder":"/srv"}`:              "host:",
		`{"host":"h","folder":"/srv","port":"22"}`:                 "port must be",
		`{"host":"h","folder":"/srv","rsyncPath":"rsync; rm -rf"}`: "rsyncPath:",
	} {
		if _, problem := w.hold("save_transfer_location", args, "srv-1", "call_1"); !strings.Contains(problem, want) || !strings.Contains(problem, "nothing was saved") {
			t.Errorf("%s: %q", args, problem)
		}
	}

	// Sign-in on: refused with the reason, and not offered.
	w.m.xferViewer = func(*http.Request) (transfers.Viewer, error) {
		return transfers.Viewer{ID: "u1", AuthOn: true, Key: "k"}, nil
	}
	if _, problem := w.hold("save_transfer_location", `{"host":"h","folder":"/srv"}`, "srv-1", "call_1"); !strings.Contains(problem, "off with sign-in on") {
		t.Errorf("sign-in on %q", problem)
	}
	if n := len(w.set.S.SavedLocations); n != 0 {
		t.Fatalf("saved with sign-in on: %d", n)
	}
	offeredHere := func() bool {
		specs, err := w.tools.Specs(w.r, nil, false, false, true, []string{"transfers"})
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range specs.Available {
			if strings.Contains(string(s), `"name":"save_transfer_location"`) {
				return true
			}
		}
		return false
	}
	if !offeredHere() {
		t.Fatal("not offered with sign-in off")
	}
	w.m.A.Cfg.Auth.JWKSURL = "https://id.example.com/jwks"
	if offeredHere() {
		t.Fatal("offered with sign-in on")
	}
}
