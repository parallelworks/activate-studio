package libapi

import (
	"bytes"
	"encoding/json"
	"strings"
)

// jsonIndent is JSON.stringify's indentation with indent, without
// escaping <, > and & (json.Indent keeps the input's own escapes).
func jsonIndent(out *strings.Builder, b []byte, indent string) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", indent); err != nil {
		return err
	}
	out.Write(buf.Bytes())
	return nil
}
