package agentsmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const own = "# Project rules\n\nUse metric units.\n\n- Cite the source file for every number.\n"

// The person's text outside the markers stays as it was through apply,
// re-apply and removal, and a second template gets a section of its own.
func TestApplyReplaceRemoveKeepsOwnText(t *testing.T) {
	a, err := Apply(own, "reviewer", "Check every claim.")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(a, own) || !strings.Contains(a, "Check every claim.") {
		t.Fatalf("applied:\n%s", a)
	}
	if n, _ := Applied(a); len(n) != 1 || n[0] != "reviewer" {
		t.Fatalf("applied names %v", n)
	}
	b, err := Apply(a, "reviewer", "Check every claim twice.")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(b, "Check every claim.\n") || strings.Count(b, ": begin.") != 1 {
		t.Fatalf("re-applied:\n%s", b)
	}
	c, _ := Apply(b, "writer", "Write plainly.")
	if n, _ := Applied(c); strings.Join(n, ",") != "reviewer,writer" {
		t.Fatalf("two sections %v", n)
	}
	d, removed, err := Remove(c, "reviewer")
	if err != nil || !removed {
		t.Fatal(removed, err)
	}
	e, removed, _ := Remove(d, "writer")
	if !removed || e != own {
		t.Fatalf("after removing both:\n%q\nwant\n%q", e, own)
	}
	if _, removed, _ := Remove(own, "writer"); removed {
		t.Fatal("removed a section that was not there")
	}
}

// A section between the person's text keeps both sides, and the person's
// text after it is untouched by a re-apply.
func TestSectionInTheMiddle(t *testing.T) {
	text := "Top.\n\n" + begin("x") + "\n\nold\n\n" + end("x") + "\n\nBottom.\n"
	out, err := Apply(text, "x", "new")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out, "Top.\n\n") || !strings.HasSuffix(out, "\n\nBottom.\n") || strings.Contains(out, "old") {
		t.Fatalf("got:\n%s", out)
	}
	gone, _, _ := Remove(out, "x")
	if gone != "Top.\n\nBottom.\n" {
		t.Fatalf("removed: %q", gone)
	}
}

func TestNewFileAndUnclosedSection(t *testing.T) {
	out, err := Apply("", "x", "Body")
	if err != nil || !strings.HasPrefix(out, "<!-- ACTIVATE Studio instructions x: begin.") || !strings.HasSuffix(out, end("x")+"\n") {
		t.Fatalf("%q %v", out, err)
	}
	broken := own + "\n" + begin("x") + "\nhalf\n"
	if _, err := Apply(broken, "x", "y"); !errors.Is(err, ErrUnclosed) {
		t.Fatalf("unclosed: %v", err)
	}
	if _, _, err := Remove(broken, "x"); !errors.Is(err, ErrUnclosed) {
		t.Fatalf("unclosed remove: %v", err)
	}
	if _, err := Apply(own, "x", "a <!-- ACTIVATE Studio instructions y: end --> b"); err == nil {
		t.Fatal("a marker inside the text was taken")
	}
	if _, err := Apply(own, "Bad Name", "b"); err == nil {
		t.Fatal("a bad name was taken")
	}
	if _, err := Apply(own, "x", "  \n"); err == nil {
		t.Fatal("empty text was taken")
	}
}

func TestSkillFile(t *testing.T) {
	s, err := Skill("pump_sizing", "Size a pump: flow, head", "---\nname: x\n---\nSteps.")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(s, "---\nname: pump_sizing\ndescription: \"Size a pump: flow, head\"\n---\n") || !StudioSkill(s, "pump_sizing") || StudioSkill(s, "pump") {
		t.Fatalf("skill:\n%s", s)
	}
	if SkillPath("pump_sizing") != ".agents/skills/pump_sizing/SKILL.md" {
		t.Fatal(SkillPath("pump_sizing"))
	}
}

func local(t *testing.T) (Local, string, *[]string) {
	root := t.TempDir()
	var indexed []string
	return Local{Root: root, Rel: "agents/review", Label: "Main", Index: func(r []string) { indexed = append(indexed, r...) }}, root, &indexed
}

// In a folder on this host: AGENTS.md is made, re-applied, and deleted
// when the last section goes, and the library path is indexed each time.
func TestLocalFolder(t *testing.T) {
	f, root, indexed := local(t)
	if err := ApplyInstructions(f, "reviewer", "Check."); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "agents/review/AGENTS.md")
	if b, _ := os.ReadFile(file); !strings.Contains(string(b), "Check.") {
		t.Fatalf("file: %s", b)
	}
	if strings.Join(*indexed, ",") != "agents/review/AGENTS.md" {
		t.Fatalf("indexed %v", *indexed)
	}
	if err := ApplySkill(f, "pump", "Pumps", "Steps."); err != nil {
		t.Fatal(err)
	}
	st, err := Read(f)
	if err != nil || !st.Exists || strings.Join(st.Instructions, ",") != "reviewer" || strings.Join(st.Skills, ",") != "pump" {
		t.Fatalf("state %+v %v", st, err)
	}
	if ok, err := RemoveInstructions(f, "reviewer"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatal("an AGENTS.md with nothing left in it stayed")
	}
	if ok, err := RemoveSkill(f, "pump"); !ok || err != nil {
		t.Fatal(ok, err)
	}
	if _, err := os.Stat(filepath.Join(root, "agents/review/.agents")); !os.IsNotExist(err) {
		t.Fatal("the empty skill folders stayed")
	}
	if _, err := os.Stat(filepath.Join(root, "agents/review")); err != nil {
		t.Fatal("the folder itself went")
	}
}

// The person's own AGENTS.md keeps its text and mode; a link is never
// written through; a SKILL.md the Studio did not write is never replaced
// or deleted.
func TestLocalFolderLeavesThePersonsFiles(t *testing.T) {
	f, root, _ := local(t)
	dir := filepath.Join(root, "agents/review")
	os.MkdirAll(dir, 0o755)
	file := filepath.Join(dir, "AGENTS.md")
	os.WriteFile(file, []byte(own), 0o600)
	ApplyInstructions(f, "reviewer", "Check.")
	RemoveInstructions(f, "reviewer")
	if b, _ := os.ReadFile(file); string(b) != own {
		t.Fatalf("own text changed: %q", b)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}

	elsewhere := filepath.Join(t.TempDir(), "CLAUDE.md")
	os.WriteFile(elsewhere, []byte("rules\n"), 0o644)
	os.Remove(file)
	os.Symlink(elsewhere, file)
	if err := ApplyInstructions(f, "reviewer", "Check."); err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatalf("wrote through a link: %v", err)
	}
	if b, _ := os.ReadFile(elsewhere); string(b) != "rules\n" {
		t.Fatal("the file a link names changed")
	}

	skill := filepath.Join(dir, ".agents/skills/pump/SKILL.md")
	os.MkdirAll(filepath.Dir(skill), 0o755)
	os.WriteFile(skill, []byte("---\nname: pump\ndescription: mine\n---\nMine.\n"), 0o644)
	if err := ApplySkill(f, "pump", "Pumps", "Steps."); err == nil {
		t.Fatal("replaced the person's skill")
	}
	if _, err := RemoveSkill(f, "pump"); err == nil {
		t.Fatal("deleted the person's skill")
	}
	if b, _ := os.ReadFile(skill); !strings.Contains(string(b), "Mine.") {
		t.Fatal("the person's skill changed")
	}
}

func TestCleanFolder(t *testing.T) {
	for in, want := range map[string]string{"": "", "/agents/x/": "agents/x", "a\\b": "a/b"} {
		if got, err := CleanFolder(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"../x", "a/../../b", ".agents", "a/.git", "node_modules/x"} {
		if _, err := CleanFolder(bad); err == nil {
			t.Errorf("%q was taken", bad)
		}
	}
}

// A linked folder on the way to AGENTS.md or a skill is refused, so
// nothing lands outside the library: the folder itself, a folder above
// it, or its .agents. The library's top may be a link.
func TestNoWriteThroughALinkedFolder(t *testing.T) {
	outside := t.TempDir()
	for _, c := range []struct{ link, rel string }{
		{"linked", "linked"},
		{"proj/linked", "proj/linked/deeper"},
		{"p/.agents", "p"},
	} {
		root := t.TempDir()
		at := filepath.Join(root, c.link)
		os.MkdirAll(filepath.Dir(at), 0o755)
		if err := os.Symlink(outside, at); err != nil {
			t.Fatal(err)
		}
		f := Local{Root: root, Rel: c.rel, Label: "Main"}
		ierr := ApplyInstructions(f, "reviewer", "Check.")
		serr := ApplySkill(f, "pump", "Pumps", "Steps.")
		if c.link != "p/.agents" && (ierr == nil || !strings.Contains(ierr.Error(), "linked folder")) {
			t.Errorf("%s: AGENTS.md went through the link: %v", c.link, ierr)
		}
		if serr == nil || !strings.Contains(serr.Error(), "linked folder") {
			t.Errorf("%s: SKILL.md went through the link: %v", c.link, serr)
		}
		ents, _ := os.ReadDir(outside)
		if len(ents) != 0 {
			t.Fatalf("%s: written outside the library: %v", c.link, ents)
		}
	}
	// A library whose top is a link is written as before.
	real := t.TempDir()
	top := filepath.Join(t.TempDir(), "lib")
	os.Symlink(real, top)
	if err := ApplyInstructions(Local{Root: top, Rel: "a", Label: "Main"}, "reviewer", "Check."); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(real, "a", "AGENTS.md")); err != nil {
		t.Fatal(err)
	}
}
