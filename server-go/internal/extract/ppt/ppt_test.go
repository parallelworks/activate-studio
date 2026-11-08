package ppt

import (
	"bytes"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

func header(verInst, rtype uint16, n int) []byte {
	b := binary.LittleEndian.AppendUint16(nil, verInst)
	b = binary.LittleEndian.AppendUint16(b, rtype)
	return binary.LittleEndian.AppendUint32(b, uint32(n))
}

func container(rtype uint16, inst uint16, kids ...[]byte) []byte {
	body := bytes.Join(kids, nil)
	return append(header(inst<<4|0x0F, rtype, len(body)), body...)
}

func chars(s string) []byte {
	var body []byte
	for _, u := range utf16.Encode([]rune(s)) {
		body = binary.LittleEndian.AppendUint16(body, u)
	}
	return append(header(0, recTextChars, len(body)), body...)
}

func bytesAtom(b []byte) []byte {
	return append(header(0, recTextBytes, len(b)), b...)
}

func raw(rtype uint16, body []byte) []byte {
	return append(header(0, rtype, len(body)), body...)
}

const (
	recDocument = 0x03E8
	recSlide    = 0x03EE
	recNotes    = 0x03F0
)

type streamCase struct {
	name   string
	stream []byte
	max    int
	want   string
}

// streamCases are checked against enrich.py's walk by
// TestStreamCasesAgainstPython; the wants are its output.
var streamCases = []streamCase{
	{"slides and notes", container(recDocument, 0,
		container(recListWithText, 0, chars("Title\rSecond line"), bytesAtom([]byte("Body caf\xe9"))),
		container(recListWithText, 1, chars("Click to edit Master title style")),
		container(recListWithText, 2, chars("Speaker notes"))),
		0, "Title\nSecond line\nBody caf\u00e9\nSpeaker notes"},
	{"masters and handouts skipped", cat(
		container(recMainMaster, 0, chars("master prompt")),
		container(recHandout, 0, chars("handout")),
		container(recSlide, 0, chars("on the slide"))),
		0, "on the slide"},
	{"repeats and field stand-ins", cat(
		container(recSlide, 0, chars("same"), chars("*"), chars("  same \x0b"), chars("other")),
		container(recNotes, 0, chars("same"))),
		0, "same\nother"},
	{"vertical tab is a line break", container(recSlide, 0, chars("a\x0bb")), 0, "a\nb"},
	{"other records ignored", cat(raw(0x0FBA, []byte("not text")), container(recSlide, 0, chars("x"))), 0, "x"},
	{"limit", container(recSlide, 0, chars("aaaa"), chars("bbbb"), chars("cccc")), 9, "aaaa\nbbbb"},
	{"record past the end", cat(chars("ok"), header(0, recTextChars, 1000), []byte{'h', 0, 'i', 0}), 0, "ok\nhi"},
	{"truncated header", cat(chars("ok"), []byte{1, 2, 3}), 0, "ok"},
	{"odd length", append(header(0, recTextChars, 3), 'A', 0, 'B'), 0, "A\uFFFD"},
	{"odd length after a high surrogate", append(header(0, recTextChars, 5), 'A', 0, 0x00, 0xD8, 'B'), 0, "A\uFFFD"},
	{"lone surrogate", append(header(0, recTextChars, 4), 0x00, 0xDC, 'A', 0), 0, "\uFFFDA"},
}

func TestStreamText(t *testing.T) {
	for _, c := range streamCases {
		if got := StreamText(c.stream, c.max); got != c.want {
			t.Errorf("%s:\n got %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestDepthLimit(t *testing.T) {
	inner := container(recSlide, 0, chars("deep"))
	for i := 0; i < 40; i++ {
		inner = container(recSlide, 0, inner)
	}
	if got := StreamText(inner, 0); got != "" {
		t.Errorf("text below 32 levels was read: %q", got)
	}
	shallow := chars("shallow")
	for i := 0; i < 30; i++ {
		shallow = container(recSlide, 0, shallow)
	}
	if got := StreamText(shallow, 0); got != "shallow" {
		t.Errorf("text at 31 levels: %q", got)
	}
}

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func TestNotOLE(t *testing.T) {
	if _, err := Text(bytes.NewReader([]byte("plain")), 5, 0); err == nil {
		t.Fatal("no error")
	}
}

// TestFixtures reads the presentations in testdata (Apache POI's, see
// testdata/NOTICE) and compares each with the .txt beside it, written by
// enrich.py's extract_ppt.
func TestFixtures(t *testing.T) {
	files, _ := filepath.Glob("testdata/*.ppt")
	if len(files) == 0 {
		t.Fatal("no fixtures")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Text(bytes.NewReader(b), int64(len(b)), 500_000)
		want, rerr := os.ReadFile(strings.TrimSuffix(f, ".ppt") + ".txt")
		if rerr != nil {
			t.Fatal(rerr)
		}
		if err != nil {
			got = ""
		}
		if got != string(want) {
			t.Errorf("%s: text differs from enrich.py's\n got %.300q\nwant %.300q", f, got, want)
		}
	}
}

// TestFixturesAgainstPython runs enrich.py's extract_ppt (with olefile,
// from ENRICH_DEPS) on the fixtures, with ENRICH_PY set.
func TestFixturesAgainstPython(t *testing.T) {
	enrich := os.Getenv("ENRICH_PY")
	if enrich == "" {
		t.Skip("ENRICH_PY not set")
	}
	files, _ := filepath.Glob("testdata/*.ppt")
	for _, f := range files {
		script := `import importlib.util, sys, pathlib
sys.path.append(sys.argv[3])
spec = importlib.util.spec_from_file_location('enrich', sys.argv[1]); m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
sys.stdout.buffer.write(m.extract_ppt(pathlib.Path(sys.argv[2])).encode('utf-8', 'surrogatepass'))`
		out, err := exec.Command("python3", "-I", "-c", script, enrich, f, os.Getenv("ENRICH_DEPS")).Output()
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		b, _ := os.ReadFile(f)
		got, gerr := Text(bytes.NewReader(b), int64(len(b)), 500_000)
		if gerr != nil {
			got = ""
		}
		if got != string(out) {
			t.Errorf("%s: differs from python (%d vs %d bytes)", f, len(got), len(out))
		}
	}
}

func FuzzStreamText(f *testing.F) {
	f.Add(container(recDocument, 0, container(recListWithText, 0, chars("Title"))), 0)
	f.Add(cat(chars("ok"), header(0, recTextChars, 1000)), 5)
	f.Fuzz(func(t *testing.T, b []byte, max int) {
		StreamText(b, max%1000)
	})
}

func FuzzText(f *testing.F) {
	files, _ := filepath.Glob("testdata/*.ppt")
	for _, p := range files {
		if b, err := os.ReadFile(p); err == nil && len(b) < 64<<10 {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		Text(bytes.NewReader(b), int64(len(b)), 1000)
	})
}

// TestStreamCasesAgainstPython runs enrich.py's extract_ppt on each
// synthetic stream, handed to it by a stand-in for olefile.
func TestStreamCasesAgainstPython(t *testing.T) {
	enrich := os.Getenv("ENRICH_PY")
	if enrich == "" {
		t.Skip("ENRICH_PY not set")
	}
	dir := t.TempDir()
	for i, c := range streamCases {
		p := filepath.Join(dir, "stream.bin")
		os.WriteFile(p, c.stream, 0o644)
		max := c.max
		if max == 0 {
			max = 500_000
		}
		script := `import importlib.util, sys, io, types, pathlib
data = open(sys.argv[2], 'rb').read()
class Ole:
    def __init__(self, path): pass
    def __enter__(self): return self
    def __exit__(self, *a): return False
    def exists(self, name): return True
    def openstream(self, name): return io.BytesIO(data)
sys.modules['olefile'] = types.SimpleNamespace(OleFileIO=Ole)
spec = importlib.util.spec_from_file_location('enrich', sys.argv[1]); m = importlib.util.module_from_spec(spec); spec.loader.exec_module(m)
m.MAX_TEXT = int(sys.argv[3])
sys.stdout.buffer.write(m.extract_ppt(pathlib.Path(sys.argv[2])).encode())`
		out, err := exec.Command("python3", "-I", "-c", script, enrich, p, strconv.Itoa(max)).Output()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got := StreamText(c.stream, c.max); got != string(out) {
			t.Errorf("case %d %s:\n    go %q\npython %q", i, c.name, got, out)
		}
	}
}
