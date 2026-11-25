package kb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/staging"
)

// A large file goes up in parts, each a request of its own, so no one
// request carries it all and a part whose connection drops is sent again
// alone. Its parts are written into one file in a folder of its own beside
// where it goes, closed to other accounts; when all are in and the size is
// right, the file takes its name in one rename. An upload that fails, is
// cancelled, or hears nothing for UploadIdle leaves nothing behind.

// UploadPart is the size of every part but the last.
const UploadPart = 16 << 20

// UploadIdle is how long an upload waits for its next part, partStall how
// long a part waits for its next bytes, and partWait how long one is written
// before its answer begins.
var (
	UploadIdle = 30 * time.Minute
	partStall  = 2 * time.Minute
	partWait   = 8 * time.Second
)

// maxOpenUploads bounds the large files one person may have under way, and
// maxUploadSize is the largest size a page can name exactly.
const (
	maxOpenUploads = 32
	maxUploadSize  = 1 << 53
)

// partsPrefix starts the name of the folder a file's parts are written in.
const partsPrefix = staging.Upload

// partUpload is one large file under way.
type partUpload struct {
	id, owner string
	lib       Library // as it was at the start
	space     bool
	rel       string // the file, in its library
	size      int64
	dir       string   // its parts' folder: absolute here, in the library for a space
	file      *os.File // the file that takes them, here
	dest      string   // where it goes, absolute, here

	mu     sync.Mutex
	got    []bool
	left   int // parts still to come
	busy   map[int]*partWrite
	state  string        // "", "landing", "landed" or "dropped"
	req    *http.Request // the last one, without its end, to clean up with
	expiry *time.Timer
}

// partWrite is a part being written.
type partWrite struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// uploads is the large files under way, and those an earlier run of the
// Studio left in people's own libraries, which only they can clean up.
type uploads struct {
	mu     sync.Mutex
	open   map[string]*partUpload
	lefts  map[string][]leftUpload
	record string // folder of the records, "" before the first
}

// leftUpload is what is kept on disk of an upload under way, so that a
// Studio that stops before it ends can remove what it wrote.
type leftUpload struct {
	ID    string `json:"id"`
	Owner string `json:"owner"`
	Lib   string `json:"library"`
	Space bool   `json:"space"`
	Dir   string `json:"dir"`
}

func (u *partUpload) parts() int { return int((u.size + UploadPart - 1) / UploadPart) }

// span is where part n is in the file and how long it is.
func (u *partUpload) span(n int) (off, length int64) {
	off = int64(n) * UploadPart
	return off, min(UploadPart, u.size-off)
}

func errUploadGone() error {
	return Error(404, "this upload is no longer under way; add the file again")
}

func uploadOwner(r *http.Request) string {
	if JobOwner != nil {
		return JobOwner(r)
	}
	return ""
}

// uploadOf is the upload a request names, if the same person started it
// and may still write in its library.
func (m *Module) uploadOf(r *http.Request) (*partUpload, error) {
	m.up.mu.Lock()
	u := m.up.open[r.PathValue("id")]
	m.up.mu.Unlock()
	if u == nil || u.owner != uploadOwner(r) {
		return nil, errUploadGone()
	}
	lib, err := m.libByID(r, u.lib.ID)
	if err != nil {
		return nil, err
	}
	if u.space {
		if !lib.AsViewer() || lib.IndexRoot != u.lib.IndexRoot || SpaceFiles == nil {
			return nil, errUploadGone()
		}
		return u, nil
	}
	return u, RequireWritable(lib)
}

// startParts begins a large file: {path, size} in the body, the folder and
// library in the query, as for an upload.
func (m *Module) startParts(w http.ResponseWriter, r *http.Request) error {
	b, err := readBody(r)
	if err != nil {
		return err
	}
	if err := b.need(); err != nil {
		return err
	}
	p, _ := b.str("path")
	rel, err := SanitizeRelPath(p)
	if err != nil {
		return err
	}
	if rel == "" {
		return Error(400, "a file needs a name")
	}
	size, _ := b.m["size"].(float64)
	if size != float64(int64(size)) || size < 1 || size > maxUploadSize {
		return Error(400, "size required")
	}
	dir := "uploads"
	if v, ok := q(r, "dir"); ok {
		dir = v
	}
	u := &partUpload{owner: uploadOwner(r), size: int64(size), busy: map[int]*partWrite{}}
	u.left = u.parts()
	u.got = make([]bool, u.left)
	u.req = r.WithContext(context.WithoutCancel(r.Context()))
	raw := make([]byte, 16)
	rand.Read(raw)
	u.id = hex.EncodeToString(raw)

	lib, _, ok := m.spaceOf(r, body{}, "upload")
	if ok {
		u.lib, u.space = lib, true
		err = m.startSpaceParts(r, u, dir, rel)
	} else {
		if err := m.writableLib(r, body{}); err != nil {
			return m.handled(w, r, err)
		}
		if u.lib, err = m.lib(r, body{}); err == nil {
			err = m.startMainParts(u, dir, rel)
		}
	}
	if err != nil {
		return err
	}
	// Now that the place is there, the record of it.
	if err := m.up.add(m.Roots.IndexBase, u); err != nil {
		m.discard(u)
		return err
	}
	u.mu.Lock()
	u.expiry = time.AfterFunc(UploadIdle, func() { m.expire(u) })
	u.mu.Unlock()
	if u.space {
		m.dropLeft(u.req, u.owner)
	}
	if r.Context().Err() != nil {
		// No one is left to send its parts.
		return m.drop(u)
	}
	writeJSON(w, 200, O("id", u.id, "part", int64(UploadPart), "parts", u.parts(), "path", u.rel))
	return nil
}

func (m *Module) startSpaceParts(r *http.Request, u *partUpload, dir, rel string) error {
	if SpaceFiles == nil {
		return Error(403, "%s is read only here", u.lib.Label)
	}
	u.rel = rel
	if strings.Trim(dir, "/") != "" {
		c, err := spaceRel(dir)
		if err != nil {
			return err
		}
		u.rel = c + "/" + rel
	}
	need, err := m.up.need(u)
	if err != nil {
		return err
	}
	u.dir, err = SpaceFiles.StartParts(r, u.lib, u.rel, u.size, need)
	return err
}

func (m *Module) startMainParts(u *partUpload, dir, rel string) error {
	abs, cleaned, err := m.Files.uploadDir(dir)
	if err != nil {
		return err
	}
	u.rel = rel
	if cleaned != "" {
		u.rel = cleaned + "/" + rel
	}
	u.dest = filepath.Join(abs, rel)
	if st, err := os.Lstat(u.dest); err == nil && st.IsDir() {
		return Error(409, "%s is already there and is a folder", u.rel)
	}
	need, err := m.up.need(u)
	if err != nil {
		return err
	}
	at := filepath.Dir(u.dest)
	for !statOK(at) && filepath.Dir(at) != at {
		at = filepath.Dir(at)
	}
	if free, ok := FreeBytes(at); ok && free < need {
		return NoRoom(u.size, free-(need-u.size))
	}
	if err := os.MkdirAll(filepath.Dir(u.dest), 0o777); err != nil {
		return err
	}
	// The folder is closed to other accounts; the file in it takes the
	// modes it would have beside it, and keeps them when it is named.
	if u.dir, err = os.MkdirTemp(filepath.Dir(u.dest), partsPrefix+"*"); err != nil {
		return err
	}
	if u.file, err = os.OpenFile(filepath.Join(u.dir, "data"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666); err != nil {
		os.Remove(u.dir)
	}
	return err
}

// NoRoom is the refusal of a file that does not fit.
func NoRoom(need, free int64) error {
	return Error(507, "not enough free space: it needs %s and %s is free", shownBytes(need), shownBytes(max(free, 0)))
}

func shownBytes(n int64) string {
	f, unit := float64(n), ""
	for _, u := range []string{"KB", "MB", "GB", "TB", "PB"} {
		if f < 1024 {
			break
		}
		f, unit = f/1024, u
	}
	if unit == "" {
		return strconv.FormatInt(n, 10) + " B"
	}
	return strconv.FormatFloat(f, 'f', 1, 64) + " " + unit
}

// need is the free space u takes, beside what is still to come of the
// uploads under way in the same library; an error when one person has too
// many.
func (p *uploads) need(u *partUpload) (int64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	need, mine := u.size, 0
	for _, x := range p.open {
		x.mu.Lock()
		live, left := x.state == "" || x.state == "landing", x.left
		x.mu.Unlock()
		if !live {
			continue
		}
		if x.owner == u.owner {
			mine++
		}
		if x.lib.IndexRoot == u.lib.IndexRoot {
			need += int64(left) * UploadPart
		}
	}
	if mine >= maxOpenUploads {
		return 0, Error(429, "too many large files are being added at once")
	}
	return need, nil
}

// add keeps u, and a record of it on disk.
func (p *uploads) add(indexBase string, u *partUpload) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.record = filepath.Join(indexBase, "uploads")
	if err := os.MkdirAll(p.record, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(leftUpload{ID: u.id, Owner: u.owner, Lib: u.lib.ID, Space: u.space, Dir: u.dir})
	if err := os.WriteFile(filepath.Join(p.record, u.id+".json"), b, 0o600); err != nil {
		return err
	}
	if p.open == nil {
		p.open = map[string]*partUpload{}
	}
	p.open[u.id] = u
	return nil
}

// forget drops the record of an upload that left nothing to clean up.
func (p *uploads) forget(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.record != "" {
		os.Remove(filepath.Join(p.record, id+".json"))
	}
}

func (p *uploads) remove(id string) {
	p.mu.Lock()
	delete(p.open, id)
	p.mu.Unlock()
}

// active reports whether a large file is under way.
func (p *uploads) active() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, u := range p.open {
		u.mu.Lock()
		live := u.state == "" || u.state == "landing"
		u.mu.Unlock()
		if live {
			return true
		}
	}
	return false
}

// begin claims part n, stopping an earlier request still writing it: the
// page sends a part again when it has heard nothing of it.
func (u *partUpload) begin(r *http.Request, n int) (*partWrite, *http.Request, error) {
	for {
		u.mu.Lock()
		if u.state != "" {
			u.mu.Unlock()
			return nil, nil, errUploadGone()
		}
		old := u.busy[n]
		if old == nil {
			ctx, cancel := context.WithCancel(r.Context())
			pw := &partWrite{cancel: cancel, done: make(chan struct{})}
			u.busy[n] = pw
			u.req = r.WithContext(context.WithoutCancel(r.Context()))
			u.mu.Unlock()
			return pw, r.WithContext(ctx), nil
		}
		u.mu.Unlock()
		old.cancel()
		select {
		case <-old.done:
		case <-r.Context().Done():
			return nil, nil, r.Context().Err()
		case <-time.After(30 * time.Second):
			return nil, nil, Error(503, "an earlier copy of this part is still being written")
		}
	}
}

func (u *partUpload) end(pw *partWrite, n int, ok bool) {
	pw.cancel()
	u.mu.Lock()
	delete(u.busy, n)
	if ok && !u.got[n] {
		u.got[n] = true
		u.left--
	}
	if u.expiry != nil {
		u.expiry.Reset(UploadIdle)
	}
	u.mu.Unlock()
	close(pw.done)
}

// stop ends every part being written, and waits for them to be.
func (u *partUpload) stop() {
	u.mu.Lock()
	var all []*partWrite
	for _, pw := range u.busy {
		all = append(all, pw)
	}
	u.mu.Unlock()
	for _, pw := range all {
		pw.cancel()
	}
	for _, pw := range all {
		select {
		case <-pw.done:
		case <-time.After(30 * time.Second):
		}
	}
}

// arriving is a part's body: each read waits partStall and no longer.
type arriving struct {
	r   *http.Request
	ctl *http.ResponseController

	mu     sync.Mutex
	since  time.Time     // when the read under way began
	waited time.Duration // spent in reads
}

func (a *arriving) Read(p []byte) (int, error) {
	if err := a.r.Context().Err(); err != nil {
		return 0, err
	}
	a.ctl.SetReadDeadline(time.Now().Add(partStall))
	a.mu.Lock()
	a.since = time.Now()
	a.mu.Unlock()
	n, err := a.r.Body.Read(p)
	a.mu.Lock()
	a.waited += time.Since(a.since)
	a.since = time.Time{}
	a.mu.Unlock()
	if err == io.EOF {
		a.ctl.SetReadDeadline(time.Time{})
	}
	return n, err
}

// reading is the time spent waiting for the page's bytes.
func (a *arriving) reading() time.Duration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.since.IsZero() {
		return a.waited
	}
	return a.waited + time.Since(a.since)
}

// partAnswer answers for a part. What is in front of the Studio can take the
// whole part at once and then wait for an answer only so long (30 seconds on
// the platform), while the part is still on its way to a slow cluster. Once
// partWait has gone to writing it, the answer begins: 200 and a space now
// and then, with what came of it at the end. Its body says when that is an
// error, with the status it would have had.
type partAnswer struct {
	w    http.ResponseWriter
	ctl  *http.ResponseController
	mu   sync.Mutex
	open bool // has begun
	over bool
	stop chan struct{}
}

func answerPart(w http.ResponseWriter, src *arriving) *partAnswer {
	a := &partAnswer{w: w, ctl: src.ctl, stop: make(chan struct{})}
	began := time.Now()
	go func() {
		t := time.NewTicker(partWait / 2)
		defer t.Stop()
		for {
			select {
			case <-a.stop:
				return
			case <-t.C:
			}
			a.mu.Lock()
			if !a.over && (a.open || time.Since(began)-src.reading() >= partWait) {
				a.beat()
			}
			a.mu.Unlock()
		}
	}()
	return a
}

func (a *partAnswer) beat() {
	if !a.open {
		// The rest of the part is read after the answer begins.
		if a.ctl.EnableFullDuplex() != nil {
			a.over = true
			return
		}
		a.open = true
		a.w.Header().Set("Content-Type", "application/json; charset=utf-8")
		a.w.Header().Set("Cache-Control", "no-store, no-transform")
		a.w.Header().Set("X-Accel-Buffering", "no")
		a.w.WriteHeader(200)
	}
	a.w.Write([]byte(" "))
	a.ctl.Flush()
}

// end answers, with err or v. It returns err where the answer has not begun.
func (a *partAnswer) end(v any, err error) error {
	close(a.stop)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.over = true
	if !a.open {
		if err == nil {
			writeJSON(a.w, 200, v)
		}
		return err
	}
	if err != nil {
		status, text := 500, "internal error"
		var e *apierr.Error
		if errors.As(err, &e) {
			status, text = e.Status, e.Message
		}
		v = O("error", text, "status", status)
	}
	if b, merr := marshal(v); merr == nil {
		a.w.Write(b)
	}
	return nil
}

// putPart writes one part: the request's body is the part itself.
func (m *Module) putPart(w http.ResponseWriter, r *http.Request) error {
	src := &arriving{r: r, ctl: http.NewResponseController(w)}
	ans := answerPart(w, src)
	n, left, err := m.takePart(r, src)
	if err != nil && !cutShort(err) {
		// Read to its end all the same: an answer sent before that can reach
		// the page as a connection that dropped, and not say why.
		io.Copy(io.Discard, io.LimitReader(&arriving{r: r, ctl: src.ctl}, UploadPart))
	}
	return ans.end(O("part", n, "left", left), partError(err))
}

func (m *Module) takePart(r *http.Request, src *arriving) (n, left int, err error) {
	u, err := m.uploadOf(r)
	if err != nil {
		return 0, 0, err
	}
	n, err = strconv.Atoi(r.PathValue("n"))
	if err != nil || n < 0 || n >= u.parts() {
		return 0, 0, Error(400, "no such part")
	}
	off, length := u.span(n)
	if r.ContentLength >= 0 && r.ContentLength != length {
		return 0, 0, Error(400, "this part is %d bytes, not %d", length, r.ContentLength)
	}
	pw, rr, err := u.begin(r, n)
	if err != nil {
		return 0, 0, err
	}
	src.r = rr
	// Stopped, a read that waits ends at once.
	unhook := context.AfterFunc(rr.Context(), func() { src.ctl.SetReadDeadline(time.Now()) })
	if u.space {
		err = SpaceFiles.WritePart(rr, u.lib, u.dir, off, length, src)
	} else {
		var got int64
		got, err = io.Copy(io.NewOffsetWriter(u.file, off), io.LimitReader(src, length))
		if err == nil && got < length {
			err = io.ErrUnexpectedEOF
		}
	}
	if err == nil {
		// Nothing more than the part.
		var one [1]byte
		if k, _ := io.ReadFull(src, one[:]); k > 0 {
			err = Error(400, "this part is longer than %d bytes", length)
		}
	}
	unhook()
	u.end(pw, n, err == nil)
	u.mu.Lock()
	defer u.mu.Unlock()
	return n, u.left, err
}

// partError is how a part that failed is answered: sent again for one that
// did not arrive whole, not for one that did not fit.
func partError(err error) error {
	switch {
	case noSpaceLeft(err):
		return Error(507, "there is no space left there")
	case cutShort(err):
		return Error(503, "the part did not arrive whole")
	}
	return err
}

// cutShort is a part that stopped arriving.
func cutShort(err error) bool {
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.Canceled) || errors.Is(err, os.ErrDeadlineExceeded)
}

// finishParts gives the file its name once every part is in.
func (m *Module) finishParts(w http.ResponseWriter, r *http.Request) error {
	u, err := m.uploadOf(r)
	if err != nil {
		return err
	}
	u.mu.Lock()
	switch {
	case u.state == "landed":
		// Its answer was lost; asked for again.
		u.mu.Unlock()
		m.answerLanded(w, r, u)
		return nil
	case u.state == "dropped":
		u.mu.Unlock()
		return errUploadGone()
	case u.state != "":
		u.mu.Unlock()
		return Error(503, "the file is being put in place")
	case u.left > 0:
		left := u.left
		u.mu.Unlock()
		return Error(409, "%d parts have not arrived", left)
	}
	u.state = "landing"
	u.mu.Unlock()
	// Only a part that is in already can still be arriving, a second time.
	u.stop()
	if u.space {
		err = SpaceFiles.LandParts(r, u.lib, u.dir, u.rel, u.size)
	} else {
		rewrote := func() {}
		if u.lib.Primary {
			rewrote = m.Files.rewriting(u.rel, u.dest)
		}
		if err = u.land(); err == nil {
			rewrote()
		}
	}
	u.mu.Lock()
	if err != nil {
		u.state = ""
		u.mu.Unlock()
		return err
	}
	u.state = "landed"
	if u.expiry != nil {
		u.expiry.Reset(10 * time.Minute)
	}
	u.mu.Unlock()
	m.up.forget(u.id)
	m.answerLanded(w, r, u)
	return nil
}

func (m *Module) answerLanded(w http.ResponseWriter, r *http.Request, u *partUpload) {
	saved := []string{u.rel}
	if u.space {
		SpaceFiles.Changed(r, u.lib, saved)
		writeJSON(w, 200, O("saved", saved, "indexed", false, "deferred", true, "reindexing", true))
		return
	}
	writeJSON(w, 200, O("saved", saved, "indexed", false, "deferred", true))
}

// land gives a file on this host its name.
func (u *partUpload) land() error {
	data := filepath.Join(u.dir, "data")
	st, err := os.Lstat(data)
	if err != nil {
		return errUploadGone()
	}
	if st.Size() != u.size || !st.Mode().IsRegular() {
		return Error(409, "the parts that arrived hold %d of %d bytes", st.Size(), u.size)
	}
	// Closed first: Windows does not rename an open file.
	if err := u.file.Close(); err != nil {
		return partError(err)
	}
	if err := os.Rename(data, u.dest); err != nil {
		if f, err2 := os.OpenFile(data, os.O_WRONLY, 0); err2 == nil {
			u.file = f
		}
		return err
	}
	os.Remove(u.dir)
	return nil
}

// dropParts is a cancelled or failed upload: what it wrote is removed.
func (m *Module) dropParts(w http.ResponseWriter, r *http.Request) error {
	u, err := m.uploadOf(r)
	if err != nil {
		return err
	}
	u.mu.Lock()
	u.req = r.WithContext(context.WithoutCancel(r.Context()))
	u.mu.Unlock()
	if err := m.drop(u); err != nil {
		return err
	}
	writeJSON(w, 200, O("dropped", true))
	return nil
}

// drop ends an upload and removes its parts. One that could not be removed
// (a library out of reach) is kept for expire to try again.
func (m *Module) drop(u *partUpload) error {
	u.mu.Lock()
	switch u.state {
	case "landing":
		u.mu.Unlock()
		return Error(409, "the file is being put in place")
	case "landed":
		u.mu.Unlock()
		return nil
	}
	u.state = "dropped"
	u.mu.Unlock()
	u.stop()
	if err := m.discard(u); err != nil {
		return err
	}
	u.mu.Lock()
	if u.expiry != nil {
		u.expiry.Stop()
	}
	u.mu.Unlock()
	m.up.remove(u.id)
	m.up.forget(u.id)
	return nil
}

// discard removes what u wrote.
func (m *Module) discard(u *partUpload) error {
	if u.space {
		u.mu.Lock()
		r := u.req
		u.mu.Unlock()
		return SpaceFiles.DropParts(r, u.lib, u.dir)
	}
	u.file.Close()
	return os.RemoveAll(u.dir)
}

// expire ends an upload that has heard nothing for UploadIdle, and forgets
// one that landed.
func (m *Module) expire(u *partUpload) {
	u.mu.Lock()
	landed, busy := u.state == "landed", len(u.busy) > 0 || u.state == "landing"
	if busy {
		u.expiry.Reset(UploadIdle)
	}
	u.mu.Unlock()
	switch {
	case busy:
	case landed:
		m.up.remove(u.id)
	default:
		if err := m.drop(u); err != nil {
			m.A.Log.Warn("an upload that stopped was not cleaned up", "library", u.lib.ID, "err", err.Error())
			m.up.remove(u.id)
			m.up.keepLeft(leftUpload{ID: u.id, Owner: u.owner, Lib: u.lib.ID, Space: u.space, Dir: u.dir})
		}
	}
}

// stopUploads removes what the uploads on this host wrote, as the Studio
// stops; those in people's own libraries stay on record.
func (m *Module) stopUploads() {
	m.up.mu.Lock()
	var all []*partUpload
	for _, u := range m.up.open {
		if !u.space {
			all = append(all, u)
		}
	}
	m.up.mu.Unlock()
	for _, u := range all {
		m.drop(u)
	}
}

// clearUploads removes what uploads an earlier run did not end left here,
// and reads those left in people's own libraries, for dropLeft.
func (m *Module) clearUploads() {
	dir := filepath.Join(m.Roots.IndexBase, "uploads")
	names, _ := os.ReadDir(dir)
	for _, e := range names {
		f := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(f)
		var l leftUpload
		if err != nil || json.Unmarshal(raw, &l) != nil || l.ID+".json" != e.Name() {
			continue
		}
		m.up.mu.Lock()
		_, open := m.up.open[l.ID]
		m.up.record = dir
		m.up.mu.Unlock()
		if !open {
			m.up.keepLeft(l)
		}
		if open || l.Space {
			continue
		}
		rel, err := filepath.Rel(m.Roots.KBRoot, l.Dir)
		if err == nil && !strings.HasPrefix(rel, "..") && strings.HasPrefix(filepath.Base(l.Dir), partsPrefix) {
			os.RemoveAll(l.Dir)
		}
		os.Remove(f)
	}
}

// keepLeft keeps what a person's upload left in their own library for the
// next time they add a large file there.
func (p *uploads) keepLeft(l leftUpload) {
	if !l.Space {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lefts == nil {
		p.lefts = map[string][]leftUpload{}
	}
	p.lefts[l.Owner] = append(p.lefts[l.Owner], l)
}

// dropLeft removes, as the person adding a file now, what their uploads
// under an earlier run left in their own libraries.
func (m *Module) dropLeft(r *http.Request, owner string) {
	m.up.mu.Lock()
	list := m.up.lefts[owner]
	delete(m.up.lefts, owner)
	m.up.mu.Unlock()
	if len(list) == 0 {
		return
	}
	go func() {
		for _, l := range list {
			lib, err := m.libByID(r, l.Lib)
			if err == nil && lib.AsViewer() && SpaceFiles != nil {
				if err = SpaceFiles.DropParts(r, lib, l.Dir); err != nil {
					m.A.Log.Warn("what an earlier upload left was not removed", "library", l.Lib, "err", err.Error())
					m.up.keepLeft(l)
					continue
				}
			}
			m.up.forget(l.ID)
		}
	}()
}
