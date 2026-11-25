package extdb

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// FsidEnv names the site's file system id for the files of this
// computer's file systems, as the site writes it in extra_info.
const FsidEnv = "STUDIO_FSID"

// Fsid is the extra_info of the files at path: the site's id where it set
// one (FsidEnv), and otherwise this computer's name and the mount point of
// the file system path is on ("host:/mnt/data"). An inode is unique within
// one file system, so either qualifies it.
func Fsid(path string) string {
	if v := strings.TrimSpace(os.Getenv(FsidEnv)); v != "" {
		return v
	}
	host, _ := os.Hostname()
	if host == "" {
		host = "localhost"
	}
	return host + ":" + MountPoint(path)
}

// MountPoint is the mount point of the file system path is on: the
// longest mount point in /proc/self/mountinfo that holds path, "/" where
// none is known (a system with no /proc).
func MountPoint(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "/"
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return "/"
	}
	defer f.Close()
	best := "/"
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		// id parent major:minor root mountpoint options ...
		fields := strings.Fields(sc.Text())
		if len(fields) < 5 {
			continue
		}
		mp := unescapeMount(fields[4])
		if (abs == mp || strings.HasPrefix(abs, strings.TrimSuffix(mp, "/")+"/")) && len(mp) > len(best) {
			best = mp
		}
	}
	return best
}

// unescapeMount undoes mountinfo's octal escapes (\040 for a space).
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			n := 0
			ok := true
			for _, c := range s[i+1 : i+4] {
				if c < '0' || c > '7' {
					ok = false
					break
				}
				n = n*8 + int(c-'0')
			}
			if ok {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
