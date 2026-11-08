package libraries

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
)

// Keys is the platform key a library's owner acts with (userLibraries.ts
// keyOf): their own personal key; the deployment's only on a one-person
// Studio. "" for none.
//
// TODO(chat track, credentials): the owner of the personal-key vault and
// the shared key implements this. DeploymentKeys is the stand-in: it knows
// no personal keys, and gives the deployment's key (PW_API_KEY,
// OPENAI_API_KEY, then the pw CLI's own sign-in for the gateway's host)
// where sign-in is off.
type Keys interface {
	KeyOf(owner string) string
}

// DeploymentKeys is the stand-in Keys.
type DeploymentKeys struct {
	Getenv      func(string) string
	AuthEnabled bool
}

// KeyOf is keyOf without personal keys.
func (k DeploymentKeys) KeyOf(owner string) string {
	// Only the one user of a Studio without sign-in ("single") acts with
	// the deployment's key; a signed-in owner needs their own.
	if k.AuthEnabled || owner != "single" {
		return ""
	}
	get := k.Getenv
	if get == nil {
		get = os.Getenv
	}
	if v := get("PW_API_KEY"); v != "" {
		return v
	}
	if v := get("OPENAI_API_KEY"); v != "" {
		return v
	}
	return CLICredential(get)
}

// CLICredential is access.ts's cliCredential, read from the file only: the
// token of the pw CLI context for the gateway's host (the active one first,
// PW_CONTEXT or the file's currentIdentity), else "". It does not renew a
// token about to expire.
// TODO(access track): replace with the access package's, which renews.
func CLICredential(get func(string) string) string {
	dir := get("PW_CREDENTIALS_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".config", "pw")
	}
	b, err := os.ReadFile(filepath.Join(dir, "credentials"))
	if err != nil {
		return ""
	}
	type identity struct {
		Server string `json:"server"`
		Token  string `json:"token"`
		APIKey string `json:"apikey"`
	}
	var store struct {
		CurrentIdentity string        `json:"currentIdentity"`
		Identities      orderedObject `json:"identities"`
	}
	if json.Unmarshal(b, &store) != nil {
		return ""
	}
	ids := map[string]identity{}
	for _, k := range store.Identities.keys {
		var i identity
		json.Unmarshal(store.Identities.vals[k], &i)
		ids[k] = i
	}
	active := get("PW_CONTEXT")
	if active == "" {
		active = store.CurrentIdentity
	}
	host := ""
	gw := get("PW_GATEWAY_URL")
	if gw == "" {
		gw = get("OPENAI_BASE_URL")
	}
	if gw != "" {
		if u, err := url.Parse(gw); err == nil {
			host = u.Host
		}
	} else if i, ok := ids[active]; ok {
		host = i.Server
	}
	if host == "" {
		host = "activate.parallel.works"
	}
	tok := func(i identity) string {
		if i.Token != "" {
			return i.Token
		}
		return i.APIKey
	}
	if i, ok := ids[active]; ok && i.Server == host {
		return tok(i)
	}
	for _, k := range store.Identities.keys {
		if ids[k].Server == host {
			return tok(ids[k])
		}
	}
	return ""
}
