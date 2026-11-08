//go:build !linux

package kb

// Off Linux the Node server has no getfattr or setfattr, so its labels go
// to the overlay; the Go server does the same while Node still reads and
// applies labels beside it (the chat tool, search), so the two agree.
// Labels on files there are an open question (docs/GO-PORT.md).
const xattrsSupported = false

func getXattr(abs, name string) (string, bool)  { return "", false }
func setXattr(abs, name, value string) bool     { return false }
func removeXattr(abs, name string)              {}
