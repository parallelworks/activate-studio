//go:build !darwin

package main

import (
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
)

// The macOS window's parts (launcher_darwin.go); nothing elsewhere.

func maybePassThrough() (int, bool)                            { return 0, false }
func platformConfig(*Config)                                   {}
func newLauncherServer(Config, func(ServerEvent)) serverRunner { return nil }
func appMenu(*App) *menu.Menu                                  { return nil }
func (a *App) reopenBundle() bool                              { return false }
func macOptions() *mac.Options                                 { return nil }
