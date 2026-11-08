//go:build !linux && !darwin

package main

func useOwnTitleBar(string) {}

func paintBar(color string) {}
