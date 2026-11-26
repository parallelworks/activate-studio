//go:build !windows

package workflows

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// The deployment workflow's sign-in and sharing settings (docs/DEPLOY.md,
// Sign-in) reach the Studio on each way it is placed: the run script on
// the login node, the container there, and the scheduler's job. Each test
// runs the workflow's own steps with bash, with stand-ins for the server,
// the container runtime and the pw CLI that record what they were given.

func deployDoc(t *testing.T) any {
	t.Helper()
	b, err := os.ReadFile("../../../deploy/workflow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonv.ParseYAML(string(b))
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func formField(doc any, group, field string) *jsonv.Object {
	return jsonv.Obj(jsonv.Path(doc, "on", "execute", "inputs", group, "items", field))
}

var expr = regexp.MustCompile(`\$\{\{\s*([^}]*?)\s*\}\}`)

// step is a job's step with its ${{ }} filled in: the values given, else
// the form's defaults.
func step(t *testing.T, doc any, job, name string, values map[string]string) string {
	t.Helper()
	var run string
	for _, s := range jsonv.Arr(jsonv.Path(doc, "jobs", job, "steps")) {
		if jsonv.Str(jsonv.Path(s, "name")) == name {
			run = jsonv.Str(jsonv.Path(s, "run"))
		}
	}
	if run == "" {
		t.Fatalf("no step %q in %s", name, job)
	}
	return expr.ReplaceAllStringFunc(run, func(m string) string {
		k := expr.FindStringSubmatch(m)[1]
		if v, ok := values[k]; ok {
			return v
		}
		p := strings.Split(strings.TrimPrefix(k, "inputs."), ".")
		if len(p) != 2 || !strings.HasPrefix(k, "inputs.") {
			t.Fatalf("no value for %s", k)
		}
		f := formField(doc, p[0], p[1])
		if f == nil {
			t.Fatalf("the form has no %s", k)
		}
		d, _ := f.Get("default")
		return jsonv.String(d)
	})
}

func bash(t *testing.T, dir, script string, env ...string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	return string(out)
}

func stub(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/bash\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func waitFile(t *testing.T, path string) string {
	t.Helper()
	for range 100 {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			return string(b)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s never written", path)
	return ""
}

const (
	exAdmins    = "ada, group:studio-admins"
	exEndpoints = "s3.storage.example.com:9443,bucket.vpce-0example.s3.us-east-1.vpce.amazonaws.com"
	exSelf      = "https://tide.sessions.example.com/"
	exSecret    = "example-client-secret"
)

func accessValues(work string) map[string]string {
	return map[string]string{
		"inputs.app_settings.workdir":                work,
		"inputs.app_settings.rundir":                 filepath.Join(work, "run"),
		"inputs.app_settings.self_url":               exSelf,
		"inputs.kb_settings.kb_root":                 filepath.Join(work, "kb"),
		"inputs.model_settings.posture":              "multi-strict",
		"inputs.model_settings.platform_host":        "platform.example.com",
		"inputs.access_settings.admins":              exAdmins,
		"inputs.access_settings.bucket_endpoints":    exEndpoints,
		"inputs.access_settings.shared_chat_history": "true",
		"inputs.access_settings.custom_tools":        "false",
		"inputs.access_settings.oidc_client_id":      "tide-studio",
		"inputs.access_settings.oidc_client_secret":  exSecret,
	}
}

// wantAccess is what the Studio is given for accessValues.
var wantAccess = map[string]string{
	"STUDIO_ADMINS":                 exAdmins,
	"STUDIO_BUCKET_ENDPOINTS":       exEndpoints,
	"CHAT_SHARED_HISTORY":           "1",
	"STUDIO_CUSTOM_TOOLS":           "0",
	"STUDIO_SELF_URL":               exSelf,
	"OIDC_ISSUER":                   "https://platform.example.com/api/oidc",
	"OIDC_CLIENT_ID":                "tide-studio",
	"OIDC_CLIENT_SECRET":            exSecret,
	"OIDC_REDIRECT_URI":             "https://tide.sessions.example.com/api/auth/callback",
	"OIDC_POST_LOGOUT_REDIRECT_URI": "https://tide.sessions.example.com/",
	"OIDC_SCOPES":                   "openid profile email groups offline_access",
	"AUTH_REQUIRED":                 "1",
	"REQUIRE_PERSONAL_KEY":          "1",
}

func needBash(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
}

func TestDeployFormSignInFields(t *testing.T) {
	doc := deployDoc(t)
	for _, f := range []string{"admins", "bucket_endpoints", "shared_chat_history", "custom_tools", "oidc_client_id", "oidc_client_secret"} {
		o := formField(doc, "access_settings", f)
		if o == nil {
			t.Fatalf("the form has no access_settings.%s", f)
		}
		if jsonv.Str(jsonv.Path(o, "tooltip")) == "" || jsonv.Str(jsonv.Path(o, "label")) == "" {
			t.Fatalf("%s has no label or help line", f)
		}
		if d, _ := o.Get("default"); jsonv.Str(jsonv.Path(o, "type")) == "boolean" {
			// Custom tools are on unless turned off, as STUDIO_CUSTOM_TOOLS is.
			if want := f == "custom_tools"; d != want {
				t.Fatalf("%s defaults to %v", f, d)
			}
		} else if d != "" || jsonv.Path(o, "optional") != true {
			t.Fatalf("%s is not optional and empty by default", f)
		}
	}
}

// listen gives the run script a port that answers, so it does not wait.
func listen(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func runAppScript(t *testing.T, source string, values map[string]string) (work, home string) {
	t.Helper()
	work = t.TempDir()
	home = t.TempDir()
	v := accessValues(work)
	for k, x := range values {
		v[k] = x
	}
	v["inputs.app_settings.source"] = source
	v["needs.get_ports.outputs.APP_PORT"] = listen(t)
	bash(t, work, step(t, deployDoc(t), "run_app", "Generate run_app.sh", v), "HOME="+home)
	return work, home
}

func TestDeployFormReachesTheLoginNodeServer(t *testing.T) {
	needBash(t)
	work, home := runAppScript(t, "github", nil)
	stub(t, filepath.Join(work, "payload/app/bin/studio-server"), `env -0 > "$PWD/server.env"; exec sleep 30`+"\n")
	bash(t, work, "bash run/run_app.sh", "HOME="+home)
	pid, _ := strconv.Atoi(strings.TrimSpace(waitFile(t, filepath.Join(work, "serve.pid"))))
	t.Cleanup(func() {
		if pid > 0 {
			syscall.Kill(-pid, syscall.SIGTERM)
		}
	})
	env := map[string]string{}
	for _, kv := range strings.Split(waitFile(t, filepath.Join(work, "server.env")), "\x00") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for k, want := range wantAccess {
		if k == "STUDIO_SELF_URL" {
			want = exSelf // given as typed on this path
		}
		if env[k] != want {
			t.Errorf("%s = %q, want %q", k, env[k], want)
		}
	}
}

// runtimeArgs is the stand-in container runtime: it records its --env
// arguments, one a line, and exits.
const runtimeArgs = `[ "$1" = run ] || exit 0
out="${RUNTIME_ARGS:-$PWD/runtime.args}"
while [ $# -gt 0 ]; do [ "$1" = --env ] && printf '%s\n' "$2" >> "$out"; shift; done
`

func envArgs(t *testing.T, path string) map[string]string {
	env := map[string]string{}
	for _, kv := range strings.Split(strings.TrimSpace(waitFile(t, path)), "\n") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	return env
}

func TestDeployFormReachesTheLoginNodeContainer(t *testing.T) {
	needBash(t)
	work, home := runAppScript(t, "container", nil)
	stub(t, filepath.Join(home, ".local/bin/apptainer"), runtimeArgs)
	os.WriteFile(filepath.Join(work, ".image"), []byte("studio.sif"), 0o644)
	bash(t, work, "bash run/run_app.sh", "HOME="+home)
	env := envArgs(t, filepath.Join(work, "runtime.args"))
	for k, want := range wantAccess {
		if env[k] != want {
			t.Errorf("--env %s = %q, want %q", k, env[k], want)
		}
	}
}

func TestDeployFormReachesTheSchedulerJob(t *testing.T) {
	needBash(t)
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	work, home := t.TempDir(), t.TempDir()
	bin := filepath.Join(home, "bin")
	stub(t, filepath.Join(bin, "apptainer"), runtimeArgs)
	// pw endpoints run starts the command after --, with the port it gives.
	stub(t, filepath.Join(bin, "pw"), `[ "$1 $2" = "endpoints run" ] || exit 0
while [ $# -gt 0 ] && [ "$1" != -- ]; do shift; done; shift
PORT=8123 exec "$@"
`)
	os.MkdirAll(filepath.Join(work, "compute-run"), 0o755)
	path := "PATH=" + bin + ":" + os.Getenv("PATH")
	doc := deployDoc(t)
	log := bash(t, work, step(t, doc, "submit_compute", "Write the job script", accessValues(work)), "HOME="+home, path)
	if strings.Contains(log, exSecret) {
		t.Fatal("the job's log shows the client secret")
	}
	args := filepath.Join(work, "runtime.args")
	bash(t, work, "bash compute-run/job.sh", "HOME="+home, path, "RUNTIME_ARGS="+args)
	env := envArgs(t, args)
	for k, want := range wantAccess {
		if k == "STUDIO_SELF_URL" {
			want = strings.TrimSuffix(exSelf, "/")
		}
		if env[k] != want {
			t.Errorf("--env %s = %q, want %q", k, env[k], want)
		}
	}

	// Empty fields set nothing.
	os.Remove(args)
	v := accessValues(work)
	for _, k := range []string{"admins", "bucket_endpoints", "oidc_client_id", "oidc_client_secret"} {
		v["inputs.access_settings."+k] = ""
	}
	v["inputs.access_settings.shared_chat_history"] = "false"
	v["inputs.access_settings.custom_tools"] = "true"
	bash(t, work, step(t, doc, "submit_compute", "Write the job script", v), "HOME="+home, path)
	bash(t, work, "bash compute-run/job.sh", "HOME="+home, path, "RUNTIME_ARGS="+args)
	env = envArgs(t, args)
	for _, k := range []string{"STUDIO_ADMINS", "STUDIO_BUCKET_ENDPOINTS", "CHAT_SHARED_HISTORY", "STUDIO_CUSTOM_TOOLS", "OIDC_ISSUER", "OIDC_CLIENT_ID", "OIDC_CLIENT_SECRET", "OIDC_REDIRECT_URI", "OIDC_SCOPES"} {
		if _, ok := env[k]; ok {
			t.Errorf("--env %s given for an empty field", k)
		}
	}
}
