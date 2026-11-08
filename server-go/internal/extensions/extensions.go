// Package extensions is extensions.ts's file-based extensions, read from
// INDEX_BASE/extensions (deployment-local) and KB_ROOT/.agents (personas
// shared with the corpus):
//
//	tools/*.json      {name, description, command}: a chat tool that runs the command
//	skills/*.md       instructions the assistant loads on demand (use_skill)
//	agents/default.md the deployment's standing persona
//
// Directory contents are read again at most every five seconds. The chat
// track uses Tools, SkillBody and AgentPrompt; the core routes the rest.
package extensions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// PersonaLabel labels every corpus persona, so retrieval can tell a
// persona definition from ordinary corpus material.
const PersonaLabel = "agent-persona"

const ttl = 5 * time.Second

// Dirs is where the extensions live.
type Dirs struct {
	Ext     string // INDEX_BASE/extensions
	Starter string // PROJECT_ROOT/extensions-starter
	Corpus  string // KB_ROOT/.agents
}

// New is the extensions of a Studio.
func New(indexBase, projectRoot, kbRoot string) *Set {
	return &Set{Dirs: Dirs{
		Ext:     filepath.Join(indexBase, "extensions"),
		Starter: filepath.Join(projectRoot, "extensions-starter"),
		Corpus:  filepath.Join(kbRoot, ".agents"),
	}}
}

// Set is one Studio's extensions, with the listing caches.
type Set struct {
	Dirs
	mu     sync.Mutex
	tools  *cached[[]Tool]
	skills *cached[[]Doc]
}

type cached[T any] struct {
	at time.Time
	v  T
}

// Tool is a tools/*.json file.
type Tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Command     string `json:"command"`
	File        string `json:"file"`
}

// Doc is a skill or agent file's name, description and file name.
type Doc struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	File        string `json:"file"`
}

var (
	nonName  = regexp.MustCompile(`[^a-z0-9_]`)
	edges    = regexp.MustCompile(`^_+|_+$`)
	front    = regexp.MustCompile(`^---\n([\s\S]*?)\n---`)
	frontCut = regexp.MustCompile(`^---\n[\s\S]*?\n---\n?`)
)

func slice(s string, n int) string {
	u := utf16.Encode([]rune(s))
	if len(u) <= n {
		return s
	}
	return string(utf16.Decode(u[:n]))
}

func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		switch r {
		case ' ', '\t', '\n', '\v', '\f', '\r', 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000, 0xfeff:
			return true
		}
		return r >= 0x2000 && r <= 0x200a
	})
}

// NormalizeName is extensions.ts's normalizeName: a safe slug.
func NormalizeName(raw string) string {
	return slice(edges.ReplaceAllString(nonName.ReplaceAllString(strings.ToLower(raw), "_"), ""), 40)
}

// StripFrontmatter is text without its leading --- block.
func StripFrontmatter(text string) string { return frontCut.ReplaceAllString(text, "") }

func listFiles(dir, ext string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ext) && !strings.HasPrefix(e.Name(), ".") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func fmField(fm, k string) (string, bool) {
	m := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(k) + `:\s*(.+)$`).FindStringSubmatch(fm)
	if m == nil {
		return "", false
	}
	return jsTrim(m[1]), true
}

// DocMeta is docMeta: name and description from --- frontmatter, else the
// file name and the first line of prose.
func DocMeta(file string) Doc {
	fallback := NormalizeName(strings.TrimSuffix(filepath.Base(file), ".md"))
	name, description := fallback, ""
	if b, err := os.ReadFile(file); err == nil {
		text := string(b)
		fm := front.FindStringSubmatch(text)
		if fm != nil {
			n, ok := fmField(fm[1], "name")
			if !ok {
				n = fallback
			}
			if name = NormalizeName(n); name == "" {
				name = fallback
			}
			description, _ = fmField(fm[1], "description")
		}
		if description == "" {
			body := text
			if fm != nil {
				body = text[len(fm[0]):]
			}
			for _, l := range strings.Split(body, "\n") {
				if l = jsTrim(l); l != "" && !strings.HasPrefix(l, "#") {
					description = l
					break
				}
			}
		}
	}
	return Doc{Name: name, Description: slice(description, 200), File: filepath.Base(file)}
}

// Tools is extTools.
func (s *Set) Tools() []Tool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tools != nil && time.Since(s.tools.at) < ttl {
		return s.tools.v
	}
	tools := []Tool{}
	for _, file := range listFiles(filepath.Join(s.Ext, "tools"), ".json") {
		b, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		var raw map[string]any
		if json.Unmarshal(b, &raw) != nil {
			continue
		}
		str := func(k, d string) string {
			v, ok := raw[k]
			if !ok || v == nil {
				return d
			}
			switch x := v.(type) {
			case string:
				return x
			default:
				bb, _ := json.Marshal(x)
				return string(bb)
			}
		}
		name := NormalizeName(str("name", strings.TrimSuffix(filepath.Base(file), ".json")))
		command := slice(str("command", ""), 500)
		if name != "" && command != "" {
			tools = append(tools, Tool{Name: name, Description: slice(str("description", ""), 300), Command: command, File: filepath.Base(file)})
		}
	}
	s.tools = &cached[[]Tool]{time.Now(), tools}
	return tools
}

// Skills is extSkills.
func (s *Set) Skills() []Doc {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.skills != nil && time.Since(s.skills.at) < ttl {
		return s.skills.v
	}
	docs := []Doc{}
	for _, f := range listFiles(filepath.Join(s.Ext, "skills"), ".md") {
		docs = append(docs, DocMeta(f))
	}
	s.skills = &cached[[]Doc]{time.Now(), docs}
	return docs
}

// SkillBody is skillBody: a skill's text, or "" and false.
func (s *Set) SkillBody(name string) (string, bool) {
	want := NormalizeName(name)
	for _, k := range s.Skills() {
		if k.Name == want {
			b, err := os.ReadFile(filepath.Join(s.Ext, "skills", k.File))
			return string(b), err == nil
		}
	}
	return "", false
}

// Agents is extAgents: the deployment's agents/*.md.
func (s *Set) Agents() []Doc {
	docs := []Doc{}
	for _, f := range listFiles(filepath.Join(s.Ext, "agents"), ".md") {
		docs = append(docs, DocMeta(f))
	}
	return docs
}

// AgentFile is one persona's file.
type AgentFile struct {
	Name   string
	File   string
	Shared bool
}

// AgentFiles is agentFiles: the corpus personas, then the deployment's,
// which win on a name collision; in first-seen order.
func (s *Set) AgentFiles() []AgentFile {
	var order []string
	found := map[string]AgentFile{}
	for _, d := range []struct {
		dir    string
		shared bool
	}{{s.Corpus, true}, {filepath.Join(s.Ext, "agents"), false}} {
		for _, f := range listFiles(d.dir, ".md") {
			meta := DocMeta(f)
			if _, ok := found[meta.Name]; !ok {
				order = append(order, meta.Name)
			}
			found[meta.Name] = AgentFile{meta.Name, f, d.shared}
		}
	}
	out := make([]AgentFile, 0, len(order))
	for _, n := range order {
		out = append(out, found[n])
	}
	return out
}

// IconOf is iconOf: the icon field, an emoji or a corpus-relative image path.
func IconOf(file string) string {
	b, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	fm := front.FindStringSubmatch(string(b))
	if fm == nil {
		return ""
	}
	v, _ := fmField(fm[1], "icon")
	return v
}

// Persona is personas()'s entry.
type Persona struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	File        string `json:"file"`
	Shared      bool   `json:"shared"`
	Icon        string `json:"icon"`
}

// Personas is personas(): the persona library.
func (s *Set) Personas() []Persona {
	out := []Persona{}
	for _, a := range s.AgentFiles() {
		m := DocMeta(a.File)
		out = append(out, Persona{m.Name, m.Description, filepath.Base(a.File), a.Shared, IconOf(a.File)})
	}
	return out
}

// AgentPrompt is agentPrompt: the named persona's text, else the standing
// default.
func (s *Set) AgentPrompt(name string) string {
	if want := NormalizeName(name); want != "" {
		for _, a := range s.AgentFiles() {
			if a.Name == want {
				if b, err := os.ReadFile(a.File); err == nil {
					return jsTrim(StripFrontmatter(string(b)))
				}
			}
		}
	}
	b, err := os.ReadFile(filepath.Join(s.Ext, "agents", "default.md"))
	if err != nil {
		return ""
	}
	return jsTrim(string(b))
}

// WriteDoc is writeDoc: frontmatter rewritten from the fields, the name
// normalized to a slug joined to a fixed directory.
func WriteDoc(dir, rawName, body, description, icon string) (string, string, error) {
	name := NormalizeName(rawName)
	if name == "" {
		return "", "", errText("a name is required")
	}
	text := jsTrim(slice(body, 100_000))
	if text == "" {
		return "", "", errText("the document is empty")
	}
	desc := jsTrim(slice(strings.ReplaceAll(description, "\n", " "), 200))
	without := jsTrim(StripFrontmatter(text))
	iconLine := ""
	if icon != "" {
		iconLine = "icon: " + strings.ReplaceAll(slice(icon, 200), "\n", "") + "\n"
	}
	doc := "---\nname: " + name + "\ndescription: " + desc + "\n" + iconLine + "---\n\n" + without + "\n"
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", "", err
	}
	file := filepath.Join(dir, name+".md")
	if err := os.WriteFile(file, []byte(doc), 0o644); err != nil {
		return "", "", err
	}
	return name, file, nil
}

type errText string

func (e errText) Error() string { return string(e) }

// Seed is seedExtensions: the starter set copied where no extensions
// directory exists, and starter files added since copied when absent and
// not on the ledger (.seeded.json), so a deleted file stays deleted.
func (s *Set) Seed(log func(string)) {
	if _, err := os.Stat(s.Starter); err != nil {
		return
	}
	if _, err := os.Stat(s.Ext); err != nil {
		if copyTree(s.Starter, s.Ext) == nil && log != nil {
			log("seeded starter extensions into " + s.Ext)
		}
	}
	ledgerFile := filepath.Join(s.Ext, ".seeded.json")
	var ledger []string
	if b, err := os.ReadFile(ledgerFile); err == nil {
		json.Unmarshal(b, &ledger)
	}
	seen := map[string]bool{}
	for _, l := range ledger {
		seen[l] = true
	}
	var added []string
	var walk func(dir, rel string)
	walk = func(dir, rel string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			r := e.Name()
			if rel != "" {
				r = rel + "/" + e.Name()
			}
			if e.IsDir() {
				walk(filepath.Join(dir, e.Name()), r)
				continue
			}
			dest := filepath.Join(s.Ext, filepath.FromSlash(r))
			if _, err := os.Stat(dest); err == nil {
				seen[r] = true
				continue
			}
			if seen[r] {
				continue
			}
			if os.MkdirAll(filepath.Dir(dest), 0o755) == nil && copyFile(filepath.Join(dir, e.Name()), dest) == nil {
				seen[r] = true
				added = append(added, r)
			}
		}
	}
	walk(s.Starter, "")
	if len(added) > 0 || len(seen) != len(ledger) {
		all := make([]string, 0, len(seen))
		for k := range seen {
			all = append(all, k)
		}
		sort.Strings(all)
		b, _ := json.MarshalIndent(all, "", " ")
		os.WriteFile(ledgerFile, b, 0o644)
	}
	if len(added) > 0 && log != nil {
		log("seeded " + itoa(len(added)) + " new starter file(s): " + strings.Join(added, ", "))
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func copyFile(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	st, _ := os.Stat(from)
	return os.WriteFile(to, b, st.Mode().Perm())
}

func copyTree(from, to string) error {
	return filepath.Walk(from, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dest := filepath.Join(to, rel)
		if info.IsDir() {
			return os.MkdirAll(dest, 0o755)
		}
		return copyFile(p, dest)
	})
}
