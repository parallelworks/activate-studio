// Package transfers is the Transfers page's server side: locations where
// files live (libraries, folders on this computer, the viewer's clusters,
// saved hosts, rclone remotes, a CONDUIT service), browsing them, planning a
// copy, move or sync, running it as a job through a mover (rsync, rclone,
// studio-copy, CONDUIT), checking what landed, and the job list with
// progress, cancel and events.
//
// The design and the HTTP contract are docs/TRANSFERS.md. In short:
//
//   - A location is named only by id; a path is relative to its root, and
//     "..", absolute paths, hidden names and links that leave the root are
//     refused (paths.go).
//   - Every action is planned first (plan.go) and started from the plan by
//     id, within 15 minutes, and refused if the source grew past it.
//   - A mover only moves bytes into a staging folder .studio-transfer-<id>
//     inside the destination. The Studio walks that folder, compares it with
//     its own listing of the source (sizes, and sha256 for a move or with
//     checksum), and only then do the files take their names, never over
//     anything the plan did not list (land.go). A mover that reports success
//     on a bad result fails the job.
//   - A move is a copy, that check, then the Studio removing the originals
//     itself. CONDUIT is only ever sent COPY.
//   - With sign-in on every transfer belongs to the viewer and runs with the
//     viewer's own credentials.
package transfers

import "time"

// Location kinds.
const (
	KindLibrary  = "library"
	KindComputer = "computer"
	KindCluster  = "cluster"
	KindSaved    = "saved"
	// KindHost is the home folder of a host a remote library is reached
	// on, offered with no setup (sign-in off only).
	KindHost    = "host"
	KindConduit = "conduit"
	// KindRclone is a remote of the user's rclone configuration (sign-in
	// off only).
	KindRclone = "rclone"
)

// Location is a place files live, as the page sees it. Indexed is a
// library with an index, so its files are searchable.
type Location struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Label     string `json:"label"`
	Detail    string `json:"detail"`
	Writable  bool   `json:"writable"`
	Indexed   bool   `json:"indexed"`
	Browsable bool   `json:"browsable"`
	Available bool   `json:"available"`
	Why       string `json:"why,omitempty"`
	// ReadOnly is why a reachable library is read only for transfers
	// (also in Detail); "" otherwise.
	ReadOnly string `json:"readOnly,omitempty"`
	// Suggest is what Studio knows of where a library's files are, so the
	// page and the assistant can offer to save a location for them. It is
	// set with sign-in off for a library whose files Studio cannot reach
	// or write; a field Studio does not know is "".
	Suggest *Suggest `json:"suggest,omitempty"`
	// Hint says what can be written instead, for such a library with
	// sign-in on, where saved locations are off.
	Hint string `json:"hint,omitempty"`
}

// Suggest is a saved location proposed for a library's files: a label,
// and the ssh host (with port and options) and folder they are on, where
// known.
type Suggest struct {
	Label      string `json:"label"`
	Host       string `json:"host,omitempty"`
	Root       string `json:"root,omitempty"`
	Port       int    `json:"port,omitempty"`
	SSHOptions string `json:"sshOptions,omitempty"`
}

// Entry is one name in a browsed folder.
type Entry struct {
	Name  string `json:"name"`
	Type  string `json:"type"` // file, dir, link
	Size  int64  `json:"size"`
	Mtime string `json:"mtime"`
}

// Listing is a browsed folder.
type Listing struct {
	Location  string  `json:"location"`
	Path      string  `json:"path"`
	Parent    *string `json:"parent"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
}

// Actions.
const (
	ActionCopy = "copy"
	ActionMove = "move"
	ActionSync = "sync"
)

// Request is a plan request (POST /api/transfers/plan).
type Request struct {
	Action string `json:"action"`
	Source struct {
		Location string   `json:"location"`
		Paths    []string `json:"paths"`
	} `json:"source"`
	Destination struct {
		Location string `json:"location"`
		Path     string `json:"path"`
	} `json:"destination"`
	Options Options `json:"options"`
}

// Options are a request's choices.
type Options struct {
	Mover         string `json:"mover,omitempty"`
	Replace       bool   `json:"replace,omitempty"`
	DeleteExtra   bool   `json:"deleteExtra,omitempty"`
	Checksum      *bool  `json:"checksum,omitempty"`
	PreserveTimes *bool  `json:"preserveTimes,omitempty"`
	Label         string `json:"label,omitempty"`
}

func (o Options) preserve() bool { return o.PreserveTimes == nil || *o.PreserveTimes }
func (o Options) checksum() bool { return o.Checksum != nil && *o.Checksum }

// End is one end of a plan or a transfer as the page sees it.
type End struct {
	Location string   `json:"location"`
	Label    string   `json:"label"`
	Paths    []string `json:"paths,omitempty"`
	Path     *string  `json:"path,omitempty"`
}

// MoverChoice is one mover as a plan offers it.
type MoverChoice struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Why  string `json:"why,omitempty"`
}

// Skipped counts what a listing left out.
type Skipped struct {
	Hidden int `json:"hidden"`
	Links  int `json:"links"`
}

// Plan is what a transfer would do, shown before it starts.
type Plan struct {
	ID            string        `json:"id"`
	Action        string        `json:"action"`
	Label         string        `json:"label"`
	Mover         string        `json:"mover"`
	MoverWhy      string        `json:"moverWhy"`
	Movers        []MoverChoice `json:"movers"`
	Source        End           `json:"source"`
	Destination   End           `json:"destination"`
	Files         int           `json:"files"`
	Dirs          int           `json:"dirs"`
	Bytes         int64         `json:"bytes"`
	Replaces      []string      `json:"replaces"`
	Deletes       []string      `json:"deletes"`
	ReplacesCount int           `json:"replacesCount"`
	DeletesCount  int           `json:"deletesCount"`
	Skipped       Skipped       `json:"skipped"`
	FreeBytes     *int64        `json:"freeBytes"`
	Warnings      []string      `json:"warnings"`
	// IndexNotes say when the index of a library the transfer changes,
	// other than the main library, shows the change.
	IndexNotes []string `json:"indexNotes"`
	ExpiresAt  string   `json:"expiresAt"`
	// Compare is how a sync tells changed files: "size and modification
	// time", or size and a hash.
	Compare string `json:"compare,omitempty"`
}

// States of a transfer.
const (
	StateAwaiting    = "awaiting-approval"
	StateQueued      = "queued"
	StateRunning     = "running"
	StateVerifying   = "verifying"
	StateSucceeded   = "succeeded"
	StateFailed      = "failed"
	StateCancelled   = "cancelled"
	StateInterrupted = "interrupted"
)

func finished(state string) bool {
	switch state {
	case StateSucceeded, StateFailed, StateCancelled, StateInterrupted:
		return true
	}
	return false
}

// Count is done of total; total -1 while counting.
type Count struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

// Event is one line of a transfer's log.
type Event struct {
	At   string `json:"at"`
	Text string `json:"text"`
}

// Transfer is a job as the page sees it.
type Transfer struct {
	ID             string   `json:"id"`
	Label          string   `json:"label"`
	Action         string   `json:"action"`
	Mover          string   `json:"mover"`
	Source         End      `json:"source"`
	Destination    End      `json:"destination"`
	State          string   `json:"state"`
	StartedBy      string   `json:"startedBy"`
	ConversationID string   `json:"conversationId,omitempty"`
	RequestedAt    string   `json:"requestedAt"`
	StartedAt      string   `json:"startedAt,omitempty"`
	EndedAt        string   `json:"endedAt,omitempty"`
	Files          Count    `json:"files"`
	Bytes          Count    `json:"bytes"`
	Rate           *float64 `json:"rate,omitempty"`
	EtaSeconds     *int64   `json:"etaSeconds,omitempty"`
	Replaced       int      `json:"replaced"`
	Deleted        int      `json:"deleted"`
	SkippedCount   int      `json:"skipped"`
	Error          string   `json:"error,omitempty"`
	// IndexNotes are the plan's: when a changed library's index shows it.
	IndexNotes []string `json:"indexNotes,omitempty"`
	Events     []Event  `json:"events,omitempty"`
}

func iso(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z") }

// Limits.
const (
	MaxFiles      = 50000
	MaxPaths      = 100
	MaxBrowse     = 2000
	PlanTTL       = 15 * time.Minute
	listShown     = 50
	keepEvents    = 200
	keepFinished  = 500
	keepFor       = 30 * 24 * time.Hour
	stagingPrefix = ".studio-transfer-"
)
