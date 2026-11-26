package chat

import "strings"

// secretWords are the parts of a variable's name, split at "_", that mark
// a credential or where one is kept: model keys (OPENAI_API_KEY,
// PW_API_KEY), the sign-in client's secret, the bridge and trace secrets,
// tokens, passwords and the cloud credentials a host may carry.
var secretWords = map[string]bool{
	"KEY": true, "KEYS": true, "APIKEY": true, "SECRET": true, "SECRETS": true,
	"TOKEN": true, "TOKENS": true, "PASSWORD": true, "PASSWD": true, "PASS": true,
	"CREDENTIAL": true, "CREDENTIALS": true, "COOKIE": true, "PRIVATE": true,
}

// toolEnv is env less the Studio's secrets, for a custom tool: any
// variable with a secret word in its name. What the tool can read on disk
// is unchanged; docs/ACCESS-CONTROL.md says what that is.
func toolEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		secret := false
		for _, w := range strings.Split(strings.ToUpper(k), "_") {
			secret = secret || secretWords[w]
		}
		if !secret {
			out = append(out, kv)
		}
	}
	return out
}
