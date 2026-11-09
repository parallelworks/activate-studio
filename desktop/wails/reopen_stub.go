//go:build windows

package main

import "time"

func fileHash(string) string         { return "" }
func (a *App) shellChanged() bool    { return false }
func (a *App) relaunchAsked() bool   { return false }
func (a *App) reopen(*instance) bool { return false }
func waitForPID(string)              {}

var _ = time.Second
