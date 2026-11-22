package search

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/libraries"
)

// A person's SQL reaches gufi_query through the engine's QueryScope (the
// Query page, saved queries run again, the API) and is shown by QuerySQL;
// both refuse a function that runs a program or opens a file, for one
// library and for all, before anything is run.
func TestQueryEntryPointsRefuseFunctionsThatRunPrograms(t *testing.T) {
	e := scopeEngine(t)
	single := libraries.ViewerOf("")
	for _, c := range []struct{ name, sql string }{
		{"strop", "SELECT strop('id') FROM summary"},
		{"blobop", `SELECT "BlobOp"/**/('id')`},
		{"run_vt", "SELECT * FROM run_vt"},
		{"load_extension", "SELECT load_extension('x')"},
	} {
		for _, lib := range []any{"u-a", "all", []string{"u-a", "u-b"}} {
			body, _ := json.Marshal(map[string]any{"library": lib, "sql": c.sql})
			for what, run := range map[string]func() error{
				"QueryScope": func() error { _, err := e.QueryScope(context.Background(), body, single, "kb"); return err },
				"QuerySQL":   func() error { _, err := e.QuerySQL(context.Background(), body, single, "kb"); return err },
			} {
				err := run()
				var ae *apierr.Error
				if !errors.As(err, &ae) || ae.Status != 400 || !strings.Contains(ae.Message, c.name) {
					t.Errorf("%s %v %q: %v", what, lib, c.sql, err)
				}
			}
		}
	}
}
