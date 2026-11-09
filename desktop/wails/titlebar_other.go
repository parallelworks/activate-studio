//go:build !linux && !darwin

package main

func useOwnTitleBar(string) {}

// titleInsetPx: the window has a title bar of its own.
const titleInsetPx = 0

func paintBar(color string) {}
