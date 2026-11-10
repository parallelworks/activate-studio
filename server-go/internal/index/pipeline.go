package index

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/embed"
	"github.com/parallelworks/activate-studio/server-go/internal/gufi"
	"github.com/parallelworks/activate-studio/server-go/internal/posixaccess"
)

// ---- running the tools ----

const watchdog = 30 * time.Minute

var (
	alreadyExists = regexp.MustCompile(`Already exists!\s*$`)
	warnLine      = regexp.MustCompile(`\bfailed\b|^preextract: .*: |embed error`)
)

// tool is one program a job runs.
type tool struct {
	name     string // for the log
	cmd      string
	args     []string
	env      []string
	onDenied func(paths []string)
	onErr    func(line string)
}

// child is the command line of a helper this binary runs as a child:
// `<this binary> <sub>`, or the override for tests (a program taking the
// subcommand's flags itself).
func child(sub, override string) (string, []string, error) {
	if v := os.Getenv(override); v != "" {
		return v, nil, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", nil, err
	}
	return exe, []string{sub}, nil
}

// runTool is indexing.ts's runTool: the program's @progress lines move the
// phase, every other line goes to the index log, and lines about a single
// file that failed are counted as the job's warnings. It runs in a process
// group of its own, so stopping the job stops its workers too.
func (c *jobCtx) runTool(ph *phaseH, t tool) ([]string, error) {
	if c.aborted() {
		return nil, ErrCancelled
	}
	cmd := exec.Command(t.cmd, t.args...)
	cmd.Env = t.env
	ownGroup(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("%s: %s", t.name, err.Error())
	}
	var mu sync.Mutex
	var out, errTail, denied []string
	last := time.Now()
	var timedOut bool
	stopped := make(chan struct{})
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stopped:
				return
			case <-c.ctx.Done():
				killGroup(cmd, false)
				select {
				case <-stopped:
				case <-time.After(5 * time.Second):
					killGroup(cmd, true)
				}
				return
			case <-tick.C:
				mu.Lock()
				idle := time.Since(last) > watchdog
				if idle && !timedOut {
					timedOut = true
					errTail = append(errTail, fmt.Sprintf("no progress for %d minutes", int(watchdog.Minutes())))
				}
				mu.Unlock()
				if idle {
					c.log(fmt.Sprintf("%s: no progress for %d minutes; stopping it", t.name, int(watchdog.Minutes())))
					killGroup(cmd, false)
					go func() {
						select {
						case <-stopped:
						case <-time.After(5 * time.Second):
							killGroup(cmd, true)
						}
					}()
				}
			}
		}
	}()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for sc.Scan() {
			line := sc.Text()
			mu.Lock()
			last = time.Now()
			mu.Unlock()
			if strings.HasPrefix(line, "@progress ") {
				var p struct {
					Done  any     `json:"done"`
					Total any     `json:"total"`
					Step  *string `json:"step"`
				}
				if json.Unmarshal([]byte(line[10:]), &p) == nil && ph != nil {
					ph.progress(jsInt(p.Done), jsInt(p.Total), p.Step)
				}
				continue
			}
			if strings.TrimSpace(line) == "" || alreadyExists.MatchString(line) {
				continue
			}
			mu.Lock()
			out = append(out, line)
			mu.Unlock()
			if c.kind() == "build" {
				c.log(t.name + ": " + truncRunes(strings.TrimSpace(line), 500))
			}
		}
		io.Copy(io.Discard, stdout)
	}()
	go func() {
		defer wg.Done()
		sc := bufio.NewScanner(stderr)
		sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
		for sc.Scan() {
			tl := strings.TrimSpace(sc.Text())
			mu.Lock()
			last = time.Now()
			mu.Unlock()
			if tl == "" || alreadyExists.MatchString(tl) {
				continue
			}
			if t.onErr != nil {
				t.onErr(tl)
			}
			c.log(t.name + ": " + truncRunes(tl, 500))
			mu.Lock()
			if deniedLineRe.MatchString(tl) && len(denied) < 500 {
				denied = append(denied, tl)
			}
			errTail = append(errTail, tl)
			if len(errTail) > 20 {
				errTail = errTail[1:]
			}
			mu.Unlock()
			if warnLine.MatchString(tl) {
				c.with(func(j *IndexJob) {
					j.WarningCount++
					if len(j.Warnings) < 20 {
						j.Warnings = append(j.Warnings, truncRunes(strings.TrimLeft(tl, " \t"), 300))
					}
				})
			}
		}
		io.Copy(io.Discard, stderr)
	}()
	wg.Wait()
	werr := cmd.Wait()
	close(stopped)
	if c.aborted() {
		return nil, ErrCancelled
	}
	code := 0
	if werr != nil {
		var ee *exec.ExitError
		if errors.As(werr, &ee) {
			code = ee.ExitCode()
		} else {
			return nil, fmt.Errorf("%s: %s", t.name, werr.Error())
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if code != 0 && t.onDenied != nil && len(denied) > 0 && OnlyDenied(strings.Join(errTail, "\n")) {
		t.onDenied(DeniedPaths(strings.Join(denied, "\n")))
		return out, nil
	}
	if code != 0 {
		tail := errTail
		if len(tail) > 3 {
			tail = tail[len(tail)-3:]
		}
		msg := strings.Join(tail, " ")
		if msg == "" {
			msg = fmt.Sprintf("exit %d", code)
			if code < 0 {
				msg = "exit null"
			}
		}
		return nil, fmt.Errorf("%s: %s", t.name, msg)
	}
	return out, nil
}

func jsInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err == nil {
			return int(f)
		}
	}
	return 0
}

func (c *jobCtx) kind() string {
	c.ix.mu.Lock()
	defer c.ix.mu.Unlock()
	return c.rec.pub.Kind
}

func (ix *Indexer) ensureSkipFile() string {
	if !exists(ix.P.SkipFile) {
		os.MkdirAll(ix.P.IndexBase, 0o755)
		os.WriteFile(ix.P.SkipFile, []byte(strings.Join(append(append([]string{}, ExcludeDirs...), ".claude", ".github"), "\n")+"\n"), 0o644)
	}
	return ix.P.SkipFile
}

func (ix *Indexer) gufiEnv(extra map[string]string, noLib bool) []string {
	return gufi.EnvFor(ix.P.GufiBin, os.Environ(), extra, gufi.EnvOptions{NoLibraries: noLib})
}

// surveyDirs counts the folders indexing enters, and lists those whose
// owner may not write them.
func surveyDirs(root string, maxLevel0 bool) (int, []Locked) {
	skip := map[string]bool{".claude": true, ".github": true}
	for _, d := range ExcludeDirs {
		skip[d] = true
	}
	modes := lockingUser()
	var mu sync.Mutex
	n := 0
	var locked []Locked
	var walk func(dir, rel string, level int, wg *sync.WaitGroup)
	walk = func(dir, rel string, level int, wg *sync.WaitGroup) {
		defer wg.Done()
		mu.Lock()
		n++
		mu.Unlock()
		deeper := !maxLevel0 || level < 0
		var entries []os.DirEntry
		if deeper {
			var err error
			if entries, err = os.ReadDir(dir); err != nil {
				return
			}
		}
		if modes {
			if st, err := os.Stat(dir); err == nil {
				if l := lockedEntry(dir, rel, st.Mode(), entries, skip); l != nil {
					mu.Lock()
					locked = append(locked, *l)
					mu.Unlock()
				}
			}
		}
		for _, e := range entries {
			if e.IsDir() && !skip[e.Name()] {
				r := e.Name()
				if rel != "" {
					r = rel + "/" + e.Name()
				}
				wg.Add(1)
				go walk(filepath.Join(dir, e.Name()), r, level+1, wg)
			}
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	walk(root, "", 0, &wg)
	wg.Wait()
	return n, locked
}

// countDbs counts the db.db files written so far under a tree.
func countDbs(root string) int {
	n := 0
	var walk func(dir string)
	walk = func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.Name() == "db.db" {
				n++
			}
		}
		for _, e := range entries {
			if e.IsDir() {
				walk(filepath.Join(dir, e.Name()))
			}
		}
	}
	walk(root)
	return n
}

// ---- the phases ----

// names is gufi_dir2index into staging; it returns the tree it wrote.
func (c *jobCtx) names(src, staging string, maxLevel0 bool) (string, error) {
	ix := c.ix
	ph := c.phase("names")
	if err := ph.start(0); err != nil {
		return "", err
	}
	total, locked := surveyDirs(src, maxLevel0)
	ph.progress(0, total, nil)
	if err := rmTree(staging); err != nil {
		return "", err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", err
	}
	built := filepath.Join(staging, filepath.Base(src))
	if err := prepareIndex(built, locked); err != nil {
		return "", err
	}
	var unmade []string
	var umu sync.Mutex
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				ph.progress(min(countDbs(built), total), total, nil)
			}
		}
	}()
	args := []string{"-x", "-n", strconv.Itoa(c.threads)}
	if maxLevel0 {
		args = append(args, "--max-level", "0")
	}
	args = append(args, "--skip-file", ix.ensureSkipFile(), src, staging)
	_, err := c.runTool(nil, tool{
		name: "gufi_dir2index", cmd: filepath.Join(ix.P.GufiBin, "gufi_dir2index"), args: args, env: ix.gufiEnv(nil, false),
		onDenied: func(paths []string) {
			c.log(fmt.Sprintf("%d folder(s) could not be read and are left out: %s", len(paths), strings.Join(first(paths, 5), ", ")))
			if ix.deniedTo != nil {
				ix.deniedTo(paths)
				return
			}
			ix.recordDenied(paths)
		},
		onErr: func(line string) {
			for _, d := range unmadeIndexDirs(line) {
				if p := sourceOf(d, built, src); p != "" {
					umu.Lock()
					unmade = append(unmade, p)
					umu.Unlock()
				}
			}
		},
	})
	close(done)
	if err != nil {
		return "", err
	}
	openIndex(built, locked)
	if len(unmade) > 0 {
		c.log(fmt.Sprintf("%d folder(s) could not be indexed and are left out: %s", len(unmade), strings.Join(first(unmade, 5), ", ")))
		if ix.deniedTo != nil {
			ix.deniedTo(unmade)
			ph.finish("")
			return built, nil
		}
		var was []string
		if d := ix.ReadDenied(); d != nil {
			was = d.Folders
		}
		ix.recordDenied(append(was, unmade...))
	}
	ph.finish("")
	return built, nil
}

func first(xs []string, n int) []string {
	if len(xs) > n {
		return xs[:n]
	}
	return xs
}

type scope struct {
	subdir    string
	noRecurse bool
}

// visionModel is the captioning model images are described with, or "":
// only where the depth reads images, AI features are on and descriptions
// are on.
func (c *jobCtx) visionModel() string {
	if c.ix.App == nil || c.ix.App.Settings == nil {
		return ""
	}
	eff := c.ix.App.Settings.Effective()
	if eff.AiEnabled && c.settings.OCR && c.settings.ImageDescriptions {
		return eff.VisionModel
	}
	return ""
}

func (ix *Indexer) extractorsFile() string { return filepath.Join(ix.P.IndexBase, "extractors.json") }

// documents is the extraction pass, `studio-server extract`, into the
// extract cache: documents, and in Go the images and the formats enrich.py
// read itself too.
func (c *jobCtx) documents(s scope) error {
	ix := c.ix
	ph := c.phase("documents")
	if !c.settings.Documents {
		ph.skip("the index depth is names")
		return nil
	}
	if err := ph.start(0); err != nil {
		return err
	}
	cmd, pre, err := child("extract", "STUDIO_EXTRACT_BIN")
	if err != nil {
		return err
	}
	args := append(pre, "--kb-root", ix.P.KBRoot, "--extract-cache", ix.P.ExtractCache, "--jobs", strconv.Itoa(c.threads), "--progress",
		"--pdf-reader", c.settings.PDFReader)
	if s.subdir != "" {
		args = append(args, "--subdir", s.subdir)
	}
	if s.noRecurse {
		args = append(args, "--no-recurse")
	}
	if !c.settings.OCR {
		args = append(args, "--no-ocr")
	}
	if c.settings.LegacyOffice {
		if so := ix.Hooks.SofficeFound(); so != "" {
			args = append(args, "--soffice", so)
		}
	}
	if exists(ix.extractorsFile()) {
		args = append(args, "--extractors", ix.extractorsFile())
	}
	extra := map[string]string{"OMP_THREAD_LIMIT": "1"}
	vm := c.visionModel()
	if vm != "" {
		extra["ADE_VISION_MODEL"] = vm
	}
	env := ix.gufiEnv(extra, true)
	if vm == "" {
		env = without(env, "ADE_VISION_MODEL")
	}
	out, err := c.runTool(ph, tool{name: "extract", cmd: cmd, args: args, env: env})
	if err != nil {
		return err
	}
	note := ""
	if len(out) > 0 {
		var s struct {
			Converted int `json:"converted"`
			Reused    int `json:"reused"`
			Failed    int `json:"failed"`
		}
		if json.Unmarshal([]byte(out[len(out)-1]), &s) == nil {
			parts := []string{fmt.Sprintf("%d converted", s.Converted)}
			if s.Reused > 0 {
				parts = append(parts, fmt.Sprintf("%d unchanged", s.Reused))
			}
			if s.Failed > 0 {
				parts = append(parts, fmt.Sprintf("%d could not be read", s.Failed))
			}
			note = strings.Join(parts, ", ")
		}
	}
	ph.finish(note)
	return nil
}

func without(env []string, key string) []string {
	out := env[:0:0]
	for _, kv := range env {
		if !strings.HasPrefix(kv, key+"=") {
			out = append(out, kv)
		}
	}
	return out
}

type textScope struct {
	kbRoot, index, cache string
	subdir               string
	noRecurse            bool
}

// text writes the words tables (enrich.py's write step) over an index
// tree that mirrors kbRoot, from the files and the extract cache.
func (c *jobCtx) text(o textScope) error {
	ix := c.ix
	ph := c.phase("text")
	if c.settings.Depth == DepthNames {
		ph.skip("the index depth is names")
		return nil
	}
	if err := ph.start(0); err != nil {
		return err
	}
	c.holdForTests()
	var ext []string
	if b, err := os.ReadFile(ix.extractorsFile()); err == nil {
		var list []struct {
			Suffixes []string `json:"suffixes"`
		}
		if json.Unmarshal(b, &list) == nil {
			for _, e := range list {
				for _, s := range e.Suffixes {
					s = strings.ToLower(strings.TrimSpace(s))
					if s != "" && !strings.HasPrefix(s, ".") {
						s = "." + s
					}
					ext = append(ext, s)
				}
			}
		}
	}
	vm := c.visionModel()
	soffice := c.settings.LegacyOffice && ix.Hooks.SofficeFound() != ""
	res, err := WriteWords(c.ctx, WordsOptions{
		KBRoot: o.kbRoot, Index: o.index, Cache: o.cache, NoRecurse: o.noRecurse, Jobs: c.threads,
		Soffice: soffice, VisionModel: vm != "", ImagesUseful: (c.settings.OCR && TesseractAvailable()) || vm != "", Extractors: ext,
		Progress: func(done, total int) { ph.progress(done, total, nil) },
		Warn:     func(s string) { c.log("text: " + strings.TrimSpace(s)) },
	})
	if c.aborted() {
		return ErrCancelled
	}
	if err != nil {
		return fmt.Errorf("text: %w", err)
	}
	if res.NameOnly > 0 {
		c.log(fmt.Sprintf("text: %s files indexed by name only: their permissions are narrower than their folder's, and anyone who can enter a folder can read its index (%s)", enUS(res.NameOnly), posixaccess.Env))
	}
	ph.finish(fmt.Sprintf("%s files with text", enUS(res.Files)))
	return nil
}

// holdForTests holds a job at the start of its text phase while the file
// STUDIO_INDEX_HOLD_FILE names exists, so a test can stop a running job.
func (c *jobCtx) holdForTests() {
	if c.ix.holdFile == "" {
		return
	}
	for exists(c.ix.holdFile) && !c.aborted() {
		time.Sleep(50 * time.Millisecond)
	}
}

// enUS is toLocaleString('en-US') for a whole number.
func enUS(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

var chunkedRe = regexp.MustCompile(`chunked: (\d+) chunks .*?, (\d+) to embed`)

type meaningScope struct {
	index     string
	noRecurse bool
	prune     bool
}

// meaning is the embedder, `studio-server embed`, over an index tree with
// the embedding cache.
func (c *jobCtx) meaning(o meaningScope) error {
	ix := c.ix
	ph := c.phase("meaning")
	level := MeaningArgs(c.settings.Meaning, c.settings.MeaningSpeed)
	if level == nil {
		ph.skip("the index depth stops before search by meaning")
		return nil
	}
	if !exists(ix.P.EmbedModel) && !embed.ModelPresent(embed.ModelDir(ix.P.IndexBase)) {
		ph.skip("no embedding model")
		return nil
	}
	sqlite3 := filepath.Join(ix.P.GufiBin, "gufi_sqlite3")
	if !exists(sqlite3) {
		ph.skip("no gufi_sqlite3")
		return nil
	}
	if err := ph.start(0); err != nil {
		return err
	}
	cmd, pre, err := child("embed", "STUDIO_EMBED_BIN")
	if err != nil {
		return err
	}
	args := append(pre, "--index", o.index, "--model", ix.P.EmbedModel, "--gufi-sqlite3", sqlite3, "--jobs", strconv.Itoa(c.threads),
		"--cache", ix.P.EmbedCache, "--progress")
	args = append(args, level...)
	if o.noRecurse {
		args = append(args, "--no-recurse")
	}
	if o.prune {
		args = append(args, "--prune")
	}
	out, err := c.runTool(ph, tool{name: "embed", cmd: cmd, args: args, env: ix.gufiEnv(map[string]string{"STUDIO_TMP": ix.P.IndexDir}, true)})
	if err != nil {
		return err
	}
	m := chunkedRe.FindStringSubmatch(strings.Join(out, "\n"))
	note := ""
	if m != nil {
		a, _ := strconv.Atoi(m[1])
		b, _ := strconv.Atoi(m[2])
		note = fmt.Sprintf("%s passages, %s new", enUS(a), enUS(b))
	}
	ph.finish(note)
	if o.prune || (m != nil && m[2] != "0") {
		d := 10 * time.Minute
		if c.kind() == "build" || o.prune {
			d = 5 * time.Second
		}
		ix.Hooks.ScheduleMapRebuild(d)
	}
	return nil
}

// swapIn puts a built tree at dest, the old one out of the way first.
func swapIn(built, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	old := fmt.Sprintf("%s.old-%d-%d", dest, os.Getpid(), time.Now().UnixMilli())
	openDir(dest)
	moved := os.Rename(dest, old) == nil
	if err := os.Rename(built, dest); err != nil {
		return err
	}
	if moved {
		return rmTree(old)
	}
	return nil
}

// ---- one folder, and the files at the top ----

func (c *jobCtx) setCurrent(s string) {
	c.with(func(j *IndexJob) {
		if j.Progress != nil {
			j.Progress.Current = &s
		}
	})
}

func (c *jobCtx) searchable() {
	c.with(func(j *IndexJob) { s := isoNow(); j.SearchableAt = &s })
}

func (c *jobCtx) indexSubtree(cleaned string) error {
	ix := c.ix
	src := filepath.Join(ix.P.KBRoot, cleaned)
	if st, err := os.Stat(src); err != nil || !st.IsDir() {
		return fmt.Errorf("not a directory: %s", cleaned)
	}
	c.setCurrent(cleaned)
	staging := filepath.Join(ix.P.IndexDir, fmt.Sprintf("inc-%d-%d", c.rec.pub.ID, randomInt(1_000_000)))
	defer rmTree(staging)
	built, err := c.names(src, staging, false)
	if err != nil {
		return err
	}
	if err := c.documents(scope{subdir: cleaned}); err != nil {
		return err
	}
	if err := c.text(textScope{kbRoot: src, index: built, cache: filepath.Join(ix.P.ExtractCache, cleaned)}); err != nil {
		return err
	}
	if err := c.meaning(meaningScope{index: built}); err != nil {
		return err
	}
	if c.aborted() {
		return ErrCancelled
	}
	if err := swapIn(built, filepath.Join(ix.P.GufiIndex, cleaned)); err != nil {
		return err
	}
	c.searchable()
	ix.afterChange(cleaned)
	return nil
}

func (c *jobCtx) indexRootFiles() error {
	ix := c.ix
	staging := filepath.Join(ix.P.IndexDir, fmt.Sprintf("inc-root-%d", c.rec.pub.ID))
	c.setCurrent("(files at the top)")
	defer rmTree(staging)
	built, err := c.names(ix.P.KBRoot, staging, true)
	if err != nil {
		return err
	}
	if err := c.documents(scope{noRecurse: true}); err != nil {
		return err
	}
	if err := c.text(textScope{kbRoot: ix.P.KBRoot, index: built, cache: ix.P.ExtractCache, noRecurse: true}); err != nil {
		return err
	}
	if err := c.meaning(meaningScope{index: built, noRecurse: true}); err != nil {
		return err
	}
	if c.aborted() {
		return ErrCancelled
	}
	if err := os.MkdirAll(ix.P.GufiIndex, 0o755); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(built, "db.db"), filepath.Join(ix.P.GufiIndex, "db.db")); err != nil {
		return err
	}
	c.searchable()
	ix.afterChange("")
	return nil
}

// cleanRel is a path below the main folder, its slashes trimmed; an error
// for one that escapes it.
func (ix *Indexer) cleanRel(rel string) (string, error) {
	cleaned := strings.Trim(rel, "/")
	src := filepath.Join(ix.P.KBRoot, cleaned)
	abs, _ := filepath.Abs(src)
	if !strings.HasPrefix(abs, ix.P.KBRoot+string(filepath.Separator)) {
		return "", errors.New("path escapes the knowledge base")
	}
	return cleaned, nil
}

func (ix *Indexer) folderJob(top string) (*jobRec, error) {
	if top == "" {
		return ix.enqueue("folder", "", "Updating the files at the top", func(c *jobCtx) error { return c.indexRootFiles() }, "folder:")
	}
	return ix.enqueue("folder", top, "Updating "+top, func(c *jobCtx) error {
		cl, err := ix.cleanRel(top)
		if err != nil {
			return err
		}
		return c.indexSubtree(cl)
	}, "folder:"+top)
}

// IndexDir is incrementalIndexDir: the subtree rel, waited on.
func (ix *Indexer) IndexDir(ctx context.Context, rel string) (int64, error) {
	cleaned, err := ix.cleanRel(rel)
	if err != nil {
		return 0, err
	}
	if cleaned == "" {
		return 0, errors.New("use a full rebuild for the root")
	}
	rec, err := ix.enqueue("folder", cleaned, "Updating "+cleaned, func(c *jobCtx) error { return c.indexSubtree(cleaned) }, "folder:"+cleaned)
	if err != nil {
		return 0, err
	}
	return rec.wait(ctx)
}

// IndexRootDb is indexRootDb, waited on.
func (ix *Indexer) IndexRootDb(ctx context.Context) (int64, error) {
	rec, err := ix.folderJob("")
	if err != nil {
		return 0, err
	}
	return rec.wait(ctx)
}

// ReindexForFile is reindexForFile: the file's top-level subtree, or the
// root db for a file directly under the root.
func (ix *Indexer) ReindexForFile(ctx context.Context, rel string) (int64, error) {
	cleaned := strings.Trim(rel, "/")
	if strings.Contains(cleaned, "/") {
		return ix.IndexDir(ctx, strings.Split(cleaned, "/")[0])
	}
	return ix.IndexRootDb(ctx)
}

// ReindexForDir is reindexForDir.
func (ix *Indexer) ReindexForDir(ctx context.Context, rel string) (int64, error) {
	cleaned := strings.Trim(rel, "/")
	if cleaned != "" {
		return ix.IndexDir(ctx, strings.Split(cleaned, "/")[0])
	}
	return ix.IndexRootDb(ctx)
}

// Enqueue is startIndexJob(rel): the folder covering rel, returned at once.
func (ix *Indexer) Enqueue(rel string) (IndexJob, error) {
	cleaned := strings.Trim(rel, "/")
	rec, err := ix.folderJob(strings.Split(cleaned, "/")[0])
	if err != nil {
		return IndexJob{}, err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return rec.pub.clone(), nil
}

// OwnWork is a job of a caller's own (a library built from another
// folder): it runs at once, beside the main folder's queue.
type OwnWork func(job *OwnJob) (int64, error)

// OwnJob is the job OwnWork runs as; Set changes it under the lock.
type OwnJob struct {
	ix  *Indexer
	pub *IndexJob
}

// Set runs fn on the job under the indexer's lock.
func (o *OwnJob) Set(fn func(j *IndexJob)) {
	o.ix.mu.Lock()
	fn(o.pub)
	o.ix.mu.Unlock()
}

// StartOwnJob is startIndexJob(rel, work).
func (ix *Indexer) StartOwnJob(rel string, work OwnWork) (IndexJob, error) {
	if err := ix.requireIndexWork(); err != nil {
		return IndexJob{}, err
	}
	ix.mu.Lock()
	ix.seq++
	now := isoNow()
	job := &IndexJob{ID: ix.seq, Kind: "folder", Dir: rel, Label: "Indexing " + rel, State: "running", StartedAt: now, RunningAt: &now,
		Threads: ix.Settings.Threads("kb"), Phases: []*Phase{}, Warnings: []string{}}
	rec := &jobRec{pub: job, done: make(chan struct{}), ctx: context.Background(), cancel: func() {}}
	ix.recs[job.ID] = rec
	ix.order = append(ix.order, job.ID)
	snap := job.clone()
	ix.mu.Unlock()
	t0 := time.Now()
	go func() {
		ms, err := work(&OwnJob{ix: ix, pub: job})
		ix.mu.Lock()
		if err != nil {
			e := truncRunes(err.Error(), 400)
			job.Error, job.State = &e, "error"
		} else {
			job.MS, job.State = &ms, "done"
		}
		f := isoNow()
		job.FinishedAt = &f
		if job.MS == nil {
			d := time.Since(t0).Milliseconds()
			job.MS = &d
		}
		ix.mu.Unlock()
	}()
	return snap, nil
}

// ---- the whole folder ----

func (ix *Indexer) stashDir(root string) string {
	abs, _ := filepath.Abs(root)
	h := sha1.Sum([]byte(abs))
	return filepath.Join(ix.P.Stash, hex.EncodeToString(h[:])[:16])
}

func moveTree(from, to string) (bool, error) {
	if _, err := os.Lstat(from); err != nil {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return false, err
	}
	if err := rmTree(to); err != nil {
		return false, err
	}
	openDir(from)
	if err := os.Rename(from, to); err != nil {
		if !isCrossDevice(err) {
			return false, err
		}
		if err := os.CopyFS(to, os.DirFS(from)); err != nil {
			return false, err
		}
		if err := rmTree(from); err != nil {
			return false, err
		}
	}
	return true, nil
}

func isCrossDevice(err error) bool {
	var le *os.LinkError
	return errors.As(err, &le) && strings.Contains(le.Err.Error(), "cross-device")
}

func (ix *Indexer) readMark() string {
	for _, f := range []string{ix.P.RootMark, ix.P.OldRootMark} {
		if b, err := os.ReadFile(f); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

func sameFolder(a, b string) bool {
	x, _ := filepath.Abs(a)
	y, _ := filepath.Abs(b)
	if x == y {
		return true
	}
	ra, e1 := filepath.EvalSymlinks(a)
	rb, e2 := filepath.EvalSymlinks(b)
	return e1 == nil && e2 == nil && ra == rb
}

// ReconcileFolder is reconcileFolder: the index of the folder used before
// put aside, and one put aside for this folder brought back. One at a time.
func (ix *Indexer) ReconcileFolder(log func(string)) error {
	ix.reconcileMu.Lock()
	defer ix.reconcileMu.Unlock()
	if log == nil {
		log = ix.Log.Write
	}
	p := ix.P
	was := ix.readMark()
	if was != "" && sameFolder(was, p.KBRoot) {
		if was != p.KBRoot {
			return os.WriteFile(p.RootMark, []byte(p.KBRoot+"\n"), 0o644)
		}
		return nil
	}
	if was != "" {
		to := ix.stashDir(was)
		if err := os.MkdirAll(to, 0o755); err != nil {
			return err
		}
		any := false
		for _, m := range [][2]string{
			{filepath.Join(p.IndexDir, "gufi", filepath.Base(was)), filepath.Join(to, "gufi")},
			{p.ExtractCache, filepath.Join(to, "extract")},
			{p.EmbedCache, filepath.Join(to, "embed-cache.db")},
			{p.SweepState, filepath.Join(to, "sweep-state.json")},
			{p.DepthState, filepath.Join(to, "index-depth.json")},
		} {
			moved, err := moveTree(m[0], m[1])
			if err != nil {
				return err
			}
			any = any || moved
		}
		if err := os.WriteFile(filepath.Join(to, "root"), []byte(was+"\n"), 0o644); err != nil {
			return err
		}
		if any {
			log(fmt.Sprintf("folder: put the index of %s aside in %s", was, to))
		}
		for _, d := range []string{"pdf-preview", "pdf-pages", "model-cache"} {
			os.RemoveAll(filepath.Join(p.IndexBase, d))
		}
		os.RemoveAll(p.SemanticMapDir)
		entries, _ := os.ReadDir(p.Stash)
		type dated struct {
			d string
			t int64
		}
		var ds []dated
		for _, e := range entries {
			d := filepath.Join(p.Stash, e.Name())
			var t int64
			if st, err := os.Stat(d); err == nil {
				t = st.ModTime().UnixNano()
			}
			ds = append(ds, dated{d, t})
		}
		sort.SliceStable(ds, func(i, j int) bool { return ds[i].t > ds[j].t })
		for i, d := range ds {
			if i >= 3 {
				rmTree(d.d)
			}
		}
		if err := rmTree(p.GufiIndex); err != nil {
			return err
		}
	}
	mine := ix.stashDir(p.KBRoot)
	if exists(filepath.Join(mine, "root")) && !exists(filepath.Join(p.GufiIndex, "db.db")) {
		for _, m := range [][2]string{
			{filepath.Join(mine, "gufi"), p.GufiIndex},
			{filepath.Join(mine, "extract"), p.ExtractCache},
			{filepath.Join(mine, "embed-cache.db"), p.EmbedCache},
			{filepath.Join(mine, "sweep-state.json"), p.SweepState},
			{filepath.Join(mine, "index-depth.json"), p.DepthState},
		} {
			if _, err := moveTree(m[0], m[1]); err != nil {
				return err
			}
		}
		rmTree(mine)
		log(fmt.Sprintf("folder: brought back the earlier index of %s; the next build updates it", p.KBRoot))
	}
	if err := os.MkdirAll(p.IndexDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p.RootMark, []byte(p.KBRoot+"\n"), 0o644); err != nil {
		return err
	}
	if p.OldRootMark != p.RootMark {
		os.Remove(p.OldRootMark)
	}
	return nil
}

// EarlierIndexOf is earlierIndexOf: an index of root from before, put
// aside when another folder was in use; nil for none.
func (ix *Indexer) EarlierIndexOf(root string) map[string]any {
	d := ix.stashDir(root)
	if !exists(filepath.Join(d, "root")) {
		return nil
	}
	var builtAt any = nil
	if st, err := os.Stat(filepath.Join(d, "gufi", "db.db")); err == nil {
		builtAt = isoAt(st.ModTime())
	}
	return map[string]any{"builtAt": builtAt}
}

// verifyTree checks that GUFI reads a tree it just wrote.
func (ix *Indexer) verifyTree(ctx context.Context, tree string) error {
	if !exists(filepath.Join(ix.P.GufiBin, "gufi_query")) || ix.App == nil || ix.App.Gufi == nil {
		return nil
	}
	threads := min(max(CPUCount(), 1), 16)
	res, err := ix.App.Gufi.Run(ctx, gufi.Command{Tool: gufi.ToolQuery, Args: []string{"-n", strconv.Itoa(threads), "-d", gufi.Delim, "-a", "1",
		"-I", "CREATE TABLE t (d INT64);", "-K", "CREATE TABLE agg (d INT64);",
		"-E", "INSERT INTO t SELECT count(*) FROM vrsummary;", "-J", "INSERT INTO agg SELECT sum(d) FROM t;",
		"-G", "SELECT sum(d) FROM agg;", tree}})
	if err != nil {
		return err
	}
	rows := gufi.ParseRows(string(res.Stdout), 1)
	n := 0.0
	if len(rows) > 0 {
		n, err = strconv.ParseFloat(strings.TrimSpace(rows[0][0]), 64)
	}
	if len(rows) == 0 || err != nil || n < 1 {
		return fmt.Errorf("GUFI could not read the index it wrote at %s", tree)
	}
	return nil
}

func (c *jobCtx) buildWholeFolder() error {
	ix := c.ix
	if err := ix.ReconcileFolder(c.log); err != nil {
		return err
	}
	snapshot := ix.sweepSnapshot()
	live := exists(filepath.Join(ix.P.GufiIndex, "db.db"))
	c.with(func(j *IndexJob) {
		if live {
			j.Label = "Updating the index"
		} else {
			j.Label = "Building the index"
		}
	})
	staging := filepath.Join(ix.P.IndexDir, fmt.Sprintf("gufi.staging-%d", c.rec.pub.ID))
	defer rmTree(staging)
	built, err := c.names(ix.P.KBRoot, staging, false)
	if err != nil {
		return err
	}
	if err := ix.verifyTree(c.ctx, built); err != nil {
		return err
	}
	target := built
	if !live {
		if err := swapIn(built, ix.P.GufiIndex); err != nil {
			return err
		}
		target = ix.P.GufiIndex
		c.searchable()
		c.log("names are searchable")
	}
	if err := c.documents(scope{}); err != nil {
		return err
	}
	if err := c.text(textScope{kbRoot: ix.P.KBRoot, index: target, cache: ix.P.ExtractCache}); err != nil {
		return err
	}
	if err := MarkTextClean(target); err != nil {
		return err
	}
	if !live {
		ix.Hooks.InvalidateContext()
	}
	if err := c.meaning(meaningScope{index: target, prune: true}); err != nil {
		return err
	}
	if c.aborted() {
		return ErrCancelled
	}
	if live {
		if err := swapIn(built, ix.P.GufiIndex); err != nil {
			return err
		}
		c.searchable()
	}
	ix.afterChange("")
	if err := ix.saveSweepState(snapshot); err != nil {
		return err
	}
	return WriteDepthRecord(ix.P.DepthState, DepthRecordOf(c.settings))
}

// StartBuild is startBuild: the whole main folder, as one job; a second
// request while one is queued or running gets that one.
func (ix *Indexer) StartBuild() (IndexJob, error) {
	ix.mu.Lock()
	for _, r := range append([]*jobRec{ix.current}, ix.queue...) {
		if r != nil && r.pub.Kind == "build" {
			c := r.pub.clone()
			ix.mu.Unlock()
			return c, nil
		}
	}
	ix.mu.Unlock()
	rec, err := ix.enqueue("build", "", "Building the index", func(c *jobCtx) error { return c.buildWholeFolder() }, "build")
	if err != nil {
		return IndexJob{}, err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return rec.pub.clone(), nil
}

// CurrentBuild is currentBuild: the running or queued whole-folder build.
func (ix *Indexer) CurrentBuild() *IndexJob {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, r := range append([]*jobRec{ix.current}, ix.queue...) {
		if r != nil && r.pub.Kind == "build" {
			c := r.pub.clone()
			return &c
		}
	}
	return nil
}

// StartDelete is startDelete: the main folder's index and its caches
// deleted, never its files, after everything running is stopped.
func (ix *Indexer) StartDelete() (IndexJob, error) {
	ix.CancelAll()
	rec, err := ix.enqueue("delete", "", "Deleting the index", func(c *jobCtx) error {
		p := ix.P
		for _, d := range []string{p.GufiIndex, p.ExtractCache, p.EmbedCache, p.SweepState, p.DepthState, p.RootMark, p.SemanticMapDir,
			filepath.Join(p.IndexBase, "pdf-preview"), filepath.Join(p.IndexBase, "pdf-pages"), filepath.Join(p.IndexBase, "model-cache")} {
			if err := rmTree(d); err != nil {
				return err
			}
		}
		c.log(fmt.Sprintf("deleted the index of %s (the folder's files are untouched)", p.KBRoot))
		ix.mu.Lock()
		ix.status.LastChanges = []string{}
		ix.mu.Unlock()
		ix.afterChange("")
		return nil
	}, "")
	if err != nil {
		return IndexJob{}, err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return rec.pub.clone(), nil
}

// StartOtherJob is startOtherJob: work that is not an index pass, in turn
// with the rest.
func (ix *Indexer) StartOtherJob(kind, label string, work func(log func(string)) error) (IndexJob, error) {
	rec, err := ix.enqueue(kind, "", label, func(c *jobCtx) error { return work(c.log) }, "")
	if err != nil {
		return IndexJob{}, err
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return rec.pub.clone(), nil
}
