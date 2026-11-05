package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDroppedFolderKeepsItsStructureAndLeavesOutDotNames(t *testing.T) {
	root := filepath.Join(t.TempDir(), "top")
	for _, f := range []string{"a.md", "sub/deep/b.md", ".DS_Store", ".git/config"} {
		p := filepath.Join(root, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(f), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var d droppedFiles
	got, err := d.list([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	rels := map[string]bool{}
	for _, f := range got {
		rels[f.Rel] = true
	}
	if len(got) != 2 || !rels["top/a.md"] || !rels["top/sub/deep/b.md"] {
		t.Fatalf("listed %v", got)
	}
}

func TestSendStreamsListedFilesToTheUploadOnce(t *testing.T) {
	root := filepath.Join(t.TempDir(), "top")
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	var query, paths, name, content, cookie string
	studio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, cookie = r.URL.RawQuery, r.Header.Get("Cookie")
		reader, err := r.MultipartReader()
		if err != nil {
			t.Error(err)
			return
		}
		for {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			b, _ := io.ReadAll(part)
			if part.FormName() == "paths" {
				paths = string(b)
			} else {
				name, content = part.FileName(), string(b)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"saved":["d/top/sub/a.md"]}`))
	}))
	defer studio.Close()
	port, err := strconv.Atoi(studio.URL[strings.LastIndex(studio.URL, ":")+1:])
	if err != nil {
		t.Fatal(err)
	}
	p := NewProxy(&App{})
	p.SetServer(port, true)
	got, err := p.dropped.list([]string{root})
	if err != nil || len(got) != 1 {
		t.Fatalf("listed %v, %v", got, err)
	}
	send := func(target, id string) *httptest.ResponseRecorder {
		body := fmt.Sprintf(`{"target":%q,"files":[{"id":%q,"rel":"top/sub/a.md"}]}`, target, id)
		r := httptest.NewRequest(http.MethodPost, droppedSend, strings.NewReader(body))
		r.Header.Set("Cookie", "session=1")
		w := httptest.NewRecorder()
		p.sendDropped(w, r)
		return w
	}
	if w := send("/api/kb/delete?path=x", got[0].ID); w.Code != 400 {
		t.Fatalf("another route: %d", w.Code)
	}
	if w := send("/api/kb/upload?dir=d&index=0", "not-an-id"); w.Code != 404 {
		t.Fatalf("unknown id: %d", w.Code)
	}
	if w := send("/api/kb/upload?dir=d&index=0", got[0].ID); w.Code != 200 || !strings.Contains(w.Body.String(), "saved") {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	if query != "dir=d&index=0" || cookie != "session=1" || paths != `["top/sub/a.md"]` || name != "a.md" || content != "hello" {
		t.Fatalf("server saw query %q cookie %q paths %q name %q content %q", query, cookie, paths, name, content)
	}
	if w := send("/api/kb/upload?dir=d&index=0", got[0].ID); w.Code != 404 {
		t.Fatalf("second send: %d", w.Code)
	}
}
