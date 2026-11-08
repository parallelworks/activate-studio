package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// First-run layout for an empty knowledge base (seed.ts): a few
// directories and a README, so a new deployment opens on something with
// shape. It runs only when the corpus is empty (dot-directories aside),
// and KB_STARTER_DIRS set to nothing gives a bare tree.

var defaultDirs = []string{"papers", "software", "datasets", "reports"}

// A singular label per directory, so filtering by kind works from the
// first day.
var dirLabel = map[string]string{"papers": "paper", "software": "software", "datasets": "dataset", "reports": "report"}

var unsafeDirChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

func seedReadme(appName, kbLabel string, dirs []string) string {
	lines := []string{
		"# " + kbLabel,
		"",
		fmt.Sprintf("This directory is the corpus behind %s. Everything in it is indexed for search and available to the assistant, which answers by reading these files and citing them.", appName),
		"",
	}
	if len(dirs) > 0 {
		lines = append(lines, "It starts with a few directories. Rename them, remove them, or add your own.", "")
		for _, d := range dirs {
			lines = append(lines, "- "+d)
		}
		lines = append(lines, "")
	}
	lines = append(lines,
		"Add material by dragging files onto the Library tree or with the Add button, which also accepts a URL. Files that arrive on disk another way are picked up by the background sync within minutes.",
		"",
		"Labels organize the corpus without moving anything: a label on a directory covers everything beneath it, and search, the query builder, and the assistant can all filter by them.",
		"",
	)
	return strings.Join(lines, "\n")
}

func isEmptyDir(dir string) bool {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	for _, e := range entries {
		if !strings.HasPrefix(e.Name(), ".") {
			return false
		}
	}
	return true
}

// SeedKnowledgeBase is seedKnowledgeBase: lays out an empty KB_ROOT and
// labels its directories through Providers.ApplyLabels, where labels can
// be applied (a filesystem that refuses them is no error).
func (a *App) SeedKnowledgeBase(ctx context.Context, log func(string)) {
	root := a.Cfg.KBRoot
	dirs := defaultDirs
	if configured, set := os.LookupEnv("KB_STARTER_DIRS"); set {
		dirs = nil
		for _, d := range strings.Split(configured, ",") {
			if d = strings.TrimSpace(d); d != "" {
				dirs = append(dirs, d)
			}
		}
	}
	var clean []string
	for _, d := range dirs {
		if d = unsafeDirChars.ReplaceAllString(d, ""); d != "" {
			clean = append(clean, d)
		}
	}
	if _, err := os.Stat(root); err == nil && !isEmptyDir(root) {
		return
	}
	err := os.MkdirAll(root, 0o755)
	for _, d := range clean {
		if err == nil {
			err = os.MkdirAll(filepath.Join(root, d), 0o755)
		}
	}
	if err == nil {
		eff := a.Settings.Effective()
		err = os.WriteFile(filepath.Join(root, "README.md"), []byte(seedReadme(eff.AppName, eff.KbLabel, clean)), 0o644)
	}
	if err != nil {
		log("could not seed the knowledge base: " + err.Error())
		return
	}
	log(fmt.Sprintf("seeded an empty knowledge base with %d directories and a README", len(clean)))
	if a.Providers.ApplyLabels == nil {
		return
	}
	for _, d := range clean {
		if label := dirLabel[d]; label != "" {
			a.Providers.ApplyLabels(ctx, nil, []string{d}, []string{label}, nil)
		}
	}
}
