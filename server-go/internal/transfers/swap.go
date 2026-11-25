package transfers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strconv"
	"strings"
)

var shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)

// Swapped is how a Swap ended: done, or not because the file has changed
// (Version is its checksum now) or is not there any more.
type Swapped struct {
	Done    bool
	Missing bool
	Version string
}

// Text is a plain file's bytes, at most max of them, to be changed and
// put back with Swap: where Swap would refuse the file, so does this.
func (r *RemoteFS) Text(ctx context.Context, rel string, max int) ([]byte, error) {
	if r.SFTPOnly(ctx) {
		return nil, r.named(409, "the account takes SFTP only, so a file cannot be edited there")
	}
	res, err := r.run(ctx, nil, "text", rel, strconv.Itoa(max))
	if err == nil {
		return res.Raw, nil
	}
	if res.Code == 15 {
		return nil, r.named(403, "your account there may not change this file")
	}
	return nil, err
}

// Swap puts new bytes in the place of a file's, in one command on the
// host that first checks the file still has the sha256 given. The file
// keeps its mode, group, ACL and attributes; one owned by another account
// is written where it is, so it stays theirs. A link is never followed or
// replaced, and what is not Done changed nothing.
func (r *RemoteFS) Swap(ctx context.Context, rel string, data []byte, sha string) (Swapped, error) {
	if r.SFTPOnly(ctx) {
		return Swapped{}, r.named(409, "the account takes SFTP only, so a file cannot be edited there")
	}
	var tag [6]byte
	rand.Read(tag[:])
	sum := sha256.Sum256(data)
	in := &smallFile{Reader: bytes.NewReader(data), data: data}
	res, err := r.run(ctx, in, "swap", rel, sha, hex.EncodeToString(sum[:]), ".studio-upload-"+hex.EncodeToString(tag[:]))
	if err == nil {
		return Swapped{Done: true}, nil
	}
	switch res.Code {
	case 13:
		// A change prints the checksum found; the guard's refusal, of the
		// same code, prints none.
		if v := strings.TrimSpace(lastLine(res.Stdout)); shaRe.MatchString(v) {
			return Swapped{Version: v}, nil
		}
	case 14:
		return Swapped{Missing: true}, nil
	case 15:
		return Swapped{}, r.named(403, "your account there may not change this file")
	case 9:
		return Swapped{}, r.named(409, strings.TrimPrefix(messageOf(err), r.Name+": "))
	}
	return Swapped{}, err
}
