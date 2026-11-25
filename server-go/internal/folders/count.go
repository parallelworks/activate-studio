package folders

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// CountFor is how long a count looks at a folder before it answers with
// what it has so far.
const CountFor = 5 * time.Second

// Kind is how many files of a kind a folder holds, by their extension.
type Kind struct {
	Name  string `json:"name"`
	Files int64  `json:"files"`
}

// Count is what a folder holds: its files, their size, and its main kinds,
// the most numerous first. Partial is a folder that took longer to look
// through than the count had: it holds at least this much.
type Count struct {
	Files   int64  `json:"files"`
	Bytes   int64  `json:"bytes"`
	Kinds   []Kind `json:"kinds"`
	Partial bool   `json:"partial,omitempty"`
}

// mainKinds is how many kinds a count names.
const mainKinds = 4

var sameKind = map[string]string{"jpeg": "jpg", "tiff": "tif", "htm": "html", "yml": "yaml", "markdown": "md"}

// kindOf is a file's extension as a kind, "" for none worth naming.
func kindOf(name string) string {
	i := strings.LastIndexByte(name, '.')
	if i <= 0 {
		return ""
	}
	ext := strings.ToLower(name[i+1:])
	if ext == "" || len(ext) > 8 {
		return ""
	}
	for _, c := range []byte(ext) {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return ""
		}
	}
	if s, ok := sameKind[ext]; ok {
		return s
	}
	return ext
}

func named(kinds map[string]int64) []Kind {
	out := []Kind{}
	for k, n := range kinds {
		out = append(out, Kind{k, n})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Files != out[j].Files {
			return out[i].Files > out[j].Files
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > mainKinds {
		out = out[:mainKinds]
	}
	return out
}

// CountLocal counts a folder of this computer, for at most CountFor; nil
// when it cannot be read, or where counting it would ask macOS for folders
// the person has not chosen (theirs, the home folder that holds
// Documents, Pictures and the rest, or a folder above it).
func CountLocal(ctx context.Context, dir string) *Count {
	dir, home := expand(dir)
	if !filepath.IsAbs(dir) {
		return nil
	}
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	if runtime.GOOS == "darwin" && holdsHome(dir, home) {
		return nil
	}
	if _, err := os.ReadDir(dir); err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, CountFor)
	defer cancel()
	c, kinds := &Count{}, map[string]int64{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil || !e.Type().IsRegular() {
			return nil
		}
		c.Files++
		if fi, err := e.Info(); err == nil {
			c.Bytes += fi.Size()
		}
		if k := kindOf(e.Name()); k != "" {
			kinds[k]++
		}
		return nil
	})
	c.Partial = err != nil
	c.Kinds = named(kinds)
	return c
}

func holdsHome(dir, home string) bool {
	if home == "" {
		return false
	}
	rel, err := filepath.Rel(dir, home)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// CountScript counts the files of the folder $1 in a POSIX shell, for at
// most $2 seconds, as ParseCount reads it. GNU find says sizes itself; with
// another one, stat does.
const CountScript = `p=$1
case "$p" in
'' | '~') p=$HOME ;;
'~/'*) p=$HOME/${p#??} ;;
esac
cd -- "$p" 2>/dev/null && [ -r . ] || exit 0
echo counted=1
list() {
  if find . -prune -printf '' 2>/dev/null; then exec find . -type f -printf '%s\t%f\n'
  else exec find . -type f -exec stat -f '%z	%N' {} +
  fi
}
{
  list < /dev/null 2>/dev/null &
  pid=$!
  ( sleep "$2"; kill "$pid" ) < /dev/null > /dev/null 2>&1 &
  late=$!
  wait "$pid"
  [ $? -gt 128 ] && echo more
  kill "$late" 2>/dev/null
} 2>/dev/null | awk -F '\t' '
$0 == "more" { more = 1; next }
$1 !~ /^[0-9]+$/ { next }
{
  files++; bytes += $1
  n = $2; sub(/.*\//, "", n)
  i = 0; while ((j = index(substr(n, i + 1), ".")) > 0) i += j
  if (i > 1) { e = tolower(substr(n, i + 1)); if (e ~ /^[a-z0-9]+$/ && length(e) <= 8) kinds[e]++ }
}
END {
  printf "files=%.0f\nbytes=%.0f\n", files, bytes
  if (more) print "more=1"
  for (e in kinds) if (shown++ < 2000) printf "kind=%d %s\n", kinds[e], e
}'
exit 0`

// ParseCount is CountScript's answer; nil when it could not open the folder.
func ParseCount(out string) *Count {
	c, kinds, ok := &Count{}, map[string]int64{}, false
	for _, line := range strings.Split(out, "\n") {
		k, v, _ := strings.Cut(strings.TrimRight(line, "\r"), "=")
		switch k {
		case "counted":
			ok = true
		case "files":
			c.Files, _ = strconv.ParseInt(v, 10, 64)
		case "bytes":
			c.Bytes, _ = strconv.ParseInt(v, 10, 64)
		case "more":
			c.Partial = true
		case "kind":
			n, ext, _ := strings.Cut(v, " ")
			if f, err := strconv.ParseInt(n, 10, 64); err == nil {
				if name := kindOf("x." + ext); name != "" {
					kinds[name] += f
				}
			}
		}
	}
	if !ok {
		return nil
	}
	c.Kinds = named(kinds)
	return c
}
