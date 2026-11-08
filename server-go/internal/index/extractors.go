package index

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/parallelworks/activate-studio/server-go/internal/extract"
)

// extractors.ts: which readers this host has for each kind of file, so a
// format that would index by its name alone is said out loud. In Go the
// extraction pass carries downmark with its PDF reader, the OpenDocument
// reader and the .xls and .ppt readers (where Node used xlrd and olefile
// from Python); pypdf has no counterpart and is never available. poppler,
// LibreOffice and tesseract are used where installed.

// ReaderOptions are what the depth and settings read.
type ReaderOptions struct {
	Documents, OCR, LegacyOffice bool
	PDFReader                    string
	Vision                       bool
}

// ExtractorReport is one reader.
type ExtractorReport struct {
	Name      string `json:"name"`
	Available bool   `json:"available"`
	Covers    string `json:"covers"`
	Note      string `json:"note"`
	Optional  bool   `json:"optional"`
}

// FormatReadiness is one group of formats.
type FormatReadiness struct {
	Formats string   `json:"formats"`
	Readers []string `json:"readers"`
	OK      bool     `json:"ok"`
	Note    string   `json:"note"`
}

// InstallItem is a tool this machine lacks.
type InstallItem struct {
	Name    string  `json:"name"`
	Adds    string  `json:"adds"`
	Command *string `json:"command"`
	Link    *string `json:"link"`
}

// InstallInfo is how to install what is missing here.
type InstallInfo struct {
	Platform string        `json:"platform"`
	Brew     *string       `json:"brew"`
	Apt      bool          `json:"apt"`
	Items    []InstallItem `json:"items"`
}

// Report is extractorReport's answer.
type Report struct {
	Extractors []ExtractorReport `json:"extractors"`
	Formats    []FormatReadiness `json:"formats"`
	Missing    []FormatReadiness `json:"missing"`
	Install    InstallInfo       `json:"install"`
}

type probed struct {
	tools                   extract.Tools
	brew, soffice, tessPath string
	apt                     bool
}

var (
	probeMu sync.Mutex
	probe   *probed
)

func getProbe() *probed {
	probeMu.Lock()
	defer probeMu.Unlock()
	if probe == nil {
		p := &probed{tools: extract.FindTools(), soffice: findSoffice()}
		p.tessPath = p.tools.Tesseract
		for _, b := range []string{"/opt/homebrew/bin/brew", "/usr/local/bin/brew"} {
			if exists(b) {
				p.brew = b
				break
			}
		}
		_, err := exec.LookPath("apt-get")
		p.apt = err == nil
		probe = p
	}
	return probe
}

// ForgetExtractors looks again, as after installing LibreOffice or tesseract.
func ForgetExtractors() {
	probeMu.Lock()
	probe = nil
	probeMu.Unlock()
	sofficeMu.Lock()
	sofficeKnown = nil
	sofficeMu.Unlock()
}

// bundledTesseract: the macOS app's own tesseract, beside the app's code.
func bundledTesseract(bin, projectRoot string) bool {
	if bin == "" {
		return false
	}
	abs, _ := filepath.Abs(bin)
	root, _ := filepath.Abs(filepath.Join(projectRoot, "..", "ocr", "bin"))
	return strings.HasPrefix(abs, root+string(filepath.Separator))
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func installCommand(tool, brew string, apt bool) *string {
	if runtime.GOOS == "darwin" {
		if brew == "" {
			return nil
		}
		if tool == "libreoffice" {
			return strPtr("brew install --cask libreoffice")
		}
		return strPtr("brew install " + tool)
	}
	if apt {
		return strPtr("sudo apt-get install " + map[string]string{"tesseract": "tesseract-ocr", "poppler": "poppler-utils", "libreoffice": "libreoffice"}[tool])
	}
	return nil
}

func installInfo(tesseract, poppler, soffice, scanOCR bool, brew string, apt bool) InstallInfo {
	item := func(name, adds, tool string) InstallItem {
		cmd := installCommand(tool, brew, apt)
		var link *string
		if cmd == nil && runtime.GOOS == "darwin" {
			link = strPtr("https://brew.sh")
		}
		return InstallItem{name, adds, cmd, link}
	}
	items := []InstallItem{}
	if !tesseract {
		items = append(items, item("tesseract", "OCR reads scanned PDFs and text in images.", "tesseract"))
	}
	if !poppler {
		adds := "Poppler reads PDFs the bundled readers cannot, and lets OCR read scanned PDF pages."
		if scanOCR {
			adds = "Poppler reads PDFs the bundled readers cannot."
		}
		items = append(items, item("poppler", adds, "poppler"))
	}
	if !soffice {
		items = append(items, item("LibreOffice", "LibreOffice renders previews of Office files.", "libreoffice"))
	}
	return InstallInfo{Platform: runtime.GOOS, Brew: strPtr(brew), Apt: apt, Items: items}
}

// ExtractorReportFor is extractorReport.
func ExtractorReportFor(o ReaderOptions, projectRoot string) Report {
	p := getProbe()
	tess := p.tools.Tesseract
	pdfVia := []string{"downmark"}
	if o.PDFReader != "pypdf" && p.tools.Pdftotext != "" {
		pdfVia = append(pdfVia, "pdftotext")
	}
	tessNote := "Not installed. Install tesseract (for example with Homebrew) to read text in images and scans."
	switch {
	case bundledTesseract(tess, projectRoot):
		tessNote = "Bundled, with its English model. Reads text in images, and scanned PDF pages with downmark."
	case tess != "":
		tessNote = "Found at " + tess + "."
	}
	sofficeNote := "soffice is not on the PATH. Office files are still read; install LibreOffice for their previews."
	if p.soffice != "" {
		sofficeNote = "Found at " + p.soffice + "."
	} else if runtime.GOOS == "darwin" {
		sofficeNote = "Not found in /Applications/LibreOffice.app. Office files are still read; install LibreOffice for their previews."
	}
	pdftotextNote := "Not installed (poppler). Not needed while downmark reads PDFs."
	if p.tools.Pdftotext != "" {
		pdftotextNote = "Found at " + p.tools.Pdftotext + "."
	}
	ex := []ExtractorReport{
		{"downmark", true, ".docx, .pptx, .xlsx, .doc, .pdf", "Bundled. Converts documents and PDFs to Markdown, keeping headings and tables.", false},
		{"pypdf", false, ".pdf", "Not used by this Studio: downmark reads PDFs, and pdftotext where installed.", true},
		{"pdftotext", p.tools.Pdftotext != "", ".pdf", pdftotextNote, true},
		{"OpenDocument reader", true, ".odt, .ods, .odp, .odg", "Part of the Studio, with nothing to install.", false},
		{"xlrd", true, ".xls", "Part of the Studio. Reads Excel 97-2003 workbooks.", false},
		{"olefile", true, ".ppt", "Part of the Studio. Reads PowerPoint 97-2003 slides and speaker notes.", false},
		{"LibreOffice", p.soffice != "", "previews of Office documents, and .doc, .ppt, .xls and OpenDocument files the readers above cannot read", sofficeNote, false},
		{"tesseract", tess != "", "text inside images and scanned PDF pages (OCR)", tessNote, false},
	}
	formats := []FormatReadiness{}
	if !o.Documents {
		formats = append(formats, FormatReadiness{".pdf, .docx, .pptx, .xlsx, .doc", []string{}, false, "The index depth is Names, so these index by name only."})
	} else {
		formats = append(formats, FormatReadiness{".docx, .pptx, .xlsx", []string{"downmark"}, true, "Read by downmark, with a standard-library reader for any it cannot."})
		lo := []string{}
		if o.LegacyOffice && p.soffice != "" {
			lo = []string{"LibreOffice"}
		}
		doc := append([]string{"downmark"}, lo...)
		formats = append(formats, FormatReadiness{".doc", doc, true, "Read by " + strings.Join(doc, ", then ") + "."})
		scans := ""
		if o.OCR && tess != "" {
			scans = " Scanned pages are read by tesseract."
		}
		formats = append(formats, FormatReadiness{".pdf", pdfVia, true, "Read by " + strings.Join(pdfVia, ", then ") + "." + scans})
		odf := append([]string{"OpenDocument reader"}, lo...)
		formats = append(formats, FormatReadiness{".odt, .ods, .odp", odf, true, "Read by " + strings.Join(odf, ", then ") + ": text, tables, slides and speaker notes."})
		for _, f := range [][2]string{{".xls", "xlrd"}, {".ppt", "olefile"}} {
			r := append([]string{f[1]}, lo...)
			formats = append(formats, FormatReadiness{f[0], r, true, "Read by " + strings.Join(r, ", then ") + "."})
		}
	}
	img := []string{}
	if o.OCR && tess != "" {
		img = append(img, "tesseract")
	}
	if o.Vision {
		img = append(img, "vision model")
	}
	imgNote := "Images index by name only: tesseract is not installed, and no vision model describes them."
	switch {
	case len(img) > 0:
		imgNote = "Text in images is read by " + strings.Join(img, " and ") + "."
	case !o.OCR:
		imgNote = "The index depth stops before Text + OCR, so images index by name only."
	}
	formats = append(formats, FormatReadiness{"images", img, len(img) > 0, imgNote})
	missing := []FormatReadiness{}
	for _, f := range formats {
		if !f.OK {
			missing = append(missing, f)
		}
	}
	return Report{ex, formats, missing, installInfo(tess != "", p.tools.Pdftotext != "" && p.tools.Pdftoppm != "", p.soffice != "", tess != "", p.brew, p.apt)}
}
