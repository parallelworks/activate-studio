package index

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// A library built for one person describes its images with that person's
// key (LibraryWorkOptions.VisionKey), or describes none and says why in
// the index log; the rest with the Studio's own, as before.
func TestImagesAreDescribedWithTheLibrarysKey(t *testing.T) {
	dir := t.TempDir()
	env := map[string]string{"KB_ROOT": filepath.Join(dir, "kb"), "INDEX_BASE": filepath.Join(dir, "ix"), "STUDIO_NODE": "off",
		"AI_ENABLED": "1", "ADE_VISION_MODEL": "caption-model", "SWEEP_INTERVAL_SEC": "0"}
	t.Setenv("HOME", dir)
	for k, v := range env {
		t.Setenv(k, v)
	}
	cfg, err := config.Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	ix := Of(a)
	s := Settings{Documents: true, OCR: true, ImageDescriptions: true}
	work := func(key func() (string, string, bool)) *jobCtx {
		return ix.LibraryWork(context.Background(), LibraryPaths{Source: dir, Index: dir}, LibraryWorkOptions{JobID: 7, Settings: s, VisionKey: key}).c
	}
	for _, c := range []struct {
		name       string
		key        func() (string, string, bool)
		model, got string
	}{
		{"the Studio's", nil, "caption-model", ""},
		{"the owner's", func() (string, string, bool) { return "owners-key", "", true }, "caption-model", "owners-key"},
		{"none", func() (string, string, bool) { return "", "Sign in with ACTIVATE first", false }, "", ""},
	} {
		if m, k := work(c.key).vision(); m != c.model || k != c.got {
			t.Errorf("%s: %q %q", c.name, m, k)
		}
	}
	if !strings.Contains(ix.Log.Tail(20), "images are not described: Sign in with ACTIVATE first") {
		t.Errorf("log %q", ix.Log.Tail(20))
	}

	// The child's environment: the Studio's own names neither a model nor
	// a person's key it did not mean.
	base := []string{"PW_API_KEY=studio", "ADE_VISION_MODEL=caption-model", "STUDIO_VISION_KEY=stray"}
	for _, c := range []struct {
		model, key string
		want       []string
	}{
		{"", "", []string{"PW_API_KEY=studio"}},
		{"caption-model", "", []string{"PW_API_KEY=studio", "ADE_VISION_MODEL=caption-model"}},
		{"caption-model", "owners-key", []string{"PW_API_KEY=studio", "ADE_VISION_MODEL=caption-model", "STUDIO_VISION_KEY=owners-key"}},
	} {
		if got := visionEnv(slices.Clone(base), c.model, c.key); !slices.Equal(got, c.want) {
			t.Errorf("%q %q: %q", c.model, c.key, got)
		}
	}
}
