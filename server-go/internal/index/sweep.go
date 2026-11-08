package index

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/semanticmap"
)

// ---- the background sync ----

// pruneCache removes cache entries whose source file no longer exists.
func (ix *Indexer) pruneCache(cacheRoot, kind string) {
	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			abs := filepath.Join(dir, e.Name())
			childRel := e.Name()
			if rel != "" {
				childRel = rel + "/" + e.Name()
			}
			if e.IsDir() {
				walk(abs, childRel)
				continue
			}
			keep := false
			switch {
			case strings.HasSuffix(e.Name(), ".tmp"):
				var age time.Duration
				if st, err := os.Stat(abs); err == nil {
					age = time.Since(st.ModTime())
				}
				keep = age < time.Hour
			case kind == ".txt" && strings.HasSuffix(e.Name(), ".txt"):
				keep = exists(filepath.Join(ix.P.KBRoot, childRel[:len(childRel)-4]))
			case kind == ".pdf" && strings.HasSuffix(e.Name(), ".pdf"):
				base := filepath.Join(ix.P.KBRoot, filepath.Dir(childRel), strings.TrimSuffix(e.Name(), ".pdf"))
				for _, ext := range []string{".docx", ".pptx", ".xlsx", ".doc", ".ppt", ".xls", ".odt", ".odp", ".ods"} {
					if exists(base + ext) {
						keep = true
						break
					}
				}
			default:
				keep = true
			}
			if !keep {
				os.Remove(abs)
			}
		}
	}
	walk(cacheRoot, "")
}

// scanKbDirs is each folder's latest change, and the folders denied.
func (ix *Indexer) scanKbDirs() Scan {
	s := ScanLatest(ix.P.KBRoot, ExcludeDirs, nil)
	ix.mu.Lock()
	ix.kbDenied = s.Denied
	ix.mu.Unlock()
	var abs []string
	for _, rel := range s.Denied {
		if rel == "" {
			abs = append(abs, ix.P.KBRoot)
		} else {
			abs = append(abs, filepath.Join(ix.P.KBRoot, rel))
		}
	}
	ix.recordDenied(abs)
	return s
}

func (ix *Indexer) underDenied(rel string) bool {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	for _, d := range ix.kbDenied {
		if d == "" || rel == d || strings.HasPrefix(rel, d+"/") {
			return true
		}
	}
	return false
}

// sweepState is sweep-state.json: each folder's latest change as the sync
// recorded it ("." for the root), in the order it was written.
type sweepState struct {
	keys []string
	m    map[string]float64
}

func (s *sweepState) set(k string, v float64) {
	if _, ok := s.m[k]; !ok {
		s.keys = append(s.keys, k)
	}
	s.m[k] = v
}

// MarshalJSON writes it as JSON.stringify does: keys that are array
// indexes first, ascending, then the rest in order.
func (s sweepState) MarshalJSON() ([]byte, error) {
	var idx, rest []string
	for _, k := range s.keys {
		if isArrayIndex(k) {
			idx = append(idx, k)
		} else {
			rest = append(rest, k)
		}
	}
	sort.Slice(idx, func(i, j int) bool {
		a, _ := strconv.ParseUint(idx[i], 10, 32)
		b, _ := strconv.ParseUint(idx[j], 10, 32)
		return a < b
	})
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range append(idx, rest...) {
		if i > 0 {
			b.WriteByte(',')
		}
		kj, _ := json.Marshal(k)
		b.Write(kj)
		b.WriteByte(':')
		b.WriteString(strconv.FormatFloat(s.m[k], 'f', -1, 64))
	}
	b.WriteByte('}')
	return []byte(b.String()), nil
}

func isArrayIndex(k string) bool {
	if k == "" || (len(k) > 1 && k[0] == '0') {
		return false
	}
	n, err := strconv.ParseUint(k, 10, 32)
	return err == nil && n < 1<<32-1
}

func (ix *Indexer) loadSweepState() sweepState {
	s := sweepState{m: map[string]float64{}}
	b, err := os.ReadFile(ix.P.SweepState)
	if err != nil {
		return s
	}
	var m map[string]float64
	if json.Unmarshal(b, &m) != nil {
		return s
	}
	for _, k := range objectKeys(b) {
		if v, ok := m[k]; ok {
			s.set(k, v)
		}
	}
	return s
}

// sweepSnapshot is each folder's latest change, keyed as the sync records them.
func (ix *Indexer) sweepSnapshot() sweepState {
	scan := ix.scanKbDirs()
	s := sweepState{m: map[string]float64{}}
	for _, rel := range scan.Order {
		k := rel
		if k == "" {
			k = "."
		}
		s.set(k, float64(scan.Latest[rel]))
	}
	return s
}

func (ix *Indexer) saveSweepState(s sweepState) error {
	if err := os.MkdirAll(ix.P.IndexBase, 0o755); err != nil {
		return err
	}
	b, _ := s.MarshalJSON()
	return os.WriteFile(ix.P.SweepState, b, 0o644)
}

// pendingDepthChange is the depth change the next sync applies; nil when
// there is nothing to run.
func (ix *Indexer) pendingDepthChange(s Settings) *DepthChange {
	if !exists(filepath.Join(ix.P.GufiIndex, "db.db")) {
		return nil
	}
	now := DepthRecordOf(s)
	was := ReadDepthRecord(ix.P.DepthState)
	var ch *DepthChange
	if was != nil {
		ch = DepthChangeOf(*was, now)
	}
	if was == nil || (ch != nil && !ch.Text && !ch.Meaning) {
		WriteDepthRecord(ix.P.DepthState, now)
	}
	if ch != nil && (ch.Text || ch.Meaning) {
		return ch
	}
	return nil
}

// applyDepthChange applies a change of depth over the whole folder, on the
// index in place: only the phases the new depth adds.
func (c *jobCtx) applyDepthChange() (*DepthChange, error) {
	ix := c.ix
	ch := ix.pendingDepthChange(c.settings)
	if ch == nil {
		return nil, nil
	}
	c.with(func(j *IndexJob) {
		j.Phases = newPhases()
		j.Label = "Applying the new index depth: " + ch.Label
	})
	c.log(fmt.Sprintf("depth: applying %s (%s) over the whole folder", c.settings.Depth, ch.Label))
	c.phase("names").skip("unchanged")
	if ch.Text {
		if err := c.documents(scope{}); err != nil {
			return nil, err
		}
		if err := c.text(textScope{kbRoot: ix.P.KBRoot, index: ix.P.GufiIndex, cache: ix.P.ExtractCache}); err != nil {
			return nil, err
		}
	} else {
		c.phase("documents").skip("unchanged")
		c.phase("text").skip("unchanged")
	}
	if ch.Meaning {
		if err := c.meaning(meaningScope{index: ix.P.GufiIndex, prune: true}); err != nil {
			return nil, err
		}
	} else {
		c.phase("meaning").skip("unchanged")
	}
	if c.aborted() {
		return nil, ErrCancelled
	}
	ix.afterChange("")
	return ch, WriteDepthRecord(ix.P.DepthState, DepthRecordOf(c.settings))
}

func (ix *Indexer) sweepInterval() float64 {
	if ix.App != nil && ix.App.Settings != nil {
		return float64(ix.App.Settings.Effective().SweepIntervalSec)
	}
	return ix.status.SweepIntervalSec
}

// RequestSweep asks for a sync soon, as a change of depth does: after a
// short wait, and after the job running or waiting. Only where the
// background sync runs and is not paused.
func (ix *Indexer) RequestSweep(delay time.Duration) {
	ix.mu.Lock()
	on := ix.sweepTimerOn
	ix.mu.Unlock()
	if !on || ix.sweepInterval() <= 0 {
		return
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.sweepWanted = true
	if ix.kickTimer != nil {
		ix.kickTimer.Stop()
	}
	ix.kickTimer = time.AfterFunc(delay, ix.runWantedSweep)
}

func (ix *Indexer) runWantedSweep() {
	ix.mu.Lock()
	ix.kickTimer = nil
	if !ix.sweepWanted || ix.sweeping || ix.current != nil || len(ix.queue) > 0 {
		ix.mu.Unlock()
		return
	}
	ix.sweepWanted = false
	ix.mu.Unlock()
	ix.Sweep(context.Background(), ix.Log.Write)
}

// Sweep is sweep(): re-indexes the folders whose content is newer than the
// last sync, and applies a change of depth since. Skipped while another
// index job is running or waiting.
func (ix *Indexer) Sweep(ctx context.Context, log func(string)) []string {
	ix.mu.Lock()
	if ix.sweeping || ix.current != nil || len(ix.queue) > 0 {
		ix.mu.Unlock()
		return []string{}
	}
	ix.sweeping = true
	ix.mu.Unlock()
	t0 := time.Now()
	defer func() {
		ix.mu.Lock()
		ix.sweeping = false
		now := isoNow()
		ms := time.Since(t0).Milliseconds()
		ix.status.LastSweepAt, ix.status.LastSweepMs = &now, &ms
		again := ix.sweepWanted
		ix.mu.Unlock()
		if again {
			go ix.runWantedSweep()
		}
	}()
	state := ix.loadSweepState()
	scan := ix.scanKbDirs()
	rootChanged := false
	var changed []string
	for _, rel := range scan.Order {
		k := rel
		if k == "" {
			k = "."
		}
		if state.m[k] < float64(scan.Latest[rel]) {
			if rel == "" {
				rootChanged = true
			} else {
				changed = append(changed, rel)
			}
		}
	}
	var removed []string
	for _, rel := range state.keys {
		if rel == "" || rel == "." {
			continue
		}
		if _, ok := scan.Latest[rel]; !ok && !ix.underDenied(rel) {
			removed = append(removed, rel)
		}
	}
	sort.Strings(changed)
	var roots []string
	for _, rel := range changed {
		covered := false
		for _, o := range changed {
			if o != rel && strings.HasPrefix(rel, o+"/") {
				covered = true
				break
			}
		}
		if !covered {
			roots = append(roots, rel)
		}
	}
	changed = roots
	depth := ix.pendingDepthChange(ix.Settings.Get("kb"))
	filesChanged := rootChanged || len(changed) > 0 || len(removed) > 0
	tidy := TextCleanNeeded(ix.P.GufiIndex)
	if !filesChanged && depth == nil && !tidy {
		ix.mu.Lock()
		ix.status.LastChanges = []string{}
		ix.mu.Unlock()
		return []string{}
	}
	var failures, indexed []string
	rootIndexed := false
	depthApplied := ""
	label := "Tidying the indexed text"
	if filesChanged {
		label = "Syncing changes"
	} else if depth != nil {
		label = "Applying the new index depth: " + depth.Label
	}
	rec, err := ix.enqueue("sweep", "", label, func(c *jobCtx) error {
		for _, rel := range removed {
			rmTree(filepath.Join(ix.P.GufiIndex, rel))
		}
		ix.pruneCache(ix.P.ExtractCache, ".txt")
		ix.pruneCache(filepath.Join(ix.P.IndexBase, "pdf-preview"), ".pdf")
		ix.pruneCache(filepath.Join(ix.P.IndexBase, "model-cache"), ".json")
		var todo []string
		if rootChanged {
			todo = append(todo, "")
		}
		todo = append(todo, changed...)
		c.with(func(j *IndexJob) {
			if j.Progress != nil {
				j.Progress.Total = len(todo)
			}
		})
		for i, rel := range todo {
			if c.aborted() {
				return ErrCancelled
			}
			name := rel
			if name == "" {
				name = "files at the top"
			}
			c.with(func(j *IndexJob) {
				j.Phases = newPhases()
				j.Label = fmt.Sprintf("Syncing changes (%d of %d): %s", i+1, len(todo), name)
			})
			if rel == "" {
				log("sweep: re-indexing root files")
			} else {
				log("sweep: re-indexing " + rel)
			}
			var err error
			if rel != "" {
				if err = c.indexSubtree(rel); err == nil {
					indexed = append(indexed, rel)
				}
			} else if err = c.indexRootFiles(); err == nil {
				rootIndexed = true
			}
			if err != nil {
				if errors.Is(err, ErrCancelled) || c.aborted() {
					return err
				}
				who := rel
				if who == "" {
					who = "(root files)"
				}
				failures = append(failures, who+": "+err.Error())
				c.with(func(j *IndexJob) {
					if j.Progress != nil {
						j.Progress.Failed = append(j.Progress.Failed, who+": "+err.Error())
					}
				})
				if rel == "" {
					log("sweep: FAILED root files: " + err.Error())
				} else {
					log("sweep: FAILED " + rel + ": " + err.Error())
				}
			}
			c.with(func(j *IndexJob) {
				if j.Progress != nil {
					j.Progress.Done = i + 1
				}
			})
		}
		if rootIndexed || len(indexed) > 0 || len(removed) > 0 {
			failed := map[string]bool{}
			for _, rel := range changed {
				if !containsStr(indexed, rel) {
					failed[rel] = true
				}
			}
			next := sweepState{m: map[string]float64{}}
			for _, rel := range scan.Order {
				key := rel
				if key == "" {
					key = "."
				}
				keepOld := failed[rel]
				if key == "." {
					keepOld = rootChanged && !rootIndexed
				}
				if keepOld {
					if v, ok := state.m[key]; ok {
						next.set(key, v)
					}
				} else {
					next.set(key, float64(scan.Latest[rel]))
				}
			}
			b, _ := next.MarshalJSON()
			if err := os.WriteFile(ix.P.SweepState, b, 0o644); err != nil {
				return err
			}
		}
		ch, err := c.applyDepthChange()
		if err != nil {
			return err
		}
		if ch != nil {
			depthApplied = string(c.settings.Depth)
		}
		if tidy && TextCleanNeeded(ix.P.GufiIndex) {
			c.with(func(j *IndexJob) { j.Label = "Tidying the indexed text" })
			dbs, rows, err := CleanIndexedText(ix.P.GufiIndex, c.aborted)
			if err != nil {
				return err
			}
			MarkTextClean(ix.P.GufiIndex)
			log(fmt.Sprintf("sweep: took the converter's provenance line out of %s files' text in %s folders", enUS(rows), enUS(dbs)))
		}
		if filesChanged {
			c.with(func(j *IndexJob) { j.Label = "Syncing changes" })
		}
		return nil
	}, "")
	if err != nil {
		ix.mu.Lock()
		e := err.Error()
		ix.status.LastError = &e
		ix.mu.Unlock()
		return []string{}
	}
	<-rec.done
	ix.mu.Lock()
	defer ix.mu.Unlock()
	lc := []string{}
	if rootIndexed {
		lc = append(lc, "(root files)")
	}
	lc = append(lc, indexed...)
	for _, r := range removed {
		lc = append(lc, r+" (removed)")
	}
	if depthApplied != "" && rec.pub.State == "done" {
		lc = append(lc, "(index depth: "+depthApplied+")")
	}
	ix.status.LastChanges = lc
	switch {
	case len(failures) > 0:
		e := strings.Join(failures, "; ")
		ix.status.LastError = &e
	case rec.pub.State == "error":
		ix.status.LastError = rec.pub.Error
	default:
		ix.status.LastError = nil
	}
	if indexed == nil {
		indexed = []string{}
	}
	return indexed
}

// Start is startSweepTimer, run where this process holds the index lock:
// a swap a stop cut off restored, the folder reconciled, the sync state
// and the depth record primed, then the sync on its interval, with the
// history snapshot after each.
func (ix *Indexer) Start(ctx context.Context, afterSweep func(ctx context.Context, log func(string))) {
	ix.mu.Lock()
	ix.sweepTimerOn = true
	ix.mu.Unlock()
	log := ix.Log.Write
	if back := recoverSwap(ix.P.GufiIndex, swapLeftovers(ix.P.GufiIndex)); back != "" {
		log(fmt.Sprintf("index: restored the index from %s, a swap that a stop cut off", filepath.Base(back)))
	}
	if err := ix.ReconcileFolder(nil); err != nil {
		log("index: " + err.Error())
	}
	if !exists(ix.P.SweepState) {
		ix.saveSweepState(ix.sweepSnapshot())
	}
	if !exists(ix.P.DepthState) {
		ix.pendingDepthChange(ix.Settings.Get("kb"))
	}
	wait := func() time.Duration { return time.Duration(max(ix.sweepInterval(), 60) * float64(time.Second)) }
	ix.mu.Lock()
	ix.status.SweepIntervalSec = ix.sweepInterval()
	ix.mu.Unlock()
	t := time.NewTimer(wait())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		sec := ix.sweepInterval()
		ix.mu.Lock()
		ix.status.SweepIntervalSec = sec
		ix.mu.Unlock()
		if sec > 0 && !ix.Running() {
			ix.Sweep(ctx, log)
			if afterSweep != nil {
				afterSweep(ctx, log)
			}
		}
		t.Reset(wait())
	}
}

// ---- the semantic map, by default ----

type mapState struct {
	mu      sync.Mutex
	running bool
	again   bool
	timer   *time.Timer
	dueAt   time.Time
}

// scheduleMap is semanticMap.ts's scheduleSemanticMapRebuild for the main
// folder, building in this process: calls close together make one build,
// the earliest time asked for wins, and a call while a build runs makes
// one more after it.
func (ix *Indexer) scheduleMap(delay time.Duration) {
	st := &ix.mapState
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.running {
		st.again = true
		return
	}
	due := time.Now().Add(delay)
	if st.timer != nil && !st.dueAt.After(due) {
		return
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	st.dueAt = due
	st.timer = time.AfterFunc(delay, ix.rebuildMap)
}

func (ix *Indexer) rebuildMap() {
	st := &ix.mapState
	st.mu.Lock()
	st.timer = nil
	if st.running {
		st.again = true
		st.mu.Unlock()
		return
	}
	st.running = true
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		st.running = false
		again := st.again
		st.again = false
		st.mu.Unlock()
		if again {
			ix.scheduleMap(time.Minute)
		}
	}()
	if _, _, m := DepthParts(ix.Settings.Get("kb").Depth); m == "off" {
		return
	}
	// The defaults of semantic_map.py and cmd/semantic-map.
	var s semanticmap.Summary
	err := safeErr(func() (err error) {
		s, err = semanticmap.Build(semanticmap.IndexSource(ix.P.GufiIndex), ix.P.SemanticMapDir, semanticmap.Options{Sample: 3000, Fit: 20000})
		return err
	})
	if err != nil {
		ix.logInfo("semantic map (kb) failed: " + err.Error())
		return
	}
	b, _ := json.Marshal(s)
	ix.logInfo("semantic map (kb): " + string(b))
}

// ---- LibreOffice ----

var (
	sofficeMu    sync.Mutex
	sofficeKnown *string
)

// findSoffice is extractors.ts's sofficeFound: SOFFICE_BIN, the PATH, then
// the app on macOS. Looked up once, until ForgetExtractors.
func findSoffice() string {
	sofficeMu.Lock()
	defer sofficeMu.Unlock()
	if sofficeKnown != nil {
		return *sofficeKnown
	}
	s := lookSoffice()
	sofficeKnown = &s
	return s
}

func lookSoffice() string {
	if v := os.Getenv("SOFFICE_BIN"); v != "" {
		return v
	}
	if p, err := exec.LookPath("soffice"); err == nil {
		return p
	}
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		for _, app := range []string{"/Applications/LibreOffice.app", filepath.Join(home, "Applications", "LibreOffice.app")} {
			if bin := filepath.Join(app, "Contents", "MacOS", "soffice"); exists(bin) {
				return bin
			}
		}
	}
	return ""
}

// logInfo is the server's own log (Node's console), not the index log.
func (ix *Indexer) logInfo(msg string) {
	if ix.App != nil && ix.App.Log != nil {
		ix.App.Log.Info(msg)
	}
}
