// Package wslpath turns a Windows path typed into the Studio under WSL
// (Windows Subsystem for Linux) into the Linux path of the same folder, so
// C:\Users\me\Documents names /mnt/c/Users/me/Documents and
// \\wsl.localhost\Ubuntu\home\me names /home/me. Outside WSL it changes
// nothing. docs/WINDOWS.md describes the setup.
package wslpath

import (
	"bufio"
	"os"
	"strings"
	"sync"
)

var (
	once sync.Once
	// detected and mountRoot are WSL's: set once, or by tests through Set.
	detected  bool
	mountRoot = "/mnt/"
)

// InWSL reports whether this process runs under WSL: WSL sets
// WSL_DISTRO_NAME for the processes it starts, and registers the interop
// handler that runs Windows programs.
func InWSL() bool {
	once.Do(func() {
		if os.Getenv("WSL_DISTRO_NAME") != "" {
			detected = true
		} else if _, err := os.Stat("/proc/sys/fs/binfmt_misc/WSLInterop"); err == nil {
			detected = true
		}
		if detected {
			mountRoot = automountRoot("/etc/wsl.conf")
		}
	})
	return detected
}

// Set fixes the detection, for tests.
func Set(inWSL bool, root string) {
	once.Do(func() {})
	detected, mountRoot = inWSL, root
}

// automountRoot is [automount] root in wsl.conf, where Windows drives are
// mounted (default /mnt/).
func automountRoot(conf string) string {
	f, err := os.Open(conf)
	if err != nil {
		return "/mnt/"
	}
	defer f.Close()
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(l, "[") && strings.HasSuffix(l, "]") {
			section = strings.ToLower(strings.TrimSpace(l[1 : len(l)-1]))
			continue
		}
		k, v, ok := strings.Cut(l, "=")
		if section == "automount" && ok && strings.EqualFold(strings.TrimSpace(k), "root") {
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if strings.HasPrefix(v, "/") {
				return strings.TrimSuffix(v, "/") + "/"
			}
		}
	}
	return "/mnt/"
}

// FromWindows is p as a Linux path when p is a Windows path and this is
// WSL: a drive path (C:\a\b, C:/a/b, C:) or a path into this or another
// distribution through \\wsl.localhost\Name\ or \\wsl$\Name\. ok is false
// for anything else, which the caller keeps as typed.
func FromWindows(p string) (string, bool) {
	if !InWSL() {
		return p, false
	}
	p = strings.TrimSpace(p)
	if len(p) >= 2 && p[1] == ':' && isLetter(p[0]) && (len(p) == 2 || p[2] == '\\' || p[2] == '/') {
		rest := strings.Trim(strings.ReplaceAll(p[2:], `\`, "/"), "/")
		out := mountRoot + strings.ToLower(p[:1])
		if rest != "" {
			out += "/" + rest
		}
		return out, true
	}
	q := strings.ReplaceAll(p, `\`, "/")
	for _, pre := range []string{"//wsl.localhost/", "//wsl$/"} {
		if len(q) > len(pre) && strings.EqualFold(q[:len(pre)], pre) {
			// The distribution's name, then the path inside it.
			_, inside, _ := strings.Cut(q[len(pre):], "/")
			return "/" + strings.Trim(inside, "/"), true
		}
	}
	return p, false
}

func isLetter(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
