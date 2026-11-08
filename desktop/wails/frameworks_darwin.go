//go:build darwin

package main

// Wails' macOS frontend uses UTType (WailsContext.m) from the
// UniformTypeIdentifiers framework, which its own cgo flags do not link.
// The framework is part of macOS 11 and later; the app targets 14.

/*
#cgo LDFLAGS: -framework UniformTypeIdentifiers
*/
import "C"
