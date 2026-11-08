//go:build race

package pdf

// raceEnabled reports a -race build. sync.Pool deliberately drops objects
// at random under the race detector, which makes allocation counts
// non-deterministic; checks that depend on them skip.
const raceEnabled = true
