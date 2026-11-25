package staging

import "testing"

func TestIn(t *testing.T) {
	for p, want := range map[string]bool{
		"": false, "a/b.txt": false, ".studio-upload": false, ".studio-uploads/x": false, "x.studio-upload-1": false, ".studio-index.json": false,
		".studio-upload-1": true, "a/.studio-upload-0f/data": true, ".studio-transfer-x/b/c.txt": true, "a/.STUDIO-Upload-1/data": true, "/a/.studio-transfer-": true,
	} {
		if In(p) != want {
			t.Errorf("%q: %v", p, !want)
		}
	}
}
