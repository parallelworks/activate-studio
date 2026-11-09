//go:build !windows && !darwin

package main

import (
	"errors"
	"reflect"
	"testing"
)

func TestWSLOpenCommand(t *testing.T) {
	have := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			for _, x := range names {
				if x == n {
					return "/bin/" + n, nil
				}
			}
			return "", errors.New("not found")
		}
	}
	win := func(p string) (string, error) { return `\\wsl.localhost\Ubuntu` + p, nil }
	for _, c := range []struct {
		target string
		url    bool
		look   func(string) (string, error)
		want   []string
	}{
		{"https://example.com/a?b=1&c=2", true, have("wslview", "rundll32.exe"), []string{"/bin/wslview", "https://example.com/a?b=1&c=2"}},
		{"https://example.com/a?b=1&c=2", true, have("rundll32.exe", "explorer.exe"), []string{"/bin/rundll32.exe", "url.dll,FileProtocolHandler", "https://example.com/a?b=1&c=2"}},
		{"https://example.com/", true, have("explorer.exe"), []string{"/bin/explorer.exe", "https://example.com/"}},
		{"https://example.com/", true, have(), nil},
		{"/home/me/.local/share/activate-studio-data/logs", false, have("explorer.exe"), []string{"/bin/explorer.exe", `\\wsl.localhost\Ubuntu/home/me/.local/share/activate-studio-data/logs`}},
		{"/home/me/logs", false, have("rundll32.exe"), nil},
	} {
		if got := wslOpenCommand(c.target, c.url, c.look, win); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %q, want %q", c.target, got, c.want)
		}
	}
}
