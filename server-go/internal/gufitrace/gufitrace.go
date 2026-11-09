// Package gufitrace is gufiTrace.ts and gufiLog.ts: the GUFI commands a
// request ran, so Search, Query and the assistant's tool blocks can show
// them, and the GUFI command log (logs/gufi-commands.log) when Settings
// turns it on. A route wraps its work with Start; each place that runs
// GUFI calls Note with how the command ended.
package gufitrace

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Command is one command as a view shows it (GufiCommand).
type Command struct {
	Engine          string  `json:"engine"`
	Site            string  `json:"site,omitempty"`
	Shell           string  `json:"shell,omitempty"`
	Command         string  `json:"command"`
	MS              *int64  `json:"ms,omitempty"`
	At              string  `json:"at,omitempty"`
	Exit            *int    `json:"exit,omitempty"`
	Failed          string  `json:"failed,omitempty"`
	Stderr          *string `json:"stderr,omitempty"`
	StderrCut       bool    `json:"stderrCut,omitempty"`
	Expected        int     `json:"expected,omitempty"`
	ExpectedExample string  `json:"expectedExample,omitempty"`
}

const (
	maxCommands = 40
	stderrKeep  = 8 * 1024
)

type trace struct {
	source string
	mu     sync.Mutex
	list   *[]Command
}

type key struct{}

// Start begins a trace of source's commands under ctx.
func Start(ctx context.Context, source string) (context.Context, func() []Command) {
	list := []Command{}
	t := &trace{source: source, list: &list}
	return context.WithValue(ctx, key{}, t), func() []Command {
		t.mu.Lock()
		defer t.mu.Unlock()
		return append([]Command{}, (*t.list)...)
	}
}

// Source names ctx's commands for the log unless a trace already does.
func Source(ctx context.Context, source string) context.Context {
	if ctx.Value(key{}) != nil {
		return ctx
	}
	return context.WithValue(ctx, key{}, &trace{source: source})
}

// Run is how a program ended (GufiRun).
type Run struct {
	Start     time.Time
	Elapsed   time.Duration // 0: since Start
	Code      *int          // nil: a signal or failure ended it
	Signal    string
	Error     string
	Stderr    *string
	Cut       bool
	Engine    string // default gufi_query
	Site      string
	SiteName  string // the site as messages name it
	ShellLine string // how to reach the site from a terminal
	Script    *string
	Hide      bool   // show: false
	Source    string // outside a trace
}

var expected = regexp.MustCompile(`no such table: (?:\w+\.)?(?:words|gvec|gchunks|gvecmodel|gvec_rowids|ext_bm25_by_file|ext_vectors_by_file)\b`)

// SplitStderr is splitStderr.
func SplitStderr(text string) (other string, n int, example string) {
	var keep []string
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if expected.MatchString(line) {
			n++
			if example == "" {
				example = utf16Prefix(line, 500)
			}
		} else {
			keep = append(keep, line)
		}
	}
	return strings.Join(keep, "\n"), n, example
}

func utf16Prefix(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_/.,:=@+%-]+$`)

// ShellQuote is gufiTrace.ts's shellQuote.
func ShellQuote(a string) string {
	if a == "\x1e" {
		return `$'\x1e'`
	}
	if shellSafe.MatchString(a) {
		return a
	}
	return "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
}

var firstLine = regexp.MustCompile(`^[^\n]*\n`)

// Log is where the command log goes and whether it is on; set by the server.
type Log struct {
	File string
	On   func() bool

	mu   sync.Mutex
	size int64
}

// Default is the log Note writes to; nil until the server sets it.
var Default *Log

// NewLog is the log in the Studio's log folder: STUDIO_DATA_DIR/logs, else
// INDEX_BASE/logs.
func NewLog(dataDir, indexBase string, on func() bool) *Log {
	dir := filepath.Join(indexBase, "logs")
	if dataDir != "" {
		dir = filepath.Join(dataDir, "logs")
	}
	return &Log{File: filepath.Join(dir, "gufi-commands.log"), On: on, size: -1}
}

// Note is noteGufi.
func Note(ctx context.Context, argv []string, o Run) {
	t, _ := ctx.Value(key{}).(*trace)
	var list *[]Command
	if t != nil && !o.Hide {
		list = t.list
	}
	log := Default
	logging := log != nil && log.On != nil && log.On()
	if !logging && list == nil {
		return
	}
	words := make([]string, len(argv))
	for i, a := range argv {
		words[i] = ShellQuote(a)
	}
	line := strings.Join(words, " ")
	if o.Script != nil {
		line += " <<'SQL'\n" + *o.Script + "\nSQL"
	}
	engine := o.Engine
	if engine == "" {
		engine = "gufi_query"
	}
	rec := Command{Engine: engine, Command: line, Site: o.SiteName, Shell: o.ShellLine}
	if !o.Start.IsZero() {
		ms := o.Elapsed.Milliseconds()
		if o.Elapsed == 0 {
			ms = time.Since(o.Start).Milliseconds()
		}
		rec.MS = &ms
		rec.At = o.Start.UTC().Format("2006-01-02T15:04:05.000Z")
	} else if o.Elapsed > 0 {
		ms := o.Elapsed.Milliseconds()
		rec.MS = &ms
	}
	if o.Code != nil {
		rec.Exit = o.Code
	} else if o.Signal != "" {
		rec.Failed = "ended by " + o.Signal
	} else if o.Error != "" {
		rec.Failed = o.Error
	}
	if o.Stderr != nil {
		s := *o.Stderr
		if o.Cut {
			s = firstLine.ReplaceAllString(s, "")
		}
		other, n, ex := SplitStderr(s)
		long := len(other) > stderrKeep
		if long {
			other = firstLine.ReplaceAllString(other[len(other)-stderrKeep:], "")
		}
		rec.Stderr = &other
		rec.StderrCut = o.Cut || long
		if n > 0 {
			rec.Expected, rec.ExpectedExample = n, ex
		}
	}
	if list != nil {
		t.mu.Lock()
		if len(*list) < maxCommands {
			*list = append(*list, rec)
		}
		t.mu.Unlock()
	}
	if logging {
		source := o.Source
		if t != nil {
			source = t.source
		}
		if source == "" {
			source = "other"
		}
		log.append(rec, source)
	}
}

func (l *Log) append(c Command, source string) {
	const maxBytes, commandKeep, stderrLogKeep = 10 << 20, 16 << 10, 2 << 10
	type entry struct {
		Time            string  `json:"time"`
		Source          string  `json:"source"`
		Engine          string  `json:"engine"`
		Site            string  `json:"site,omitempty"`
		MS              *int64  `json:"ms"`
		Exit            *int    `json:"exit"`
		Failed          string  `json:"failed,omitempty"`
		Command         string  `json:"command"`
		CommandCut      bool    `json:"commandCut,omitempty"`
		Stderr          *string `json:"stderr"`
		StderrCut       bool    `json:"stderrCut,omitempty"`
		Expected        int     `json:"expected,omitempty"`
		ExpectedExample string  `json:"expectedExample,omitempty"`
	}
	at := c.At
	if at == "" {
		at = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	}
	e := entry{Time: at, Source: source, Engine: c.Engine, Site: c.Site, MS: c.MS, Exit: c.Exit, Failed: c.Failed, Command: c.Command,
		StderrCut: c.StderrCut, Expected: c.Expected, ExpectedExample: c.ExpectedExample}
	if len(c.Command) > commandKeep {
		e.Command, e.CommandCut = c.Command[:commandKeep], true
	}
	if c.Stderr != nil {
		s := *c.Stderr
		if len(s) > stderrLogKeep {
			s, e.StderrCut = s[len(s)-stderrLogKeep:], true
		}
		e.Stderr = &s
	}
	b, _ := json.Marshal(e)
	b = append(b, '\n')
	l.mu.Lock()
	defer l.mu.Unlock()
	os.MkdirAll(filepath.Dir(l.File), 0o755)
	if l.size < 0 {
		if st, err := os.Stat(l.File); err == nil {
			l.size = st.Size()
		} else {
			l.size = 0
		}
	}
	if l.size >= maxBytes {
		os.Rename(l.File, l.File+".1")
		l.size = 0
	}
	f, err := os.OpenFile(l.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	if n, err := f.Write(b); err == nil {
		l.size += int64(n)
	}
}
