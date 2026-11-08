package userlibs

import (
	"encoding/base64"
	"net/http"
	"strings"
	"time"

	"github.com/parallelworks/activate-studio/server-go/internal/apierr"
	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/auth"
	"github.com/parallelworks/activate-studio/server-go/internal/config"
	"github.com/parallelworks/activate-studio/server-go/internal/jsonv"
	"github.com/parallelworks/activate-studio/server-go/internal/platform"
)

// GET /api/whoami where the Go front does not verify identity itself (a
// Node server reached at STUDIO_NODE_URL, or the front's identity turned
// off): the platform token in AUTH_HEADER, verified here as auth.ts's
// hook verifies it, and the answer routes.ts gives. With the front's
// identity on, auth.Front answers it.
func registerWhoami(mux *http.ServeMux, a *app.App) {
	cfg := a.Cfg
	cfg.OIDC = config.OIDC{}
	front := auth.NewFront(cfg, a.Log, nil, nil)
	mux.HandleFunc("GET /api/whoami", platform.Handler(a.Log, func(w http.ResponseWriter, r *http.Request) (any, error) {
		return whoami(cfg.Auth, front, r)
	}))
}

func whoami(a config.Auth, front *auth.Front, r *http.Request) (any, error) {
	enabled := a.JWKSURL != ""
	var u *auth.User
	if enabled {
		var invalid bool
		u, invalid = front.Identify(r)
		if u == nil && a.Required {
			if invalid {
				return nil, apierr.New(401, "invalid token")
			}
			return nil, apierr.New(401, "authentication required")
		}
	}
	out := jsonv.NewObject("authEnabled", enabled, "header", a.Header, "verified", u != nil, "user", nil, "token", nil, "claims", nil)
	if u == nil {
		return out, nil
	}
	user := jsonv.NewObject("id", u.ID, "username", u.Username)
	if u.Name != "" {
		user.Set("name", u.Name)
	}
	claims := payloadOf(r.Header.Get(a.Header))
	if claims == nil {
		claims = jsonv.NewObject()
		for k, v := range u.Claims {
			claims.Set(k, v)
		}
	}
	iss, _ := claims.Get("iss")
	aud, _ := claims.Get("aud")
	iat, _ := claims.Get("iat")
	exp, _ := claims.Get("exp")
	out.Set("user", user)
	out.Set("token", jsonv.NewObject("issuer", iss, "audience", aud, "issuedAt", isoSeconds(iat), "expiresAt", isoSeconds(exp)))
	out.Set("claims", claims)
	return out, nil
}

// payloadOf is the token's payload in the order it was written, as jose
// gives it; nil when it does not decode.
func payloadOf(tok string) *jsonv.Object {
	if len(tok) >= 7 && strings.EqualFold(tok[:7], "bearer ") {
		tok = tok[7:]
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil
	}
	v, err := jsonv.Decode(b)
	if err != nil {
		return nil
	}
	return jsonv.Obj(v)
}

// isoSeconds is new Date(v * 1000).toISOString() for a number, else null.
func isoSeconds(v any) any {
	f, ok := v.(float64)
	if !ok {
		return nil
	}
	return time.UnixMilli(int64(f * 1000)).UTC().Format("2006-01-02T15:04:05.000Z")
}
