package tasks

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
)

// The board end to end: a task in this engine, and Node's /api/mcp (from
// server/dist, as a Go server that starts Node runs it: STUDIO_GO_PLATFORM=1,
// STUDIO_FRONT_URL, STUDIO_BRIDGE_SECRET) answering board_post and
// board_read for an agent's token by calling the Go board. Skipped
// without node or a built server/dist.

func nodeBin() string {
	if p := os.Getenv("STUDIO_TEST_NODE"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	if p := filepath.Join(home, ".nvm", "versions", "node", "v26.10.0", "bin", "node"); fileExists(p) {
		return p
	}
	p, _ := exec.LookPath("node")
	return p
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

// listenIn takes a free port in 5990-5994.
func listenIn(t *testing.T) (net.Listener, int) {
	for port := 5990; port <= 5994; port++ {
		if l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); err == nil {
			return l, port
		}
	}
	t.Skip("no free port in 5990-5994")
	return nil, 0
}

func TestBoardBridgeWithNode(t *testing.T) {
	node := nodeBin()
	dist, _ := filepath.Abs("../../../server/dist")
	if node == "" || !fileExists(filepath.Join(dist, "mcp.js")) {
		t.Skip("node or server/dist missing")
	}
	const secret = "e2e-board-secret"
	t.Setenv("STUDIO_BRIDGE_SECRET", secret)
	v := newEnv(t, "#!/bin/bash\nsleep 30\n", nil)
	e := v.engine()
	m, err := e.Create(CreateOptions{Objective: "bridge test", Model: "stub", Agents: []AgentSpec{{Objective: "a"}}, MaxAgents: 2.0})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Stop(m.ID)

	goL, goPort := listenIn(t)
	srv := &http.Server{Handler: boardMux(t, e)}
	go srv.Serve(goL)
	defer srv.Close()
	nodeL, nodePort := listenIn(t)
	nodeL.Close()

	script := filepath.Join(t.TempDir(), "mcp.mjs")
	os.WriteFile(script, []byte(`
const { default: Fastify } = await import(`+"`"+`${process.env.NODE_MODULES}/fastify/fastify.js`+"`"+`)
const { mcpRoutes } = await import(process.env.DIST + '/mcp.js')
const app = Fastify()
await app.register(mcpRoutes)
await app.listen({ port: Number(process.env.NODE_PORT), host: '127.0.0.1' })
console.log('listening')
`), 0o644)
	cmd := exec.Command(node, script)
	cmd.Dir = filepath.Dir(dist)
	cmd.Env = append(os.Environ(),
		"KB_ROOT="+v.kb, "INDEX_BASE="+t.TempDir(), "DIST="+dist, "NODE_MODULES="+filepath.Join(filepath.Dir(dist), "node_modules"),
		fmt.Sprintf("NODE_PORT=%d", nodePort), "STUDIO_GO_PLATFORM=1", fmt.Sprintf("STUDIO_FRONT_URL=http://127.0.0.1:%d", goPort),
		"STUDIO_BRIDGE_SECRET="+secret, "PW_CLI=/nonexistent/pw")
	out, _ := cmd.StdoutPipe()
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "listening") {
				ready <- true
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("node exited")
		}
	case <-time.After(30 * time.Second):
		t.Fatal("node did not start")
	}

	rpc := func(token, agent string, body any) any {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/mcp", nodePort), strings.NewReader(string(b)))
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("X-Task-Agent", agent)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		v, err := jsonv.Decode(raw)
		if err != nil {
			t.Fatalf("%s", raw)
		}
		return v
	}
	tools := func(token string) string {
		r := rpc(token, "agent-1", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list"})
		var names []string
		for _, x := range jsonv.Arr(jsonv.Path(r, "result", "tools")) {
			names = append(names, jsonv.String(jsonv.Path(x, "name")))
		}
		return strings.Join(names, ",")
	}
	if l := tools(m.Token); !strings.Contains(l, "board_claim") || !strings.Contains(l, "search_kb") {
		t.Fatal("a worker sees the board and the corpus:", l)
	}
	if l := tools("not-a-task"); strings.Contains(l, "board_claim") {
		t.Fatal("a wrong token gets no board:", l)
	}
	call := func(name string, args map[string]any) string {
		r := rpc(m.Token, "agent-1", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		return jsonv.String(jsonv.Path(jsonv.Arr(jsonv.Path(r, "result", "content"))[0], "text"))
	}
	if r := call("board_post", map[string]any{"topic": "finding", "body": "posted through Node to Go"}); r != `{"ok":true}` {
		t.Fatal(r)
	}
	read := call("board_read", map[string]any{"since": 0})
	if !strings.Contains(read, `"from":"agent-1","topic":"finding","body":"posted through Node to Go"`) {
		t.Fatal(read)
	}
	found := false
	for _, b := range e.Get(m.ID).Board {
		found = found || (b.Topic == "finding" && b.From == "agent-1")
	}
	if !found {
		t.Fatal("the post is on the Go board")
	}
}
