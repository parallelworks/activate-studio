package transfers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
	"github.com/parallelworks/activate-studio/server-go/internal/settings"
)

// SettingsKey is the key of settings.json that holds the Transfers section.
const SettingsKey = "transfers"

// Settings is the Transfers section of Settings as it is stored and as
// PUT takes it. With sign-in on it is not used: the operator's environment
// decides (managedSettings), since saved hosts, library hosts and CONDUIT
// would reach other systems with the account the Studio runs as.
type Settings struct {
	SavedLocations  []SavedLocation  `json:"savedLocations"`
	ComputerFolders []ComputerFolder `json:"computerFolders"`
	Movers          MoverSettings    `json:"movers"`
	Defaults        Defaults         `json:"defaults"`
	// HostLocations is the home folder of each host a remote library is
	// reached on, offered with no setup; hidden names hosts left out.
	HostLocations HostLocations `json:"hostLocations"`
}

// SavedLocation is a host and a folder on it. Host is an ssh destination
// (user@host, or an alias of the ssh config). Port and SSHOptions are
// optional, for a host the ssh config does not describe.
type SavedLocation struct {
	ID         string `json:"id"`
	Label      string `json:"label"`
	Host       string `json:"host"`
	Root       string `json:"root"`
	RsyncPath  string `json:"rsyncPath,omitempty"`
	Port       int    `json:"port,omitempty"`
	SSHOptions string `json:"sshOptions,omitempty"`
}

// ComputerFolder is a folder on this computer. Downloads, Desktop and
// Documents are built in (stored by label, switched on or off); others are
// added by path.
type ComputerFolder struct {
	Path    string `json:"path"`
	Label   string `json:"label"`
	BuiltIn bool   `json:"builtIn"`
	Enabled bool   `json:"enabled"`
}

// BuiltInFolders are the computer folders offered without setup.
var BuiltInFolders = []string{"Downloads", "Desktop", "Documents"}

type MoverSettings struct {
	Rsync      RsyncSettings   `json:"rsync"`
	StudioCopy Switch          `json:"studioCopy"`
	Conduit    ConduitSettings `json:"conduit"`
	Rclone     RcloneSettings  `json:"rclone"`
}

// RcloneSettings: Path is the rclone program on this computer, a name
// (looked for in Homebrew's folders on macOS, then on PATH) or an absolute
// path. Studio uses rclone's own configuration and never changes it.
type RcloneSettings struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
}

type Switch struct {
	Enabled bool `json:"enabled"`
}

// RsyncSettings: Path is the rsync program on this computer, a name looked
// up on PATH (and the usual folders) or an absolute path.
type RsyncSettings struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path"`
}

// ConduitSettings names the attached MCP server that is a CONDUIT service.
// Mounts say where Studio locations sit in its namespace, so it can move
// between them and Studio can check what landed.
type ConduitSettings struct {
	Enabled bool           `json:"enabled"`
	Server  *string        `json:"server"`
	Mounts  []ConduitMount `json:"mounts"`
}

// ConduitMount says that a folder of a Studio location is a path in the
// service's namespace.
type ConduitMount struct {
	Location string `json:"location"`
	Path     string `json:"path"`
	Conduit  string `json:"conduit"`
}

// ConduitServer is the CONDUIT service in use, as the mover takes it.
type ConduitServer struct {
	Server string
	Label  string
	Mounts []ConduitMount
}

// Defaults are a new transfer's choices where the request names none.
type Defaults struct {
	Mover         string `json:"mover"`
	Checksum      bool   `json:"checksum"`
	PreserveTimes bool   `json:"preserveTimes"`
	Parallel      int    `json:"parallel"`
}

type HostLocations struct {
	Enabled bool     `json:"enabled"`
	Hidden  []string `json:"hidden"`
}

// DefaultSettings is the section before anything is saved.
func DefaultSettings() Settings {
	return Settings{
		SavedLocations:  []SavedLocation{},
		ComputerFolders: []ComputerFolder{},
		Movers: MoverSettings{
			Rsync:      RsyncSettings{Enabled: true, Path: "rsync"},
			StudioCopy: Switch{Enabled: true},
			Conduit:    ConduitSettings{Mounts: []ConduitMount{}},
			Rclone:     RcloneSettings{Enabled: true, Path: "rclone"},
		},
		Defaults:      Defaults{Mover: "auto", PreserveTimes: true, Parallel: 4},
		HostLocations: HostLocations{Enabled: true, Hidden: []string{}},
	}
}

// conduit is the CONDUIT service in use, if one is switched on.
func (s Settings) conduit() (ConduitServer, bool) {
	c := s.Movers.Conduit
	if !c.Enabled || c.Server == nil || *c.Server == "" {
		return ConduitServer{}, false
	}
	return ConduitServer{Server: *c.Server, Label: "CONDUIT (" + *c.Server + ")", Mounts: c.Mounts}, true
}

// off is the movers switched off.
func (s Settings) off() map[string]bool {
	off := map[string]bool{}
	if !s.Movers.Rsync.Enabled {
		off[MoverRsync] = true
	}
	if _, ok := s.conduit(); !ok {
		off[MoverConduit] = true
	}
	if !s.Movers.Rclone.Enabled {
		off[MoverRclone] = true
	}
	return off
}

// revision is a short hash of the stored section: the same section
// always has the same revision, and any change gives another.
func (s Settings) revision() string {
	b, _ := json.Marshal(s.normal())
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:8])
}

func (s Settings) builtInOn(label string) bool {
	for _, f := range s.ComputerFolders {
		if f.BuiltIn && f.Label == label {
			return f.Enabled
		}
	}
	return true
}

// SettingsStore reads and writes the section.
type SettingsStore interface {
	Load() Settings
	Save(Settings) error
}

// FileSettings keeps the section in INDEX_BASE/settings.json through the
// settings package, beside the other settings.
type FileSettings struct{ S *settings.Store }

func (f FileSettings) Load() Settings {
	s := DefaultSettings()
	if raw, ok := f.S.Saved()[SettingsKey]; ok {
		json.Unmarshal(raw, &s)
	}
	return s.normal()
}

func (f FileSettings) Save(s Settings) error {
	return f.S.Update(map[string]any{SettingsKey: s.normal()})
}

// MemorySettings is a SettingsStore in memory (tests).
type MemorySettings struct {
	mu sync.Mutex
	S  Settings
}

func (m *MemorySettings) Load() Settings {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.S.normal()
}

func (m *MemorySettings) Save(s Settings) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.S = s
	return nil
}

func (s Settings) normal() Settings {
	d := DefaultSettings()
	if s.SavedLocations == nil {
		s.SavedLocations = []SavedLocation{}
	}
	if s.ComputerFolders == nil {
		s.ComputerFolders = []ComputerFolder{}
	}
	if s.Movers.Rsync.Path == "" {
		s.Movers.Rsync.Path = d.Movers.Rsync.Path
	}
	s.Movers.StudioCopy.Enabled = true
	if s.Movers.Rclone.Path == "" {
		s.Movers.Rclone.Path = d.Movers.Rclone.Path
	}
	if s.Movers.Conduit.Mounts == nil {
		s.Movers.Conduit.Mounts = []ConduitMount{}
	}
	if s.Defaults.Mover == "" {
		s.Defaults.Mover = "auto"
	}
	if s.Defaults.Parallel == 0 {
		s.Defaults.Parallel = d.Defaults.Parallel
	}
	if s.HostLocations.Hidden == nil {
		s.HostLocations.Hidden = []string{}
	}
	return s
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
var nonID = regexp.MustCompile(`[^a-z0-9]+`)

// programRe is a program name or path rsync may be given as: no spaces or
// shell characters, so it can stand in a command line as it is.
var programRe = regexp.MustCompile(`^[A-Za-z0-9_./+-]+$`)

func slugID(s string) string {
	s = strings.Trim(nonID.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 32 {
		s = strings.TrimRight(s[:32], "-")
	}
	if s == "" {
		s = "location"
	}
	return s
}

func bad(field, format string, a ...any) error {
	return apierr.New(400, "%s: %s", field, fmt.Sprintf(format, a...))
}

// CheckHost refuses a host ssh could read as an option or that holds
// spaces or control characters.
func CheckHost(field, h string) error {
	switch {
	case h == "":
		return bad(field, "name a host, such as user@host or an ssh config alias")
	case strings.HasPrefix(h, "-"):
		return bad(field, "a host cannot start with \"-\"")
	case strings.IndexFunc(h, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0:
		return bad(field, "a host has no spaces")
	}
	return nil
}

// CheckRoot is an absolute folder on a host, with no "..".
func CheckRoot(field, root string) (string, error) {
	if !strings.HasPrefix(root, "/") {
		return "", bad(field, "give an absolute path, starting with /")
	}
	r, err := libraries.ReadThroughRoot(root)
	if err != nil || r == nil {
		return "", bad(field, "give an absolute path with no \"..\"")
	}
	return *r, nil
}

func checkProgram(field, p string) error {
	if !programRe.MatchString(p) || strings.HasPrefix(p, "-") {
		return bad(field, "a program name or an absolute path, with no spaces")
	}
	if strings.Contains(p, "/") && !strings.HasPrefix(p, "/") {
		return bad(field, "give a name looked up on PATH, or an absolute path")
	}
	return nil
}

// siteOf is the ssh site of a saved location.
func (l SavedLocation) site() (string, error) {
	opts, err := remote.ParseSSHOptions(l.SSHOptions)
	if err != nil {
		return "", err
	}
	return remote.SSHSite(l.Host, l.Port, opts)
}

// Check validates a section from a PUT and returns it in its stored form.
// refused says why a computer folder may not be offered ("" when it may).
func (s Settings) Check(refused func(string) string) (Settings, error) {
	s = s.normal()
	// Its lists are cleaned in place, so they are copies: the caller's
	// (a store's own, in tests) stay as they were.
	s.SavedLocations = append([]SavedLocation{}, s.SavedLocations...)
	s.Movers.Conduit.Mounts = append([]ConduitMount{}, s.Movers.Conduit.Mounts...)
	if len(s.SavedLocations) > 50 || len(s.ComputerFolders) > 50+len(BuiltInFolders) {
		return s, apierr.New(400, "too many entries: at most 50 saved locations and 50 added folders")
	}
	seen := map[string]bool{}
	for i := range s.SavedLocations {
		l := &s.SavedLocations[i]
		f := fmt.Sprintf("savedLocations[%d]", i)
		l.Host = strings.TrimSpace(l.Host)
		if err := CheckHost(f+".host", l.Host); err != nil {
			return s, err
		}
		root, err := CheckRoot(f+".root", strings.TrimSpace(l.Root))
		if err != nil {
			return s, err
		}
		l.Root = root
		if l.RsyncPath = strings.TrimSpace(l.RsyncPath); l.RsyncPath != "" {
			if err := checkProgram(f+".rsyncPath", l.RsyncPath); err != nil {
				return s, err
			}
		}
		if l.Port < 0 || l.Port > 65535 {
			return s, bad(f+".port", "a port from 1 to 65535")
		}
		if _, err := l.site(); err != nil {
			return s, bad(f+".host", "%s", messageOf(err))
		}
		l.Label = strings.TrimSpace(l.Label)
		if l.Label == "" {
			l.Label = l.Host + ":" + l.Root
		}
		if l.ID == "" {
			l.ID = slugID(l.Label)
			for n := 2; seen[l.ID]; n++ {
				l.ID = slugID(l.Label) + "-" + itoa(n)
			}
		}
		if !idRe.MatchString(l.ID) || seen[l.ID] {
			return s, bad(f+".id", "use lower case letters, digits, - and _, once each")
		}
		seen[l.ID] = true
	}
	var folders []ComputerFolder
	paths := map[string]bool{}
	for i, c := range s.ComputerFolders {
		f := fmt.Sprintf("computerFolders[%d]", i)
		if c.BuiltIn {
			known := false
			for _, n := range BuiltInFolders {
				known = known || n == c.Label
			}
			if !known {
				return s, bad(f+".label", "the built-in folders are Downloads, Desktop and Documents")
			}
			folders = append(folders, ComputerFolder{Label: c.Label, BuiltIn: true, Enabled: c.Enabled})
			continue
		}
		p := strings.TrimSpace(c.Path)
		if !filepath.IsAbs(p) {
			return s, bad(f+".path", "give an absolute folder on this computer")
		}
		p = filepath.Clean(p)
		if fi, err := os.Stat(p); err != nil {
			return s, bad(f+".path", "%s is not there", p)
		} else if !fi.IsDir() {
			return s, bad(f+".path", "%s is not a folder", p)
		}
		if refused != nil {
			if why := refused(p); why != "" {
				return s, bad(f+".path", "%s", why)
			}
		}
		if paths[p] {
			continue
		}
		paths[p] = true
		label := strings.TrimSpace(c.Label)
		if label == "" {
			label = filepath.Base(p)
		}
		folders = append(folders, ComputerFolder{Path: p, Label: label, Enabled: c.Enabled})
	}
	s.ComputerFolders = folders
	if s.ComputerFolders == nil {
		s.ComputerFolders = []ComputerFolder{}
	}
	s.Movers.Rsync.Path = strings.TrimSpace(s.Movers.Rsync.Path)
	if s.Movers.Rsync.Path == "" {
		s.Movers.Rsync.Path = "rsync"
	}
	if err := checkProgram("movers.rsync.path", s.Movers.Rsync.Path); err != nil {
		return s, err
	}
	s.Movers.Rclone.Path = strings.TrimSpace(s.Movers.Rclone.Path)
	if s.Movers.Rclone.Path == "" {
		s.Movers.Rclone.Path = "rclone"
	}
	if err := checkRclonePath("movers.rclone.path", s.Movers.Rclone.Path); err != nil {
		return s, err
	}
	c := &s.Movers.Conduit
	if c.Server != nil {
		if v := strings.TrimSpace(*c.Server); v == "" {
			c.Server = nil
		} else {
			c.Server = &v
		}
	}
	if c.Enabled && c.Server == nil {
		return s, bad("movers.conduit.server", "choose the attached MCP server that is the CONDUIT service")
	}
	for j := range c.Mounts {
		m := &c.Mounts[j]
		f := fmt.Sprintf("movers.conduit.mounts[%d]", j)
		p, err := CleanRel(m.Path)
		if err != nil {
			return s, bad(f+".path", "%s", messageOf(err))
		}
		m.Path = p
		root, err := CheckRoot(f+".conduit", m.Conduit)
		if err != nil {
			return s, err
		}
		m.Conduit = root
		if !strings.Contains(m.Location, ":") || strings.HasPrefix(m.Location, KindConduit+":") {
			return s, bad(f+".location", "name a Studio location by id, such as library:kb")
		}
	}
	switch s.Defaults.Mover {
	case "auto", MoverRsync, MoverCopy, MoverConduit, MoverRclone:
	default:
		return s, bad("defaults.mover", "auto, rsync, rclone, studio-copy or conduit")
	}
	if s.Defaults.Parallel < 1 || s.Defaults.Parallel > 16 {
		return s, bad("defaults.parallel", "a number from 1 to 16")
	}
	var hidden []string
	for _, h := range s.HostLocations.Hidden {
		if h = strings.TrimSpace(h); h != "" {
			hidden = append(hidden, h)
		}
	}
	s.HostLocations.Hidden = hidden
	return s.normal(), nil
}

// checkRclonePath is a program name or an absolute path to rclone. It is
// run directly, never through a shell, so an absolute path may hold spaces
// (as macOS folders do).
func checkRclonePath(field, p string) error {
	switch {
	case strings.HasPrefix(p, "-"):
		return bad(field, "a program cannot start with \"-\"")
	case strings.IndexFunc(p, unicode.IsControl) >= 0:
		return bad(field, "a program name or an absolute path")
	case strings.ContainsAny(p, `/\\`) && !filepath.IsAbs(p):
		return bad(field, "give a name looked up on PATH, or an absolute path")
	case !strings.ContainsAny(p, `/\\`) && strings.ContainsAny(p, " \t"):
		return bad(field, "a program name has no spaces")
	}
	return nil
}

func itoa(n int) string { return strconv.Itoa(n) }

// envFolders is STUDIO_TRANSFER_FOLDERS: folders separated by the list
// separator (":" here, ";" on Windows) or new lines.
func envFolders(v string) []string {
	var out []string
	for _, line := range strings.Split(v, "\n") {
		for _, f := range filepath.SplitList(line) {
			if f = strings.TrimSpace(f); f != "" && filepath.IsAbs(f) {
				out = append(out, filepath.Clean(f))
			}
		}
	}
	return out
}

// managedSettings is the section with sign-in on, from the environment:
// STUDIO_TRANSFER_FOLDERS (this computer's folders), STUDIO_RSYNC (the
// rsync program, "off" for none), STUDIO_TRANSFER_MOVER (the default
// mover) and STUDIO_TRANSFER_STREAMS (parallel). No saved locations, host
// locations or CONDUIT.
func managedSettings(getenv func(string) string) Settings {
	s := DefaultSettings()
	for _, f := range envFolders(getenv("STUDIO_TRANSFER_FOLDERS")) {
		s.ComputerFolders = append(s.ComputerFolders, ComputerFolder{Path: f, Label: filepath.Base(f), Enabled: true})
	}
	switch v := strings.TrimSpace(getenv("STUDIO_RSYNC")); v {
	case "":
	case "off", "0", "false":
		s.Movers.Rsync.Enabled = false
	default:
		s.Movers.Rsync.Path = v
	}
	switch v := getenv("STUDIO_TRANSFER_MOVER"); v {
	case MoverRsync, MoverCopy:
		s.Defaults.Mover = v
	}
	if n, err := strconv.Atoi(getenv("STUDIO_TRANSFER_STREAMS")); err == nil && n >= 1 && n <= 16 {
		s.Defaults.Parallel = n
	}
	s.HostLocations.Enabled = false
	// rclone would act with the server account's credentials for everyone.
	s.Movers.Rclone.Enabled = false
	return s.normal()
}

// settingsFor is the section that applies to the viewer: the saved one
// with sign-in off, the environment's with it on.
func (s *Service) settingsFor(v Viewer) Settings {
	if v.AuthOn {
		return managedSettings(s.d.Getenv)
	}
	if s.d.Settings == nil {
		return DefaultSettings()
	}
	return s.d.Settings.Load()
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}
