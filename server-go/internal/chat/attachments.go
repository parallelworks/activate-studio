package chat

import (
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
)

// attachments.ts: files attached to a chat message, kept in the main
// folder under uploads/chat and indexed like any other file there.

const (
	attachDir      = "uploads/chat"
	maxAttachBytes = 100 * 1024 * 1024
)

func encodeAttachID(rel string) string { return base64.RawURLEncoding.EncodeToString([]byte(rel)) }

func decodeAttachID(id string) (string, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(id, "="))
	if err != nil {
		// Buffer.from(id, 'base64url') skips what it cannot read
		b = lenientB64(id)
	}
	rel := string(b)
	if !strings.HasPrefix(rel, attachDir+"/") || strings.Contains(rel, "..") {
		return "", apierr.New(400, "invalid attachment id")
	}
	return rel, nil
}

func lenientB64(s string) []byte {
	var keep strings.Builder
	for _, c := range s {
		if c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' {
			keep.WriteRune(c)
		}
	}
	t := keep.String()
	if len(t)%4 == 1 {
		t = t[:len(t)-1]
	}
	b, _ := base64.RawURLEncoding.DecodeString(t)
	return b
}

var (
	unsafeNameRe = regexp.MustCompile(`[^\w.\- ()]`)
	spacesRe     = regexp.MustCompile(`\s+`)
)

func sanitizeAttachName(name string) (string, error) {
	base := path.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "" || strings.HasSuffix(name, "/") {
		base = ""
	}
	base = jsTrim(spacesRe.ReplaceAllString(unsafeNameRe.ReplaceAllString(base, "_"), " "))
	if base == "" || strings.HasPrefix(base, ".") {
		return "", apierr.New(400, "unusable filename: %s", name)
	}
	return base, nil
}

type attachRef struct {
	rel string
	at  time.Time
	obj any
}

func (m *Module) kbRoot() string { return m.A.Cfg.KBRoot }

func (m *Module) refFor(rel string) *attachRef {
	abs, err := kb.Resolve(rel, m.kbRoot())
	if err != nil {
		return nil
	}
	st, err := os.Stat(abs)
	if err != nil || !st.Mode().IsRegular() {
		return nil
	}
	at := st.ModTime()
	return &attachRef{rel, at, reqbody.O("id", encodeAttachID(rel), "filename", path.Base(rel), "contentType", kb.MimeFor(abs),
		"size", st.Size(), "uploadedAt", at.UTC().Format("2006-01-02T15:04:05.000Z"))}
}

func (m *Module) reindexAttachments(r *http.Request) {
	if kb.Default != nil {
		_, _ = kb.ReindexForDir(r.Context(), kb.Default.Files.Indexer, attachDir)
	}
}

func (m *Module) attachmentRoutes(h func(string, func(http.ResponseWriter, *http.Request) error)) {
	h("POST /api/chat/attachments", func(w http.ResponseWriter, r *http.Request) error {
		mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || !strings.HasPrefix(mt, "multipart/") || params["boundary"] == "" {
			return apierr.New(406, "the request is not multipart")
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		var part *multipart.Part
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				return apierr.New(400, "no file in request")
			}
			if p.FileName() != "" {
				part = p
				break
			}
			p.Close()
		}
		if part == nil {
			return apierr.New(400, "no file in request")
		}
		dirAbs, err := kb.Resolve(attachDir, m.kbRoot())
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dirAbs, 0o755); err != nil {
			return err
		}
		name, err := sanitizeAttachName(part.FileName())
		if err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dirAbs, name)); err == nil {
			ext := path.Ext(name)
			name = strings.TrimSuffix(name, ext) + "-" + strconv.FormatInt(time.Now().UnixMilli(), 36) + ext
		}
		data, err := io.ReadAll(io.LimitReader(part, maxAttachBytes+1))
		if err != nil {
			return err
		}
		if len(data) > maxAttachBytes {
			return apierr.New(413, "request file too large")
		}
		if err := os.WriteFile(filepath.Join(dirAbs, name), data, 0o644); err != nil {
			return err
		}
		m.reindexAttachments(r)
		ref := m.refFor(attachDir + "/" + name)
		if ref == nil {
			return apierr.New(500, "attachment write failed")
		}
		return reqbody.OK(w, ref.obj)
	})
	h("GET /api/chat/attachments", func(w http.ResponseWriter, r *http.Request) error {
		dirAbs, err := kb.Resolve(attachDir, m.kbRoot())
		if err != nil {
			return err
		}
		ents, _ := os.ReadDir(dirAbs)
		refs := []*attachRef{}
		for _, e := range ents {
			if ref := m.refFor(attachDir + "/" + e.Name()); ref != nil {
				refs = append(refs, ref)
			}
		}
		sort.SliceStable(refs, func(i, j int) bool { return refs[i].at.After(refs[j].at) })
		num := func(k string, def int) int {
			v := r.URL.Query().Get(k)
			if !r.URL.Query().Has(k) {
				return def
			}
			f := reqbody.JSNumber(v)
			if f != f || f == 0 {
				if k == "limit" {
					return 50
				}
				return 0
			}
			return int(f)
		}
		off, lim := num("offset", 0), num("limit", 50)
		out := []any{}
		for i := max(off, 0); i < len(refs) && i < off+lim; i++ {
			out = append(out, refs[i].obj)
		}
		return reqbody.OK(w, reqbody.O("attachments", out, "total", len(refs)))
	})
	h("GET /api/chat/attachments/{id}/download", func(w http.ResponseWriter, r *http.Request) error {
		rel, err := decodeAttachID(r.PathValue("id"))
		if err != nil {
			return err
		}
		abs, err := kb.Resolve(rel, m.kbRoot())
		if err != nil {
			return err
		}
		f, err := os.Open(abs)
		if err != nil {
			return apierr.New(404, "not found")
		}
		defer f.Close()
		w.Header().Set("Content-Type", kb.MimeFor(abs))
		w.Header().Set("Content-Disposition", `attachment; filename="`+filepath.Base(abs)+`"`)
		_, err = io.Copy(w, f)
		return err
	})
	h("DELETE /api/chat/attachments/{id}", func(w http.ResponseWriter, r *http.Request) error {
		rel, err := decodeAttachID(r.PathValue("id"))
		if err != nil {
			return err
		}
		abs, err := kb.Resolve(rel, m.kbRoot())
		if err != nil {
			return err
		}
		os.RemoveAll(abs)
		roots := kb.RootsFrom(m.A.Cfg)
		os.Remove(roots.ExtractPath(rel))
		if kb.Default != nil {
			kb.Default.Previews.RemovePreview(rel)
		}
		os.Remove(roots.ModelCachePath(rel))
		m.reindexAttachments(r)
		return reqbody.OK(w, reqbody.O("deleted", rel))
	})
}
