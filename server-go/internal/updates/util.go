package updates

import (
	"fmt"
	"os"

	"github.com/parallelworks/activate-studio/server-go/internal/state"
)

// Brief is a snapshot without its file list, as the status shows it.
type Brief struct {
	ID     string  `json:"id"`
	From   string  `json:"from"`
	To     *string `json:"to"`
	Reason string  `json:"reason"`
	Format int     `json:"format"`
	At     string  `json:"at"`
}

func brief(s state.Snapshot) Brief {
	return Brief{ID: s.ID, From: s.From, To: s.To, Reason: s.Reason, Format: s.Format, At: s.At}
}

func strp(s string) *string { return &s }

// writeRename writes file through a temporary file renamed over it.
func writeRename(file string, b []byte) error {
	tmp := fmt.Sprintf("%s.%d.tmp", file, os.Getpid())
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, file)
}
