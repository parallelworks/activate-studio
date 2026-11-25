package userlibs

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/transfers"
	"github.com/parallelworks/activate-studio/server-go/internal/writehelper"
)

// The write helper (cmd/studio-write) on a person's system: the Studio's
// copy goes to their ~/.activate-studio the first time the Library writes
// there, as GUFI does, and every write in a library of theirs runs through
// it.

const writeHelperName = "studio-write"

// writeTools is the helpers found on systems.
type writeTools struct {
	toolsMu sync.Mutex
	tools   map[string]foundTool
	// noWriteTool, in tests, is why no helper runs.
	noWriteTool string
}

type foundTool struct {
	tool writehelper.Tool
	at   time.Time
}

// writeToolFor is how long a helper found there is used without looking
// again. One that is gone meanwhile is sent again at once (Tool.Missing).
const writeToolFor = 10 * time.Minute

// writeProbeSh finds, as the person, the write helper for the system it
// runs on: $1 the library's folder, $2 and $3 the helper's version for
// Linux x86_64 and aarch64, and $4 the grants the library's owner made. It
// says the folder's inode and owner, and how another account could move it.
const writeProbeSh = `umask 077
LOOSE_OK=$4
` + transfers.LooseSh + `
v=
case "$(uname -sm)" in
"Linux x86_64") v=$2 ;;
"Linux aarch64"|"Linux arm64") v=$3 ;;
esac
echo "platform=$(uname -sm)"
echo "version=$v"
if r=$(cd -P -- "$1" 2>/dev/null && pwd -P); then
  echo "id=$(lz_stat %i %i "$r"):$(lz_stat %u %u "$r")"
  w=$(looseroute "$r"); [ -n "$w" ] || w=$(looseroute "$1")
  echo "above=$w"
fi
[ -n "$v" ] || exit 0
mkdir -p -- "$HOME/.activate-studio" 2>/dev/null && chmod 700 -- "$HOME/.activate-studio" 2>/dev/null
a=$(cd -P -- "$HOME/.activate-studio" 2>/dev/null && pwd -P) || { echo home=none; exit 0; }
LOOSE_OK=
echo "home=$(looseway "$a")"
h="$a/write/$v/` + writeHelperName + `"
echo "path=$h"
if [ -x "$h" ]; then
  if o=$("$h" version 2>&1); then echo runs=1; else echo "failed=$(printf %s "$o" | head -n 1)"; fi
fi
exit 0`

// writeUnpackSh takes the helper on standard input into
// ~/.activate-studio/write/$1, whole or not at all, once its digest is $2
// and it runs there. Versions unused for a month go.
const writeUnpackSh = `set -e
umask 077
a="$HOME/.activate-studio"; mkdir -p -- "$a"; chmod 700 -- "$a"
d="$a/write"; mkdir -p -- "$d"; chmod 700 -- "$d"
t=$(mktemp -d "$d/.part-XXXXXX")
trap 'rm -rf -- "$t"' EXIT
cat > "$t/` + writeHelperName + `"
s=$( { sha256sum < "$t/` + writeHelperName + `" 2>/dev/null || shasum -a 256 < "$t/` + writeHelperName + `" 2>/dev/null; } | cut -c1-64)
if [ -n "$s" ] && [ "$s" != "$2" ]; then echo "it did not arrive whole" >&2; exit 3; fi
chmod 700 "$t/` + writeHelperName + `"
if ! o=$("$t/` + writeHelperName + `" version 2>&1); then printf 'it does not run there: %s\n' "$o" | head -n 1 >&2; exit 5; fi
rm -rf -- "$d/$1"
mv -- "$t" "$d/$1"
trap - EXIT
find "$d" -mindepth 1 -maxdepth 1 -type d ! -name "$1" -mtime +30 -exec rm -rf -- {} + 2>/dev/null || :
echo installed`

// writeVersion is the folder name of a platform's write helper: the
// platform and the start of its digest; "" with none here.
func (m *Module) writeVersion(platform string) (version, path, sum string) {
	p := m.helperFile(platform, writeHelperName)
	if p == "" {
		return "", "", ""
	}
	sum, _, err := m.digestOf(p)
	if err != nil {
		return "", "", ""
	}
	return platform + "-" + sum[:12], p, sum
}

// canWrite is why the write helper cannot run on a system uname -sm names,
// "" where it can.
func (m *Module) canWrite(uname string) string {
	if m.noWriteTool != "" {
		return m.noWriteTool
	}
	p := helperPlatform(uname)
	if v, _, _ := m.writeVersion(p); v == "" || !strings.HasPrefix(p, "linux-") {
		return "The Studio's write helper is not built for " + strings.TrimSpace(uname) + "."
	}
	return ""
}

// writeTool is the helper that writes in a library, over a session of
// key's at site: the one there, or the Studio's copy, sent first. Where it
// cannot run, Path is empty and Why says why.
func (m *Module) writeTool(ctx context.Context, owner string, d *libraries.UserDef, site, key string) writehelper.Tool {
	k := keyID(key) + "|" + site + "|" + owner + "/" + d.ID + "|" + spaceRoot(d)
	m.toolsMu.Lock()
	kept, ok := m.tools[k]
	m.toolsMu.Unlock()
	if ok && time.Since(kept.at) < writeToolFor {
		return kept.tool
	}
	var t writehelper.Tool
	// One look at a time, so one copy is sent.
	m.queued(keyID(key)+"|"+site+"|write", func() {
		m.toolsMu.Lock()
		kept, ok := m.tools[k]
		m.toolsMu.Unlock()
		if ok && time.Since(kept.at) < writeToolFor {
			t = kept.tool
			return
		}
		var lasting bool
		t, lasting = m.findWriteTool(ctx, owner, d, site, key)
		t.Missing = func() {
			m.toolsMu.Lock()
			defer m.toolsMu.Unlock()
			delete(m.tools, k)
		}
		if !lasting {
			return
		}
		m.toolsMu.Lock()
		if m.tools == nil {
			m.tools = map[string]foundTool{}
		}
		m.tools[k] = foundTool{t, time.Now()}
		m.toolsMu.Unlock()
	})
	return t
}

// findWriteTool looks for the helper there, and sends it; lasting is
// whether what it found holds for a while.
func (m *Module) findWriteTool(ctx context.Context, owner string, d *libraries.UserDef, site, key string) (writehelper.Tool, bool) {
	name := remote.SiteName(site)
	amd, _, _ := m.writeVersion("linux-amd64")
	arm, _, _ := m.writeVersion("linux-arm64")
	r, err := m.Pool.Run(ctx, site, key, []string{"bash", "-c", writeProbeSh, "_", spaceRoot(d), amd, arm, grantsLine(m.writeShares(owner, d.ID))}, 90*time.Second)
	if err != nil {
		return writehelper.Tool{Why: messageOf(m.sessionErr(name, key, err))}, false
	}
	kv := keyValues(r.Stdout)
	t := writehelper.Tool{Pin: m.pinOf(owner, d, kv["id"], kv["id"] != "" && kv["above"] == "")}
	if t.Why = m.canWrite(kv["platform"]); t.Why == "" {
		switch {
		case kv["home"] == "none":
			t.Why = "Your ~/.activate-studio could not be made on " + name + ", so the Studio's write helper cannot run there."
		case kv["home"] != "":
			t.Why = helperHomeWhy(name, kv["home"])
		case !strings.HasPrefix(kv["path"], "/"):
			return writehelper.Tool{Pin: t.Pin, Why: name + " did not say where the Studio's write helper goes: " + lastOrExit(remote.QuietStderr(r.Stderr), r.Code)}, false
		case kv["runs"] != "1":
			if err := m.sendWriteTool(ctx, site, key, helperPlatform(kv["platform"])); err != nil {
				t.Why = messageOf(err)
				break
			}
			fallthrough
		default:
			t.Path = kv["path"]
		}
	}
	m.settle(owner, d, site, key, t)
	return t, true
}

// lookAgain has the next write in a library look for its helper again.
func (m *Module) lookAgain(owner, id string) {
	m.toolsMu.Lock()
	defer m.toolsMu.Unlock()
	for k := range m.tools {
		if strings.Contains(k, "|"+owner+"/"+id+"|") {
			delete(m.tools, k)
		}
	}
}

func messageOf(err error) string {
	if e, ok := err.(*apierr.Error); ok {
		return e.Message
	}
	return err.Error()
}

// sendWriteTool puts the helper for a platform in the person's
// ~/.activate-studio.
func (m *Module) sendWriteTool(ctx context.Context, site, key, platform string) error {
	version, p, sum := m.writeVersion(platform)
	f, err := os.Open(p)
	if err != nil {
		return apierr.New(409, "The Studio has no write helper for %s.", platform)
	}
	defer f.Close()
	r, err := m.Pool.RunOnce(ctx, site, key, []string{"bash", "-c", writeUnpackSh, "_", version, sum}, 5*time.Minute, f)
	if err != nil {
		return apierr.New(502, "The Studio's write helper could not be copied to %s: %s", remote.SiteName(site), err.Error())
	}
	if r.Code != 0 || !strings.Contains(r.Stdout, "installed") {
		return apierr.New(502, "The Studio's write helper could not be put in your ~/.activate-studio on %s: %s.", remote.SiteName(site), strings.TrimSuffix(lastOrExit(remote.QuietStderr(r.Stderr), r.Code), "."))
	}
	return nil
}

// pinOf is the folder a library's writes are for: the one recorded, or
// the one there now when none is. A folder with another inode is taken
// only where no other account could have put it there, and only with the
// same owner: the person made it again, or its file system numbers its
// folders anew. steady is whether no other account could have: neither on
// the way to the folder as it is nor on the way its path is written.
func (m *Module) pinOf(owner string, d *libraries.UserDef, now string, steady bool) string {
	ino, uid, ok := strings.Cut(now, ":")
	if !ok || ino == "" || uid == "" {
		return d.Pin
	}
	if d.Shared != nil {
		// The person who shared it may make it again.
		now = "0:" + uid
	}
	if d.Pin == now {
		return now
	}
	if d.Pin != "" {
		_, was, _ := strings.Cut(d.Pin, ":")
		if !steady || was != uid || d.Shared != nil {
			return d.Pin
		}
	}
	was := d.Pin
	got := m.update(owner, d.ID, func(x *libraries.UserDef) {
		if x.Pin == was {
			x.Pin = now
		}
	})
	if got != nil {
		return got.Pin
	}
	return now
}

// settle has who else can change a library's folder read, in the
// background, for one it was never read for, and for one whose record says
// otherwise than where its writes go now.
func (m *Module) settle(owner string, d *libraries.UserDef, site, key string, t writehelper.Tool) {
	if d.Shared != nil {
		return
	}
	checked := d.Guard && (d.ReadOnly == nil || t.Path == "") && (d.Loose == nil || d.ReadOnly != nil || t.Path != "")
	if checked {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(m.Background(), 2*time.Minute)
		defer cancel()
		m.recheck(ctx, owner, d, site, key)
	}()
}

// recheck reads again who else can change a library's folder, and keeps it.
func (m *Module) recheck(ctx context.Context, owner string, d *libraries.UserDef, site, key string) {
	if f := m.foundAt(ctx, probeHost{Site: site, Key: key, Name: remote.SiteName(site)}, spaceRoot(d), m.writeShares(owner, d.ID)); f != nil {
		m.update(owner, d.ID, f.keep)
	}
}
