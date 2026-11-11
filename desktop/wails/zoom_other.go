//go:build !darwin && !linux

package main

// Elsewhere (Windows) the shell sets no zoom of its own (zoom.go).
const pageZoomWorks = false
const zoomKeys = false

func initPageZoom(float64)         {}
func (a *App) setPageZoom(float64) {}
