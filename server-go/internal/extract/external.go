package extract

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/extract/pytext"
)

// External is a contributed extractor: a command that reads one file and
// writes its text to stdout, for a format the binary does not read, or in
// place of the built-in reader for one it does. It needs no rebuild, so
// an extractor can be added on one installation and shared as a command
// and a line of configuration.
//
// The contract:
//
//   - The command is Command with "{path}" in any argument replaced by the
//     file's absolute path; with no "{path}" the path is appended as the
//     last argument. No shell is involved.
//   - stdin is empty and the working directory is a scratch directory
//     removed afterwards. The environment is the Studio's, plus
//     OMP_THREAD_LIMIT=1.
//   - Exit status 0 with text on stdout is the file's text: UTF-8 (invalid
//     bytes become U+FFFD), "\r\n" read as "\n", and the first MaxText
//     characters kept. Exit 0 with nothing but whitespace means the file
//     has no text; any other status is a failure. Either way no cache
//     entry is written and the next pass tries again; the first lines of
//     stderr go to the log.
//   - The command is stopped (with its process group, where the platform
//     has them) after TimeoutSeconds, 120 when zero.
//
// Its entries are plain text, current while they are at least as new as
// the file, like every entry not from downmark.
type External struct {
	// Name labels the extractor in the log and the summary.
	Name string `json:"name"`
	// Suffixes are the file name extensions it reads (".dwg"); case
	// does not matter. The text formats (TextSuffixes) cannot be taken.
	Suffixes []string `json:"suffixes"`
	// Command is the program and its arguments.
	Command []string `json:"command"`
	// TimeoutSeconds bounds one file; 120 when zero, at most 3600.
	TimeoutSeconds int `json:"timeoutSeconds,omitempty"`
}

const (
	externalDefaultTimeout = 120 * time.Second
	externalMaxTimeout     = time.Hour
	// externalMaxOutput bounds what is read from stdout: MaxText
	// characters are at most four bytes each.
	externalMaxOutput = 4*MaxText + 4
)

// ExternalConfig is the file LoadExternal reads:
//
//	{"extractors": [
//	  {"name": "dwg", "suffixes": [".dwg"], "command": ["dwg2txt", "{path}"], "timeoutSeconds": 60}
//	]}
type ExternalConfig struct {
	Extractors []External `json:"extractors"`
}

// LoadExternal reads a configuration of contributed extractors.
func LoadExternal(file string) ([]External, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var c ExternalConfig
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return nil, fmt.Errorf("extract: %s: %w", file, err)
	}
	for i := range c.Extractors {
		if err := c.Extractors[i].check(); err != nil {
			return nil, fmt.Errorf("extract: %s: %w", file, err)
		}
	}
	return c.Extractors, nil
}

func (e *External) label() string {
	if e.Name != "" {
		return e.Name
	}
	if len(e.Command) > 0 {
		return e.Command[0]
	}
	return "extractor"
}

func (e *External) check() error {
	if len(e.Command) == 0 || strings.TrimSpace(e.Command[0]) == "" {
		return fmt.Errorf("extractor %s: no command", e.label())
	}
	if len(e.Suffixes) == 0 {
		return fmt.Errorf("extractor %s: no suffixes", e.label())
	}
	for _, s := range e.Suffixes {
		if n := normSuffix(s); n == "" || n == "." || strings.ContainsAny(n[1:], `./\`) {
			return fmt.Errorf("extractor %s: bad suffix %q", e.label(), s)
		}
	}
	if e.TimeoutSeconds < 0 || time.Duration(e.TimeoutSeconds)*time.Second > externalMaxTimeout {
		return fmt.Errorf("extractor %s: timeoutSeconds must be between 0 and %d", e.label(), int(externalMaxTimeout/time.Second))
	}
	return nil
}

func (e *External) timeout() time.Duration {
	if e.TimeoutSeconds == 0 {
		return externalDefaultTimeout
	}
	return time.Duration(e.TimeoutSeconds) * time.Second
}

func (e *External) argv(path string) []string {
	out := make([]string, 0, len(e.Command)+1)
	used := false
	for _, a := range e.Command {
		if strings.Contains(a, "{path}") {
			a = strings.ReplaceAll(a, "{path}", path)
			used = true
		}
		out = append(out, a)
	}
	if !used {
		out = append(out, path)
	}
	return out
}

// RunExternal runs a contributed extractor on one file.
func (x *Extractor) RunExternal(ctx context.Context, e *External, path string) (string, error) {
	dir, err := os.MkdirTemp("", "studio-extractor-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	argv := e.argv(path)
	cmd, hctx, cancel := helper(ctx, e.timeout(), argv[0], argv[1:]...)
	defer cancel()
	cmd.Dir = dir
	cmd.Stdin = nil
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &limitWriter{w: &stdout, n: externalMaxOutput}
	cmd.Stderr = &limitWriter{w: &stderr, n: 4096}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	werr := cmd.Wait()
	if hctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s: timed out after %s", e.label(), e.timeout())
	}
	if werr != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.IndexByte(msg, '\n'); i >= 0 {
			msg = msg[:i]
		}
		if msg != "" {
			return "", fmt.Errorf("%s: %v: %.200s", e.label(), werr, msg)
		}
		return "", fmt.Errorf("%s: %v", e.label(), werr)
	}
	text := pytext.ReadText(stdout.Bytes(), MaxText)
	if pytext.Strip(text) == "" {
		return "", errNoText
	}
	return text, nil
}

type limitWriter struct {
	w io.Writer
	n int
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.n > 0 {
		k := min(len(p), l.n)
		l.w.Write(p[:k])
		l.n -= k
	}
	return len(p), nil
}
