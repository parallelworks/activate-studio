//go:build !linux && !darwin

package main

func useOwnTitleBar(string) {}

func paintBar(color string) {}

// titleMetrics: the window has a title bar of its own, in either layout.
func titleMetrics(bool) (int, int) { return 0, 0 }

// setWindowCompact: nothing to move.
func setWindowCompact(bool) {}
