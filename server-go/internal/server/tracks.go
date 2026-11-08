package server

import (
	"net/http"
	"sort"

	"github.com/parallelworks/activate-studio/server-go/internal/app"
)

// A track registers its routes on the ported-routes mux (a route registered
// there is served by Go, never by Node) and its start-up hooks and background
// jobs on the App. Each track does it from its own file, tracks_<name>.go,
// with an init calling registerTrack, so the tracks' merges never touch the
// same lines.
type track struct {
	order    int // core 10, index 20, files 30, libraries 40, platform 50, updates 60, chat 70
	name     string
	register func(mux *http.ServeMux, a *app.App)
}

var tracks []track

func registerTrack(order int, name string, register func(*http.ServeMux, *app.App)) {
	for _, t := range tracks {
		if t.name == name {
			panic("server: track registered twice: " + name)
		}
	}
	tracks = append(tracks, track{order, name, register})
}

func registerTracks(mux *http.ServeMux, a *app.App) {
	sort.SliceStable(tracks, func(i, j int) bool { return tracks[i].order < tracks[j].order })
	for _, t := range tracks {
		t.register(mux, a)
	}
}
