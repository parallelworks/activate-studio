package extractfiles

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/parallelworks/activate-studio/server-go/internal/extdb"
	"github.com/parallelworks/activate-studio/server-go/internal/extract/cli"
)

const usage = `usage: index-extract <command> [options]

commands:
  files    read the files of a list and write their text to an extraction artifact
  spread   put the records of extraction artifacts into a spread tree
  shards   write each folder's permission shards of an index from a spread tree
  schema   print the SQL of the tables the artifact and the index's external databases hold

index-extract files --list FILE|- --out ARTIFACT.db [--root DIR] [--strip PREFIX] [--delim D]
  [--extra-info S] [--shard K/N] [--retry] [--extract-cache DIR] [--jobs N] [--progress]
  [--no-ocr] [--no-documents] [--pdf-reader auto|pdftotext|pypdf] [--soffice PATH]
  [--extractors FILE] [--report FILE]
index-extract spread --spread DIR --fsid NAME [--prefix studio] ARTIFACT.db...
index-extract shards --spread DIR --fsid NAME --index DIR [--prefix studio] [--extra-info S]
`

// Main is the index-extract command: args without the program name; the
// exit status is 0, 1 when the run failed, 2 for a bad command line.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "files":
		return filesMain(args[1:], stdin, stdout, stderr)
	case "spread", "shards":
		return loadMain(args[0], args[1:], stdout, stderr)
	case "schema":
		fmt.Fprint(stdout, extdb.Schema)
		return 0
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	fmt.Fprintf(stderr, "index-extract: unknown command %q\n%s", args[0], usage)
	return 2
}

func filesMain(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("index-extract files", flag.ContinueOnError)
	fs.SetOutput(stderr)
	list := fs.String("list", "-", "the file list, - for standard input: one line per file, inode, mtime, size and path")
	out := fs.String("out", "", "the extraction artifact to write (made if missing; a run goes on from what it holds)")
	root := fs.String("root", "", "the folder a relative path is found in (default: the working directory)")
	strip := fs.String("strip", "", "a leading path to remove from each listed path, such as the index's top folder")
	delim := fs.String("delim", `\x1e`, "the list's column separator (GUFI's default)")
	extra := fs.String("extra-info", "", "the extra_info written with every record")
	shard := fs.String("shard", "", "K/N: read only the files whose inode modulo N is K, as one job of an array of N")
	retry := fs.Bool("retry", false, "read again every file the artifact holds without text (skipped, empty, missing, changed or failed)")
	cache := fs.String("extract-cache", "", "an extract cache to use and keep (default: a temporary one)")
	ef := cli.AddExtractorFlags(fs)
	progress := fs.Bool("progress", false, "print @progress lines on stdout")
	report := fs.String("report", "", "also write the summary to this JSON file")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" || fs.NArg() > 0 {
		fmt.Fprint(stderr, usage)
		return 2
	}
	d, err := ParseDelim(*delim)
	if err != nil {
		fmt.Fprintln(stderr, "index-extract:", err)
		return 2
	}
	var sh Shard
	if *shard != "" {
		if sh, err = ParseShard(*shard); err != nil {
			fmt.Fprintln(stderr, "index-extract:", err)
			return 2
		}
	}
	in, total := stdin, -1
	if *list != "-" {
		f, err := os.Open(*list)
		if err != nil {
			fmt.Fprintln(stderr, "index-extract:", err)
			return 2
		}
		defer f.Close()
		in = f
		if *progress {
			total = countLines(*list)
		}
	}
	x, rc := ef.Extractor(stderr)
	if x == nil {
		return rc
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var mu sync.Mutex
	o := Options{
		List: in, Total: total, Delim: d,
		Source: Local{Root: *root, Strip: *strip}, Strip: *strip,
		ExtraInfo: *extra, Shard: sh, Out: *out, Retry: *retry, Cache: *cache, Jobs: ef.Jobs(),
		Log: func(s string) { mu.Lock(); fmt.Fprintln(stderr, s); mu.Unlock() },
	}
	if *progress {
		o.Progress = func(done, total int, b int64) {
			line, _ := json.Marshal(map[string]any{"phase": "files", "done": done, "total": total, "bytes": b})
			mu.Lock()
			fmt.Fprintf(stdout, "@progress %s\n", line)
			mu.Unlock()
		}
	}
	s, err := Run(ctx, x, o)
	b, _ := json.Marshal(s)
	mu.Lock()
	fmt.Fprintf(stdout, "%s\n", b)
	mu.Unlock()
	if *report != "" {
		if werr := os.WriteFile(*report, b, 0o644); werr != nil && err == nil {
			err = werr
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "index-extract:", err)
		return 1
	}
	if s.Stopped {
		return 1
	}
	return 0
}

// countLines is a list file's line count, for progress; -1 when it
// cannot be read.
func countLines(file string) int {
	f, err := os.Open(file)
	if err != nil {
		return -1
	}
	defer f.Close()
	n := 0
	r := bufio.NewReaderSize(f, 1<<20)
	buf := make([]byte, 1<<20)
	last := byte('\n')
	for {
		k, err := r.Read(buf)
		n += bytes.Count(buf[:k], []byte{'\n'})
		if k > 0 {
			last = buf[k-1]
		}
		if err != nil {
			break
		}
	}
	if last != '\n' {
		n++
	}
	return n
}

// loadMain is the spread and shards commands.
func loadMain(cmd string, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("index-extract "+cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	top := fs.String("spread", "", "the spread tree's top folder")
	fsid := fs.String("fsid", "", "the name of the file system or store the records are of, a folder of the spread tree")
	prefix := fs.String("prefix", extdb.Prefix, "the prefix family")
	index := fs.String("index", "", "shards: the index whose folders get the shards")
	extra := fs.String("extra-info", "", "shards: the extra_info of the records to copy")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	s := extdb.Spread{Top: *top, Prefix: *prefix, Fsid: *fsid}
	if err := s.Check(); err != nil {
		fmt.Fprintln(stderr, "index-extract:", err)
		return 2
	}
	var out any
	switch cmd {
	case "spread":
		if fs.NArg() == 0 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		var total extdb.SpreadStats
		for _, a := range fs.Args() {
			st, err := extdb.LoadSpread(s, a)
			total.Files += st.Files
			total.Chunks += st.Chunks
			total.DBs += st.DBs
			if err != nil {
				fmt.Fprintf(stderr, "index-extract: %s: %v\n", a, err)
				return 1
			}
		}
		out = total
	case "shards":
		if *index == "" || fs.NArg() > 0 {
			fmt.Fprint(stderr, usage)
			return 2
		}
		st, err := extdb.FillShards(s, *index, *extra)
		if err != nil {
			fmt.Fprintln(stderr, "index-extract:", err)
			return 1
		}
		out = st
	}
	b, _ := json.Marshal(out)
	fmt.Fprintf(stdout, "%s\n", b)
	return 0
}
