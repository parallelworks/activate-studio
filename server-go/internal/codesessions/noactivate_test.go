package codesessions

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/parallelworks/activate-studio/server-go/internal/pwcode"
	"github.com/parallelworks/activate-studio/server-go/internal/pwcode/pwcodetest"
)

// On a Studio of one with no ACTIVATE in use, this computer's pw code is
// not used (it signs in with that ACTIVATE): the Code page asks to sign in,
// answers no 502, and asks nothing of a daemon, even one already running.
func TestTheCodePageWithoutActivateAsksToSignIn(t *testing.T) {
	for _, c := range []struct {
		name string
		env  map[string]string
		stop bool
	}{
		{"no ACTIVATE at all", map[string]string{}, false},
		{"Stop using the pw CLI", map[string]string{"PW_API_KEY": "owner-key"}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			var mu sync.Mutex
			var asked []string
			sock := pwcodetest.Socket(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				asked = append(asked, r.Method+" "+r.URL.Path)
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/v1/status" {
					w.Write([]byte(`{"protocolVersion":` + strconv.Itoa(pwcode.ProtocolVersion) + `,"machineId":"m1","hostname":"studio"}`))
					return
				}
				w.Write([]byte(`[{"id":"20261123-120000-aaaaaa","name":"mine"}]`))
			}))
			c.env["PW_CODE_SOCKET"] = sock
			c.env["PW_CREDENTIALS_DIR"] = t.TempDir()
			base := codeServer(t, c.env, true).URL
			if c.stop {
				os.MkdirAll(c.env["INDEX_BASE"], 0o755)
				os.WriteFile(filepath.Join(c.env["INDEX_BASE"], "access.json"), []byte(`{"stopped": true}`), 0o644)
			}
			if code, out := getCode(t, base+pagePrefix+"/where"); code != 200 || out["local"] != false || out["signIn"] != true {
				t.Fatalf("where: %d %v", code, out)
			}
			if code, out := getCode(t, base+pagePrefix+"/sessions"); code != 409 || !strings.Contains(fmt.Sprint(out), "Settings, Accounts") {
				t.Fatalf("sessions: %d %v", code, out)
			}
			if code, _ := getCode(t, base+pagePrefix+"/openai/models"); code != 200 {
				t.Fatalf("models: %d", code)
			}
			if code, out := getCode(t, base+"/api/code-sessions"); code < 400 || out["local"] == true {
				t.Fatalf("code-sessions: %d %v", code, out)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(asked) != 0 {
				t.Fatalf("this computer's pw code was asked: %v", asked)
			}
		})
	}
}
