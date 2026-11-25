// Package agentsmd writes agent instructions and skills into the folders
// pw code agents work in. pw code reads AGENTS.md from a session's
// workspace folder, and skills from .agents/skills/<name>/SKILL.md there
// (and from the person's own ~/.agents/skills). Instructions applied from
// the Studio go into AGENTS.md as a section between markers that name
// them: the section is made when absent, replaced when the same
// instructions are applied again, and removed when they are taken off.
// Text outside the markers is the person's and is never changed. A skill
// is a SKILL.md of its own, which carries a line saying the Studio wrote
// it; the Studio replaces or deletes only a file that carries that line.
package agentsmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// FileName is the instructions file pw code reads.
const FileName = "AGENTS.md"

// SkillsDir is the folder pw code finds a workspace's skills in.
const SkillsDir = ".agents/skills"

// MaxFile is the largest AGENTS.md or SKILL.md the Studio reads or writes.
const MaxFile = 1 << 20

// MaxBody is the most text one section or skill may carry.
const MaxBody = 100_000

var (
	// A section's markers. The name is a template's normalized name.
	beginRe = regexp.MustCompile(`<!-- ACTIVATE Studio instructions ([a-z0-9_]+): begin[^>]*-->`)
	endRe   = regexp.MustCompile(`<!-- ACTIVATE Studio instructions ([a-z0-9_]+): end -->`)
	// Any marker of either kind, refused inside a template's text.
	anyMark = regexp.MustCompile(`<!-- ACTIVATE Studio (instructions|skill) `)
	nameRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,63}$`)
)

// skillMark is the line in a SKILL.md the Studio wrote.
const skillMarkPrefix = "<!-- ACTIVATE Studio skill "

// ErrUnclosed is a section whose end marker is gone: the Studio cannot
// tell where it ends, so the person fixes the file.
var ErrUnclosed = errors.New("unclosed section")

// CheckName is a template name the markers and pw code take.
func CheckName(name string) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("%q is not a usable name: lowercase letters, digits and underscores, starting with a letter or digit", name)
	}
	return nil
}

// StudioSection is the section the Studio keeps in the folders of its own
// that its pw code chats work in: how to find files with its tools. It is
// not one of the instructions a person applies, so it is not listed.
const StudioSection = "studio_tools"

func begin(name string) string {
	if name == StudioSection {
		return "<!-- ACTIVATE Studio instructions " + name + ": begin. ACTIVATE Studio writes this section when a chat of its starts work here, and keeps it up to date. Write your own text outside it. -->"
	}
	return "<!-- ACTIVATE Studio instructions " + name + ": begin. ACTIVATE Studio replaces this section when these instructions are applied again and removes it when they are taken off. Write your own text outside it. -->"
}

func end(name string) string {
	return "<!-- ACTIVATE Studio instructions " + name + ": end -->"
}

// span is one section: from its begin marker to the end of its end marker.
type span struct {
	name       string
	start, end int
}

// sections are the sections in text, in order. A begin marker with no end
// marker of the same name after it is ErrUnclosed.
func sections(text string) ([]span, error) {
	var out []span
	at := 0
	for {
		b := beginRe.FindStringSubmatchIndex(text[at:])
		if b == nil {
			return out, nil
		}
		name := text[at+b[2] : at+b[3]]
		start := at + b[0]
		rest := at + b[1]
		e := strings.Index(text[rest:], end(name))
		if e < 0 {
			return out, fmt.Errorf("%w: the section %s in %s has no end marker; mend or remove it by hand", ErrUnclosed, name, FileName)
		}
		stop := rest + e + len(end(name))
		out = append(out, span{name, start, stop})
		at = stop
	}
}

// Applied is the names of the instructions applied in an AGENTS.md, in
// the order they appear.
func Applied(text string) ([]string, error) {
	ss, err := sections(text)
	names := make([]string, 0, len(ss))
	for _, s := range ss {
		names = append(names, s.name)
	}
	return names, err
}

// cleanBody is a template's text as a section or skill carries it.
func cleanBody(body string) (string, error) {
	body = strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n"))
	if body == "" {
		return "", errors.New("the text is empty")
	}
	if len(body) > MaxBody {
		return "", fmt.Errorf("the text is over %d characters", MaxBody)
	}
	if anyMark.MatchString(body) {
		return "", errors.New("the text holds an ACTIVATE Studio marker; take it out")
	}
	return body, nil
}

// Apply is text with the named instructions in it: their section replaced
// where it is, or added at the end.
func Apply(text, name, body string) (string, error) {
	if err := CheckName(name); err != nil {
		return "", err
	}
	body, err := cleanBody(body)
	if err != nil {
		return "", err
	}
	ss, err := sections(text)
	if err != nil {
		return "", err
	}
	section := begin(name) + "\n\n" + body + "\n\n" + end(name)
	for _, s := range ss {
		if s.name == name {
			return text[:s.start] + section + text[s.end:], nil
		}
	}
	head := strings.TrimRight(text, "\n")
	if strings.TrimSpace(head) == "" {
		return section + "\n", nil
	}
	return head + "\n\n" + section + "\n", nil
}

// Remove is text without the named instructions, and whether they were
// there. The blank lines that set the section apart go with it.
func Remove(text, name string) (string, bool, error) {
	ss, err := sections(text)
	if err != nil {
		return text, false, err
	}
	for _, s := range ss {
		if s.name != name {
			continue
		}
		before := strings.TrimRight(text[:s.start], "\n")
		after := strings.TrimLeft(text[s.end:], "\n")
		switch {
		case before == "":
			return after, true, nil
		case after == "":
			return before + "\n", true, nil
		}
		return before + "\n\n" + after, true, nil
	}
	return text, false, nil
}

// Empty: nothing is left in the file but white space, so it can go.
func Empty(text string) bool { return strings.TrimSpace(text) == "" }

// SkillPath is a skill's SKILL.md, relative to the workspace folder.
func SkillPath(name string) string { return path.Join(SkillsDir, name, "SKILL.md") }

// Skill is the SKILL.md the Studio writes for a skill: front matter pw
// code reads (name, description), the line that marks it as the Studio's,
// then the skill's text.
func Skill(name, description, body string) (string, error) {
	if err := CheckName(name); err != nil {
		return "", err
	}
	body, err := cleanBody(body)
	if err != nil {
		return "", err
	}
	desc := strings.Join(strings.Fields(description), " ")
	if desc == "" {
		desc = "The " + name + " skill from ACTIVATE Studio."
	}
	if r := []rune(desc); len(r) > 1000 {
		desc = string(r[:1000])
	}
	return "---\nname: " + name + "\ndescription: " + yamlString(desc) + "\n---\n\n" +
		skillMarkPrefix + name + ". ACTIVATE Studio replaces this file when the skill is applied again and deletes it when the skill is taken off. -->\n\n" +
		body + "\n", nil
}

// StudioSkill: a SKILL.md the Studio wrote, for the named skill.
func StudioSkill(text, name string) bool {
	return strings.Contains(text, skillMarkPrefix+name+". ")
}

// yamlString is s as a YAML double-quoted scalar (a JSON string is one).
func yamlString(s string) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	return strings.TrimSpace(b.String())
}

// Folder is a folder in a library, read and written with the rights the
// Library has there for the person asking. Paths are relative to it.
type Folder interface {
	// Read is a plain file whole; found is false when it is not there. A
	// link, or a linked folder on the way to it, is an error: the Studio
	// never writes through one.
	Read(rel string) (data []byte, found bool, err error)
	// Write puts data at rel whole, making the folders above it.
	Write(rel string, data []byte) error
	// Delete removes a file, then, where that is safe to tell, the
	// folders above it that it left empty (never the folder itself).
	Delete(rel string) error
	// Dirs is the names of the folders in rel; none when it is missing.
	Dirs(rel string) ([]string, error)
	// Changed tells the library's index that rels changed.
	Changed(rels []string)
	// Where names the folder for messages: the library and the path.
	Where() string
}

// State is what the Studio has put in a folder.
type State struct {
	// Exists: the folder has an AGENTS.md.
	Exists bool `json:"exists"`
	// Instructions are the sections in it, in order.
	Instructions []string `json:"instructions"`
	// Skills are the skills the Studio wrote under .agents/skills.
	Skills []string `json:"skills"`
	// Problem is why the file could not be read as the Studio wrote it.
	Problem string `json:"problem,omitempty"`
}

// Read is what the Studio has put in f.
func Read(f Folder) (State, error) {
	st := State{Instructions: []string{}, Skills: []string{}}
	b, found, err := f.Read(FileName)
	if err != nil {
		return st, err
	}
	if found {
		st.Exists = true
		names, err := Applied(string(b))
		st.Instructions = slices.DeleteFunc(names, func(n string) bool { return n == StudioSection })
		if err != nil {
			st.Problem = err.Error()
		}
	}
	dirs, err := f.Dirs(SkillsDir)
	if err != nil {
		return st, err
	}
	sort.Strings(dirs)
	for _, d := range dirs {
		if CheckName(d) != nil {
			continue
		}
		b, found, err := f.Read(SkillPath(d))
		if err == nil && found && StudioSkill(string(b), d) {
			st.Skills = append(st.Skills, d)
		}
	}
	return st, nil
}

// ApplyInstructions writes the named instructions into f's AGENTS.md.
func ApplyInstructions(f Folder, name, body string) error {
	b, _, err := f.Read(FileName)
	if err != nil {
		return err
	}
	out, err := Apply(string(b), name, body)
	if err != nil {
		return err
	}
	if out == string(b) {
		return nil
	}
	if err := f.Write(FileName, []byte(out)); err != nil {
		return err
	}
	f.Changed([]string{FileName})
	return nil
}

// ApplyStudio writes the Studio's own section (StudioSection) into f's
// AGENTS.md.
func ApplyStudio(f Folder, body string) error { return ApplyInstructions(f, StudioSection, body) }

// RemoveInstructions takes the named instructions out of f's AGENTS.md,
// deleting the file when nothing else is left in it. removed is false
// when they were not there.
func RemoveInstructions(f Folder, name string) (removed bool, err error) {
	b, found, err := f.Read(FileName)
	if err != nil || !found {
		return false, err
	}
	out, removed, err := Remove(string(b), name)
	if err != nil || !removed {
		return false, err
	}
	if Empty(out) {
		err = f.Delete(FileName)
	} else {
		err = f.Write(FileName, []byte(out))
	}
	if err != nil {
		return false, err
	}
	f.Changed([]string{FileName})
	return true, nil
}

// ApplySkill writes the skill's SKILL.md into f, over one the Studio wrote
// for it before, never over the person's own.
func ApplySkill(f Folder, name, description, body string) error {
	text, err := Skill(name, description, body)
	if err != nil {
		return err
	}
	rel := SkillPath(name)
	b, found, err := f.Read(rel)
	if err != nil {
		return err
	}
	if found && !StudioSkill(string(b), name) {
		return fmt.Errorf("%s in %s is not one the Studio wrote; rename the skill or move that file first", rel, f.Where())
	}
	if found && string(b) == text {
		return nil
	}
	if err := f.Write(rel, []byte(text)); err != nil {
		return err
	}
	f.Changed([]string{rel})
	return nil
}

// RemoveSkill deletes the skill's SKILL.md from f when the Studio wrote
// it, with the folders the Studio made for it once they are empty.
func RemoveSkill(f Folder, name string) (removed bool, err error) {
	if err := CheckName(name); err != nil {
		return false, err
	}
	rel := SkillPath(name)
	b, found, err := f.Read(rel)
	if err != nil || !found {
		return false, err
	}
	if !StudioSkill(string(b), name) {
		return false, fmt.Errorf("%s in %s is not one the Studio wrote, so it stays", rel, f.Where())
	}
	if err := f.Delete(rel); err != nil {
		return false, err
	}
	f.Changed([]string{rel})
	return true, nil
}
