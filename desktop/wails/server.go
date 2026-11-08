package main

// serverRunner is what the window asks of whatever runs the Studio's
// server: the Supervisor (a child of the shell: Linux, Windows, a
// checkout), or on macOS the launcher (launcher_darwin.go), which leaves the
// server to a LaunchAgent or a run loop of its own that outlives the window,
// as the Swift shell did.
type serverRunner interface {
	Run()
	Restart(reason string)
	Retry()
	Stop()
	Port() int
}
