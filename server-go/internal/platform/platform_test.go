package platform

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// fakeCLI writes a pw CLI script that answers with body and records its
// arguments in calls.
func fakeCLI(t *testing.T, body string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := filepath.Join(dir, "pw")
	os.WriteFile(script, []byte("#!/bin/sh\necho \"$@\" >> "+calls+"\n"+body+"\n"), 0o755)
	return script, calls
}

func newTest(t *testing.T, env map[string]string) *Platform {
	t.Helper()
	ix := t.TempDir()
	home := t.TempDir()
	return New(Options{IndexBase: ix, Home: home, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok }})
}

func writeStore(t *testing.T, dir string, v any) {
	t.Helper()
	os.MkdirAll(dir, 0o755)
	b, _ := json.Marshal(v)
	os.WriteFile(filepath.Join(dir, "credentials"), b, 0o600)
}

func jwt(exp time.Time) string {
	p := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return "e30." + p + ".sig"
}

func TestContextsChoiceAndGateway(t *testing.T) {
	creds := t.TempDir()
	writeStore(t, creds, map[string]any{
		"currentIdentity": "b",
		"identities": map[string]any{
			"b": map[string]any{"server": "b.example", "apikey": "key-b"},
			"a": map[string]any{"server": "a.example", "token": jwt(time.Now().Add(-time.Hour)), "organization": "org"},
			"z": map[string]any{"server": "a.example", "oauth": map[string]any{"expiresAt": "2099-01-01T00:00:00Z"}, "token": "tok"},
		},
	})
	p := newTest(t, map[string]string{"PW_CREDENTIALS_DIR": creds})
	if got := p.GatewayBase(); got != "https://b.example/api/openai/v1" {
		t.Fatal(got)
	}
	list := p.ListContexts()
	if len(list) != 3 {
		t.Fatal(len(list))
	}
	// Go's json.Marshal sorts map keys, so the file lists a, b, z.
	got := string(jsonv.MustMarshal(list[0]))
	if !strings.HasPrefix(got, `{"name":"a","server":"a.example","organization":"org","kind":"token","expiresAt":"`) || !strings.Contains(got, `"expired":true,"current":false,"renewError":null}`) {
		t.Fatal(got)
	}
	if p.CredentialContextName() != "b" || p.CLICredential() != "key-b" {
		t.Fatal("credential context")
	}
	if err := p.ChooseContext("nope"); err == nil || err.Error() != "the pw CLI has no context named nope" {
		t.Fatal(err)
	}
	if err := p.ChooseContext("z"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(p.IndexBase(), "access.json"))
	if string(b) != "{\n \"pwContext\": \"z\"\n}" {
		t.Fatalf("%q", b)
	}
	if p.GatewayBase() != "https://a.example/api/openai/v1" || p.CredentialContextName() != "z" || p.PWContext() != "z" {
		t.Fatal("moved gateway")
	}
	p.ChooseContext("")
	b, _ = os.ReadFile(filepath.Join(p.IndexBase(), "access.json"))
	if string(b) != "{}" {
		t.Fatal(string(b))
	}
	// Following the CLI does not move the gateway back (syncGateway only
	// moves it to the active context's host, which is b's).
	if p.ActiveContextName() != "b" || p.GatewayBase() != "https://b.example/api/openai/v1" {
		t.Fatal(p.ActiveContextName(), p.GatewayBase())
	}
	// A new process reads the choice back.
	p.ChooseContext("a")
	q := New(Options{IndexBase: p.IndexBase(), Home: t.TempDir(), LookupEnv: func(k string) (string, bool) {
		if k == "PW_CREDENTIALS_DIR" {
			return creds, true
		}
		return "", false
	}})
	if q.ChosenContext() != "a" {
		t.Fatal("reread")
	}
}

func TestGatewayFromEnvWins(t *testing.T) {
	p := newTest(t, map[string]string{"PW_GATEWAY_URL": "http://127.0.0.1:9/api/openai/v1/", "PW_API_KEY": "k"})
	if p.GatewayBase() != "http://127.0.0.1:9/api/openai/v1" || !p.GatewayFromEnv() || !p.PlatformGateway() || p.GatewayKey() != "k" {
		t.Fatal(p.GatewayBase())
	}
	p = newTest(t, map[string]string{"OPENAI_BASE_URL": "http://127.0.0.1:9/v1"})
	if p.PlatformGateway() || p.Origin() != "http://127.0.0.1:9" {
		t.Fatal("not a platform gateway")
	}
	p = newTest(t, nil)
	if p.GatewayBase() != "https://activate.parallel.works/api/openai/v1" {
		t.Fatal(p.GatewayBase())
	}
}

func TestRenewAndExec(t *testing.T) {
	creds := t.TempDir()
	writeStore(t, creds, map[string]any{"currentIdentity": "s", "identities": map[string]any{
		"s": map[string]any{"server": "s.example", "oauth": map[string]any{"expiresAt": time.Now().Add(time.Minute).UTC().Format(time.RFC3339)}, "token": "old"},
	}})
	cli, calls := fakeCLI(t, `echo 'notice'; echo '{"access_token":"fresh","expires_in":3600}'`)
	p := newTest(t, map[string]string{"PW_CREDENTIALS_DIR": creds, "PW_CLI": cli})
	if p.CLI() != cli || p.CLISource() != SourceEnv {
		t.Fatal(p.CLI(), p.CLISource())
	}
	p.EnsureFreshAccess(5 * time.Second)
	if got := p.CLICredential(); got != "fresh" {
		t.Fatal(got)
	}
	b, _ := os.ReadFile(calls)
	if strings.TrimSpace(string(b)) != "auth token --print -o json --context s" {
		t.Fatalf("%q", b)
	}
	// A failing CLI: Node's message and stderr.
	bad, _ := fakeCLI(t, `echo 'boom' >&2; exit 3`)
	_, err := p.ExecFile(t.Context(), bad, []string{"x", "y"}, ExecOptions{})
	var ee *ExecError
	if err == nil || ErrText(err) != "boom\n" || err.Error() != "Command failed: "+bad+" x y\nboom\n" {
		t.Fatalf("%v %q", err, ErrText(err))
	}
	_ = ee
	gone := filepath.Join(t.TempDir(), "pw")
	_, err = p.ExecFile(t.Context(), gone, nil, ExecOptions{})
	if !IsMissing(err) || err.Error() != "spawn "+gone+" ENOENT" {
		t.Fatal(err)
	}
	slow, _ := fakeCLI(t, `exec sleep 5`)
	start := time.Now()
	_, err = p.ExecFile(t.Context(), slow, nil, ExecOptions{Timeout: 100 * time.Millisecond})
	if err == nil || !err.(*ExecError).Killed || time.Since(start) > 3*time.Second {
		t.Fatal(err)
	}
}

func TestFindCLIOrder(t *testing.T) {
	ix := t.TempDir()
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755)
	common := filepath.Join(home, ".local", "bin", "pw")
	os.WriteFile(common, []byte("#!/bin/sh\n"), 0o755)
	p := New(Options{IndexBase: ix, Home: home, LookupEnv: func(k string) (string, bool) {
		if k == "PATH" {
			return "/nonexistent", true
		}
		return "", false
	}})
	if p.CLI() != common || p.CLISource() != SourceCommon {
		t.Fatal(p.CLI(), p.CLISource())
	}
	chosen, _ := fakeCLI(t, "echo v7.1.2")
	if _, _, err := p.ChooseCLI("relative/pw"); err == nil {
		t.Fatal("relative")
	}
	if path, src, _ := p.ChooseCLI(chosen); path != chosen || src != SourceChosen {
		t.Fatal(path, src)
	}
	if info := p.Info(true); !info.Available || info.Version != "v7.1.2" || info.Source != SourceChosen {
		t.Fatal(info)
	}
	p.ChooseCLI("")
	if p.CLISource() != SourceCommon {
		t.Fatal("forgot")
	}
}

func TestJSONHeads(t *testing.T) {
	v, err := FirstJSON("x [1, {\"a\": \"]\"}] trailing notice {")
	if err != nil || string(jsonv.MustMarshal(v)) != `[1,{"a":"]"}]` {
		t.Fatal(v, err)
	}
	if _, err := FirstJSON("nothing"); err == nil || err.Error() != "no JSON in CLI output" {
		t.Fatal(err)
	}
	if _, err := FirstJSON("{\"a\":1"); err == nil || err.Error() != "unterminated JSON in CLI output" {
		t.Fatal(err)
	}
	if _, err := JSONHead("plain"); err == nil || err.Error() != "no JSON in CLI output: plain" {
		t.Fatal(err)
	}
}

func TestBodyAsFastify(t *testing.T) {
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"b":1,"a":2}`))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	o, err := BodyObject(r)
	if err != nil || strings.Join(o.Keys(), ",") != "b,a" {
		t.Fatal(o, err)
	}
	r = httptest.NewRequest("POST", "/", nil)
	if o, err := BodyObject(r); err != nil || o.Len() != 0 {
		t.Fatal(err)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(""))
	r.Header.Set("Content-Type", "application/json")
	if _, err := Body(r); err == nil {
		t.Fatal("empty JSON body is refused")
	}
	r = httptest.NewRequest("GET", "/?a=1&a=2&b=", nil)
	if Q(r, "a") != "1,2" || Q(r, "b") != "" {
		t.Fatal("query")
	}
	if _, ok := Query(r, "c"); ok {
		t.Fatal("absent")
	}
}

func TestDeviceLogin(t *testing.T) {
	creds := t.TempDir()
	writeStore(t, creds, map[string]any{"identities": map[string]any{}})
	cli, _ := fakeCLI(t, `echo "Open https://p.example/device and enter"; echo "  ABCD-EFGH"; sleep 0.3; printf '{"currentIdentity":"p","identities":{"p":{"server":"p.example","token":"t"}}}' > `+creds+`/credentials`)
	p := newTest(t, map[string]string{"PW_CREDENTIALS_DIR": creds, "PW_CLI": cli, "PW_CONTEXT": "old"})
	if _, err := p.StartLogin("not a host!", 0, 0); err == nil {
		t.Fatal("bad host")
	}
	st, err := p.StartLogin("HTTPS://P.example/x", 5*time.Second, 0)
	if err != nil || st.State != "waiting" || st.URL != "https://p.example/device" || st.Code != "ABCD-EFGH" || st.Host != "p.example" {
		t.Fatal(st, err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for p.Login().State == "waiting" && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	got := p.Login()
	// Started with PW_CONTEXT, the Studio moves to the new sign-in.
	if got.State != "done" || got.Context != "p" || p.ChosenContext() != "p" {
		t.Fatalf("%+v", got)
	}
}
