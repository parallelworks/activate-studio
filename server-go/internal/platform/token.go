package platform

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/netx"
)

// The pw-token dependency: a sign-in's access token, renewed by asking the
// CLI (`pw auth token --print -o json --context <name>`), which renews
// when the token is about to expire. The Studio never renews it itself.

const renewAhead = 2 * time.Minute

type renewed struct {
	token string
	until time.Time
}

type renewErr struct {
	at      time.Time
	message string
	offline bool
}

type tokenState struct {
	mu       sync.Mutex
	renewed  map[string]renewed
	renewing map[string]*renewCall
	errs     map[string]renewErr
}

type renewCall struct {
	done  chan struct{}
	token string
}

func newTokenState() *tokenState {
	return &tokenState{renewed: map[string]renewed{}, renewing: map[string]*renewCall{}, errs: map[string]renewErr{}}
}

func (p *Platform) renewErrorMessage(name string) string {
	p.tokens.mu.Lock()
	defer p.tokens.mu.Unlock()
	return p.tokens.errs[name].message
}

func fileExpiry(i Identity) (time.Time, bool) {
	if i.OAuth == nil {
		return time.Time{}, false
	}
	return ParseISO(i.OAuth.ExpiresAt)
}

// needsRenewal: whether a context's credential has to come from the CLI now.
func (p *Platform) needsRenewal(name string) bool {
	i, ok := p.ReadStore().identity(name)
	if !ok {
		return false
	}
	p.tokens.mu.Lock()
	r, have := p.tokens.renewed[name]
	p.tokens.mu.Unlock()
	if have && time.Until(r.until) > renewAhead {
		return false
	}
	if i.Token == "" && i.APIKey == "" {
		return true
	}
	if i.OAuth == nil {
		return false
	}
	exp, ok := fileExpiry(i)
	return ok && time.Until(exp) < renewAhead
}

// tokenEnv: the CLI asked for a token must not see a PW_API_KEY, which it
// would print back instead.
var tokenEnv = map[string]string{"PW_API_KEY": ""}

// RenewContext is renewContext: ask the CLI for a context's current token,
// renewing it if it is about to expire. Concurrent askers share one run.
// It returns "" when no token came back (the reason is kept for the
// context's renewError).
func (p *Platform) RenewContext(name string, timeout time.Duration) string {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	t := p.tokens
	t.mu.Lock()
	if c, ok := t.renewing[name]; ok {
		t.mu.Unlock()
		<-c.done
		return c.token
	}
	c := &renewCall{done: make(chan struct{})}
	t.renewing[name] = c
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		delete(t.renewing, name)
		t.mu.Unlock()
		close(c.done)
	}()
	stdout, err := p.Exec(context.Background(), []string{"auth", "token", "--print", "-o", "json", "--context", name}, ExecOptions{Timeout: timeout, Env: tokenEnv})
	token := ""
	var expiresIn *float64
	if err == nil {
		if s, e := strings.Index(stdout, "{"), strings.LastIndex(stdout, "}"); s >= 0 && e > s {
			var d struct {
				AccessToken any `json:"access_token"`
				ExpiresIn   any `json:"expires_in"`
			}
			if json.Unmarshal([]byte(stdout[s:e+1]), &d) == nil {
				if d.AccessToken != nil {
					token = strings.TrimSpace(anyString(d.AccessToken))
				}
				if f, ok := d.ExpiresIn.(float64); ok {
					expiresIn = &f
				}
			}
		}
	}
	if token != "" {
		until := time.Now().Add(10 * time.Minute)
		if expiresIn != nil {
			until = time.Now().Add(time.Duration(*expiresIn * float64(time.Second)))
		}
		t.mu.Lock()
		t.renewed[name] = renewed{token: token, until: until}
		delete(t.errs, name)
		t.mu.Unlock()
		c.token = token
		return token
	}
	missing := IsMissing(err)
	if missing {
		p.FindCLI()
	}
	said := "no token printed"
	if err != nil {
		said = ErrText(err)
	}
	said = strings.TrimSpace(StripANSI(said))
	offline := !missing && netx.CLINetworkFailure(said)
	var msg string
	switch {
	case missing:
		msg = "The Studio cannot find the pw CLI, so this sign-in cannot be renewed. Choose where it is under pw CLI below."
	case offline:
		i, _ := p.ReadStore().identity(name)
		msg = netx.UnreachableMessage(i.Server, nil)
	default:
		msg = "the CLI could not renew this sign-in"
		var lines []string
		for _, l := range strings.Split(said, "\n") {
			if l != "" {
				lines = append(lines, l)
			}
		}
		if len(lines) > 0 {
			msg = sliceRunes(lines[len(lines)-1], 300)
		}
	}
	t.mu.Lock()
	t.errs[name] = renewErr{at: time.Now(), offline: offline, message: msg}
	t.mu.Unlock()
	return ""
}

func anyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64, bool:
		b, _ := json.Marshal(x)
		return string(b)
	}
	return ""
}

// sliceRunes is s.slice(0, n) by UTF-16 units, near enough by runes.
func sliceRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ContextToken is contextToken: a context's credential at once. A sign-in
// about to expire is renewed in the background and its current token
// returned meanwhile.
func (p *Platform) ContextToken(name string) string {
	i, ok := p.ReadStore().identity(name)
	if !ok {
		return ""
	}
	fileToken := i.Token
	if fileToken == "" {
		fileToken = i.APIKey
	}
	p.tokens.mu.Lock()
	r, have := p.tokens.renewed[name]
	p.tokens.mu.Unlock()
	exp, expOK := fileExpiry(i)
	useRenewed := have && (fileToken == "" || !expOK || r.until.After(exp))
	if p.needsRenewal(name) {
		go p.RenewContext(name, 0)
	}
	if useRenewed && r.token != "" {
		return r.token
	}
	if fileToken != "" {
		return fileToken
	}
	return r.token
}

// CLICredential is cliCredential: the CLI's credential for the gateway's
// host, or "".
func (p *Platform) CLICredential() string {
	name := p.CredentialContextName()
	if name == "" {
		return ""
	}
	return p.ContextToken(name)
}

// EnsureFreshAccess is ensureFreshAccess: renew the credential in use when
// it is about to expire, waiting at most timeout. Offline, a failed renewal
// is not retried for 30 seconds.
func (p *Platform) EnsureFreshAccess(timeout time.Duration) {
	name := p.CredentialContextName()
	if name == "" || !p.needsRenewal(name) {
		return
	}
	p.tokens.mu.Lock()
	e, had := p.tokens.errs[name]
	p.tokens.mu.Unlock()
	if had && e.offline && time.Since(e.at) < 30*time.Second {
		return
	}
	done := make(chan struct{})
	go func() { p.RenewContext(name, 0); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// ForgetRenewals clears renewed tokens and errors (resetAccessForTests).
func (p *Platform) ForgetRenewals() {
	p.tokens.mu.Lock()
	p.tokens.renewed = map[string]renewed{}
	p.tokens.errs = map[string]renewErr{}
	p.tokens.mu.Unlock()
}
