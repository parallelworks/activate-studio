package remote

import (
	"os"
	"path/filepath"
	"strings"
)

// ConfigHosts is the names an ssh config file offers as hosts: those of its
// Host entries that are no pattern, in the file's order, each once. What an
// entry sets is not read, and neither is a Match block. An Include is
// followed at the top of the file and under "Host *", where ssh always
// follows it. A file that is not there, or cannot be read, offers none.
func ConfigHosts(file string) []string {
	c := &configHosts{dir: filepath.Dir(file), seen: map[string]bool{}, names: []string{}}
	c.read(file, 0)
	return c.names
}

const (
	// maxInclude is how deep ssh follows Include.
	maxInclude    = 16
	maxConfigSize = 1 << 20
)

type configHosts struct {
	dir   string // where an Include that is not a whole path starts
	seen  map[string]bool
	names []string
}

func (c *configHosts) read(file string, depth int) {
	fi, err := os.Stat(file)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxConfigSize {
		return
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return
	}
	always := true
	for _, line := range strings.Split(string(raw), "\n") {
		words := configWords(line)
		if len(words) < 2 {
			continue
		}
		args := words[1:]
		switch strings.ToLower(words[0]) {
		case "match":
			always = len(args) == 1 && strings.EqualFold(args[0], "all")
		case "host":
			always = len(args) == 1 && args[0] == "*"
			for _, name := range args {
				if !c.seen[name] && !strings.Contains(name, "@") && SSHDestination.MatchString(name) {
					c.seen[name] = true
					c.names = append(c.names, name)
				}
			}
		case "include":
			if !always || depth >= maxInclude {
				continue
			}
			for _, p := range args {
				if strings.HasPrefix(p, "~/") {
					home, err := os.UserHomeDir()
					if err != nil {
						continue
					}
					p = filepath.Join(home, p[2:])
				} else if !filepath.IsAbs(p) {
					p = filepath.Join(c.dir, p)
				}
				files, _ := filepath.Glob(p)
				for _, f := range files {
					c.read(f, depth+1)
				}
			}
		}
	}
}

// configWords is a line's keyword and arguments, as ssh splits them: at
// spaces, or a = after the keyword, with quotes taken off and a comment
// left out.
func configWords(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" || line[0] == '#' {
		return nil
	}
	i := strings.IndexAny(line, " \t=")
	if i <= 0 {
		return nil
	}
	words := []string{line[:i]}
	rest := strings.TrimLeft(line[i:], " \t")
	if strings.HasPrefix(rest, "=") {
		rest = rest[1:]
	}
	for rest = strings.TrimLeft(rest, " \t"); rest != "" && rest[0] != '#'; rest = strings.TrimLeft(rest, " \t") {
		if rest[0] == '"' {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				return words
			}
			words, rest = append(words, rest[1:1+end]), rest[end+2:]
			continue
		}
		end := strings.IndexAny(rest, " \t")
		if end < 0 {
			end = len(rest)
		}
		words, rest = append(words, rest[:end]), rest[end:]
	}
	return words
}
