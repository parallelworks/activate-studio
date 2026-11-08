package extract

import (
	"context"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const sofficeTimeout = 180 * time.Second

// Legacy reads a file through LibreOffice, as enrich.py's extract_legacy
// does: converted to its OOXML counterpart (LegacySuffixes) in a scratch
// directory, then read with the flat OOXML reader. Each conversion at a
// time has a LibreOffice profile of its own, so conversions run side by
// side instead of fighting over one. "" without LibreOffice.
func (x *Extractor) Legacy(ctx context.Context, path string) string {
	target := LegacySuffixes[Suffix(path)]
	if x.opt.Soffice == "" || target == "" {
		return ""
	}
	var profile string
	select {
	case profile = <-x.profiles:
	case <-ctx.Done():
		return ""
	}
	defer func() { x.profiles <- profile }()
	out, err := os.MkdirTemp("", "studio-legacy-")
	if err != nil {
		return ""
	}
	defer os.RemoveAll(out)
	ctx, cancel := context.WithTimeout(ctx, sofficeTimeout)
	defer cancel()
	profileURL := (&url.URL{Scheme: "file", Path: filepath.ToSlash(profile)}).String()
	if !strings.HasPrefix(filepath.ToSlash(profile), "/") {
		profileURL = "file:///" + filepath.ToSlash(profile) // a Windows drive path
	}
	cmd := exec.CommandContext(ctx, x.opt.Soffice, "-env:UserInstallation="+profileURL, "--headless",
		"--convert-to", target, "--outdir", out, path)
	cmd.Env = helperEnv()
	if err := cmd.Run(); err != nil && ctx.Err() != nil {
		return ""
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	made := filepath.Join(out, stem+"."+target)
	f, err := os.Open(made)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	switch target {
	case "docx":
		return ooxmlDocx(f, st.Size())
	case "pptx":
		return ooxmlPptx(f, st.Size())
	default:
		return ooxmlXlsx(f, st.Size())
	}
}
