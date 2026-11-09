package libapi

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/gufitrace"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonobj"
)

func readBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return []byte("{}"), nil
	}
	return b, nil
}

func (s *Service) query(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	viewer, err := s.viewer(r)
	if err != nil {
		return err
	}
	ctx, commands := gufitrace.Start(r.Context(), "query")
	res, err := s.Engine.QueryScope(ctx, body, viewer, s.kbLabel())
	cmds := commands()
	if cmds == nil {
		cmds = []gufitrace.Command{}
	}
	if err != nil {
		var kb *apierr.Error
		if errors.As(err, &kb) {
			apierr.JSON(w, kb.Status, struct {
				Error string              `json:"error"`
				Gufi  []gufitrace.Command `json:"gufi"`
			}{kb.Message, cmds})
			return nil
		}
		return err
	}
	b, err := jsonobj.Marshal(res)
	if err != nil {
		return err
	}
	g, _ := jsonobj.Marshal(cmds)
	out := append(append(append(b[:len(b)-1:len(b)-1], []byte(`,"gufi":`)...), g...), '}')
	writeRaw(w, 200, out)
	return nil
}

func writeRaw(w http.ResponseWriter, status int, b []byte) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	w.Write(b)
}

func (s *Service) querySQL(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	viewer, err := s.viewer(r)
	if err != nil {
		return err
	}
	res, err := s.Engine.QuerySQL(r.Context(), body, viewer, s.kbLabel())
	if err != nil {
		return err
	}
	apierr.JSON(w, 200, res)
	return nil
}

// Saved queries (INDEX_BASE/saved-queries.json), as query.ts keeps them.

func (s *Service) savedFile() string { return filepath.Join(s.a.Cfg.IndexBase, "saved-queries.json") }

func isoNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

const starters = `[{"id":"starter-large","name":"Files over 10 MB","createdAt":"%s","payload":{"builder":{"source":"files","filters":[{"field":"size","op":"gt","value":"10485760"}],"groupBy":"none","sort":"size","dir":"desc","limit":200,"subtree":""}}},{"id":"starter-pdfs","name":"Largest PDFs","createdAt":"%s","payload":{"builder":{"source":"files","filters":[{"field":"extension","op":"eq","value":"pdf"}],"groupBy":"none","sort":"size","dir":"desc","limit":100,"subtree":""}}}]`

func (s *Service) loadSaved() []json.RawMessage {
	f := s.savedFile()
	if _, err := os.Stat(f); err != nil {
		now := isoNow()
		raw := strings.ReplaceAll(starters, "%s", now)
		var seeded []json.RawMessage
		json.Unmarshal([]byte(raw), &seeded)
		if b, err := indent1(seeded); err == nil {
			os.WriteFile(f, b, 0o644)
		}
		return seeded
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return []json.RawMessage{}
	}
	var out []json.RawMessage
	if json.Unmarshal(b, &out) != nil {
		// JSON.parse of something not a list answers as it is; a broken file is [].
		var any json.RawMessage
		if json.Unmarshal(b, &any) == nil {
			return []json.RawMessage{any}
		}
		return []json.RawMessage{}
	}
	if out == nil {
		out = []json.RawMessage{}
	}
	return out
}

// indent1 is JSON.stringify(v, null, 1) of raw values kept as they are.
func indent1(v []json.RawMessage) ([]byte, error) {
	b, err := jsonobj.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out strings.Builder
	if err := jsonIndent(&out, b, " "); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}

func (s *Service) savedList(w http.ResponseWriter, r *http.Request) error {
	b, err := jsonobj.Marshal(s.loadSaved())
	if err != nil {
		return err
	}
	writeRaw(w, 200, b)
	return nil
}

func randID() string {
	n, _ := rand.Int(rand.Reader, big.NewInt(1e4))
	return "q" + strconv.FormatInt(time.Now().UnixMilli(), 36) + n.String()
}

func (s *Service) savedAdd(w http.ResponseWriter, r *http.Request) error {
	body, err := readBody(r)
	if err != nil {
		return err
	}
	var b struct {
		Name    any             `json:"name"`
		Payload json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(body, &b) != nil {
		return &apierr.Validation{Message: "body must be an object"}
	}
	name := ""
	if b.Name != nil {
		name = jsString(b.Name)
	}
	name = strings.TrimSpace(name)
	if u := []rune(name); len(u) > 80 {
		name = string(u[:80])
	}
	if name == "" || len(b.Payload) == 0 || string(b.Payload) == "null" {
		return apierr.New(400, "name and payload required")
	}
	entry := struct {
		ID        string          `json:"id"`
		Name      string          `json:"name"`
		Payload   json.RawMessage `json:"payload"`
		CreatedAt string          `json:"createdAt"`
	}{randID(), name, b.Payload, isoNow()}
	eb, _ := jsonobj.Marshal(entry)
	saved := append([]json.RawMessage{eb}, s.loadSaved()...)
	if len(saved) > 200 {
		saved = saved[:200]
	}
	out, err := indent1(saved)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.savedFile(), out, 0o644); err != nil {
		return err
	}
	writeRaw(w, 200, eb)
	return nil
}

func (s *Service) savedDelete(w http.ResponseWriter, r *http.Request) error {
	id := r.PathValue("id")
	var keep []json.RawMessage
	for _, e := range s.loadSaved() {
		var x struct {
			ID any `json:"id"`
		}
		json.Unmarshal(e, &x)
		if x.ID != id {
			keep = append(keep, e)
		}
	}
	if keep == nil {
		keep = []json.RawMessage{}
	}
	out, err := indent1(keep)
	if err != nil {
		return err
	}
	if err := os.WriteFile(s.savedFile(), out, 0o644); err != nil {
		return err
	}
	apierr.JSON(w, 200, map[string]string{"deleted": id})
	return nil
}

func jsString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	case nil:
		return "null"
	}
	b, _ := json.Marshal(v)
	return string(b)
}
