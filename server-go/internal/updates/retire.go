package updates

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/state"
)

// The copy an update replaced (.<name>.previous) is kept only until the new
// version's first clean start: updates/pending.json cleared after a minute
// up (the run loop's rollback after three failed starts needs the copy
// until then, and works offline), and no process left running from it (the
// window of the old copy runs from it until it is reopened). Then it is
// removed, with the files in the data folder that only the Node version
// made and that its run loop makes again (nodeLeftovers). The version it
// held is kept in updates/previous.json; Roll back then downloads that
// version and installs it as installing a picked version does (signature,
// size and SHA-256, the swap, the state snapshot put back when needed).

// previousRecord names the version the last update replaced.
const previousRecord = "previous.json"

// nodeLeftovers are paths under INDEX_BASE that only the Node version made
// and that its own run loop makes again at its next start, so a rollback to
// it loses nothing: each is removed only when it is a symbolic link that
// leads nowhere (into the removed copy). docs/UPDATES.md lists what was
// checked and kept.
var nodeLeftovers = []string{
	// The GGUF embedding model studio-run.sh links from the app's models/
	// for sqlite-lembed; the Go version embeds with ONNX and carries none.
	filepath.Join("models", "minilm384.gguf"),
}

type prevRecord struct {
	Version string `json:"version"`
	At      string `json:"at"`
}

// previousInfo is the version Roll back goes to, and the size of the
// download that takes (null when the release is not known here).
type previousInfo struct {
	Version string `json:"version"`
	Size    *int64 `json:"size"`
}

// previousVersion is the version Roll back installs: the one in the copy
// still kept beside the app, else the one the record names (the caller
// holds the lock).
func (u *Updater) previousVersion() *previousInfo {
	if u.Mode() != "app" {
		return nil
	}
	v := u.versionOf(u.beside("previous"))
	if v == "" {
		var r prevRecord
		if readJSON(u.inUpdates(previousRecord), &r) {
			v = r.Version
		}
	}
	if _, ok := ParseVersion(v); !ok || sameVersion(v, u.running().Version) {
		return nil
	}
	p := &previousInfo{Version: v}
	if rel := u.rollbackRelease(v); rel != nil {
		if a, ok := rel.Assets[u.kit().asset]; ok && a.Size > 0 {
			size := a.Size
			p.Size = &size
		}
	}
	return p
}

// rollbackRelease is the release of version on this Studio's channel with
// a signed build for it, or nil (the caller holds the lock).
func (u *Updater) rollbackRelease(version string) *release {
	return u.pickable(version, u.policy().Channel)
}

// retirePrevious removes the copy kept beside the app once the version
// running has started cleanly and nothing runs from the copy; true when
// there is none left (the caller holds no lock).
func (u *Updater) retirePrevious() bool {
	u.mu.Lock()
	prev := u.beside("previous")
	if u.Mode() != "app" || u.installRoot() == "" || !exists(prev) {
		u.mu.Unlock()
		return true
	}
	if exists(u.inUpdates("pending.json")) || u.op != nil || u.inProgress() {
		u.mu.Unlock()
		return false
	}
	if pids := u.inUse(prev); len(pids) > 0 {
		u.mu.Unlock()
		return false
	}
	v := u.versionOf(prev)
	if _, ok := ParseVersion(v); ok && !sameVersion(v, u.running().Version) {
		_ = writeJSON(u.inUpdates(previousRecord), prevRecord{Version: v, At: state.ISO(time.Now())})
	}
	// Out of the way under the lock, removed after it: the removal of a
	// whole app can take seconds.
	gone := u.beside("retired")
	_ = rmTree(gone)
	err := os.Rename(prev, gone)
	u.mu.Unlock()
	if err != nil {
		u.log("update: the previous copy could not be removed: " + plain(err))
		return false
	}
	if err := rmTree(gone); err != nil {
		u.log("update: the previous copy could not be removed: " + plain(err))
	}
	removed := u.removeNodeLeftovers()
	u.log(fmt.Sprintf("update: the copy of %s kept beside the app was removed%s; Roll back downloads it again", orText(v, "the previous version"), removed))
	return true
}

// removeNodeLeftovers removes the nodeLeftovers that lead nowhere, and says
// which.
func (u *Updater) removeNodeLeftovers() string {
	if u.o.IndexBase == "" {
		return ""
	}
	var gone []string
	for _, rel := range nodeLeftovers {
		p := filepath.Join(u.o.IndexBase, rel)
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&os.ModeSymlink == 0 {
			continue
		}
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if os.Remove(p) == nil {
			gone = append(gone, rel)
		}
	}
	if len(gone) == 0 {
		return ""
	}
	return ", and from the data folder " + strings.Join(gone, ", ")
}

// retireLoop tries retirePrevious now and then every Timings.Retire until
// it is done or the server stops.
func (u *Updater) retireLoop() {
	if u.retirePrevious() {
		return
	}
	tick := time.NewTicker(u.o.Timings.Retire)
	defer tick.Stop()
	for {
		select {
		case <-u.stop:
			return
		case <-tick.C:
		}
		if u.retirePrevious() {
			return
		}
	}
}

// inUse lists the processes running from dir: their program or working
// directory is inside it.
func (u *Updater) inUse(dir string) []int {
	if u.o.InUse != nil {
		return u.o.InUse(dir)
	}
	// The kernel names the real path.
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	switch u.o.GOOS {
	case "linux":
		return procsUnder(dir)
	case "darwin":
		return lsofUnder(dir)
	}
	return nil
}

func under(dir, p string) bool {
	p = strings.TrimSuffix(p, " (deleted)")
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// procsUnder reads /proc: each process's exe and cwd links name the path
// they have now, so a renamed copy is found by its new name.
func procsUnder(dir string) []int {
	ents, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range ents {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == os.Getpid() {
			continue
		}
		for _, link := range []string{"exe", "cwd"} {
			if p, err := os.Readlink(filepath.Join("/proc", e.Name(), link)); err == nil && under(dir, p) {
				out = append(out, pid)
				break
			}
		}
	}
	return out
}

// lsofUnder asks lsof for every process's program and working directory,
// by the path each has now. When lsof cannot answer, the copy counts as in
// use, so it is kept.
func lsofUnder(dir string) []int {
	cmd := exec.Command("/usr/sbin/lsof", "-n", "-P", "-w", "-F", "pn", "-d", "txt,cwd")
	out, err := cmd.Output()
	if err != nil && len(out) == 0 {
		return []int{-1}
	}
	var pids []int
	pid := 0
	seen := map[int]bool{}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			if pid > 0 && pid != os.Getpid() && !seen[pid] && under(dir, line[1:]) {
				seen[pid] = true
				pids = append(pids, pid)
			}
		}
	}
	return pids
}
