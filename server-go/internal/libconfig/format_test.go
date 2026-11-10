package libconfig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

func sp(s string) *string { return &s }
func ip(n int) *int       { return &n }
func bp(b bool) *bool     { return &b }
func i64(n int64) *int64  { return &n }

// source is a Studio with one library of every kind, each carrying the
// state an export must leave out.
func source() Source {
	return Source{
		Libraries: []libraries.Library{
			{ID: "kb", Label: "Main", IndexRoot: "/idx/main", SourceRoot: "/data/main", Primary: true, Depth: "text"},
			{ID: "projects", Label: "Projects", IndexRoot: "/home/admin/.local/idx/projects/projects", SourceRoot: "/srv/projects", Depth: "ocr",
				Build: &libraries.LocalBuild{IndexDir: "/home/admin/.local/idx/projects", Managed: true, State: "ready", BuiltAt: sp("2026-11-01T00:00:00.000Z"), Files: i64(41)}},
			{ID: "archive", Label: "Archive", IndexRoot: "/fast/idx/archive/archive", SourceRoot: "/srv/archive", Depth: "names",
				Build: &libraries.LocalBuild{IndexDir: "/fast/idx/archive", Managed: false, State: "failed", Error: sp("disk full at 03:00")}},
			{ID: "shared", Label: "Shared drive", IndexRoot: "/mnt/gufi/shared", Pinned: true,
				ReadThrough: &libraries.ReadThrough{Destination: "admin@files.example", Port: ip(2222), SSHOptions: []string{"-i", "/home/admin/.ssh/id_admin", "-o", "ServerAliveInterval=30"}, Root: sp("/export/shared")}},
		},
		Users: []libraries.UserDef{
			{ID: "box-index", Label: "Index server", Transport: "ssh", Site: "admin@index.example", Destination: sp("admin@index.example"), Port: ip(2200),
				SSHOptions: []string{"-J", "admin@jump.example", "-l", "admin", "-i", "/home/admin/.ssh/key", "-o", "IdentityFile=/home/admin/.ssh/k2", "-o", "User=admin", "-C"},
				IndexRoot:  sp("/gufi/index"), SourceRoot: sp("/gufi/src"), GufiBin: sp("/opt/gufi/bin"), State: "ready",
				ReadThrough: &libraries.ReadThrough{Destination: "admin@files.example", SSHOptions: []string{"-i", "/home/admin/.ssh/k3"}, Root: sp("/files")},
				Directories: i64(7), Files: i64(99), BuiltAt: sp("2026-10-30T10:00:00.000Z"), GufiVersion: sp("0.8.0"), Error: sp("secret-ish error text")},
			{ID: "box-data", Label: "Data on box", Transport: "ssh", Site: "admin@box.example", Destination: sp("admin@box.example"), State: "building",
				Build: &libraries.RemoteBuild{ID: "b-123456", IndexDir: "/home/admin/.activate-studio/indexes/data-1a2b3c4d", SourceRoot: "/home/admin/data", GufiBin: "/home/admin/.activate-studio/gufi/bin"}},
			{ID: "c1-index", Label: "Cluster index", Transport: "pw", Site: "c1", Existing: bp(true), IndexRoot: sp("/scratch/gufi"), GufiBin: sp("/apps/gufi/bin"), State: "ready"},
			{ID: "c1-built", Label: "Cluster folder", Transport: "pw", Site: "c1", Existing: bp(false), State: "ready",
				Build: &libraries.RemoteBuild{ID: "b-9", IndexDir: "/scratch/my-index", SourceRoot: "~/work", GufiBin: "/apps/gufi/bin"}},
			{ID: "wf", Label: "Workflow built", Transport: "pw", Site: "c1", Resource: "pw://someone/c1", IndexRoot: sp("/x/idx"), State: "ready",
				Run: &libraries.WorkflowRun{Slug: "run-77", Mode: "build", StartedAt: "2026-10-01T00:00:00.000Z"}},
		},
		Depth: func(id string) string {
			return map[string]string{"box-data": "meaning-full", "c1-built": "text"}[id]
		},
	}
}

func export(t *testing.T, o Options) (string, File, []Skipped) {
	t.Helper()
	o.Now = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	o.StudioVersion = "v9.9"
	f, skipped := Export(source(), o)
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b), f, skipped
}

func TestExportLeavesOutStateAndSecrets(t *testing.T) {
	text, f, skipped := export(t, Options{})
	for _, bad := range []string{
		`"id"`, "box-index", "c1-built", "b-123456", "run-77", "2026-11-01", "2026-10-30", "disk full", "secret-ish", "0.8.0",
		"pw://", `"state"`, `"files"`, ".ssh", "IdentityFile", "User=", `"-l"`, "admin@", ".activate-studio", "/data/main",
		`"build": {`, `"run"`, `"existing"`, `"transport"`,
	} {
		if strings.Contains(text, bad) {
			t.Errorf("export has %q:\n%s", bad, text)
		}
	}
	if f.Format != FormatName || f.Version != 1 || f.ExportedAt != "2026-11-10T12:00:00.000Z" || f.StudioVersion != "v9.9" {
		t.Errorf("header: %+v", f)
	}
	want := []Entry{
		{Kind: "folder", Label: "Projects", SourceRoot: "/srv/projects", Depth: "ocr"},
		{Kind: "folder", Label: "Archive", SourceRoot: "/srv/archive", IndexDir: "/fast/idx/archive", Depth: "names"},
		{Kind: "index", Label: "Shared drive", IndexRoot: "/mnt/gufi/shared",
			ReadThrough: &ReadThrough{Destination: "files.example", Port: 2222, SSHOptions: []string{"-o", "ServerAliveInterval=30"}, Root: "/export/shared"}},
		{Kind: "ssh", Label: "Index server", Mode: "connect", Destination: "index.example", Port: 2200, SSHOptions: []string{"-J", "jump.example", "-C"},
			IndexRoot: "/gufi/index", SourceRoot: "/gufi/src", GufiBin: "/opt/gufi/bin", ReadThrough: &ReadThrough{Destination: "files.example", Root: "/files"}},
		{Kind: "ssh", Label: "Data on box", Mode: "build", Destination: "box.example", SourceRoot: "/home/admin/data", Depth: "meaning-full"},
		{Kind: "pw", Label: "Cluster index", Mode: "connect", Cluster: "c1", IndexRoot: "/scratch/gufi", GufiBin: "/apps/gufi/bin"},
		{Kind: "pw", Label: "Cluster folder", Mode: "build", Cluster: "c1", SourceRoot: "~/work", IndexDir: "/scratch/my-index", GufiBin: "/apps/gufi/bin", Depth: "text"},
	}
	if !reflect.DeepEqual(f.Libraries, want) {
		got, _ := json.MarshalIndent(f.Libraries, "", "  ")
		t.Errorf("entries:\n%s", got)
	}
	if len(skipped) != 1 || skipped[0].ID != "wf" || !strings.Contains(skipped[0].Why, "workflow") {
		t.Errorf("skipped: %+v", skipped)
	}
}

func TestExportKeepsUserWhenAsked(t *testing.T) {
	text, f, _ := export(t, Options{KeepUser: true, IDs: []string{"box-index", "shared"}})
	if len(f.Libraries) != 2 {
		t.Fatalf("want the two chosen: %s", text)
	}
	if f.Libraries[1].Destination != "admin@index.example" || f.Libraries[0].ReadThrough.Destination != "admin@files.example" {
		t.Errorf("user names: %s", text)
	}
	// The login and jump host keep their user; key files never go.
	if got := strings.Join(f.Libraries[1].SSHOptions, " "); got != "-J admin@jump.example -l admin -o User=admin -C" {
		t.Errorf("options: %q", got)
	}
}

func TestExportPrimaryOnlyWhenTicked(t *testing.T) {
	_, f, _ := export(t, Options{IDs: []string{"kb", "projects"}})
	if len(f.Libraries) != 1 || f.Libraries[0].Label != "Projects" {
		t.Errorf("the main folder came without Primary: %+v", f.Libraries)
	}
	_, f, _ = export(t, Options{Primary: true, IDs: []string{"kb"}})
	if len(f.Libraries) != 1 || !reflect.DeepEqual(f.Libraries[0], Entry{Kind: "folder", Label: "Main", SourceRoot: "/data/main", Depth: "text"}) {
		t.Errorf("main folder: %+v", f.Libraries)
	}
}

func TestExportThenParseRoundTrips(t *testing.T) {
	text, f, _ := export(t, Options{})
	h, parsed, err := Parse([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if h.Version != 1 || h.StudioVersion != "v9.9" {
		t.Errorf("header %+v", h)
	}
	for i, p := range parsed {
		if p.Error != "" {
			t.Errorf("entry %d: %s", i, p.Error)
			continue
		}
		if !reflect.DeepEqual(*p.Entry, f.Libraries[i]) {
			t.Errorf("entry %d: %+v, want %+v", i, *p.Entry, f.Libraries[i])
		}
	}
}

func file(entries ...string) []byte {
	return []byte(`{"format":"activate-studio-libraries","version":1,"libraries":[` + strings.Join(entries, ",") + `]}`)
}

func TestParseRefusesWhatIsNotAList(t *testing.T) {
	for _, in := range []string{`not json`, `[]`, `{"format":"other","version":1,"libraries":[]}`, `{"format":"activate-studio-libraries","libraries":[]}`,
		`{"format":"activate-studio-libraries","version":1}`, `{"format":"activate-studio-libraries","version":1.5,"libraries":[]}`} {
		if _, _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: no error", in)
		}
	}
}

func TestParseReportsBadEntriesOneByOne(t *testing.T) {
	_, parsed, err := Parse(file(
		`{"kind":"index","indexRoot":"/idx/a","label":"A","id":"ignored","state":"ready","extra":{"x":1}}`,
		`{"kind":"vault","label":"B"}`,
		`{"label":"no kind"}`,
		`{"kind":"index","indexRoot":"relative/path"}`,
		`{"kind":"folder","sourceRoot":"/a/../etc"}`,
		`{"kind":"ssh","destination":"bad host!","indexRoot":"/i"}`,
		`{"kind":"ssh","destination":"host","indexRoot":"/i","sshOptions":"-o ProxyCommand=nc"}`,
		`{"kind":"ssh","destination":"me@host:2222","indexRoot":"~/idx/"}`,
		`{"kind":"pw","cluster":"-oops","indexRoot":"/i"}`,
		`{"kind":"pw","cluster":"c1","mode":"sideways","indexRoot":"/i"}`,
		`{"kind":"folder","sourceRoot":"/src","depth":"deep"}`,
		`"just a string"`,
		`{"kind":"ssh","destination":"h","mode":"build"}`,
		`{"kind":"ssh","destination":"h","port":70000,"indexRoot":"/i"}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	ok := map[int]bool{0: true, 7: true}
	for i, p := range parsed {
		if ok[i] != (p.Error == "") {
			t.Errorf("entry %d: error %q", i, p.Error)
		}
	}
	if e := parsed[0].Entry; !reflect.DeepEqual(*e, Entry{Kind: "index", Label: "A", IndexRoot: "/idx/a"}) {
		t.Errorf("unknown keys kept: %+v", e)
	}
	if e := parsed[7].Entry; e.Destination != "me@host" || e.Port != 2222 || e.IndexRoot != "~/idx" || e.Mode != "connect" || e.Label != "idx" {
		t.Errorf("ssh entry: %+v", e)
	}
	if !strings.Contains(parsed[1].Error, `"vault"`) || !strings.Contains(parsed[6].Error, "ProxyCommand") {
		t.Errorf("reasons: %q, %q", parsed[1].Error, parsed[6].Error)
	}
}

func TestParseNewerVersionPerEntry(t *testing.T) {
	_, parsed, err := Parse([]byte(`{"format":"activate-studio-libraries","version":2,"libraries":[{"kind":"index","indexRoot":"/a","label":"A"},{"kind":"hologram"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 {
		t.Fatalf("%+v", parsed)
	}
	for _, p := range parsed {
		if !strings.Contains(p.Error, "version 2") {
			t.Errorf("error %q", p.Error)
		}
	}
	if parsed[0].Entry.Label != "A" {
		t.Errorf("label for the preview: %+v", parsed[0].Entry)
	}
}

func TestCheckMarksPresentNewAndUnavailable(t *testing.T) {
	src := source()
	have := Existing{Libraries: src.Libraries, Users: src.Users}
	_, parsed, err := Parse(file(
		`{"kind":"index","indexRoot":"/mnt/gufi/shared/"}`,                                           // 0 present
		`{"kind":"index","indexRoot":"/mnt/gufi/other"}`,                                             // 1 new
		`{"kind":"folder","sourceRoot":"/srv/projects"}`,                                             // 2 present (built here)
		`{"kind":"folder","sourceRoot":"/data/main"}`,                                                // 3 present (main folder)
		`{"kind":"ssh","destination":"someone@index.example","port":2200,"indexRoot":"/gufi/index"}`, // 4 present, other user
		`{"kind":"ssh","destination":"index.example","indexRoot":"/gufi/index"}`,                     // 5 new: other port
		`{"kind":"ssh","destination":"box.example","mode":"build","sourceRoot":"~/data"}`,            // 6 present: ~ resolved there
		`{"kind":"pw","cluster":"c1","indexRoot":"/scratch/gufi"}`,                                   // 7 present
		`{"kind":"pw","cluster":"c2","indexRoot":"/scratch/gufi"}`,                                   // 8 new
		`{"kind":"index","indexRoot":"/mnt/gufi/other"}`,                                             // 9 same as 1
		`{"kind":"ssh"}`, // 10 invalid
	))
	if err != nil {
		t.Fatal(err)
	}
	got := Check(parsed, have, Offer{Build: true, SSH: true}, "/home/me")
	want := []string{"present", "new", "present", "present", "present", "new", "present", "present", "new", "present", "invalid"}
	for i, c := range got {
		if c.State != want[i] {
			t.Errorf("entry %d: %s (%s), want %s", i, c.State, c.Why, want[i])
		}
	}
	if got[0].Why != "Shared drive" || got[4].Why != "Index server" || !strings.Contains(got[9].Why, "entry 2") {
		t.Errorf("reasons: %q %q %q", got[0].Why, got[4].Why, got[9].Why)
	}
	if got[4].User == nil || *got[4].User != "someone" || got[4].Host != "index.example" || got[5].User == nil || *got[5].User != "" {
		t.Errorf("user split: %+v %+v", got[4], got[5])
	}
	if got[4].Where != "someone@index.example port 2200:/gufi/index" {
		t.Errorf("where: %q", got[4].Where)
	}

	// With sign-in: nothing on this computer, no ssh; pw as the viewer's key allows.
	got = Check(parsed, have, Offer{Auth: true, PwWhy: "Add your key."}, "/home/me")
	for _, i := range []int{1, 2, 5} {
		if got[i].State != "unavailable" {
			t.Errorf("signed in, entry %d: %s", i, got[i].State)
		}
	}
	if got[8].State != "unavailable" || got[8].Why != "Add your key." {
		t.Errorf("pw: %+v", got[8])
	}
	if got[5].Why != libraries.SSHSingleUser {
		t.Errorf("ssh reason: %q", got[5].Why)
	}
}

func TestCleanOptions(t *testing.T) {
	cases := []struct {
		in    []string
		strip bool
		want  string
	}{
		{[]string{"-iKEY", "-FCONF", "-p", "22"}, false, "-p 22"},
		{[]string{"-ladmin", "-Jme@a,you@b:22", "-oUser=x", "-o", "ProxyJump=me@j"}, true, "-J a,b:22 -o ProxyJump=j"},
		{[]string{"-o", "identityfile=/k", "-o", "CertificateFile=/c", "-4"}, false, "-4"},
	}
	for _, c := range cases {
		if got := strings.Join(CleanOptions(c.in, c.strip), " "); got != c.want {
			t.Errorf("%v: %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFileName(t *testing.T) {
	for in, want := range map[string]string{"Shared drive": "shared-drive.studio-libraries.json", "": "libraries.studio-libraries.json", "  ??": "libraries.studio-libraries.json"} {
		if got := FileName(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
