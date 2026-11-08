package extract

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/extract/pytext"
)

const (
	// MinImageBytes skips icons and tiny assets.
	MinImageBytes = 8 * 1024
	// MaxCaptionBytes is the largest image sent to the vision model.
	MaxCaptionBytes = 8 << 20
	captionTimeout  = 180 * time.Second
	// DefaultGateway is the platform's OpenAI-compatible gateway.
	DefaultGateway = "https://activate.parallel.works/api/openai/v1"
	captionPrompt  = "Describe this image in two to four factual sentences for a search index. Then transcribe any visible text labels."
)

// Vision is the image captioner: a vision model through the gateway's
// streaming Responses API, as enrich.py's caption_image calls it.
type Vision struct {
	Model   string // ADE_VISION_MODEL; empty turns captions off
	Key     string // the gateway key
	BaseURL string // PW_GATEWAY_URL; DefaultGateway when empty
	Client  *http.Client
}

func (v Vision) enabled() bool { return v.Model != "" && v.Key != "" }

// VisionFromEnv is the captioner enrich.py would use: ADE_VISION_MODEL,
// PW_GATEWAY_URL, and the key from PW_API_KEY or, failing that, the pw
// CLI's credentials for the gateway's host.
func VisionFromEnv() Vision {
	return Vision{Model: os.Getenv("ADE_VISION_MODEL"), Key: GatewayKey(), BaseURL: os.Getenv("PW_GATEWAY_URL")}
}

// GatewayKey is enrich.py's gateway_key().
func GatewayKey() string {
	if k := os.Getenv("PW_API_KEY"); k != "" {
		return k
	}
	gw := os.Getenv("PW_GATEWAY_URL")
	if gw == "" {
		gw = DefaultGateway
	}
	u, err := url.Parse(gw)
	if err != nil {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(home, ".config", "pw", "credentials"))
	if err != nil {
		return ""
	}
	ids, err := identities(b)
	if err != nil {
		return ""
	}
	for _, id := range ids {
		if id.Server == u.Host {
			if id.Token != "" {
				return id.Token
			}
			return id.APIKey
		}
	}
	return ""
}

type identity struct {
	Server string `json:"server"`
	Token  string `json:"token"`
	APIKey string `json:"apikey"`
}

// identities are the credentials file's identities in file order, the
// order Python's dict keeps and enrich.py takes the first match in.
func identities(b []byte) ([]identity, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(b, &top); err != nil {
		return nil, err
	}
	raw, ok := top["identities"]
	if !ok {
		return nil, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if t, err := d.Token(); err != nil || t != json.Delim('{') {
		return nil, errors.New("identities is not an object")
	}
	var out []identity
	for d.More() {
		if _, err := d.Token(); err != nil { // the key
			return nil, err
		}
		var id identity
		if err := d.Decode(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, nil
}

// errCaption is a caption request that failed, which the next pass retries.
var errCaption = errors.New("caption request failed")

// caption describes an image. It returns "" with no error when captions
// are off or the image is too large, and errCaption when the request
// failed, so a picture with nothing to say is told from one that was
// never described.
func (x *Extractor) caption(ctx context.Context, path string, size int64) (string, error) {
	v := x.opt.Vision
	if !v.enabled() || size > MaxCaptionBytes {
		return "", nil
	}
	img, err := os.ReadFile(path)
	if err != nil {
		return "", errCaption
	}
	s := Suffix(path)
	mime := "image/" + strings.TrimPrefix(s, ".")
	if s == ".jpg" || s == ".jpeg" {
		mime = "image/jpeg"
	}
	body, _ := json.Marshal(map[string]any{
		"model":  v.Model,
		"stream": true,
		"input": []any{map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": captionPrompt},
			map[string]any{"type": "input_image", "image_url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img)},
		}}},
	})
	base := v.BaseURL
	if base == "" {
		base = DefaultGateway
	}
	ctx, cancel := context.WithTimeout(ctx, captionTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/responses", bytes.NewReader(body))
	if err != nil {
		return "", errCaption
	}
	req.Header.Set("Authorization", "Bearer "+v.Key)
	req.Header.Set("Content-Type", "application/json")
	client := v.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		x.log("caption failed %s: %.200s", path, err)
		return "", errCaption
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		x.log("caption failed %s: HTTP %d", path, resp.StatusCode)
		return "", errCaption
	}
	var text strings.Builder
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		data, ok := strings.CutPrefix(line, "data:")
		if !ok {
			continue
		}
		data = strings.TrimSpace(data)
		if data == "[DONE]" {
			break
		}
		var ev struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		if ev.Type == "response.output_text.delta" {
			text.WriteString(ev.Delta)
		}
	}
	if err := sc.Err(); err != nil {
		x.log("caption failed %s: %.200s", path, err)
		return "", errCaption
	}
	return pytext.Strip(text.String()), nil
}

// Image is enrich.py's extract_image_status: the image's caption and OCR
// text, and whether the result is complete enough to cache (false when a
// caption request failed, so the next pass asks again). Images under
// MinImageBytes are skipped, except in the chat attachments directory
// (uploads/chat), where the user attached them deliberately.
func (x *Extractor) Image(ctx context.Context, path string) (text string, complete bool, err error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", false, err
	}
	dir := filepath.Dir(path)
	deliberate := filepath.Base(dir) == "chat" && filepath.Base(filepath.Dir(dir)) == "uploads"
	if !deliberate && st.Size() < MinImageBytes {
		return "", true, nil
	}
	var parts []string
	cap, cerr := x.caption(ctx, path, st.Size())
	if cap != "" {
		parts = append(parts, "Image description:\n"+cap)
	}
	if x.ocr {
		if t, err := x.tesseract(ctx, path, ocrImageTimeout); err == nil {
			if t = pytext.Strip(t); pytext.Len(t) > 20 {
				parts = append(parts, "Text found in image (OCR):\n"+t)
			}
		}
	}
	return strings.Join(parts, "\n\n"), cerr == nil, nil
}

// ImageCacheUsable is enrich.py's image_cache_usable: a cached image
// result is reused unless it is empty while a vision model is configured
// (a successful caption is never empty).
func (x *Extractor) ImageCacheUsable(text string) bool {
	return pytext.Strip(text) != "" || x.opt.Vision.Model == ""
}
