package libconfig

import (
	"encoding/json"
	"net/http"
	"os"
	"os/user"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/reqbody"
	"github.com/parallelworks/activate-studio/server-go/internal/userlibs"
	"github.com/parallelworks/activate-studio/server-go/internal/version"
)

// Register serves the two routes of library lists: the export, and the
// check before an import. The import itself adds each entry through the
// routes the Add a library forms use (POST /api/libraries, /api/libraries/
// build, /api/user-libraries/ssh, /pw and their /build), so nothing here
// adds a library.
func Register(mux *http.ServeMux, a *app.App) {
	s := &service{a: a}
	h := func(fn func(http.ResponseWriter, *http.Request) error) http.HandlerFunc {
		return apierr.Handler(a.Log, fn)
	}
	mux.HandleFunc("POST /api/libraries/export", h(s.export))
	mux.HandleFunc("POST /api/libraries/import/check", h(s.check))
}

type service struct{ a *app.App }

func (s *service) reg() *libraries.Registry { return libraries.Of(s.a) }

func (s *service) deployment() []libraries.Library {
	return s.reg().List(s.a.Settings.Effective().KbLabel, libraries.Deployment)
}

func (s *service) userDefs(owner string) []libraries.UserDef {
	if owner == "" {
		return nil
	}
	return s.reg().Users().Load(owner)
}

type exportAnswer struct {
	File    File      `json:"file"`
	Name    string    `json:"name"`
	Skipped []Skipped `json:"skipped"`
}

func (s *service) export(w http.ResponseWriter, r *http.Request) error {
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	offer, err := userlibs.Of(s.a).OfferedTo(r)
	if err != nil {
		return err
	}
	o := Options{Primary: body.Get("primary") == true, KeepUser: body.Get("keepUser") == true,
		StudioVersion: version.Of(s.a.Cfg.ProjectRoot).Version, Now: time.Now()}
	if ids, ok := body.Get("ids").([]any); ok {
		for _, id := range ids {
			if v, ok := id.(string); ok && v != "" {
				o.IDs = append(o.IDs, v)
			}
		}
		if len(o.IDs) == 0 && !o.Primary {
			return apierr.New(400, "Choose at least one library to export.")
		}
		if len(o.IDs) == 0 {
			// Only the main folder.
			o.IDs = []string{libraries.PrimaryID}
		}
	}
	reg := s.reg()
	f, skipped := Export(Source{Libraries: s.deployment(), Users: s.userDefs(offer.Owner), Depth: reg.Settings().Depth}, o)
	if skipped == nil {
		skipped = []Skipped{}
	}
	label := s.a.Settings.Effective().AppName
	if len(f.Libraries) == 1 {
		label = f.Libraries[0].Label
	}
	apierr.JSON(w, 200, exportAnswer{File: f, Name: FileName(label), Skipped: skipped})
	return nil
}

type checkAnswer struct {
	Version       int       `json:"version"`
	ExportedAt    string    `json:"exportedAt"`
	StudioVersion string    `json:"studioVersion"`
	Entries       []Checked `json:"entries"`
	// LocalUser is the login name the Studio runs as, which the preview
	// offers for an ssh entry with no user name.
	LocalUser string `json:"localUser"`
}

func localUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

func (s *service) check(w http.ResponseWriter, r *http.Request) error {
	body, err := reqbody.Read(r)
	if err != nil {
		return err
	}
	var data []byte
	switch v := body.Get("file").(type) {
	case map[string]any:
		data, _ = json.Marshal(v)
	case string:
		data = []byte(v)
	default:
		if t, ok := body.Get("text").(string); ok {
			data = []byte(t)
		}
	}
	if len(data) == 0 {
		return apierr.New(400, "Give the library list as file (the file's JSON) or text.")
	}
	h, parsed, err := Parse(data)
	if err != nil {
		return apierr.New(400, "%s", err.Error())
	}
	offer, err := userlibs.Of(s.a).OfferedTo(r)
	if err != nil {
		return err
	}
	home, _ := os.UserHomeDir()
	have := Existing{Libraries: s.deployment(), Users: s.userDefs(offer.Owner)}
	entries := Check(parsed, have, Offer{Auth: offer.Auth, Build: !offer.Auth, SSH: offer.SSH, PwWhy: offer.PwWhy}, home)
	apierr.JSON(w, 200, checkAnswer{Version: h.Version, ExportedAt: h.ExportedAt, StudioVersion: h.StudioVersion, Entries: entries, LocalUser: localUser()})
	return nil
}
