package chat

import (
	"context"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/kb"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
)

// maxPWCodeAttachments is how many of a message's attachments go to its
// session, as attachmentContext reads at most eight.
const maxPWCodeAttachments = 8

var imagePlaceholder = regexp.MustCompile(`\[Image #(\d+)\]`)

// nextImageSeq is the number the session's next image takes: one past
// every placeholder its history names, as the session view numbers them.
func nextImageSeq(history any) int {
	seq := 0
	for _, it := range jsonv.Arr(history) {
		for _, m := range imagePlaceholder.FindAllStringSubmatch(jsonv.String(jsonv.Path(it, "text")), -1) {
			if n, err := strconv.Atoi(m[1]); err == nil && n > seq {
				seq = n
			}
		}
	}
	return seq + 1
}

// pwcodeAttachments readies a chat message's attachments for its pw code
// session. Each picture pw code takes is saved with the session and goes
// with the message under its placeholder, as the session view's composer
// sends one, so the model sees it. Any other file, or a picture that could
// not be saved, is named in the text by where it is in the library, with
// what was read from it, as the Studio assistant is told.
func (m *Module) pwcodeAttachments(ctx context.Context, c pwcode.Client, session string, ids []string, history any) ([]pwcode.Image, string) {
	if len(ids) > maxPWCodeAttachments {
		ids = ids[:maxPWCodeAttachments]
	}
	seq := nextImageSeq(history)
	var images []pwcode.Image
	var rest []string
	for _, id := range ids {
		rel, err := decodeAttachID(id)
		if err != nil {
			continue
		}
		if data, mime := m.attachedImage(rel); data != nil {
			path, err := pwcode.SaveImage(ctx, c, session, seq, mime, data)
			if err == nil {
				images = append(images, pwcode.Image{Placeholder: "[Image #" + strconv.Itoa(seq) + "]", Path: path, Mime: mime})
				seq++
				continue
			}
			m.A.Log.Warn("pw code chat: an attached image was not saved with the session", "session", session, "err", err)
		}
		rest = append(rest, id)
	}
	return images, m.attachmentContext(rest)
}

// attachedImage is an attachment's bytes and type when it is a picture pw
// code takes (PNG, JPEG, GIF or WebP, by its content, up to 5 MiB).
func (m *Module) attachedImage(rel string) ([]byte, string) {
	abs, err := kb.Resolve(rel, m.kbRoot())
	if err != nil {
		return nil, ""
	}
	st, err := os.Stat(abs)
	if err != nil || !st.Mode().IsRegular() || st.Size() == 0 || st.Size() > pwcode.MaxImageBytes {
		return nil, ""
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return nil, ""
	}
	mime, _, _ := strings.Cut(http.DetectContentType(data), ";")
	if !imageTypes[mime] {
		return nil, ""
	}
	return data, mime
}

// withAttachments is the message's text with each image's placeholder it
// does not name already, then what the other attachments hold.
func withAttachments(text string, images []pwcode.Image, note string) string {
	for _, im := range images {
		if !strings.Contains(text, im.Placeholder) {
			text = strings.TrimSpace(text + " " + im.Placeholder)
		}
	}
	if note != "" {
		text = strings.TrimSpace(text + "\n\n" + note)
	}
	return text
}
