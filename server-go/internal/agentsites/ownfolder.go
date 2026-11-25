package agentsites

import (
	"context"
	"encoding/base64"
	"path"
	"strconv"
	"strings"
)

// maxOwnFile is the largest file an OwnFolder reads or writes: what one
// command line carries.
const maxOwnFile = 48 << 10

// OwnFolder is the Studio's own folder on a machine (~/studio-chat), read and
// written as its owner over pw ssh: an agentsmd.Folder for its AGENTS.md. A
// write is refused when the file changed after it was read.
type OwnFolder struct {
	s              *Sites
	ctx            context.Context
	site, key, dir string
	read           map[string]string // rel to its cksum, "" for no file
}

// OwnFolder is dir on site, as key's owner there.
func (s *Sites) OwnFolder(ctx context.Context, site, key, dir string) *OwnFolder {
	return &OwnFolder{s: s, ctx: ctx, site: site, key: key, dir: dir, read: map[string]string{}}
}

func (f *OwnFolder) run(script string) (string, error) {
	args := []string{"ssh"}
	if h := f.s.platformHost(); h != "" {
		args = append(args, "--platform-host", h)
	}
	out, err := f.s.pwWithKey(f.ctx, append(args, f.site, "sh -c "+shq("cd -- "+shq(f.dir)+" && "+script)), f.key)
	return trimJS(out), err
}

// Where names the folder for messages.
func (f *OwnFolder) Where() string { return f.dir + " on " + f.site }

// Changed: no library holds the folder.
func (f *OwnFolder) Changed([]string) {}

// Dirs lists nothing: only its own files are used.
func (f *OwnFolder) Dirs(string) ([]string, error) { return nil, nil }

// Delete is not offered.
func (f *OwnFolder) Delete(string) error { return kb(409, "the Studio deletes nothing here") }

// Read is the file rel, in the folder itself; a link is refused.
func (f *OwnFolder) Read(rel string) ([]byte, bool, error) {
	if path.Base(rel) != rel {
		return nil, false, kb(400, "a file in the folder itself")
	}
	q := shq("./" + rel)
	out, err := f.run("if [ -L " + q + " ]; then echo studio-link; elif [ ! -e " + q + " ]; then echo studio-none; elif [ ! -f " + q + " ] || [ \"$(wc -c < " + q + ")\" -gt " + strconv.Itoa(maxOwnFile) + " ]; then echo studio-skip; else echo studio-file; cksum < " + q + "; base64 < " + q + "; fi")
	if err != nil {
		return nil, false, err
	}
	// A login message may come first.
	i := strings.LastIndex(out, "studio-")
	if i < 0 {
		return nil, false, kb(502, "could not read "+rel+" in "+f.Where())
	}
	lines := strings.Split(out[i:], "\n")
	switch strings.TrimSpace(lines[0]) {
	case "studio-none":
		f.read[rel] = ""
		return nil, false, nil
	case "studio-file":
		if len(lines) < 2 {
			break
		}
		b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(strings.Join(lines[2:], "")), ""))
		if err != nil {
			break
		}
		f.read[rel] = strings.TrimSpace(lines[1])
		return b, true, nil
	case "studio-link":
		return nil, false, kb(409, rel+" in "+f.Where()+" is a link; the Studio does not write through one")
	case "studio-skip":
		return nil, false, kb(409, rel+" in "+f.Where()+" is not a file the Studio changes")
	}
	return nil, false, kb(502, "could not read "+rel+" in "+f.Where())
}

// Write puts data at rel whole, in one rename, when the file is as it was
// read. A file that was there keeps its mode.
func (f *OwnFolder) Write(rel string, data []byte) error {
	was, ok := f.read[rel]
	if !ok {
		return kb(409, "read "+rel+" first")
	}
	if len(data) > maxOwnFile {
		return kb(409, rel+" in "+f.Where()+" would be over 48 KB")
	}
	q := shq("./" + rel)
	same := "[ ! -e " + q + " ] && [ ! -L " + q + " ]"
	keep := ""
	if was != "" {
		same = "[ ! -L " + q + " ] && [ \"$(cksum < " + q + ")\" = " + shq(was) + " ]"
		keep = "cp -p " + q + " \"$t\" && "
	} else {
		keep = "chmod 644 \"$t\" && "
	}
	out, err := f.run("if " + same + "; then t=$(mktemp ./.studio-agents-md-XXXXXX) && " + keep + "printf %s " + shq(base64.StdEncoding.EncodeToString(data)) + " | base64 -d > \"$t\" && mv -f -- \"$t\" " + q + " && echo studio-wrote || { rm -f -- \"$t\"; echo studio-failed; }; else echo studio-changed; fi")
	switch {
	case err != nil:
		return err
	case strings.HasSuffix(out, "studio-wrote"):
		return nil
	case strings.HasSuffix(out, "studio-changed"):
		return kb(409, rel+" in "+f.Where()+" changed as it was read; it is left as it is")
	}
	return kb(502, "could not write "+rel+" in "+f.Where())
}
