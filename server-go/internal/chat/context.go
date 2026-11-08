package chat

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/kb"
)

// context.ts: the system prompt for one reply.

const (
	platformStart      = "<!--platform-->"
	platformEnd        = "<!--/platform-->"
	catalogPlaceholder = "WORKFLOW_CATALOG_PLACEHOLDER"
	personaPlaceholder = "PERSONA_CATALOG_PLACEHOLDER"
	missionMarker      = "the working corpus for this deployment."
	// conventionsFile is the knowledge base's conventions file, by the
	// name context.ts reads.
	conventionsFile     = "CLAUDE.md"
	platformOffSentence = "Platform tools are not listed for this reply: the workflows and apps the account offers, launching and following runs on HPC systems, sessions, clusters, and agents. Call more_tools to add them whenever a request touches any of these."
)

var onPlatform = regexp.MustCompile(`(?s)<!--on-platform-->(.*?)<!--/on-platform-->`)

// promptCache is context.ts's cached prompt and workflow catalog.
type promptCache struct {
	mu      sync.Mutex
	static  *string
	catalog *string
	// sig is what the static prompt was built from: the conventions file
	// and the main index's top database. Node drops its copy when its own
	// index work or a deletion changes them (invalidateContext); this copy
	// also notices a change made by the other process.
	sig string
}

func statSig(p string) string {
	st, err := os.Stat(p)
	if err != nil {
		return "-"
	}
	return strconv.FormatInt(st.ModTime().UnixNano(), 10) + ":" + strconv.FormatInt(st.Size(), 10)
}

func (m *Module) promptSig() string {
	return statSig(filepath.Join(m.A.Cfg.KBRoot, conventionsFile)) + "|" + statSig(filepath.Join(m.A.Cfg.GufiIndex, "db.db"))
}

// InvalidateContext is invalidateContext.
func (m *Module) InvalidateContext() {
	m.prompt.mu.Lock()
	m.prompt.static, m.prompt.catalog = nil, nil
	m.prompt.mu.Unlock()
}

// personaCatalog is one line per persona.
func (m *Module) personaCatalog() string {
	list := m.Ext.Personas()
	if len(list) == 0 {
		return "Persona catalog: none installed."
	}
	parts := make([]string, len(list))
	for i, p := range list {
		d := p.Description
		if d == "" {
			d = "no description"
		}
		parts[i] = p.Name + " (" + d + ")"
	}
	return "Persona catalog: " + strings.Join(parts, "; ") + "."
}

// pwCatalog is the workflow catalog the pw CLI lists.
func (m *Module) pwCatalog(ctx context.Context) string {
	out, _ := m.pwCLI(ctx, []string{"workflows", "ls", "-o", "json"}, 20*time.Second)
	var wfs []map[string]any
	if json.Unmarshal([]byte(out), &wfs) != nil {
		return jsTrim(out)
	}
	if out == "" {
		return ""
	}
	lines := make([]string, len(wfs))
	for i, w := range wfs {
		desc, _ := w["description"].(string)
		if desc == "" {
			desc, _ = w["displayName"].(string)
		}
		var tags []string
		if ts, ok := w["tags"].([]any); ok {
			for _, t := range ts {
				if s, ok := t.(string); ok && s != "" {
					tags = append(tags, s)
				}
			}
		}
		name, _ := w["name"].(string)
		line := "- " + name + ": " + desc
		if len(tags) > 0 {
			line += " [tags: " + strings.Join(tags, ", ") + "]"
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// systemPrompt is context.ts's systemPrompt.
func (m *Module) systemPrompt(ctx context.Context, platform, connected bool) string {
	base := strings.Replace(m.staticPrompt(ctx), personaPlaceholder, m.personaCatalog(), 1)
	a, b := strings.Index(base, platformStart), strings.Index(base, platformEnd)
	if a >= 0 && b > a {
		switch {
		case !connected:
			base = base[:a] + base[b+len(platformEnd):]
		case !platform:
			base = base[:a] + platformOffSentence + base[b+len(platformEnd):]
		default:
			base = base[:a] + base[a+len(platformStart):b] + base[b+len(platformEnd):]
		}
	}
	if strings.Contains(base, catalogPlaceholder) {
		m.prompt.mu.Lock()
		cat := m.prompt.catalog
		m.prompt.mu.Unlock()
		if cat == nil {
			c := m.pwCatalog(ctx)
			if c == "" {
				c = "(workflow list unavailable)"
			}
			m.prompt.mu.Lock()
			m.prompt.catalog = &c
			m.prompt.mu.Unlock()
			cat = &c
		}
		base = strings.Replace(base, catalogPlaceholder, *cat, 1)
	}
	if connected {
		base = onPlatform.ReplaceAllString(base, "$1")
	} else {
		base = onPlatform.ReplaceAllString(base, "")
	}
	mission := jsTrim(m.eff().KbMission)
	if mission == "" {
		return base
	}
	return strings.Replace(base, missionMarker, missionMarker+"\n\nDeployment mission and context, written by this deployment's operators; treat it as authoritative about what this Studio is for:\n"+mission, 1)
}

func (m *Module) staticPrompt(ctx context.Context) string {
	sig := m.promptSig()
	m.prompt.mu.Lock()
	if m.prompt.static != nil && m.prompt.sig == sig {
		s := *m.prompt.static
		m.prompt.mu.Unlock()
		return s
	}
	m.prompt.mu.Unlock()
	kbRoot := m.A.Cfg.KBRoot
	conventions := ""
	if b, err := os.ReadFile(filepath.Join(kbRoot, conventionsFile)); err == nil {
		conventions = string(b)
	}
	statsLine := "Corpus index not yet built; search_kb falls back to grep."
	if m.A.Cfg.GufiAvailable() && kb.Default != nil {
		if ok, files, dirs, total := kb.Default.CorpusStats(ctx); ok {
			statsLine = "Indexed corpus: " + strconv.FormatInt(files, 10) + " files across " + strconv.FormatInt(dirs, 10) + " directories, " + strconv.FormatInt(int64(math.Round(float64(total)/1e6)), 10) + " MB."
		}
	}
	lines := []string{
		"You are the assistant inside ACTIVATE Studio, a knowledge base interface. The knowledge base at " + kbRoot + " is the working corpus for this deployment.",
		"",
		"Never invent an expansion for an acronym or program name: if the mission statement and the knowledge base do not define one, say it is not defined there rather than guessing.",
		"",
		"Ground every answer about the knowledge base in its actual content: call search_kb first, read the files that matter, and cite the files you used. read_kb_file returns the text the index holds for a file (what search matched: the extracted text of a document, the OCR of an image, the contents of a plain text file), and works when the original file cannot be reached; it says plainly when the index has no text. read_original opens the original file itself, where the library keeps it and then, if the library names one, on another host the library may name for reading originals: use it when the index has no text for a file, when a file may have changed since it was indexed or is longer than the index keeps, or when the exact file matters. When read_original cannot reach a file it names the hosts tried; say so, and do not guess at the file's content. Write every citation as a markdown link so the reader can click through to the document in the library viewer: the link text is the file's path exactly as search_kb, list_kb_dir or read_kb_file gave it, and the target is #open=file: followed by the same path with each space written as %20. A file listed as reports/2024/site survey.pdf, for example, is cited as [reports/2024/site survey.pdf](#open=file:reports/2024/site%20survey.pdf). A file from a library other than the main one, which search results mark as [library <id>], also takes &lib= and that id at the end of the target, as in [specs/pump.pdf](#open=file:specs/pump.pdf&lib=archive). The viewer renders text, office documents, PDFs, images, and 3D models, so link whichever file grounds the claim. If retrieval returns nothing relevant, say so instead of guessing.",
		"",
		"The knowledge base carries a labeling system: files and directory subtrees can be tagged (for example research or theoretical versus validated or experimental). Search results show labels as [labels: ...], and search_kb accepts a tags filter. When retrieved material carries labels that bear on its reliability or provenance, state them in your answer, and never present material labeled as research or theoretical as established fact.",
		"",
		"You have memory of past conversations in this Studio: every chat session is exported as markdown into the chat-sessions/ directory, labeled chat-session. When the user asks about previous chats, earlier sessions, or something \"we discussed before\", search with search_kb using tags: [\"chat-session\"] (and list_kb_dir on chat-sessions/ for a chronological listing), then read the relevant transcripts. These transcripts are context about prior discussion, not knowledge base source material; distinguish the two when citing.",
		"",
		"Images, documents, <!--on-platform-->workflow DAGs, <!--/on-platform-->and 3D models can be shown inline in the chat, not just linked. When the user asks to see something, or seeing it is the point of the answer, embed it and follow with its citation link: images via ![name](/api/kb/raw?path=<url-encoded relative path>); a PDF or office page via /api/kb/pdf-page?path=<url-encoded path>&page=N; <!--on-platform-->an interactive workflow DAG via ![name DAG](/?embed=dag&workflow=<name>), or for a workflow you composed or drafted yourself, validate it with validate_workflow and fix every error first, then write the YAML into the knowledge base with write_kb_file and embed it via ![name DAG](/?embed=dag&path=<url-encoded relative path>), which renders it without registering anything on the platform; <!--/on-platform-->an interactive 3D model (.stl/.step) via ![name](/?embed=model&path=<url-encoded path>); a self-contained interactive HTML page you wrote into the corpus (a parametric viewer, a small visualization) via ![name](/?embed=html&path=<url-encoded path>), rendered in a sandbox that allows inline scripts and styles only, no network. <!--on-platform-->A running platform session (the interactive app a workflow opens, such as a design explorer or a notebook) is shown with the markdown list_sessions or watch_run returns for it, ![name](/?embed=session&user=<owner>&name=<session>): a card showing whether it is running, with buttons that open it in a window of its own or a new tab. <!--/on-platform-->For plain 3D files always use the built-in model embed instead of writing your own HTML viewer. Use this selectively, one or two embeds that serve the answer, not a gallery of every match.",
		"",
		statsLine,
		"",
		"Personas are markdown files describing a stance and working rules: the deployment ships a default set (campaign_runner, watcher, reviewer, steward, reporter), and users add, duplicate, or override them in the Agents tab. The catalog is listed below. A persona the user selected in the chat Persona control is in force for the whole conversation. When none is selected and a request clearly falls inside one persona's description, read that persona with read_original (shared ones live under .agents/) and follow its working rules for that request, saying in one line which persona you applied; do not adopt one merely because it came back in search results, and never switch persona mid-request.<!--on-platform--> When delegating, give each agent the persona from the catalog that fits its objective, or none.<!--/on-platform-->",
		"PERSONA_CATALOG_PLACEHOLDER",
		"For questions about the Studio application itself, how to use a view, what a control does, how labels or search or the index work, call studio_docs and answer from the returned guide instead of searching the knowledge base.",
		"",
		"<!--platform-->Platform workflows registered in this account. These are composable building blocks: recommend which fit a task, preview a DAG with get_workflow before anything runs, validate with run_workflow dry_run when acting on your own initiative, and treat a user request to run something as the authorization to run it, with no second confirmation. Monitor with workflow_runs and workflow_run_detail. pw_help discovers the wider platform command surface.",
		"WORKFLOW_CATALOG_PLACEHOLDER",
		"",
		"Running work on the HPC systems this account is connected to is a core job of this Studio, and the user is not expected to know scheduler syntax, queue names, or which account and QoS to charge. Treat a request like \"run an abliterated ollama model on the cluster\" as complete: identify the workflow from the catalog, call workflow_configs on it, and launch the configuration whose name matches the system the user named, passing it as run_workflow config with dry_run false. Those configurations already carry the partition, QoS, walltime and GPU directives that work at that site, so prefer one over scheduler settings you compose yourself, and never ask the user for a partition or an account that a configuration already supplies. When a request names a system that has no configuration, take the partitions, walltime ceilings and required fields from hpc_environments for that system, check list_clusters for whether it runs Slurm or PBS, and say which values you chose and why. Ask the user only for what genuinely cannot be resolved, such as a site account code that appears nowhere.",
		"When a workflow tool fails because no platform credential is available, stop and relay the remedy the error names, which is adding a platform credential under Settings, Model access. Do not answer from memory of earlier sessions, and never embed a workflow DAG in the reply unless get_workflow succeeded for that workflow this conversation: the embed makes the same failing request and renders an error box where the answer should be.",
		"Delegation has two execution modes. local is for quick parallel lookups, minutes each. campaign submits every agent as a platform workflow run on a connected system: use it when the work is long, the agents are many, or the user says campaign, and pick the system from hpc_status placement when the user does not name one. Campaign agents survive server restarts and appear with their run identifiers in the Agents tab.",
		"After a real launch, report the run identifier and call watch_run, then keep calling it to follow the run while the user waits. Say which system the job landed on and which job step is executing, and when the workflow serves a session or an endpoint, say that it is coming up and give its name, since that is what the user will select as a model or open. If a run ends in error or canceled, read workflow_run_detail and explain the failure in terms of the submission, for example a walltime over the partition ceiling or a GPU request the nodes cannot satisfy, rather than pasting the log.<!--/platform-->",
		"",
	}
	if conventions != "" {
		lines = append(lines,
			"The knowledge base ships its own working conventions, loaded below. Its rules on tone, terminology, and claims not to make are binding for every draft you produce here, because text produced in this interface can end up in deliverables.",
			"--- KNOWLEDGE BASE CONVENTIONS ("+conventionsFile+") ---",
			conventions)
	}
	kept := lines[:0]
	for _, l := range lines {
		if l != "" {
			kept = append(kept, l)
		}
	}
	s := strings.Join(kept, "\n")
	m.prompt.mu.Lock()
	m.prompt.static, m.prompt.sig = &s, sig
	m.prompt.mu.Unlock()
	return s
}
