package transfers

import (
	"context"
	"testing"
)

// testWriter: no write helper is built for Windows.
func testWriter(*testing.T) func(context.Context) WriteTool { return nil }
