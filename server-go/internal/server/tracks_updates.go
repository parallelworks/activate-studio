package server

import (
	"net/http"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
	"github.com/parallelworks/activate-studio/server-go/internal/updates"
)

func init() {
	registerTrack(60, "updates", func(mux *http.ServeMux, a *app.App) { updates.Register(mux, a) })
}
