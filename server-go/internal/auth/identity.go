// Package auth is who a request is: the platform session proxy's JWT
// (server/src/auth.ts), an OpenID Connect sign-in (the oidc-signin
// branch's oidc.ts, ported here and only here), and the hand-off of that
// identity to the Node server behind the Go front (handoff.go). See
// docs/GO-PORT.md, Stage 1.
package auth

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/parallelworks/activate-studio/server-go/internal/config"
)

// User is the verified person behind a request, the shape identity.ts
// gives req.user whichever way they were verified.
type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name,omitempty"`
	// Source is "proxy" or "oidc".
	Source string `json:"source,omitempty"`
	// Account, UID and GID only when the deployment's mapping names claims
	// present in the token (AUTH_ACCOUNT_CLAIM, AUTH_UID_CLAIM,
	// AUTH_GID_CLAIM). Recorded; nothing acts on them yet.
	Account string   `json:"account,omitempty"`
	UID     *uint32  `json:"uid,omitempty"`
	GID     *uint32  `json:"gid,omitempty"`
	Groups  []string `json:"groups,omitempty"`
	Claims  Claims   `json:"claims"`
}

// claim reads a claim by name; a dotted name like ext.uidNumber is read
// through nested objects when no claim has the whole name.
func claim(c map[string]any, name string) any {
	if name == "" {
		return nil
	}
	if v, ok := c[name]; ok {
		return v
	}
	var cur any = c
	for _, part := range strings.Split(name, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			if cm, isClaims := cur.(Claims); isClaims {
				m = cm
			} else {
				return nil
			}
		}
		cur = m[part]
	}
	return cur
}

func jsTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || r == 0xfeff })
}

// StringClaim is identity.ts's stringClaim: a non-blank string claim,
// trimmed.
func StringClaim(c map[string]any, name string) (string, bool) {
	s, ok := claim(c, name).(string)
	if !ok || jsTrim(s) == "" {
		return "", false
	}
	return jsTrim(s), true
}

var digits = regexp.MustCompile(`^\d{1,10}$`)

func numericID(v any) *uint32 {
	var s string
	switch x := v.(type) {
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		s = jsTrim(x)
	default:
		return nil
	}
	if !digits.MatchString(s) {
		return nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil || n > 0xffffffff {
		return nil
	}
	u := uint32(n)
	return &u
}

// mapIdentity fills the optional account, uid, gid and groups.
func mapIdentity(u *User, c map[string]any, a config.Auth) {
	if s, ok := claim(c, a.AccountClaim).(string); ok && jsTrim(s) != "" {
		u.Account = jsTrim(s)
	}
	u.UID = numericID(claim(c, a.UIDClaim))
	u.GID = numericID(claim(c, a.GIDClaim))
	if gs, ok := c["groups"].([]any); ok {
		u.Groups = []string{}
		for _, g := range gs {
			if s, ok := g.(string); ok {
				u.Groups = append(u.Groups, s)
			}
		}
	}
}

// proxyUser is auth.ts's verify(): the platform proxy's sub is
// user:<username>; the prefix is dropped.
func proxyUser(c Claims, a config.Auth) *User {
	username := firstString(c, "preferred_username", "username", "email", "sub")
	if username == "" {
		username = "user"
	}
	username = strings.TrimPrefix(username, "user:")
	id := username
	if v, ok := c["sub"]; ok && v != nil {
		id = jsString(v)
	}
	u := &User{ID: id, Username: username, Source: "proxy", Claims: c}
	if n, ok := c["name"].(string); ok {
		u.Name = n
	}
	mapIdentity(u, c, a)
	return u
}

// firstString is `a ?? b ?? c`, each made a string as String() does.
func firstString(c Claims, keys ...string) string {
	for _, k := range keys {
		if v, ok := c[k]; ok && v != nil {
			return jsString(v)
		}
	}
	return ""
}

func jsString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	}
	return "[object Object]"
}
