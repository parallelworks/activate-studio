package transfers

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/remote"
)

// MCPServer is an attached MCP server, for choosing CONDUIT's.
type MCPServer struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Connected bool   `json:"connected"`
}

// Detected is what Studio found without any setup, so the page can show
// what is ready before anything is tested.
type Detected struct {
	RsyncLocal   bool   `json:"rsyncLocal"`
	RsyncVersion string `json:"rsyncVersion,omitempty"`
	// RsyncPath is the rsync program found here, and RsyncText says which
	// transfers it is used for.
	RsyncPath    string   `json:"rsyncPath,omitempty"`
	RsyncText    string   `json:"rsyncText,omitempty"`
	Clusters     int      `json:"clusters"`
	LibraryHosts []string `json:"libraryHosts"`
	// Rclone is rclone on this computer and the remotes it has.
	Rclone *RcloneDetected `json:"rclone,omitempty"`
}

// SettingsView is GET /api/transfers/settings: the section, with the
// read-only fields.
type SettingsView struct {
	Managed bool `json:"managed"`
	Settings
	MCPServers []MCPServer `json:"mcpServers"`
	Detected   Detected    `json:"detected"`
}

// SettingsView is the section as the viewer sees it. Computer folders list
// the built-in ones first, with their paths here, then the added ones.
func (s *Service) SettingsView(ctx context.Context, v Viewer) SettingsView {
	set := s.settingsFor(v)
	out := SettingsView{Managed: v.AuthOn, Settings: set, MCPServers: []MCPServer{}}
	if !v.AuthOn {
		var folders []ComputerFolder
		for _, n := range BuiltInFolders {
			p := ""
			if s.d.Home != "" {
				p = filepath.Join(s.d.Home, n)
			}
			folders = append(folders, ComputerFolder{Path: p, Label: n, BuiltIn: true, Enabled: set.builtInOn(n)})
		}
		for _, f := range set.ComputerFolders {
			if !f.BuiltIn {
				folders = append(folders, f)
			}
		}
		out.ComputerFolders = folders
		if s.d.Conduit != nil {
			c, cancel := context.WithTimeout(ctx, 5*time.Second)
			out.MCPServers = s.d.Conduit.Servers(c)
			cancel()
		}
	}
	out.Detected = s.detect(ctx, v, set)
	return out
}

// rsyncMover is the service's rsync mover.
func (s *Service) rsyncMover() *rsyncMover {
	for _, m := range s.movers {
		if r, ok := m.(*rsyncMover); ok {
			return r
		}
	}
	return nil
}

// detect is local rsync (the program found, its version and what it is
// used for), the viewer's clusters and, with
// sign-in off, the hosts libraries are reached on.
func (s *Service) detect(ctx context.Context, v Viewer, set Settings) Detected {
	d := Detected{LibraryHosts: []string{}}
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if m := s.rsyncMover(); m != nil && set.Movers.Rsync.Enabled {
		if p, rc, why := m.local(c, set); why == "" {
			d.RsyncLocal, d.RsyncVersion, d.RsyncPath, d.RsyncText = true, rc.version(), p, localUse(rc, p)
		}
	}
	if s.d.Clusters != nil && v.Key != "" {
		if names, err := s.clusterNames(c, v); err == nil {
			d.Clusters = len(names)
		}
	}
	if !v.AuthOn {
		for _, r := range s.libraryHosts(c, v) {
			d.LibraryHosts = append(d.LibraryHosts, remote.SiteName(r.SiteID))
		}
	}
	d.Rclone = s.detectRclone(c, v, set)
	return d
}

// SaveSettings validates and stores the section (403 with sign-in on).
func (s *Service) SaveSettings(ctx context.Context, v Viewer, in Settings) (SettingsView, error) {
	if v.AuthOn {
		return SettingsView{}, apierr.New(403, "with sign-in on, the operator's environment sets transfers (STUDIO_TRANSFER_FOLDERS, STUDIO_RSYNC, STUDIO_TRANSFER_MOVER, STUDIO_TRANSFER_STREAMS)")
	}
	c, err := in.Check(s.refusedFolder)
	if err != nil {
		return SettingsView{}, err
	}
	if err := s.d.Settings.Save(c); err != nil {
		return SettingsView{}, err
	}
	return s.SettingsView(ctx, v), nil
}

// TestRequest is POST /api/transfers/settings/test: a saved location
// (host and root, with its optional rsyncPath, port and sshOptions), or a
// CONDUIT server by id.
type TestRequest struct {
	Host       string  `json:"host"`
	Root       string  `json:"root"`
	RsyncPath  string  `json:"rsyncPath"`
	Port       int     `json:"port"`
	SSHOptions string  `json:"sshOptions"`
	Conduit    *string `json:"conduit"`
}

// TestResult says whether Studio reaches the place and what it found.
type TestResult struct {
	OK           bool   `json:"ok"`
	Why          string `json:"why,omitempty"`
	Rsync        *bool  `json:"rsync,omitempty"`
	RsyncVersion string `json:"rsyncVersion,omitempty"`
	// RsyncText says which transfers the host's rsync is used for.
	RsyncText   string `json:"rsyncText,omitempty"`
	Writable    *bool  `json:"writable,omitempty"`
	FreeBytes   *int64 `json:"freeBytes,omitempty"`
	RoundTripMs *int64 `json:"roundTripMs,omitempty"`
	// SFTPOnly: the account takes SFTP only, so Studio reaches it over the
	// SFTP protocol, copies use studio-copy and rsync is not available.
	SFTPOnly bool `json:"sftpOnly,omitempty"`
	// Rclone: whether rclone is on the host, for information only
	// (transfers do not run rclone on hosts).
	Rclone        *bool  `json:"rclone,omitempty"`
	RcloneVersion string `json:"rcloneVersion,omitempty"`
}

// TestSettings checks a saved location or a CONDUIT server: over ssh (or
// SFTP, for an account that takes SFTP only), that the root is a folder,
// whether rsync is there (and its version, and
// whether the login prints text that would break it), whether Studio can
// write there (a staging folder made and removed), the free space, and the
// time of one round trip on the open connection.
func (s *Service) TestSettings(ctx context.Context, v Viewer, in TestRequest) (TestResult, error) {
	if v.AuthOn {
		return TestResult{}, apierr.New(403, "with sign-in on, saved locations and CONDUIT are not used")
	}
	if in.Conduit != nil {
		if s.d.Conduit == nil {
			return TestResult{Why: "no MCP client"}, nil
		}
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		t0 := time.Now()
		ok, why := s.d.Conduit.Ready(c, *in.Conduit)
		ms := time.Since(t0).Milliseconds()
		return TestResult{OK: ok, Why: why, RoundTripMs: &ms}, nil
	}
	host := strings.TrimSpace(in.Host)
	if err := CheckHost("host", host); err != nil {
		return TestResult{}, err
	}
	root, err := CheckRoot("root", strings.TrimSpace(in.Root))
	if err != nil {
		return TestResult{}, err
	}
	if in.RsyncPath != "" {
		if err := checkProgram("rsyncPath", in.RsyncPath); err != nil {
			return TestResult{}, err
		}
	}
	site, err := SavedLocation{Host: host, Port: in.Port, SSHOptions: in.SSHOptions}.site()
	if err != nil {
		return TestResult{}, bad("host", "%s", messageOf(err))
	}
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	fs := &RemoteFS{R: s.d.Runner, SiteID: site, Dir: root, Name: remote.SiteName(site), RsyncPath: in.RsyncPath, Timeout: 30 * time.Second}
	// The first call opens the connection; the round trip is timed on the
	// second, as later calls see it.
	if _, _, err := fs.List(c, "", 1); err != nil {
		return TestResult{Why: messageOf(err)}, nil
	}
	out := TestResult{OK: true}
	t0 := time.Now()
	free, ok := fs.Free(c, "")
	ms := time.Since(t0).Milliseconds()
	out.RoundTripMs = &ms
	if ok {
		out.FreeBytes = &free
	}
	out.SFTPOnly = fs.SFTPOnly(c)
	var rp rsyncProbe
	has, clean := false, true
	if !out.SFTPOnly {
		rp, has, clean = RsyncProbe(c, s.d.Runner, site, "", remoteRsync(fs))
	}
	out.Rsync = &has
	if has {
		if rc, ok := parseRsyncCaps(rp.version, rp.help, rp.trust); ok {
			out.RsyncVersion = rc.version()
			out.RsyncText = hostUse(rc)
		}
		if !clean {
			out.Why = "the login prints text before a command runs, which rsync's protocol cannot pass; studio-copy will carry transfers"
		}
	}
	if !out.SFTPOnly {
		if ver, found := RcloneOnHost(c, s.d.Runner, site, ""); found {
			out.Rclone, out.RcloneVersion = &found, ver
		} else {
			out.Rclone = &found
		}
	}
	probe := stagingPrefix + "test-" + newID()
	w := fs.Mkdir(c, probe) == nil
	if w {
		fs.RemoveStaging(c, probe)
	}
	out.Writable = &w
	return out, nil
}

// savedOff is why saved locations cannot be added with sign-in on.
const savedOff = "with sign-in on, saved locations are off, since they would reach other systems with the ssh keys of the account the Studio runs as"

// AddSavedLocation adds one saved location to the section, checked as a
// PUT checks it, and returns it with its id. Its id is made from the
// label (with -2, -3 when taken); an id given is ignored. 403 with sign-in
// on. It does not test the location: the Transfers page tests it first
// in the same dialog as Settings, and the assistant's
// save_transfer_location uses TestAndSave.
func (s *Service) AddSavedLocation(ctx context.Context, v Viewer, in SavedLocation) (SavedLocation, error) {
	if v.AuthOn {
		return SavedLocation{}, apierr.New(403, "%s", savedOff)
	}
	if s.d.Settings == nil {
		return SavedLocation{}, apierr.New(503, "settings cannot be saved here")
	}
	s.savedMu.Lock()
	defer s.savedMu.Unlock()
	set := s.d.Settings.Load()
	in.ID = ""
	set.SavedLocations = append(append([]SavedLocation{}, set.SavedLocations...), in)
	c, err := set.Check(s.refusedFolder)
	if err != nil {
		// The new entry's field errors name it by its place in the list;
		// the caller sent one location, so they name the field alone.
		return SavedLocation{}, renameField(err, len(set.SavedLocations)-1)
	}
	if err := s.d.Settings.Save(c); err != nil {
		return SavedLocation{}, err
	}
	return c.SavedLocations[len(c.SavedLocations)-1], nil
}

// renameField drops "savedLocations[i]." from a 400 about entry i.
func renameField(err error, i int) error {
	var e *apierr.Error
	prefix := "savedLocations[" + itoa(i) + "]."
	if errors.As(err, &e) && e.Status == 400 && strings.HasPrefix(e.Message, prefix) {
		return apierr.New(400, "%s", strings.TrimPrefix(e.Message, prefix))
	}
	return err
}

// Saved is the outcome of TestAndSave: the test, and the location saved
// when the test passed.
type Saved struct {
	Test     TestResult
	Location *SavedLocation
	// ID is the saved location's location id ("saved:<id>").
	ID string
}

// TestAndSave tests a saved location as Settings' Test and save does and,
// when the test passes, adds it. A failed test saves nothing.
func (s *Service) TestAndSave(ctx context.Context, v Viewer, in SavedLocation) (Saved, error) {
	if v.AuthOn {
		return Saved{}, apierr.New(403, "%s", savedOff)
	}
	r, err := s.TestSettings(ctx, v, TestRequest{Host: in.Host, Root: in.Root, RsyncPath: in.RsyncPath, Port: in.Port, SSHOptions: in.SSHOptions})
	if err != nil {
		return Saved{}, err
	}
	out := Saved{Test: r}
	if !r.OK {
		return out, nil
	}
	l, err := s.AddSavedLocation(ctx, v, in)
	if err != nil {
		return out, err
	}
	out.Location, out.ID = &l, KindSaved+":"+l.ID
	return out, nil
}

// CheckSavedLocation checks one saved location as a PUT checks it and
// returns it in its stored form (the label defaults to host:folder), with
// errors naming the field alone. Its id is left empty.
func CheckSavedLocation(l SavedLocation) (SavedLocation, error) {
	set := DefaultSettings()
	l.ID = ""
	set.SavedLocations = []SavedLocation{l}
	c, err := set.Check(nil)
	if err != nil {
		return l, renameField(err, 0)
	}
	out := c.SavedLocations[0]
	out.ID = ""
	return out, nil
}
