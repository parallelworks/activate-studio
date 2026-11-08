package render

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestCorpus renders the PDFs listed one a line in RENDER_CORPUS, up to
// RENDER_PAGES pages each (default 3) at 200 dpi, writing
// RENDER_OUT/<n>/page-NNN.png when RENDER_OUT is set and a JSON line a
// file to RENDER_OUT/results.jsonl, for comparing with pdftoppm. Errors
// are recorded, not failed on; a panic that escapes Page fails the test.
func TestCorpus(t *testing.T) {
	list := os.Getenv("RENDER_CORPUS")
	if list == "" {
		t.Skip("RENDER_CORPUS not set")
	}
	out := os.Getenv("RENDER_OUT")
	pages := 3
	fmt.Sscan(os.Getenv("RENDER_PAGES"), &pages)
	f, err := os.Open(list)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var res *os.File
	if out != "" {
		os.MkdirAll(out, 0o755)
		res, _ = os.Create(filepath.Join(out, "results.jsonl"))
		defer res.Close()
	}
	sc := bufio.NewScanner(f)
	n := 0
	counts := map[string]int{}
	for sc.Scan() {
		path := strings.TrimSpace(sc.Text())
		if path == "" {
			continue
		}
		n++
		type pageRes struct {
			Err string  `json:"err,omitempty"`
			Ms  float64 `json:"ms"`
		}
		r := struct {
			Path  string    `json:"path"`
			Dir   int       `json:"dir"`
			Open  string    `json:"open,omitempty"`
			Count int       `json:"count"`
			Pages []pageRes `json:"pages"`
		}{Path: path, Dir: n}
		d, err := Open(context.Background(), path)
		if err != nil {
			r.Open = err.Error()
			counts["unreadable"]++
		} else {
			r.Count = d.PageCount()
			for p := 1; p <= min(pages, d.PageCount()); p++ {
				ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
				ps := time.Now()
				img, err := d.Page(ctx, p, 200)
				cancel()
				pr := pageRes{Ms: float64(time.Since(ps).Microseconds()) / 1000}
				if err != nil {
					pr.Err = err.Error()
					if strings.Contains(pr.Err, "render: ") && !strings.Contains(pr.Err, "too complex") && !strings.Contains(pr.Err, "no page") {
						counts["page error"]++
					}
				}
				if img != nil && out != "" {
					dir := filepath.Join(out, fmt.Sprint(n))
					os.MkdirAll(dir, 0o755)
					w, _ := os.Create(filepath.Join(dir, fmt.Sprintf("page-%03d.png", p)))
					png.Encode(w, img)
					w.Close()
				}
				r.Pages = append(r.Pages, pr)
			}
			d.Close()
			counts["opened"]++
		}
		if res != nil {
			b, _ := json.Marshal(r)
			res.Write(append(b, '\n'))
		}
	}
	t.Logf("%d files: %v", n, counts)
}
