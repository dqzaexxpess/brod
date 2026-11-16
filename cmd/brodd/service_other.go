//go:build !windows

package main

// serviceStop есть только у службы Windows.
var serviceStop chan struct{}

func runAsService(func()) bool { return false }
