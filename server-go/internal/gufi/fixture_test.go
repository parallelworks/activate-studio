package gufi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
)

// The fixture test runs the Go walk and query plans on an index built
// from testdata/tree with the GUFI on this host (STUDIO_GUFI_BIN, default
// /opt/gufi/bin; skipped without it) and compares the answers with
// testdata/walk.golden.json, which the Node server gave on the same index
// (scripts/golden/gufi-walk.mjs). With STUDIO_GOLDEN_NODE set it asks
// Node again and compares with that too.

func gufiBin(t *testing.T) string {
	t.Helper()
	bin := os.Getenv("STUDIO_GUFI_BIN")
	if bin == "" {
		bin = "/opt/gufi/bin"
	}
	for _, tool := range []string{"gufi_dir2index", ToolQuery, ToolSQLite3} {
		if _, err := os.Stat(filepath.Join(bin, tool)); err != nil {
			t.Skipf("no %s in %s", tool, bin)
		}
	}
	return bin
}

// fixtureEpoch is the fixture files' first mtime; each next file in path
// order is an hour later, so newest and oldest have one answer.
var fixtureEpoch = time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC)

// buildFixture copies testdata/tree to base/kb, indexes it into
// base/ix/gufi/kb, and adds a words table with each file's text, as the
// enrich step does. It returns the index root.
func buildFixture(t testing.TB, bin, base string) string {
	t.Helper()
	kb := filepath.Join(base, "kb")
	var files, dirs []string
	err := filepath.WalkDir("testdata/tree", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel("testdata/tree", p)
		dst := filepath.Join(kb, rel)
		if d.IsDir() {
			dirs = append(dirs, dst)
			return os.MkdirAll(dst, 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files = append(files, dst)
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	for i, f := range files {
		at := fixtureEpoch.Add(time.Duration(i) * time.Hour)
		os.Chtimes(f, at, at)
	}
	for _, d := range dirs {
		os.Chtimes(d, fixtureEpoch, fixtureEpoch)
	}
	ix := filepath.Join(base, "ix", "gufi")
	os.MkdirAll(ix, 0o755)
	if out, err := exec.Command(filepath.Join(bin, "gufi_dir2index"), kb, ix).CombinedOutput(); err != nil {
		t.Fatalf("gufi_dir2index: %v %s", err, out)
	}
	root := filepath.Join(ix, "kb")
	local := NewLocal(bin)
	ctx := context.Background()
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.Name() != "db.db" {
			return err
		}
		res, err := local.Exec(ctx, Command{Tool: ToolSQLite3, Args: []string{"-d", "x", p, "SELECT inode, name FROM entries WHERE type = 'f';"}})
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, filepath.Dir(p))
		sql := []string{"CREATE VIRTUAL TABLE words USING fts5(tinode UNINDEXED, fname UNINDEXED, wordf);"}
		for _, r := range ParseRows(string(res.Stdout), 2) {
			text, err := os.ReadFile(filepath.Join(kb, rel, r[1]))
			if err != nil || strings.HasSuffix(r[1], ".bin") {
				continue
			}
			sql = append(sql, fmt.Sprintf("INSERT INTO words VALUES (%s, %s, %s);", q(r[0]), q(r[1]), q(string(text))))
		}
		// One statement an argument: gufi_sqlite3 reads its input a line at
		// a time, and the text has newlines.
		res, err = local.Exec(ctx, Command{Tool: ToolSQLite3, Args: append([]string{p}, sql...)})
		if err == nil && res.Stderr != "" {
			err = errors.New(res.Stderr)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

type walkCase struct {
	Q      string         `json:"q"`
	Lim    WalkLimits     `json:"lim"`
	Refine *RefineSpec    `json:"refine"`
	Caps   map[string]int `json:"caps"`
	Limit  int            `json:"limit"`
}

type walkCases struct {
	Walks []walkCase        `json:"walks"`
	Plans []json.RawMessage `json:"plans"`
}

type answers struct {
	Walks []json.RawMessage `json:"walks"`
	Plans []json.RawMessage `json:"plans"`
}

// planAnswer is a plan's answer as the script records it; an unordered
// (raw) answer's rows are sorted so runs compare.
func planAnswer(res QueryResult, raw bool, err error) any {
	if err != nil {
		var e *apierr.Error
		if errors.As(err, &e) {
			return map[string]any{"error": e.Message, "status": e.Status}
		}
		return map[string]any{"error": err.Error(), "status": nil}
	}
	if raw {
		sortRows(res.Rows)
	}
	return map[string]any{"columns": res.Columns, "rows": res.Rows, "truncated": res.Truncated}
}

func sortRows(rows [][]string) {
	sort.Slice(rows, func(a, b int) bool { return strings.Join(rows[a], "\x00") < strings.Join(rows[b], "\x00") })
}

// sortedRawPlans sorts the rows of the raw plans' answers in a Node answer.
func sortedRawPlans(t *testing.T, c walkCases, a answers) {
	for i, body := range c.Plans {
		if i >= len(a.Plans) || !strings.Contains(string(body), `"sql"`) {
			continue
		}
		var v struct {
			Columns   []string   `json:"columns"`
			Rows      [][]string `json:"rows"`
			Truncated bool       `json:"truncated"`
		}
		if json.Unmarshal(a.Plans[i], &v) == nil && v.Columns != nil {
			sortRows(v.Rows)
			a.Plans[i], _ = json.Marshal(v)
		}
	}
}

func TestFixtureWalkAndPlans(t *testing.T) {
	bin := gufiBin(t)
	base := t.TempDir()
	root := buildFixture(t, bin, base)
	c := load[walkCases](t, "testdata/walk-cases.json")
	want := load[answers](t, "testdata/walk.golden.json")
	sortedRawPlans(t, c, want)

	runner := NewRunner(NewLocal(bin), nil, RunnerOptions{Concurrency: 2, Threads: 2})
	ctx := context.Background()
	got := answers{}
	for _, w := range c.Walks {
		stages := OnePassStages(w.Q, w.Lim, []string{root}, nil, w.Refine)
		args := append(append([]string{"-n", "2", "-d", Delim, "-a", "1", "--no-print-sql-on-err"}, stages...), root)
		res, err := runner.Run(ctx, Command{Tool: ToolQuery, Args: args})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(FuseRows(WalkRows(string(res.Stdout), []string{root}, []string{root}), w.Caps, w.Limit))
		got.Walks = append(got.Walks, b)
	}
	p := &Planner{IndexRoot: root}
	for _, body := range c.Plans {
		plan, err := p.PlanFor(body)
		var res QueryResult
		if err == nil {
			res, err = p.Query(ctx, runner, plan)
		}
		b, _ := json.Marshal(planAnswer(res, plan.Raw, err))
		// The index's own directory is printed whole (as Node prints it).
		got.Plans = append(got.Plans, []byte(strings.ReplaceAll(string(b), root, "<index>")))
	}
	compare := func(label string, want answers) {
		for i := range c.Walks {
			same(t, fmt.Sprintf("%s walk %q", label, c.Walks[i].Q), json.RawMessage(got.Walks[i]), want.Walks[i])
		}
		for i := range c.Plans {
			same(t, fmt.Sprintf("%s plan %s", label, c.Plans[i]), json.RawMessage(got.Plans[i]), want.Plans[i])
		}
	}
	writing := os.Getenv("STUDIO_GOLDEN_WRITE") == "1"
	if !writing {
		compare("committed", want)
	}
	if s := runner.Stats(); s.Runs != int64(len(c.Walks)+len(c.Plans)) || s.Running != 0 || s.Queued != 0 {
		t.Errorf("runner stats %+v", s)
	}

	node := os.Getenv("STUDIO_GOLDEN_NODE")
	if node == "" {
		return
	}
	cmd := exec.Command(node, "../../scripts/golden/gufi-walk.mjs", "testdata/walk-cases.json", "../../..")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "INDEX_BASE=" + filepath.Join(base, "ix"), "KB_ROOT=" + filepath.Join(base, "kb"), "GUFI_BIN=" + bin}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	out = []byte(strings.ReplaceAll(string(out), root, "<index>"))
	if writing {
		os.WriteFile("testdata/walk.golden.json", out, 0o644)
	}
	var fresh answers
	if err := json.Unmarshal(out, &fresh); err != nil {
		t.Fatal(err)
	}
	sortedRawPlans(t, c, fresh)
	compare("node", fresh)
}
