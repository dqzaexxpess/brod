//go:build !linux && !windows

package main

// meteredFunc: определение платной сети на этой платформе не сделано (Android передаёт её сам, на Windows — metered_windows.go).
func meteredFunc() func() bool { return nil }
